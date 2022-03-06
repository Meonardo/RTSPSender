package main

import (
	"RTSPSender/internal/webrtc"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
	"io/ioutil"
	"log"
	"net/http"
	"os/exec"
	"runtime"
)

const port = ":9001"

func serveHTTP() {
	log.Printf("Staring HTTP server at port %s\n", port)
	gin.SetMode(gin.ReleaseMode)

	router := gin.Default()
	router.Use(CORSMiddleware())

	router.POST("/camera/push/stop", Stop)
	router.POST("/camera/push/start", Start)
	router.POST("/camera/push/configure", Configure)

	err := router.Run(port)
	if err != nil {
		log.Fatalln("Start HTTP Server error", err)
	}
}

func Configure(c *gin.Context) {
	configs := c.PostForm("configs")
	if len(configs) == 0 {
		MakeResponse(false, -1, "Missing mandatory field `configs`!", c)
		return
	}
	log.Println("Configure Request, params: ", configs)

	var jsonMap map[string]interface{}
	err := json.Unmarshal([]byte(configs), &jsonMap)
	if err != nil {
		MakeResponse(false, -2, "Decode JSON object failed!", c)
		return
	}

	server := jsonMap["server"].(map[string]interface{})

	janus := server["janus"].(string)
	if len(janus) == 0 {
		MakeResponse(false, -3, "Missing janus server address", c)
		return
	}

	ices := server["ice_servers"].([]interface{})
	iceServers := make([]string, len(ices))
	for i, v := range ices {
		iceServers[i] = v.(string)
	}

	iceUsername := server["ice_username"].(string)
	icePasswd := server["ice_credential"].(string)

	if len(iceServers) == 0 || len(iceUsername) == 0 || len(icePasswd) == 0 {
		MakeResponse(false, -4, "Missing ICE servers", c)
		return
	}

	ser := ServerST{
		janus,
		port,
		iceServers,
		iceUsername,
		icePasswd,
	}
	if ser.Janus == Config.Server.Janus {
		MakeResponse(false, -6, "Please do NOT configure twice!", c)
		return
	} else {
		Config.Server = ser
	}

	if len(Config.Streams) == 0 {
		Config.Streams = make(map[string]StreamST)
	}

	var failedTimes = 0
	streams := jsonMap["streams"].([]interface{})
	for _, s := range streams {
		stream := s.(map[string]interface{})
		id := stream["id"].(string)
		rtsp := stream["url"].(string)
		if len(id) == 0 || len(rtsp) == 0 {
			failedTimes++
			continue
		}
		if _, ok := Config.Streams[id]; !ok {
			Config.Streams[id] = StreamST{
				ID:           id,
				URL:          rtsp,
				OnDemand:     false,
				DisableAudio: true,
				Cl:           make(map[string]viewer),
			}
		}
	}
	if failedTimes > 0 {
		MakeResponse(false, -5, "Please configure RTSP camera id & URL", c)
		return
	}

	// 预先启动 RTSP worker
	//go serveStreams()

	MakeResponse(true, 1, fmt.Sprintf("Configure {%s} successfully!", configs), c)
}

func Start(c *gin.Context) {
	room := c.PostForm("room")
	if len(room) == 0 {
		MakeResponse(false, -5, "Please input room number", c)
		return
	}

	id := c.PostForm("id")
	if len(id) == 0 {
		MakeResponse(false, -5, "Please input camera ID", c)
		return
	}
	if Config.ext(id) {
		MakeResponse(false, -8, fmt.Sprintf("Camera ID %s is currently publishing!", id), c)
	}

	display := c.PostForm("display")
	if len(display) == 0 {
		MakeResponse(false, -4, "Please input display name", c)
		return
	}

	mic := c.PostForm("mic")

	if runtime.GOOS == "windows" && len(mic) > 0 {
		/// Recording mic only supports Windows
		out, err := exec.Command("gst-device-monitor-1.0", "Audio/Source").Output()
		if err != nil {
			log.Println("Read gst-device-monitor-1.0 cmd error:", err)
		}

		output, _ := GbkToUtf8(out)

		devices := GstDevicesFromCLI(string(output))
		micID := FindWASAPIMicGUID(mic, devices)

		if len(micID) == 0 {
			MakeResponse(false, -7, "Invalidate microphone device name!", c)
			return
		}
		mic = micID
	}

	if !Config.UpdateStream(id, room, display, mic) {
		MakeResponse(false, -5, fmt.Sprintf("Camera(%s) not config yet!", id), c)
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

	MakeResponse(true, 1, fmt.Sprintf("Publish camera %s in Room %s successfully!", id, room), c)
}

func Stop(c *gin.Context) {
	id := c.PostForm("id")
	if !Config.ext(id) {
		MakeResponse(true, 1, fmt.Sprintf("Camera ID %s not exist!", id), c)
	}

	stream := Config.Streams[id]
	// destroy webrtc client
	if stream.WebRTC != nil {
		log.Printf("Destroying (%s) WebRTC resource\n", stream.ID)
		stream.WebRTC.Close()
		stream.WebRTC = nil
		Config.Streams[id] = stream
	} else {
		log.Printf("Destroy (%s) WebRTC resource failed: client does not exist! exec anyway\n", stream.ID)
	}
	//delete(Config.Streams, id)

	MakeResponse(true, 1, fmt.Sprintf("Stop ID %s successfully!", id), c)
}

func MakeResponse(success bool, code int, data string, c *gin.Context) {
	var state = 1
	if !success {
		state = code
	}
	log.Printf("*[Response, Success: (%t), Code: (%d), Msg: (%s)]*\n", success, code, data)
	c.JSON(http.StatusOK, gin.H{"state": state, "code": data})
}

//StreamWebRTC stream video over WebRTC
func StreamWebRTC(uuid string) (string, error) {
	if !Config.ext(uuid) {
		return "", errors.New(fmt.Sprintf("Stream %s NOT found", uuid))
	}

	stream := Config.Streams[uuid]
	//Config.RunIFNotRun(uuid)

	//codecs := Config.coGe(uuid)
	//if codecs == nil {
	//	return "", errors.New(fmt.Sprintf("Stream %s Codec NOT found", uuid))
	//}
	muxerWebRTC := webrtc.NewMuxer(webrtc.Options{
		ICEServers:    Config.GetICEServers(),
		ICEUsername:   Config.GetICEUsername(),
		ICECredential: Config.GetICECredential(),
	})

	msg, err := muxerWebRTC.WriteHeader(stream.URL,
		Config.Server.Janus,
		stream.Room,
		stream.ID,
		stream.Display,
		stream.Mic,
		stream.Pin)
	if err != nil {
		return msg, err
	}

	Config.AddRTC2Stream(uuid, muxerWebRTC)

	//go func() {
	//	cid, ch := Config.clAd(uuid)
	//
	//	//defer reconnect(uuid)
	//	defer Config.clDe(uuid, cid)
	//	defer muxerWebRTC.Close()
	//
	//	var videoStart bool
	//	noVideo := time.NewTimer(10 * time.Second)
	//	for {
	//		select {
	//		case <-noVideo.C:
	//			log.Printf("Stream %s no video...", uuid)
	//			return
	//		case pck := <-ch:
	//			if pck.IsKeyFrame {
	//				noVideo.Reset(10 * time.Second)
	//				videoStart = true
	//			}
	//			if !videoStart {
	//				continue
	//			}
	//			err = muxerWebRTC.WritePacket(pck)
	//			if err != nil {
	//				log.Printf("Stream %s writePacket error: %s", uuid, err)
	//				return
	//			}
	//		}
	//	}
	//}()

	return "", nil
}

func reconnect(uuid string) {
	log.Println("Prepare to reconnect: ", uuid)

	msg, err := StreamWebRTC(uuid)

	if err != nil {
		log.Printf("Reconnect error: %s, msg: %s", err, msg)
	}
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

func GbkToUtf8(s []byte) ([]byte, error) {
	reader := transform.NewReader(bytes.NewReader(s), simplifiedchinese.GBK.NewDecoder())
	d, e := ioutil.ReadAll(reader)
	if e != nil {
		return nil, e
	}
	return d, nil
}
