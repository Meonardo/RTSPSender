package main

import (
	gst "RTSPSender/internal/gstreamer-src"
	janus "RTSPSender/internal/janus"
	"fmt"
	"github.com/pion/interceptor"
	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
	"github.com/pion/webrtc/v3/pkg/media/h264reader"
	"github.com/pion/webrtc/v3/pkg/media/ivfreader"
	"io"
	"log"
	"os"
	"time"
)

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

func main() {
	// Everything below is the pion-WebRTC API! Thanks for using it ❤️.

	// Prepare the configuration
	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{"stun:stun.l.google.com:19302"},
			},
		},
		SDPSemantics: webrtc.SDPSemanticsUnifiedPlanWithFallback,
	}

	// Create a new RTCPeerConnection
	peerConnection, err := NewPeerConnection(config)
	if err != nil {
		panic(err)
	}

	peerConnection.OnICEConnectionStateChange(func(connectionState webrtc.ICEConnectionState) {
		fmt.Printf("Connection State has changed %s \n", connectionState.String())
	})

	// Create an audio track
	//opusTrack, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus}, "audio", "pion")
	//if err != nil {
	//	panic(err)
	//} else if _, err = peerConnection.AddTrack(opusTrack); err != nil {
	//	panic(err)
	//}

	// Create a video track
	videoRTCPFeedback := []webrtc.RTCPFeedback{{"goog-remb", ""}, {"ccm", "fir"}, {"nack", ""}, {"nack", "pli"}}
	h264Codec := webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f", RTCPFeedback: videoRTCPFeedback}
	vp8Track, err := webrtc.NewTrackLocalStaticSample(h264Codec, "video", "pion")
	if err != nil {
		panic(err)
	} else if _, err = peerConnection.AddTrack(vp8Track); err != nil {
		panic(err)
	}

	//const vp8Filename = "/Users/amdox/Downloads/vp8.ivf"
	//loadVP8VideoTrackFromFile(peerConnection, vp8Filename)
	//const h264Filename = "/Users/amdox/Downloads/cam2.h264"
	//loadH264VideoTrackFromFile(peerConnection, h264Filename)

	offer, err := peerConnection.CreateOffer(nil)
	if err != nil {
		panic(err)
	}

	// Create channel that is blocked until ICE Gathering is complete
	gatherComplete := webrtc.GatheringCompletePromise(peerConnection)

	if err = peerConnection.SetLocalDescription(offer); err != nil {
		panic(err)
	}

	// Block until ICE Gathering is complete, disabling trickle ICE
	// we do this because we only can exchange one signaling message
	// in a production application you should exchange ICE Candidates via OnICECandidate
	<-gatherComplete

	gateway, err := janus.Connect("ws://192.168.5.12:8188")
	if err != nil {
		panic(err)
	}

	session, err := gateway.Create()
	if err != nil {
		panic(err)
	}

	handle, err := session.Attach("janus.plugin.videoroom")
	if err != nil {
		panic(err)
	}

	go func() {
		for {
			if _, keepAliveErr := session.KeepAlive(); keepAliveErr != nil {
				panic(keepAliveErr)
			}

			time.Sleep(30 * time.Second)
		}
	}()

	go watchHandle(handle)

	var room = 10001370
	_, err = handle.Message(map[string]interface{}{
		"request": "join",
		"ptype":   "publisher",
		"room":    room,
		"id":      1,
		"display":	"Golang Client",
		"pin": fmt.Sprintf("%d", room),
	}, nil)
	if err != nil {
		panic(err)
	}

	msg, err := handle.Message(map[string]interface{}{
		"request": "publish",
		"audio":   false,
		"video":   true,
		"data":    false,
	}, map[string]interface{}{
		"type":    "offer",
		"sdp":     peerConnection.LocalDescription().SDP,
		"trickle": false,
	})
	if err != nil {
		panic(err)
	}

	if msg.Jsep != nil {
		err = peerConnection.SetRemoteDescription(webrtc.SessionDescription{
			Type: webrtc.SDPTypeAnswer,
			SDP:  msg.Jsep["sdp"].(string),
		})
		if err != nil {
			panic(err)
		}

		// Start pushing buffers on these tracks
		//screenPipeline := "avfvideosrc capture-screen=true ! video/x-raw,framerate=20/1 ! videoconvert ! x264enc tune=zerolatency bitrate=500 speed-preset=superfast key-int-max=20 ! video/x-h264,stream-format=byte-stream !"
		//rtspPipeline := "rtspsrc location=rtsp://192.168.5.159/1 protocols=tcp latency=0 ! qtdemux ! queue ! h264parse ! video/x-h264,alignment=nal,stream-format=byte-stream !"
		//gst.CreatePipeline("opus", []*webrtc.TrackLocalStaticSample{opusTrack}, "audiotestsrc").Start()
		filePipeline := "filesrc location=/Users/amdox/Downloads/cam1.mp4 ! qtdemux ! queue ! h264parse ! video/x-h264,alignment=nal,stream-format=byte-stream !"
		//filePipeline1 := "filesrc location=/Users/amdox/Downloads/cam1.mp4 ! qtdemux ! h264parse ! decodebin ! x264enc tune=zerolatency bitrate=500 speed-preset=superfast key-int-max=20 ! video/x-h264,stream-format=byte-stream !"
		gst.CreatePipeline("h264", []*webrtc.TrackLocalStaticSample{vp8Track}, filePipeline).Start()
	}

	select {}
}

func NewPeerConnection(configuration webrtc.Configuration) (*webrtc.PeerConnection, error) {
	m := &webrtc.MediaEngine{}

	videoRTCPFeedback := []webrtc.RTCPFeedback{{"goog-remb", ""}, {"ccm", "fir"}, {"nack", ""}, {"nack", "pli"}}
	codec:= webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f", RTCPFeedback: videoRTCPFeedback}

	codecParam := webrtc.RTPCodecParameters{
		RTPCodecCapability: codec,
		PayloadType:        102,
	}

	if err := m.RegisterCodec(codecParam, webrtc.RTPCodecTypeVideo); err != nil {
		return nil, err
	}

	i := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(m, i); err != nil {
		return nil, err
	}

	api := webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(i))
	return api.NewPeerConnection(configuration)
}

func loadH264VideoTrackFromFile(pc *webrtc.PeerConnection, filename string) {
	_, err := os.Stat(filename)
	if err != nil {
		panic(err)
	}

	fileExist := !os.IsNotExist(err)
	if fileExist {

		videoRTCPFeedback := []webrtc.RTCPFeedback{{"goog-remb", ""}, {"ccm", "fir"}, {"nack", ""}, {"nack", "pli"}}
		h264Codec := webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f", RTCPFeedback: videoRTCPFeedback}

		videoTrack, videoTrackErr := webrtc.NewTrackLocalStaticSample(h264Codec, "video", "pion")
		if videoTrackErr != nil {
			panic(videoTrackErr)
		}
		rtpSender, videoTrackErr := pc.AddTrack(videoTrack)
		if videoTrackErr != nil {
			panic(videoTrackErr)
		}

		go func() {
			rtcpBuf := make([]byte, 1500)
			for {
				if _, _, rtcpErr := rtpSender.Read(rtcpBuf); rtcpErr != nil {
					return
				}
			}
		}()

		const h264FrameDuration = time.Millisecond * 33

		go func() {
			file, h264Err := os.Open(filename)
			if h264Err != nil {
				panic(h264Err)
			}

			reader, h264Err := h264reader.NewReader(file)
			if h264Err != nil {
				panic(h264Err)
			}

			ticker := time.NewTicker(h264FrameDuration)
			for ; true; <-ticker.C {
				nal, h264Err := reader.NextNAL()

				if h264Err == io.EOF {
					fmt.Printf("Read file fininshed! exit program.")
					os.Exit(0)
				}

				if h264Err != nil {
					panic(h264Err)
				}

				if h264Err = videoTrack.WriteSample(media.Sample{Data: nal.Data, Duration: time.Second}); h264Err != nil {
					panic(h264Err)
				}
			}
		}()
	}
}

func loadVP8VideoTrackFromFile(pc *webrtc.PeerConnection, filename string) {
	_, err := os.Stat(filename)
	if err != nil {
		panic(err)
	}

	fileExist := !os.IsNotExist(err)
	if fileExist {
		// Create a video track
		videoTrack, videoTrackErr := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8}, "video", "pion")
		if videoTrackErr != nil {
			panic(videoTrackErr)
		}

		rtpSender, videoTrackErr := pc.AddTrack(videoTrack)
		if videoTrackErr != nil {
			panic(videoTrackErr)
		}

		go func() {
			rtcpBuf := make([]byte, 1500)
			for {
				if _, _, rtcpErr := rtpSender.Read(rtcpBuf); rtcpErr != nil {
					return
				}
			}
		}()

		go func() {
			// Open a IVF file and start reading using our IVFReader
			file, ivfErr := os.Open(filename)
			if ivfErr != nil {
				panic(ivfErr)
			}

			ivf, header, ivfErr := ivfreader.NewWith(file)
			if ivfErr != nil {
				panic(ivfErr)
			}

			ticker := time.NewTicker(time.Millisecond * time.Duration((float32(header.TimebaseNumerator)/float32(header.TimebaseDenominator))*1000))
			for ; true; <-ticker.C {
				frame, _, ivfErr := ivf.ParseNextFrame()
				if ivfErr == io.EOF {
					fmt.Printf("All video frames parsed and sent")
					os.Exit(0)
				}

				if ivfErr != nil {
					panic(ivfErr)
				}

				if ivfErr = videoTrack.WriteSample(media.Sample{Data: frame, Duration: time.Second}); ivfErr != nil {
					panic(ivfErr)
				}
			}
		}()
	}
}