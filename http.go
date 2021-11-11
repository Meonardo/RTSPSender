package main

import (
	"RTSPSender/internal/webrtc"
	"encoding/json"
	"fmt"
	webrtc2 "github.com/pion/webrtc/v3"
	"log"
	"net/http"
	"sort"
	"time"

	"github.com/deepch/vdk/av"
	"github.com/gin-gonic/gin"
)

type JCodec struct {
	Type string
}

type Turn struct {
	URL		string
	User	string
	Passwd	string
}

type Client struct {
	debug 	int
	
	Room	string
	RTSP	string
	Display	string
	ID		string
	Janus	string
	Mic string

	Turn Turn
	Stun string
	PeerConnection *webrtc2.PeerConnection
}

var clients []Client

func serveHTTP() {
	gin.SetMode(gin.DebugMode)

	router := gin.Default()
	router.Use(CORSMiddleware())

	router.POST("/camera/push/stop", Stop)
	router.POST("/camera/push/start", Start)

	var HTTPPort = "9001"
	err := router.Run(HTTPPort)
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

	var client = Client{Room: room, RTSP: RTSP, Display: display, ID: id, Janus: janus, Mic: mic}
	turnServer := c.PostForm("turn_server")
	turnPasswd := c.PostForm("turn_passwd")
	turnUser := c.PostForm("turn_user")

	stunServer := c.PostForm("stun_server")
	if len(stunServer) > 0 {
		client.Stun = stunServer
	}

	if len(turnServer) > 0 && len(turnPasswd) > 0 && len(turnUser) > 0 {
		turn := Turn{turnServer, turnUser,turnPasswd}
		client.Turn = turn
	}

	Config.Server = ServerST{
		room,
		"9001",
		[]string{stunServer, turnServer},
		turnUser,
		turnPasswd,
	}
	if _, ok := Config.Streams[id]; !ok {
		Config.Streams[id] = StreamST{
			URL:      RTSP,
			Mic: 	  mic,
			OnDemand: true,
			DisableAudio: true,
			Cl:       make(map[string]viewer),
		}
	} else {
		MakeResponse(false, -7, "Stream already published", c)
		return
	}

	clients = append(clients, client)
	MakeResponse(true, 1, fmt.Sprintf("Publish RTSP %s in Room %s successfully!", RTSP, room), c)
}

func Stop(c *gin.Context) {
	id := c.PostForm("id")

	if _, ok := Config.Streams[id]; !ok {
		message := fmt.Sprintf("Can not found ID %s", id)
		MakeResponse(false, -1, message, c)
		return
	}
	message := fmt.Sprintf("Stop ID %s successfully!", id)
	MakeResponse(true, 1, message, c)
}

func MakeResponse(success bool, code int, data string, c *gin.Context) {
	var state = 1
	if !success {
		state = code
	}
	c.JSON(http.StatusOK, gin.H{"state": state, "code": data})
}

//HTTPAPIServerIndex  index
func HTTPAPIServerIndex(c *gin.Context) {
	_, all := Config.list()
	if len(all) > 0 {
		c.Header("Cache-Control", "no-cache, max-age=0, must-revalidate, no-store")
		c.Header("Access-Control-Allow-Origin", "*")
		c.Redirect(http.StatusMovedPermanently, "stream/player/"+all[0])
	} else {
		c.HTML(http.StatusOK, "index.tmpl", gin.H{
			"port":    Config.Server.HTTPPort,
			"version": time.Now().String(),
		})
	}
}

//HTTPAPIServerStreamPlayer stream player
func HTTPAPIServerStreamPlayer(c *gin.Context) {
	_, all := Config.list()
	sort.Strings(all)
	c.HTML(http.StatusOK, "player.tmpl", gin.H{
		"port":     Config.Server.HTTPPort,
		"suuid":    c.Param("uuid"),
		"suuidMap": all,
		"version":  time.Now().String(),
	})
}

//HTTPAPIServerStreamCodec stream codec
func HTTPAPIServerStreamCodec(c *gin.Context) {
	if Config.ext(c.Param("uuid")) {
		Config.RunIFNotRun(c.Param("uuid"))
		codecs := Config.coGe(c.Param("uuid"))
		if codecs == nil {
			return
		}
		var tmpCodec []JCodec
		for _, codec := range codecs {
			if codec.Type() != av.H264 && codec.Type() != av.PCM_ALAW && codec.Type() != av.PCM_MULAW && codec.Type() != av.OPUS {
				log.Println("Codec Not Supported WebRTC ignore this track", codec.Type())
				continue
			}
			if codec.Type().IsVideo() {
				tmpCodec = append(tmpCodec, JCodec{Type: "video"})
			} else {
				tmpCodec = append(tmpCodec, JCodec{Type: "audio"})
			}
		}
		b, err := json.Marshal(tmpCodec)
		if err == nil {
			_, err = c.Writer.Write(b)
			if err != nil {
				log.Println("Write Codec Info error", err)
				return
			}
		}
	}
}

//HTTPAPIServerStreamWebRTC stream video over WebRTC
func HTTPAPIServerStreamWebRTC(c *gin.Context, suuid string) {
	if !Config.ext(suuid) {
		log.Println("Stream Not Found")
		return
	}
	Config.RunIFNotRun(suuid)
	codecs := Config.coGe(suuid)
	if codecs == nil {
		log.Println("Stream Codec Not Found")
		return
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
	answer, err := muxerWebRTC.WriteHeader(codecs, c.PostForm("data"))
	if err != nil {
		log.Println("WriteHeader", err)
		return
	}
	_, err = c.Writer.Write([]byte(answer))
	if err != nil {
		log.Println("Write", err)
		return
	}
	go func() {
		cid, ch := Config.clAd(c.PostForm("suuid"))
		defer Config.clDe(c.PostForm("suuid"), cid)
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

type Response struct {
	Tracks []string `json:"tracks"`
	Sdp64  string   `json:"sdp64"`
}

type ResponseError struct {
	Error  string   `json:"error"`
}

func HTTPAPIServerStreamWebRTC2(c *gin.Context) {
	url := c.PostForm("url")
	if _, ok := Config.Streams[url]; !ok {
		Config.Streams[url] = StreamST{
			URL:      url,
			OnDemand: true,
			Cl:       make(map[string]viewer),
		}
	}

	Config.RunIFNotRun(url)

	codecs := Config.coGe(url)
	if codecs == nil {
		log.Println("Stream Codec Not Found")
		c.JSON(500, ResponseError{Error: Config.LastError.Error()})
		return
	}

	muxerWebRTC := webrtc.NewMuxer(
		webrtc.Options{
			ICEServers: Config.GetICEServers(),
		},
	)

	sdp64 := c.PostForm("sdp64")
	answer, err := muxerWebRTC.WriteHeader(codecs, sdp64)
	if err != nil {
		log.Println("Muxer WriteHeader", err)
		c.JSON(500, ResponseError{Error: err.Error()})
		return
	}

	response := Response{
		Sdp64: answer,
	}

	for _, codec := range codecs {
		if codec.Type() != av.H264 &&
			codec.Type() != av.PCM_ALAW &&
			codec.Type() != av.PCM_MULAW &&
			codec.Type() != av.OPUS {
			log.Println("Codec Not Supported WebRTC ignore this track", codec.Type())
			continue
		}
		if codec.Type().IsVideo() {
			response.Tracks = append(response.Tracks, "video")
		} else {
			response.Tracks = append(response.Tracks, "audio")
		}
	}

	c.JSON(200, response)

	AudioOnly := len(codecs) == 1 && codecs[0].Type().IsAudio()

	go func() {
		cid, ch := Config.clAd(url)
		defer Config.clDe(url, cid)
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
}
