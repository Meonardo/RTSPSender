package webrtc

import (
	"RTSPSender/internal/janus"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/pion/logging"
	"github.com/pion/mediadevices"

	"github.com/pion/dtls/v2/pkg/protocol/extension"
	"github.com/pion/interceptor"
	"github.com/pion/webrtc/v3"

	_ "RTSPSender/internal/speaker" // This is required to register microphone adapter

	"github.com/pion/mediadevices/pkg/codec/opus" // This is required to use opus audio encoder
	"github.com/pion/mediadevices/pkg/driver"
	"github.com/pion/mediadevices/pkg/prop"
)

type Muxer struct {
	status             webrtc.ICEConnectionState
	pcStatus           webrtc.PeerConnectionState
	stop               bool
	pc                 *webrtc.PeerConnection
	audioCodecSelector *mediadevices.CodecSelector
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

//////////////////////////////////////////////////////////////////////////////////////////////////////////////
//////////////////////////////////////////////////////////////////////////////////////////////////////////////
//////////////////////////////////////////////////////////////////////////////////////////////////////////////
/// logger
type customLogger struct{}

// Print all messages except trace
func (c customLogger) Trace(msg string)                          {}
func (c customLogger) Tracef(format string, args ...interface{}) {}

func (c customLogger) Debug(msg string) { log.Printf("Pion Debug: %s\n", msg) }
func (c customLogger) Debugf(format string, args ...interface{}) {
	c.Debug(fmt.Sprintf(format, args...))
}
func (c customLogger) Info(msg string) { log.Printf("Pion Info: %s\n", msg) }
func (c customLogger) Infof(format string, args ...interface{}) {
	c.Trace(fmt.Sprintf(format, args...))
}
func (c customLogger) Warn(msg string) { log.Printf("Pion Warn: %s\n", msg) }
func (c customLogger) Warnf(format string, args ...interface{}) {
	c.Warn(fmt.Sprintf(format, args...))
}
func (c customLogger) Error(msg string) { log.Printf("Pion Error: %s\n", msg) }
func (c customLogger) Errorf(format string, args ...interface{}) {
	c.Error(fmt.Sprintf(format, args...))
}

// customLoggerFactory satisfies the interface logging.LoggerFactory
// This allows us to create different loggers per subsystem. So we can
// add custom behavior
type customLoggerFactory struct{}

func (c customLoggerFactory) NewLogger(subsystem string) logging.LeveledLogger {
	fmt.Printf("Creating logger for %s \n", subsystem)
	return customLogger{}
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

	videoRTCPFeedback := []webrtc.RTCPFeedback{
		{Type: "goog-remb", Parameter: ""},
		{Type: "ccm", Parameter: "fir"},
		{Type: "nack", Parameter: ""},
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
	s := webrtc.SettingEngine{
		LoggerFactory: customLoggerFactory{},
	}
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

func (element *Muxer) getAudioTrack() (*mediadevices.AudioTrack, error) {
	//var deviceID = ""
	devices := mediadevices.EnumerateDevices()

	for _, device := range devices {
		if device.Kind == mediadevices.AudioOutput {
			log.Printf("Found Speaker: %s", device.Name)
			//deviceID = device.DeviceID
		}
	}

	s, err := mediadevices.GetUserMedia(mediadevices.MediaStreamConstraints{
		Audio: func(c *mediadevices.MediaTrackConstraints) {
			//c.DeviceID = prop.String(deviceID)
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

func (element *Muxer) WriteHeader(
	ID string,
	Room string,
	Pin string,
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

	audioTrack, err := element.getAudioTrack()
	if err != nil {
		return "Can not find audio track", err
	}
	_, err = peerConnection.AddTransceiverFromTrack(audioTrack, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly})
	if err != nil {
		return "Add audio track failed", err
	}

	peerConnection.OnICEConnectionStateChange(func(connectionState webrtc.ICEConnectionState) {
		element.status = connectionState
		log.Println("OnICEConnectionStateChange: ", connectionState)
	})
	peerConnection.OnConnectionStateChange(func(connectionState webrtc.PeerConnectionState) {
		element.pcStatus = connectionState
		log.Println("OnConnectionStateChange: ", connectionState)
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
		return "", errors.New("GatherCompletePromise wait")
	case <-gatherCompletePromise:
		//Connected
		break
	}

	// Connect to janus, set remote sdp.
	return element.connectJanusAndSendMsgs(ID, Room, Pin, Janus, Display, peerConnection)
}

// Connect to Janus server & join the video room
func (element *Muxer) connectJanusAndSendMsgs(
	ID string,
	Room string,
	Pin string,
	Janus string,
	Display string, pc *webrtc.PeerConnection) (string, error) {
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
		"audio":   true,
		"video":   false,
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

func (element *Muxer) Close() {
	if element.pcStatus == webrtc.PeerConnectionStateNew ||
		element.pcStatus == webrtc.PeerConnectionStateConnecting {
		log.Println("PeerConnection pending... ignore close cmd.")
		return
	}
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

	if element.pc != nil {
		element.CloseAudioDriverIfNecessary()

		log.Println("Closing pc...")
		err := element.pc.Close()
		if err != nil {
			log.Println("Close pc failed", err)
		}
		element.pc = nil
		log.Println("Close pc finished")
	}
}

func (element *Muxer) CloseAudioDriverIfNecessary() {
	log.Println("Close driver starting...")
	audioDrivers := driver.GetManager().Query(driver.FilterAudioRecorder())
	for _, d := range audioDrivers {
		if d.Status() != driver.StateClosed {
			err := d.Close()
			if err != nil {
				log.Println("Close driver failed:", err)
			}
		}
	}

	log.Println("Close driver finished")
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

func (element *Muxer) janusEventsHandle(handle *janus.Handle) {
	// wait for event
	for {
		msg := <-handle.Events
		switch msg := msg.(type) {
		case *janus.SlowLinkMsg:
			log.Println("SlowLinkMsg type, user:", handle.User)
		case *janus.MediaMsg:
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
