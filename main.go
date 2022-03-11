package main

import (
	"RTSPSender/internal/config"
	"RTSPSender/internal/webrtc"
	"fmt"
	"log"
	"os"
	"runtime"

	"C"
	"encoding/json"
	"os/signal"
	"strings"
	"syscall"
)

const DEBUG = true

func main() {
	if DEBUG {
		go serveHTTP()
		MakeConfig()

		sigs := make(chan os.Signal, 1)
		done := make(chan bool, 1)
		signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
		go func() {
			sig := <-sigs
			log.Println(sig)
			done <- true
		}()
		log.Println("Server Start Awaiting Signal")
		<-done
		log.Println("Exiting")
	}
}

//MakeConfig :
//export MakeConfig
func MakeConfig() {
	config.Config.Clients = make(map[string]config.RTSPClient)
	if runtime.GOOS == "windows" {
		home := os.Getenv("HOMEDRIVE") + os.Getenv("HOMEPATH")
		if home == "" {
			home = os.Getenv("USERPROFILE")
		}
		LOG_FILE := home + "\\rtspsender_log"
		logFile, err := os.OpenFile(LOG_FILE, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0644)
		if err != nil {
			log.Fatalln(err)
		}

		log.SetOutput(logFile)
		// optional: log date-time, filename, and line number
		log.SetFlags(log.Lshortfile | log.LstdFlags)
		log.Println("Service starting...")
	}
}

//StartPublishing :
//export StartPublishing
func StartPublishing(p *C.char) int {
	c := strings.Fields(C.GoString(p))
	configs := strings.Join(c, "")
	log.Printf("Configs = %s", configs)

	if len(configs) == 0 {
		log.Println("Missing mandatory field `configs`!")
		return -1
	}

	var client config.RTSPClient
	err := json.Unmarshal([]byte(configs), &client)
	if err != nil {
		log.Println("Decode JSON object failed!")
		return -2
	}

	room := client.Room
	if len(room) == 0 {
		log.Println("Please input room number")
		return -3
	}
	id := client.ID
	if len(id) == 0 {
		log.Println("Please input camera ID")
		return -4
	}
	uuid := room + "_" + id
	if config.Config.Exist(uuid) {
		log.Printf("Camera ID %s is currently publishing!", id)
		return -5
	}

	display := client.Display
	if len(display) == 0 {
		log.Println("Please input display name")
		return -6
	}

	mic := client.Mic
	if runtime.GOOS == "windows" && len(mic) > 0 {
		micID := config.MicGUIDFromName(mic)
		if len(micID) == 0 {
			log.Println("Invalidate microphone device name!")
			return -7
		}
		client.Mic = micID
		log.Println("Found microphone device ID: ", micID)
	}

	if !config.Config.AddClient(uuid, client) {
		log.Printf("Camera(%s) add failed!", id)
		return -8
	}

	msg, err := Stream2WebRTC(uuid)

	if err != nil {
		if len(msg) == 0 {
			msg += "janus error: " + err.Error()
		} else {
			msg += ", " + err.Error()
		}
		log.Println(msg)
		return -9
	}

	return 0
}

//StopPublishing :
//export StopPublishing
func StopPublishing(ID int64, Room int64) int {
	if ID <= 0 || Room <= 0 {
		log.Print("Please input room number and Camera ID")
		return -1
	}
	log.Printf("StopPublishing ID = %d, Room = %d", ID, Room)

	id := fmt.Sprint(ID)
	room := fmt.Sprint(Room)

	uuid := room + "_" + id
	if !config.Config.Exist(uuid) {
		log.Printf("Camera ID %s not exist!", id)
		return -2
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

	return 0
}

//Stream2WebRTC RTSP stream video over WebRTC
func Stream2WebRTC(uuid string) (string, error) {
	if !config.Config.Exist(uuid) {
		return "", fmt.Errorf(fmt.Sprintf("Stream %s NOT found", uuid))
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
