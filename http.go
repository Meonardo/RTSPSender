package main

import (
	"RTSPSender/internal/config"
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

const port = ":9981"

func serveHTTP() {
	log.Printf("Staring HTTP server at port %s\n", port)
	gin.SetMode(gin.ReleaseMode)

	router := gin.Default()
	router.Use(CORSMiddleware())

	router.POST("/camera/push/stop", Stop)
	router.POST("/camera/push/start", Start)

	config.Config.Clients = make(map[string]config.RTSPClient)

	err := router.Run(port)
	if err != nil {
		log.Fatalln("Start HTTP Server error", err)
	}
}

func Start(c *gin.Context) {
	configs := c.PostForm("configs")
	if len(configs) == 0 {
		MakeResponse(false, -1, "Missing mandatory field `configs`!", c)
		return
	}
	log.Println("Configure Request, params: ", configs)

	var client config.RTSPClient
	err := json.Unmarshal([]byte(configs), &client)
	if err != nil {
		MakeResponse(false, -2, "Decode JSON object failed!", c)
		return
	}

	room := client.Room
	if len(room) == 0 {
		MakeResponse(false, -5, "Please input room number", c)
		return
	}
	id := client.ID
	if len(id) == 0 {
		MakeResponse(false, -5, "Please input camera ID", c)
		return
	}
	uuid := room + "_" + id
	if config.Config.Exist(uuid) {
		MakeResponse(false, -8, fmt.Sprintf("Camera ID %s is currently publishing!", id), c)
	}

	display := client.Display
	if len(display) == 0 {
		MakeResponse(false, -4, "Please input display name", c)
		return
	}

	mic := client.Mic
	if runtime.GOOS == "windows" && len(mic) > 0 {
		/// Recording mic only supports Windows
		out, err := exec.Command("gst-device-monitor-1.0", "Audio/Source").Output()
		if err != nil {
			log.Println("Read gst-device-monitor-1.0 cmd error:", err)
		}

		output, _ := GbkToUtf8(out)

		devices := config.GstDevicesFromCLI(string(output))
		micID := config.FindWASAPIMicGUID(mic, devices)

		if len(micID) == 0 {
			MakeResponse(false, -7, "Invalidate microphone device name!", c)
			return
		}
		mic = micID
	}

	if !config.Config.AddClient(uuid, client) {
		MakeResponse(false, -5, fmt.Sprintf("Camera(%s) not config yet!", id), c)
		return
	}

	msg, err := StreamWebRTC(uuid)

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
	room := c.PostForm("room")
	if len(id) == 0 || len(room) == 0 {
		MakeResponse(false, -5, "Please input room number and Camera ID", c)
		return
	}

	uuid := room + "_" + id
	if !config.Config.Exist(uuid) {
		MakeResponse(false, -1, fmt.Sprintf("Camera ID %s not exist!", id), c)
		return
	}

	client := config.Config.Clients[uuid]
	// destroy webrtc client
	if client.WebRTC != nil {
		log.Printf("Destroying (%s) WebRTC resource\n", client.ID)
		client.WebRTC.Close()
		client.WebRTC = nil
	} else {
		log.Printf("Destroy (%s) WebRTC resource failed: client does not exist! exec anyway\n", client.ID)
	}
	config.Config.DelClient(uuid)

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
	if !config.Config.Exist(uuid) {
		return "", errors.New(fmt.Sprintf("Stream %s NOT found", uuid))
	}

	client := config.Config.Clients[uuid]
	muxerWebRTC := webrtc.NewMuxer(webrtc.Options{
		ICEServers:    client.ICEServers,
		ICEUsername:   client.ICEUsername,
		ICECredential: client.ICECredential,
	})

	msg, err := muxerWebRTC.WriteHeader(
		client.ID,
		client.Room,
		client.Pin,
		client.URL,
		client.Janus,
		client.Mic,
		client.Display)
	if err != nil {
		return msg, err
	}

	config.Config.AddRTC2Stream(uuid, muxerWebRTC)

	return "", nil
}

//func reconnect(uuid string) {
//	log.Println("Prepare to reconnect: ", uuid)
//
//	msg, err := StreamWebRTC(uuid)
//
//	if err != nil {
//		log.Printf("Reconnect error: %s, msg: %s", err, msg)
//	}
//}

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
