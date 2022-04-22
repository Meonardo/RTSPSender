package webrtc

import (
	"RTSPSender/internal/janus"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/pion/mediadevices"

	"github.com/aler9/gortsplib"
	"github.com/aler9/gortsplib/pkg/base"
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

func (element *Muxer) janusEventsHandle(handle *janus.Handle) {
	// wait for event
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
								log.Println("No audio in 3s, closinng audio driver...")
								element.closeAudioDriverIfNecessary()
							}
						})
					}
				} else {
					element.stopSendingAudio = false
				}
			}
			log.Println("MediaEvent type", msg.Type, "receiving", msg.Receiving, "user:", handle.User)
		case *janus.WebRTCUpMsg:
			log.Println("WebRTCUpMsg type, user:", handle.User)
		case *janus.HangupMsg:
			log.Println("HangupEvent type", handle.User)
		case *janus.EventMsg:
			log.Printf("EventMsg %+v", msg.Plugindata.Data)
		}
	}
}

func NewMuxer(options Options) *Muxer {
	tmp := Muxer{Options: options}
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

	var hasAudio = len(Mic) > 0 && Mic != "mute"
	var deviceID = ""
	if hasAudio {
		deviceInfo := mediadevices.EnumerateDevices()
		if len(deviceInfo) > 0 {
			for _, device := range deviceInfo {
				deviceName := strings.ReplaceAll(device.Name, " ", "")
				if device.Kind == mediadevices.AudioInput && deviceName == Mic {
					hasAudio = true
					deviceID = device.DeviceID
					log.Printf("Found Audio Device: %s, name: %s", device, device.Name)
					break
				}
			}
		} else {
			hasAudio = false
			log.Println("No microphone device found in this machine, not going to send audio...")
		}
	}

	if hasAudio && element.audioCodecSelector != nil {
		s, err := mediadevices.GetUserMedia(mediadevices.MediaStreamConstraints{
			Audio: func(c *mediadevices.MediaTrackConstraints) {
				c.DeviceID = prop.String(deviceID)
				c.SampleSize = prop.Int(2)
			},
			Codec: element.audioCodecSelector,
		})

		if err != nil {
			log.Println("Audio track create failed", err)
			hasAudio = false
		} else {
			audioTrack := s.GetAudioTracks()[0].(*mediadevices.AudioTrack)
			_, err = peerConnection.AddTransceiverFromTrack(audioTrack,
				webrtc.RTPTransceiverInit{
					Direction: webrtc.RTPTransceiverDirectionSendonly,
				},
			)

			if err != nil {
				return "Add audio track failed", err
			}
		}
	}

	// Create a video track
	videoTrack, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264}, "video", "rtsp")
	//webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264}, "video", "rtsp")
	if err != nil {
		return "Create video track failed", err
	} else if _, err = peerConnection.AddTrack(videoTrack); err != nil {
		return "Add video track failed", err
	}

	// Connect to RTSP Camera
	h264TrackID, err := element.h264trackID(RTSP)
	if err != nil || h264TrackID == -1 {
		return "Get H264 Track id error: ", err
	}
	element.connectRTSPCamera(RTSP, h264TrackID, videoTrack)

	peerConnection.OnICEConnectionStateChange(func(connectionState webrtc.ICEConnectionState) {
		element.status = connectionState
	})

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
		//Connected
	}

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

	go element.janusEventsHandle(handle)

	roomNum, err := strconv.Atoi(Room)
	publisherID, err := strconv.Atoi(ID)
	if err != nil {
		roomNum = 1234
		log.Printf("Room number invalid %s", err)
	}

	_, err = handle.Message(map[string]interface{}{
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
	handle.User = fmt.Sprint(publisherID)

	msg, err := handle.Message(map[string]interface{}{
		"request": "publish",
		"audio":   hasAudio,
		"video":   true,
		"data":    false,
	}, map[string]interface{}{
		"type":    "offer",
		"sdp":     peerConnection.LocalDescription().SDP,
		"trickle": false,
	})
	if err != nil {
		return fmt.Sprintf("Publish to room %s failed", Room), err
	}

	if msg.Jsep != nil {
		err = peerConnection.SetRemoteDescription(webrtc.SessionDescription{
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

func (element *Muxer) h264trackID(rtsp string) (int, error) {
	c := gortsplib.Client{}

	defer c.Close()

	// parse URL
	u, err := base.ParseURL(rtsp)
	if err != nil {
		return -1, err
	}

	// connect to the server
	err = c.Start(u.Scheme, u.Host)
	if err != nil {
		return -1, err
	}

	// find published tracks
	tracks, _, _, err := c.Describe(u)
	if err != nil {
		return -1, err
	}

	// find the H264 track
	h264TrackID, _ := func() (int, *gortsplib.TrackH264) {
		for i, track := range tracks {
			if h264track, ok := track.(*gortsplib.TrackH264); ok {
				return i, h264track
			}
		}
		return -1, nil
	}()

	return h264TrackID, nil
}

func (element *Muxer) connectRTSPCamera(rtsp string, h264TrackID int, track *webrtc.TrackLocalStaticRTP) {
	go func() {
		c := gortsplib.Client{
			OnPacketRTP: func(p *gortsplib.ClientOnPacketRTPCtx) {
				if p.TrackID == h264TrackID {
					err := track.WriteRTP(p.Packet)
					if err != nil {
						fmt.Println("Write RTP pkt error: ", err)
					}
				}
			}, Transport: func() *gortsplib.Transport {
				v := gortsplib.TransportTCP
				return &v
			}(),
		}
		element.rtspClient = &c
		err := c.StartReadingAndWait(rtsp)
		log.Println("Connect to RTSP camera", err)
	}()
}

func (element *Muxer) closeAudioDriverIfNecessary() {
	audioDrivers := driver.GetManager().Query(driver.FilterAudioRecorder())
	for _, d := range audioDrivers {
		if d.Status() != driver.StateOpened {
			log.Println("Closing microphone...")
			err := d.Close()
			if err != nil {
				log.Println("Close driver failed", err)
			}
			log.Println("Close microphone finished")
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
}
