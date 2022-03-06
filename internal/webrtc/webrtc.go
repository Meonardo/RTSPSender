package webrtc

import (
	gst "RTSPSender/internal/gstreamer-src"
	"RTSPSender/internal/janus"
	"errors"
	"fmt"
	"github.com/deepch/vdk/codec/h264parser"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"log"
	"strconv"
	"time"

	"github.com/aler9/gortsplib"
	"github.com/pion/dtls/v2/pkg/protocol/extension"
	"github.com/pion/interceptor"
	"github.com/pion/webrtc/v3"

	"github.com/deepch/vdk/av"
	"github.com/pion/webrtc/v3/pkg/media"
)

var (
	ErrorNotFound          = errors.New("WebRTC Stream Not Found")
	ErrorCodecNotSupported = errors.New("WebRTC Codec Not Supported")
	ErrorClientOffline     = errors.New("WebRTC Client Offline")
	ErrorNotTrackAvailable = errors.New("WebRTC Not Track Available")
)

type Muxer struct {
	streams       map[int8]*Stream
	status        webrtc.ICEConnectionState
	stop          bool
	pc            *webrtc.PeerConnection
	audioPipeline *gst.Pipeline
	videoPipeline *gst.Pipeline

	ClientACK *time.Timer
	StreamACK *time.Timer
	Options   Options
	Janus     *janus.Gateway
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
			log.Println("SlowLinkMsg type, user:", handle.User)
		case *janus.MediaMsg:
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
	s.SetSRTPProtectionProfiles(extension.SRTP_AES128_CM_HMAC_SHA1_80)

	if element.Options.PortMin > 0 && element.Options.PortMax > 0 && element.Options.PortMax > element.Options.PortMin {
		err := s.SetEphemeralUDPPortRange(element.Options.PortMin, element.Options.PortMax)
		if err != nil {
			return nil, err
		}
		log.Println("Set UDP ports to", element.Options.PortMin, "..", element.Options.PortMax)
	}

	api := webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(i), webrtc.WithSettingEngine(s))
	return api.NewPeerConnection(configuration)
}

func (element *Muxer) WriteHeader(rtsp string, janusServer string,
	room string, id string, display string, mic string, pin string) (string, error) {

	peerConnection, err := element.NewPeerConnection(webrtc.Configuration{
		SDPSemantics: webrtc.SDPSemanticsUnifiedPlanWithFallback,
	})
	if err != nil {
		return "Create pc failed", err
	}

	var hasAudio = len(mic) > 0 && mic != "mute"
	if hasAudio {
		// Create an audio track
		audioTrack, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus}, "audio", "microphone")
		if err != nil {
			return "Audio track create failed", err
		}
		if _, err = peerConnection.AddTrack(audioTrack); err != nil {
			return "Add audio track failed", err
		}

		var audioPipelineDesc = fmt.Sprintf("wasapisrc device=\"%s\" ! queue ! audioconvert ! audioresample", mic)
		audioPipeline := gst.CreatePipeline("opus", []*webrtc.TrackLocalStaticSample{audioTrack}, audioPipelineDesc)
		audioPipeline.Start()
		element.audioPipeline = audioPipeline
	}

	// Create a video track
	videoTrack, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264}, "video", "rtsp")
		//webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264}, "video", "rtsp")
	if err != nil {
		return "Create video track failed", err
	} else if _, err = peerConnection.AddTrack(videoTrack); err != nil {
		return "Add video track failed", err
	}

	// rtspsrc location=rtsp://192.168.100.234/1 latency=0 ! rtph264depay ! queue ! h264parse ! video/x-h264,alignment=nal,stream-format=byte-stream ! appsink emit-signals=True name=h264_sink
	//var videoPipelineDesc = fmt.Sprintf("rtspsrc location=%s latency=0 ! queue ! rtph264depay ! h264parse ! video/x-h264,alignment=nal,stream-format=byte-stream", rtsp)
	//videoPipeline := gst.CreatePipeline("h264", []*webrtc.TrackLocalStaticSample{videoTrack}, videoPipelineDesc)
	//videoPipeline.Start()
	//element.videoPipeline = videoPipeline

	// Connect to RTSP Camera
	element.connectRTSPCamera(rtsp, videoTrack)

	//if len(element.streams) == 0 {
	//	return "", ErrorNotTrackAvailable
	//}
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
		"display": display,
		"pin":     pin,
	}, nil)
	if err != nil {
		return fmt.Sprintf("Join room %s failed", room), err
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

		return "", nil
	} else {
		return fmt.Sprintf("No JSEP found %s error", room), err
	}
}

func (element *Muxer) connectRTSPCamera(rtsp string, track *webrtc.TrackLocalStaticRTP) {
	go func() {
		c := gortsplib.Client{
			// called when a RTP packet arrives
			OnPacketRTP: func(trackID int, pkt *rtp.Packet) {
				err :=  track.WriteRTP(pkt)
				if err != nil {
					fmt.Println("Write RTP pkt error: ", err)
				}
				//log.Printf("RTP packet from track %d, payload type %d\n", trackID, pkt.Header.PayloadType)
			},
			// called when a RTCP packet arrives
			OnPacketRTCP: func(trackID int, pkt rtcp.Packet) {
				log.Printf("RTCP packet from track %d, type %T\n", trackID, pkt)
			},
		}
		// connect to the server and start reading all tracks
		panic(c.StartReadingAndWait(rtsp))
	}()
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
		if tmp.codec.Type() == av.H264  {
			codec := tmp.codec.(h264parser.CodecData)
			pkt.Data = pkt.Data[4:]

			if pkt.IsKeyFrame {
				pkt.Data = append([]byte{0, 0, 0, 1}, pkt.Data...)
				pkt.Data = append(codec.SPS(), pkt.Data...)
				pkt.Data = append([]byte{0, 0, 0, 1}, pkt.Data...)
				pkt.Data = append(codec.PPS(), pkt.Data...)
				pkt.Data = append([]byte{0, 0, 0, 1}, pkt.Data...)
			}
		} else {
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

	if element.audioPipeline != nil {
		element.audioPipeline.Stop()
		element.audioPipeline = nil
	}
	if element.videoPipeline != nil {
		element.videoPipeline.Stop()
		element.videoPipeline = nil
	}

	if element.pc != nil {
		err := element.pc.Close()
		if err != nil {
			log.Println("Close pc failed", err)
		}
		element.pc = nil
	}
}
