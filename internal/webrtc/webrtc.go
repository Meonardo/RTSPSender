package webrtc

import (
	"RTSPSender/internal/janus"
	"RTSPSender/mediadevices"
	"bytes"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/webrtc/v3"

	"RTSPSender/mediadevices/pkg/codec/opus"          // This is required to use opus audio encoder
	_ "RTSPSender/mediadevices/pkg/driver/microphone" // This is required to register microphone adapter
	"github.com/deepch/vdk/av"
	"github.com/deepch/vdk/codec/h264parser"
	"github.com/pion/webrtc/v3/pkg/media"
)

var (
	ErrorNotFound          = errors.New("WebRTC Stream Not Found")
	ErrorCodecNotSupported = errors.New("WebRTC Codec Not Supported")
	ErrorClientOffline     = errors.New("WebRTC Client Offline")
	ErrorNotTrackAvailable = errors.New("WebRTC Not Track Available")
	ErrorIgnoreAudioTrack  = errors.New("WebRTC Ignore Audio Track codec not supported WebRTC support only PCM_ALAW or PCM_MULAW")
)

type Muxer struct {
	streams   map[int8]*Stream
	status    webrtc.ICEConnectionState
	stop      bool
	pc        *webrtc.PeerConnection
	audioCodecSelector *mediadevices.CodecSelector
	ClientACK *time.Timer
	StreamACK *time.Timer
	Options Options
	Janus   *janus.Gateway
}

type Stream struct {
	codec av.CodecData
	track *webrtc.TrackLocalStaticSample
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

func watchHandle(handle *janus.Handle) {
	// wait for event
	for {
		msg := <-handle.Events
		switch msg := msg.(type) {
		case *janus.SlowLinkMsg:
			log.Println("SlowLinkMsg type ", handle.ID)
		case *janus.MediaMsg:
			log.Println("MediaEvent type", msg.Type, " receiving ", msg.Receiving)
		case *janus.WebRTCUpMsg:
			log.Println("WebRTCUp type ", handle.ID)
		case *janus.HangupMsg:
			log.Println("HangupEvent type ", handle.ID)
		case *janus.EventMsg:
			log.Printf("EventMsg %+v", msg.Plugindata.Data)
		}
	}
}

func NewMuxer(options Options) *Muxer {
	tmp := Muxer{Options: options, ClientACK: time.NewTimer(time.Second * 20), StreamACK: time.NewTimer(time.Second * 20), streams: make(map[int8]*Stream)}
	//go tmp.WaitCloser()
	return &tmp
}

func (element *Muxer) NewPeerConnection(configuration webrtc.Configuration) (*webrtc.PeerConnection, error) {
	if len(element.Options.ICEServers) > 0 {
		log.Println("Set ICEServers", element.Options.ICEServers)
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

func (element *Muxer) WriteHeader(streams []av.CodecData, janusServer string,
	room string, id string, display string, mic string) (string, error) {

	var WriteHeaderSuccess bool
	if len(streams) == 0 {
		return "No stream found", ErrorNotFound
	}

	peerConnection, err := element.NewPeerConnection(webrtc.Configuration{
		SDPSemantics: webrtc.SDPSemanticsUnifiedPlanWithFallback,
	})
	if err != nil {
		return "Create pc failed", err
	}
	defer func() {
		if !WriteHeaderSuccess {
			err = element.Close()
			if err != nil {
				log.Println(err)
			}
		}
	}()

	// Create an audio track
	deviceInfo := mediadevices.EnumerateDevices()
	var hasAudio = len(mic) > 0 && mic != "mute"
	if len(deviceInfo) > 0 {
		for _, device := range deviceInfo {
			if device.Kind == mediadevices.AudioInput {
				hasAudio = true
				fmt.Println("Enumerated Audio Device: ", device)
				break
			}
		}
	} else {
		hasAudio = false
	}

	if hasAudio && element.audioCodecSelector != nil {
		s, err := mediadevices.GetUserMedia(mediadevices.MediaStreamConstraints{Codec: element.audioCodecSelector})
		if err != nil {
			return "Audio track create failed", err
		}

		for _, track := range s.GetTracks() {
			track.OnEnded(func(err error) {
				fmt.Printf("Track (ID: %s) ended with error: %v\n",
					track.ID(), err)
			})

			rtpSender, err := peerConnection.AddTransceiverFromTrack(track,
				webrtc.RTPTransceiverInit{
					Direction: webrtc.RTPTransceiverDirectionSendonly,
				},
			)
			if err != nil {
				return "Add audio track create failed", err
			}
			if rtpSender.Kind() == webrtc.RTPCodecTypeAudio {
				break
			}
		}
	}

	for i, i2 := range streams {
		var track *webrtc.TrackLocalStaticSample
		if i2.Type().IsVideo() {
			if i2.Type() == av.H264 {
				track, err = webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{
					MimeType: "video/h264",
				}, "video", "rtsp-video")
				if err != nil {
					return "Create track failed", err
				}
				if _, err = peerConnection.AddTrack(track); err != nil {
					return "Add video track failed", err
				}
			}
		}
		element.streams[int8(i)] = &Stream{track: track, codec: i2}
	}

	if len(element.streams) == 0 {
		return "", ErrorNotTrackAvailable
	}
	peerConnection.OnICEConnectionStateChange(func(connectionState webrtc.ICEConnectionState) {
		element.status = connectionState
		if connectionState == webrtc.ICEConnectionStateDisconnected {
			element.Close()
		}
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
	gateway, err := janus.Connect(janusServer)
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
			if _, keepAliveErr := session.KeepAlive(); keepAliveErr != nil {
				log.Printf("Can not send keep-alive msg to janus %s", keepAliveErr)
				return
			}
			time.Sleep(30 * time.Second)
		}
	}()

	go watchHandle(handle)

	roomNum, err := strconv.Atoi(room)
	publisherID, err := strconv.Atoi(id)
	if err != nil {
		roomNum = 1234
		log.Printf("Room number invalid %s", err)
	}

	_, err = handle.Message(map[string]interface{}{
		"request": "join",
		"ptype":   "publisher",
		"room":    roomNum,
		"id":      publisherID,
		"display":	display,
		"pin": room,
	}, nil)
	if err != nil {
		return fmt.Sprintf("Join room %s failed", room), err
	}

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
		return fmt.Sprintf("Publish to room %s failed", room), err
	}

	if msg.Jsep != nil {
		err = peerConnection.SetRemoteDescription(webrtc.SessionDescription{
			Type: webrtc.SDPTypeAnswer,
			SDP:  msg.Jsep["sdp"].(string),
		})
		if err != nil {
			return fmt.Sprintf("No remote sdp found %s error", room), err
		}
		WriteHeaderSuccess = true

		return "", nil
	} else {
		return fmt.Sprintf("No JSEP found %s error", room), err
	}
}

func (element *Muxer) WritePacket(pkt av.Packet) (err error) {
	//log.Println("WritePacket", pkt.Time, element.stop, webrtc.ICEConnectionStateConnected, pkt.Idx, element.streams[pkt.Idx])
	var WritePacketSuccess bool
	defer func() {
		if !WritePacketSuccess {
			element.Close()
		}
	}()
	if element.stop {
		return ErrorClientOffline
	}
	if element.status == webrtc.ICEConnectionStateChecking {
		WritePacketSuccess = true
		return nil
	}
	if element.status != webrtc.ICEConnectionStateConnected {
		return nil
	}
	if tmp, ok := element.streams[pkt.Idx]; ok {
		element.StreamACK.Reset(10 * time.Second)
		if len(pkt.Data) < 5 {
			return nil
		}
		switch tmp.codec.Type() {
		case av.H264:
			codec := tmp.codec.(h264parser.CodecData)
			if pkt.IsKeyFrame {
				pkt.Data = append([]byte{0, 0, 0, 1}, bytes.Join([][]byte{codec.SPS(), codec.PPS(), pkt.Data[4:]}, []byte{0, 0, 0, 1})...)
			} else {
				pkt.Data = pkt.Data[4:]
			}
		case av.PCM_ALAW:
		case av.OPUS:
		case av.PCM_MULAW:
		case av.AAC:
			//TODO: NEED ADD DECODER AND ENCODER
			return ErrorCodecNotSupported
		case av.PCM:
			//TODO: NEED ADD ENCODER
			return ErrorCodecNotSupported
		default:
			return ErrorCodecNotSupported
		}
		err = tmp.track.WriteSample(media.Sample{Data: pkt.Data, Duration: pkt.Duration})
		if err == nil {
			WritePacketSuccess = true
		}
		return err
	} else {
		WritePacketSuccess = true
		return nil
	}
}

func (element *Muxer) Close() error {
	element.stop = true
	if element.pc != nil {
		err := element.pc.Close()
		if err != nil {
			return err
		}
	}

	if element.Janus != nil {
		err := element.Janus.Close()
		if err != nil {
			return err
		}
	}

	return nil
}
