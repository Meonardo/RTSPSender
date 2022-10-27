package main

import (
	"RTSPSender/internal/config"
	"RTSPSender/internal/webrtc"
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
)

const port = ":9982"

func serveHTTP() {
	log.Printf("Staring HTTP server at port %s\n", port)
	gin.SetMode(gin.ReleaseMode)

	router := gin.Default()
	router.Use(CORSMiddleware())

	router.POST("/audio/push/stop", Stop)
	router.POST("/audio/push/start", Start)
	err := router.Run(port)
	if err != nil {
		log.Fatalln("Start HTTP Server error", err)
	}
}

func Start(c *gin.Context) {
	var startedSuccess = false

	defer func() {
		if !startedSuccess {
			if config.Config.Client.WebRTC != nil {
				config.Config.Client.WebRTC.Close()
				config.Config.Client.WebRTC = nil
			}
			config.Config.Client = nil
		}
	}()

	configs := c.PostForm("configs")
	if len(configs) == 0 {
		MakeResponse(false, -1, "Missing mandatory field `configs`!", c)
		return
	}
	log.Println("Configure Request, params: ", configs)

	var client config.Client
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

	display := client.Display
	if len(display) == 0 {
		MakeResponse(false, -4, "Please input display name", c)
		return
	}

	mic := client.Mic
	if len(mic) > 0 {
		client.Mic = config.GetMD5Hash(mic)
	}

	config.Config.Client = &client
	msg, err := StreamWebRTC(&client)

	if err != nil {
		if len(msg) == 0 {
			msg += "janus error: " + err.Error()
		} else {
			msg += ", " + err.Error()
		}
		MakeResponse(false, -9, msg, c)
		return
	}

	startedSuccess = true
	MakeResponse(true, 1, fmt.Sprintf("Publish camera %s in Room %s successfully!", id, room), c)
}

func Stop(c *gin.Context) {
	if config.Config.Client == nil {
		MakeResponse(false, -9, "No sound is publising!", c)
	}

	client := config.Config.Client
	// destroy webrtc client
	if client.WebRTC != nil {
		log.Printf("Destroying (%s) WebRTC resource\n", client.ID)
		client.WebRTC.Close()
		client.WebRTC = nil
		config.Config.Client = nil
	} else {
		log.Printf("Destroy (%s) WebRTC resource failed: client does not exist! exec anyway\n", client.ID)
	}

	MakeResponse(true, 1, "Stop successfully!", c)
}

func MakeResponse(success bool, code int, data string, c *gin.Context) {
	var state = 1
	if !success {
		state = code
	}
	log.Printf("*[Response, Success: (%t), Code: (%d), Msg: (%s)]*\n", success, code, data)
	c.JSON(http.StatusOK, gin.H{"state": state, "code": data})
}

//StreamWebRTC
func StreamWebRTC(client *config.Client) (string, error) {
	muxerWebRTC := webrtc.NewMuxer(webrtc.Options{
		ICEServers:    client.ICEServers,
		ICEUsername:   client.ICEUsername,
		ICECredential: client.ICECredential,
	})

	msg, err := muxerWebRTC.WriteHeader(
		client.ID,
		client.Room,
		client.Pin,
		client.Janus,
		client.Mic,
		client.Display)
	if err != nil {
		return msg, err
	}

	client.WebRTC = muxerWebRTC

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
