package webrtc

import (
	"RTSPSender/internal/janus"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/aler9/gortsplib/pkg/url"

	"github.com/pion/mediadevices"

	"github.com/aler9/gortsplib"
	"github.com/pion/dtls/v2/pkg/protocol/extension"
	"github.com/pion/interceptor"
	"github.com/pion/webrtc/v3"

	"github.com/pion/mediadevices/pkg/codec/opus" // This is required to use opus audio encoder
	"github.com/pion/mediadevices/pkg/driver"
	_ "github.com/pion/mediadevices/pkg/driver/microphone" // This is required to register microphone adapter
	"github.com/pion/mediadevices/pkg/prop"
)

type Muxer struct {
	status             webrtc.ICEConnectionState
	stop               bool
	pc                 *webrtc.PeerConnection
	rtspClient         *gortsplib.Client
	audioCodecSelector *mediadevices.CodecSelector
	stopSendingAudio   bool
	rtspRetryTimes     int
	userId             string

	Hangup  bool
	Options Options
	Janus   *janus.Gateway
}

type Options struct {
	// ICEServers is a required array of ICE server URLs to connect to (e.g., STUN or TURN server URLs)
	ICEServers []string
	// ICEUsername is an optional username for authenticating with the given ICEServers
	ICEUsername string
	// ICECredential is an optional credential (i.e., password) for authenticating with the given ICEServers
	ICECredential string
	// PortMin is an optional minimum (inclusive) ephemeral UDP port range for the ICEServers connections
	PortMin uint16
	// PortMin is an optional maximum (inclusive) ephemeral UDP port range for the ICEServers connections
	PortMax uint16
}

func NewMuxer(options Options) *Muxer {
	tmp := Muxer{Options: options}
	tmp.rtspRetryTimes = 3
	return &tmp
}

func (element *Muxer) NewPeerConnection(configuration webrtc.Configuration) (*webrtc.PeerConnection, error) {
	if len(element.Options.ICEServers) > 0 {
		configuration.ICEServers = append(configuration.ICEServers, webrtc.ICEServer{
			URLs:           element.Options.ICEServers,
			Username:       element.Options.ICEUsername,
			Credential:     element.Options.ICECredential,
			CredentialType: webrtc.ICECredentialTypePassword,
		})
	}
	m := &webrtc.MediaEngine{}
	if err := m.RegisterDefaultCodecs(); err != nil {
		return nil, err
	}

	videoRTCPFeedback := []webrtc.RTCPFeedback{
		{Type: "goog-remb", Parameter: ""},
		{Type: "ccm", Parameter: "fir"},
		{Type: "nack", Parameter: "pli"},
	}
	for _, codec := range []webrtc.RTPCodecParameters{
		{
			RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH265, ClockRate: 90000, Channels: 0, SDPFmtpLine: "profile-id=1", RTCPFeedback: videoRTCPFeedback},
			PayloadType:        96,
		},
	} {
		if err := m.RegisterCodec(codec, webrtc.RTPCodecTypeVideo); err != nil {
			return nil, err
		}
	}

	i := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(m, i); err != nil {
		return nil, err
	}
	s := webrtc.SettingEngine{}
	s.SetSRTPProtectionProfiles(extension.SRTP_AES128_CM_HMAC_SHA1_80)

	if element.Options.PortMin > 0 && element.Options.PortMax > 0 && element.Options.PortMax > element.Options.PortMin {
		err := s.SetEphemeralUDPPortRange(element.Options.PortMin, element.Options.PortMax)
		if err != nil {
			return nil, err
		}
		log.Println("Set UDP ports to", element.Options.PortMin, "..", element.Options.PortMax)
	}

	opusParams, err := opus.NewParams()
	if err != nil {
		log.Printf("Create opus params failed %s", err)
	}
	audioCodecSelector := mediadevices.NewCodecSelector(mediadevices.WithAudioEncoders(&opusParams))
	audioCodecSelector.Populate(m)
	element.audioCodecSelector = audioCodecSelector

	api := webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(i), webrtc.WithSettingEngine(s))
	return api.NewPeerConnection(configuration)
}

func (element *Muxer) WriteHeader(
	ID string,
	Room string,
	Pin string,
	RTSP string,
	Janus string,
	Mic string,
	Display string) (string, error) {

	peerConnection, err := element.NewPeerConnection(webrtc.Configuration{
		SDPSemantics: webrtc.SDPSemanticsUnifiedPlanWithFallback,
	})
	if err != nil {
		return "Create pc failed", err
	}
	element.userId = ID

	// Get audio track
	var hasAudio = false
	audioTrack, err := element.getAudioTrack(Mic)
	if err != nil {
		// if there is not a video device for use, send audio anyway
		log.Println("Can not find audio track, error:", err)
		hasAudio = false
	} else {
		// Add audio track
		_, err = peerConnection.AddTransceiverFromTrack(audioTrack,
			webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly})
		if err != nil {
			return "Add audio track failed", err
		}
		hasAudio = true
	}

	// Get video track info from RTSP URL
	rtspVideoTrack, videoType, err := element.videoTrackID(RTSP)
	if err != nil || rtspVideoTrack == nil {
		element.rtspClient.Close()
		return "Get video Track id error: ", err
	}

	// Create a video track
	videoTrack, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: videoType}, "video", "rtsp")
	if err != nil {
		return "Create video track failed", err
	} else if _, err = peerConnection.AddTrack(videoTrack); err != nil {
		return "Add video track failed", err
	}

	// Connect to RTSP Camera
	element.connectRTSPCamera(RTSP, rtspVideoTrack, videoTrack)

	// RTC state callbacks
	peerConnection.OnICEConnectionStateChange(func(connectionState webrtc.ICEConnectionState) {
		element.status = connectionState
		log.Println("ICEConnectionState:", connectionState)
	})
	peerConnection.OnConnectionStateChange(func(connectionState webrtc.PeerConnectionState) {
		log.Println("PeerConnectionState:", connectionState)
	})

	// Wait offer & answer steps are completed
	gatherCompletePromise := webrtc.GatheringCompletePromise(peerConnection)

	offer, err := peerConnection.CreateOffer(nil)
	if err != nil {
		return "Create offer failed", err
	}
	if err = peerConnection.SetLocalDescription(offer); err != nil {
		return "Set local sdp failed", err
	}
	element.pc = peerConnection

	waitT := time.NewTimer(time.Second * 10)
	select {
	case <-waitT.C:
		return "", errors.New("gatherCompletePromise wait")
	case <-gatherCompletePromise:
		// Completed
	}

	// Connect to janus, set remote sdp.
	return element.connectJanusAndSendMsgs(ID, Room, Pin, Janus, Display, hasAudio, peerConnection)
}

// Connect to Janus server & join the video room
func (element *Muxer) connectJanusAndSendMsgs(
	ID string, Room string, Pin string, Janus string, Display string,
	HasAudio bool, pc *webrtc.PeerConnection) (string, error) {
	// Janus
	gateway, err := janus.Connect(Janus)
	if err != nil {
		return "Connect janus server error", err
	}
	element.Janus = gateway

	session, err := gateway.Create()
	if err != nil {
		return "Create janus session error", err
	}

	handle, err := session.Attach("janus.plugin.videoroom")
	if err != nil {
		return "Attach janus session error", err
	}

	// Send keep-alive to janus in every 30s
	go func() {
		for {
			if element.stop {
				return
			}
			if _, keepAliveErr := session.KeepAlive(); keepAliveErr != nil {
				log.Printf("Can not send keep-alive msg to janus %s", keepAliveErr)
				return
			}
			time.Sleep(30 * time.Second)
		}
	}()

	// Receive janus message
	go element.janusEventsHandle(handle)

	roomNum, _ := strconv.Atoi(Room)
	publisherID, err := strconv.Atoi(ID)
	if err != nil {
		roomNum = 1234
		log.Printf("Room number invalid %s", err)
	}

	msg, err := handle.Message(map[string]interface{}{
		"request": "join",
		"ptype":   "publisher",
		"room":    roomNum,
		"id":      publisherID,
		"display": Display,
		"pin":     Pin,
	}, nil)
	if err != nil {
		return fmt.Sprintf("Join room %s failed", Room), err
	}
	if msg != nil && msg.Plugindata.Data != nil {
		data := msg.Plugindata.Data
		if data["error"] != nil {
			return fmt.Sprintf("Join room %s failed", Room), fmt.Errorf("join room %s failed, reason: %s", Room, data["error"])
		}
	}
	handle.User = fmt.Sprint(publisherID)

	msg, err = handle.Message(map[string]interface{}{
		"request": "publish",
		"audio":   HasAudio,
		"video":   true,
		"data":    false,
	}, map[string]interface{}{
		"type":    "offer",
		"sdp":     pc.LocalDescription().SDP,
		"trickle": false,
	})
	if err != nil {
		return fmt.Sprintf("Publish to room %s failed", Room), err
	}

	// set remote sdp
	if msg.Jsep != nil {
		err = pc.SetRemoteDescription(webrtc.SessionDescription{
			Type: webrtc.SDPTypeAnswer,
			SDP:  msg.Jsep["sdp"].(string),
		})
		if err != nil {
			return fmt.Sprintf("No remote sdp found %s error", Room), err
		}

		return "", nil
	} else {
		return fmt.Sprintf("No JSEP found %s error", Room), err
	}
}

// Get audio id by the filter device name hash
func (element *Muxer) getAudioTrack(deviceNameHash string) (*mediadevices.AudioTrack, error) {
	var hasAudio = len(deviceNameHash) > 0 && deviceNameHash != "mute"
	if !hasAudio {
		return nil, errors.New("invalid microphone device name")
	}

	var deviceID = ""
	deviceInfo := mediadevices.EnumerateDevices()
	if len(deviceInfo) > 0 {
		for _, device := range deviceInfo {
			log.Printf("Enum Audio Device: %s, name: %s", device, device.Name)

			deviceNameHash_ := getMD5Hash(device.Name)
			if device.Kind == mediadevices.AudioInput && strings.EqualFold(deviceNameHash_, deviceNameHash) {
				hasAudio = true
				deviceID = device.DeviceID
				break
			} else {
				hasAudio = false
			}
		}
	} else {
		hasAudio = false
	}

	if !hasAudio {
		return nil, errors.New("can not found the target device")
	}

	// Filter audio(microphone) device id by name
	s, err := mediadevices.GetUserMedia(mediadevices.MediaStreamConstraints{
		Audio: func(c *mediadevices.MediaTrackConstraints) {
			c.DeviceID = prop.String(deviceID)
			c.SampleSize = prop.Int(2)
			c.SampleRate = prop.Int(48000)
			c.IsFloat = prop.BoolExact(true)
			c.IsInterleaved = prop.BoolExact(true)
		},
		Codec: element.audioCodecSelector,
	})

	if err != nil {
		log.Println("Audio track create failed", err)
		return nil, err
	} else {
		audioTrack := s.GetAudioTracks()[0].(*mediadevices.AudioTrack)
		return audioTrack, err
	}
}

// Get RTSP video track id
func (element *Muxer) videoTrackID(rtsp string) (gortsplib.Track, string, error) {
	c := gortsplib.Client{
		UserAgent: "RTSPSender",
		// ReadTimeout: 8,
	}

	element.rtspClient = &c

	videoCodeType := webrtc.MimeTypeH264

	// parse URL
	u, err := url.Parse(rtsp)
	if err != nil {
		return nil, videoCodeType, err
	}

	// connect to the server
	err = c.Start(u.Scheme, u.Host)
	if err != nil {
		return nil, videoCodeType, err
	}

	// find published tracks
	tracks, _, _, err := c.Describe(u)
	if err != nil {
		return nil, videoCodeType, err
	}

	trackIndex := -1
	for i, track := range tracks {
		// find the video track h264 or h265
		if _, ok := track.(*gortsplib.TrackH264); ok {
			videoCodeType = webrtc.MimeTypeH264
			trackIndex = i
			break
		} else if _, ok := track.(*gortsplib.TrackH265); ok {
			videoCodeType = webrtc.MimeTypeH265
			trackIndex = i
			break
		}
	}

	if trackIndex < 0 {
		fmt.Println("Can not find video track, rtsp=", rtsp)
		return nil, videoCodeType, err
	}

	return tracks[trackIndex], videoCodeType, nil
}

// Connect to RTSP camera & get video pkg data from video stream
func (element *Muxer) connectRTSPCamera(rtsp string, videoTrack gortsplib.Track, track *webrtc.TrackLocalStaticRTP) {
	// parse URL
	baseURL, err := url.Parse(rtsp)
	if err != nil {
		log.Print("Parse URL error:", err)
		return
	}

	// pass the video data to Pion
	go func() {
		element.rtspClient.OnPacketRTP = func(p *gortsplib.ClientOnPacketRTPCtx) {
			err := track.WriteRTP(p.Packet)
			if err != nil {
				fmt.Println("Write RTP pkt error:", err)
			}
		}

		_, err = element.rtspClient.Setup(videoTrack, baseURL, 0, 0)
		_, err = element.rtspClient.Play(nil)
		err = element.rtspClient.Wait()

		if err != nil {
			log.Println("Connect to RTSP camera error:", err)
			// retry
			if element.rtspRetryTimes > 0 && !element.stop {
				element.rtspRetryTimes--
				time.AfterFunc(1*time.Second, func() {
					if !element.stop {
						log.Println("Reconnect to RTSP", rtsp)
						element.connectRTSPCamera(rtsp, videoTrack, track)
					}
				})
			} else {
				log.Printf("Reconnect to RTSP %s failed, close WebRTC", rtsp)
				element.Close()
			}
		}
	}()
}

func (element *Muxer) closeAudioDriverIfNecessary() {
	log.Println("Closing microphone...")
	audioDrivers := driver.GetManager().Query(driver.FilterAudioRecorder())
	for _, d := range audioDrivers {
		if d.Status() != driver.StateClosed {
			err := d.Close()
			if err != nil {
				log.Println("Close driver failed", err)
			}
		}
	}
	log.Println("Close microphone finished.")
}

func (element *Muxer) Mute() {
	audioDrivers := driver.GetManager().Query(driver.FilterAudioRecorder())
	for _, d := range audioDrivers {
		if d.Status() != driver.StateClosed {
			success := d.Mute()
			if !success {
				log.Println("Mute failed")
			}
		}
	}
}

func (element *Muxer) Unmute() {
	audioDrivers := driver.GetManager().Query(driver.FilterAudioRecorder())
	for _, d := range audioDrivers {
		if d.Status() != driver.StateClosed {
			success := d.Unmute()
			if !success {
				log.Println("Unmute failed")
			}
		}
	}
}

func (element *Muxer) Close() {
	if element.stop {
		log.Println("This WebRTC instance is stopping, please wait...")
		return
	}
	element.stop = true

	if element.Janus != nil {
		err := element.Janus.Close()
		if err != nil {
			log.Println("Close janus ws failed", err)
		}
		element.Janus = nil
	}

	if element.rtspClient != nil {
		err := element.rtspClient.Close()
		log.Println("Close RTSP client failed", err)
		element.rtspClient = nil
	}

	if element.pc != nil {
		element.closeAudioDriverIfNecessary()

		log.Println("Closing pc...")
		err := element.pc.Close()
		if err != nil {
			log.Println("Close pc failed", err)
		}
		element.pc = nil
		log.Println("Close pc finished")
	}

	element.stop = false
}

func (element *Muxer) janusEventsHandle(handle *janus.Handle) {
	for {
		msg := <-handle.Events
		switch msg := msg.(type) {
		case *janus.SlowLinkMsg:
			log.Println("SlowLinkMsg type, user:", handle.User)
		case *janus.MediaMsg:
			if msg.Type == "audio" {
				if !msg.Receiving {
					if !element.stopSendingAudio {
						element.stopSendingAudio = true
						time.AfterFunc(3*time.Second, func() {
							if element.stopSendingAudio {
								log.Println("No audio in 3s, closing audio driver...")
								element.closeAudioDriverIfNecessary()
							}
						})
					}
				} else {
					element.stopSendingAudio = false
				}
			} else if msg.Type == "video" {
				if msg.Receiving {
					element.rtspRetryTimes = 3
				}
			}
			log.Println("MediaEvent type", msg.Type, "receiving", msg.Receiving, "user:", handle.User)
		case *janus.WebRTCUpMsg:
			log.Println("WebRTCUpMsg type, user:", handle.User)
		case *janus.HangupMsg:
			log.Println("HangupEvent type", handle.User)
			if handle.User == element.userId {
				element.handleUserHangup()
				return
			}
		case *janus.EventMsg:
			// log.Printf("EventMsg %+v", msg.Plugindata.Data)
		}
	}
}

func (element *Muxer) handleUserHangup() {
	element.Close()
	element.Hangup = true
}

func getMD5Hash(text string) string {
	hash := md5.Sum([]byte(text))
	return hex.EncodeToString(hash[:])
}
