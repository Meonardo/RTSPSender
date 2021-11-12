package main

import (
	"RTSPSender/internal/webrtc"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

const port = ":9001"

func serveHTTP() {
	log.Printf("Staring HTTP server at port %s\n", port)
	gin.SetMode(gin.DebugMode)

	router := gin.Default()
	router.Use(CORSMiddleware())

	router.POST("/camera/push/stop", Stop)
	router.POST("/camera/push/start", Start)

	err := router.Run(port)
	if err != nil {
		log.Fatalln("Start HTTP Server error", err)
	}
}

func Start(c *gin.Context) {
	room := c.PostForm("room")
	if len(room) == 0 {
		MakeResponse(false, -5, "Please input room number", c)
		return
	}

	id := c.PostForm("id")
	if len(id) == 0 {
		MakeResponse(false, -5, "Please input display name", c)
		return
	}

	display := c.PostForm("display")
	if len(display) == 0 {
		MakeResponse(false, -4, "Please input display name", c)
		return
	}
	var mic = "mute"
	mic = c.PostForm("mic")

	RTSP := c.PostForm("rtsp")
	if len(RTSP) == 0 {
		MakeResponse(false, -3, "Please input RTSP stream address", c)
		return
	}

	janus := c.PostForm("janus")
	if len(janus) == 0 {
		MakeResponse(false, -2, "Please input janus server address", c)
		return
	}

	turnServer := c.PostForm("turn_server")
	turnPasswd := c.PostForm("turn_passwd")
	turnUser := c.PostForm("turn_user")
	stunServer := c.PostForm("stun_server")

	if len(turnServer) == 0 || len(turnPasswd) == 0 || len(turnUser) == 0 {
		MakeResponse(false, -2, "Please set ICE servers", c)
		return
	}

	Config.Server = ServerST{
		room,
		janus,
		port,
		[]string{stunServer, turnServer},
		turnUser,
		turnPasswd,
	}

	if len(Config.Streams) == 0 {
		Config.Streams = make(map[string]StreamST)
	}

	if _, ok := Config.Streams[id]; !ok {
		Config.Streams[id] = StreamST{
			ID: 	  id,
			URL:      RTSP,
			Display:  display,
			Mic: 	  mic,
			OnDemand: true,
			DisableAudio: true,
			Cl:       make(map[string]viewer),
		}
	} else {
		MakeResponse(false, -7, "Stream already published", c)
		return
	}

	msg, err := StreamWebRTC(id)

	if err != nil {
		if len(msg) == 0 {
			msg += "janus error: " + err.Error()
		} else {
			msg += ", " + err.Error()
		}
		MakeResponse(false, -9, msg, c)
		return
	}

	MakeResponse(true, 1, fmt.Sprintf("Publish RTSP %s in Room %s successfully!", RTSP, room), c)
}

func Stop(c *gin.Context) {
	id := c.PostForm("id")

	if stream, ok := Config.Streams[id]; ok {
		if stream.WebRTC == nil {
			const message = "Destroy WebRTC resource failed: client does not exist!"
			MakeResponse(true, 1, message, c)
			return
		}
		err := stream.WebRTC.Close()
		if err != nil {
			var message = fmt.Sprintf("Destroy WebRTC resource failed: %s", err)
			MakeResponse(true, 1, message, c)
			return
		}
		delete(Config.Streams, id)

		message := fmt.Sprintf("Stop ID %s successfully!", id)
		MakeResponse(true, 1, message, c)
		return
	}

	message := fmt.Sprintf("ID %s not found!", id)
	MakeResponse(false, -1, message, c)
	return
}

func MakeResponse(success bool, code int, data string, c *gin.Context) {
	var state = 1
	if !success {
		state = code
	}
	c.JSON(http.StatusOK, gin.H{"state": state, "code": data})
}

//StreamWebRTC stream video over WebRTC
func StreamWebRTC(uuid string) (string, error) {
	if !Config.ext(uuid) {
		return "", errors.New("stream Not Found")
	}
	stream := Config.Streams[uuid]

	Config.RunIFNotRun(uuid)
	codecs := Config.coGe(uuid)
	if codecs == nil {
		return "", errors.New("stream Codec Not Found")
	}
	var AudioOnly bool
	if len(codecs) == 1 && codecs[0].Type().IsAudio() {
		AudioOnly = true
	}
	muxerWebRTC := webrtc.NewMuxer(webrtc.Options{
		ICEServers: Config.GetICEServers(),
		ICEUsername: Config.GetICEUsername(),
		ICECredential: Config.GetICECredential(),
	})

	msg, err := muxerWebRTC.WriteHeader(codecs, Config.Server.Janus, Config.Server.Room, stream.ID, stream.Display)
	if err != nil {
		return msg, err
	}

	stream.WebRTC = muxerWebRTC
	Config.Streams[uuid] = stream

	go func() {
		cid, ch := Config.clAd(uuid)
		defer Config.clDe(uuid, cid)
		defer muxerWebRTC.Close()
		var videoStart bool
		noVideo := time.NewTimer(10 * time.Second)
		for {
			select {
			case <-noVideo.C:
				log.Println("noVideo")
				return
			case pck := <-ch:
				if pck.IsKeyFrame || AudioOnly {
					noVideo.Reset(10 * time.Second)
					videoStart = true
				}
				if !videoStart && !AudioOnly {
					continue
				}
				err = muxerWebRTC.WritePacket(pck)
				if err != nil {
					log.Println("WritePacket", err)
					return
				}
			}
		}
	}()

	return "", nil
}

func CORSMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Credentials", "true")
		c.Header("Access-Control-Allow-Headers", "Origin, X-Requested-With, Content-Type, Accept, Authorization, x-access-token")
		c.Header("Access-Control-Expose-Headers", "Content-Length, Access-Control-Allow-Origin, Access-Control-Allow-Headers, Cache-Control, Content-Language, Content-Type")
		c.Header("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}

