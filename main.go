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
	"strings"
	"time"
)
import (
	"bufio"
	"os/signal"
	"syscall"
)

const DEBUG = false
const UsingCLI = true

func main() {
	if DEBUG {
		makeConfig()

		if UsingCLI {
			testFromCLI()
		} else {
			testFromGinHTTP()
		}
	}
}

func makeConfig() {
	if config.Config.Clients != nil {
		log.Println("Already make config, ignore it.")
		return
	}
	config.Config.Clients = make(map[string]config.Client)

	if runtime.GOOS == "windows" && !DEBUG {
		home := os.Getenv("HOMEDRIVE") + os.Getenv("HOMEPATH")
		if home == "" {
			home = os.Getenv("USERPROFILE")
		}

		logPath := home + "\\AudioSender"
		if _, err := os.Stat(logPath); os.IsNotExist(err) {
			err := os.Mkdir(logPath, 0644)
			if err != nil {
				log.Fatalln(err)
			}
		}
		LOG_FILE := logPath + fmt.Sprintf("\\%d", time.Now().Unix()) + ".log"
		logFile, err := os.OpenFile(LOG_FILE, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0644)
		if err != nil {
			log.Fatalln(err)
		}

		log.SetOutput(logFile)
		// optional: log date-time, filename, and line number
		// log.SetFlags()
		log.Println("Service starting...")
	}
}

//StartPublishing :
//export StartPublishing
func StartPublishing(p *C.char) int {
	// Config first
	makeConfig()
	var startedSuccess = false

	defer func() {
		if !startedSuccess {
			if config.Config.Client != nil {
				if config.Config.Client.WebRTC != nil {
					config.Config.Client.WebRTC.Close()
					config.Config.Client.WebRTC = nil
				}
				config.Config.Client = nil
			}
		}
	}()

	if config.Config.Client != nil {
		log.Println("Already published!")
		return -10
	}

	c := strings.Fields(C.GoString(p))
	configs := strings.Join(c, "")
	log.Printf("StartPublishing..., Configs = %s", configs)

	if len(configs) == 0 {
		log.Println("Missing mandatory field `configs`!")
		return -1
	}

	var client config.Client
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
		log.Println("Please input ID")
		return -4
	}

	display := client.Display
	if len(display) == 0 {
		log.Println("Please input display name")
		return -6
	}

	// save the client
	config.Config.Client = &client
	msg, err := Stream2WebRTC(&client)

	if err != nil {
		if len(msg) == 0 {
			msg += "janus error: " + err.Error()
		} else {
			msg += ", " + err.Error()
		}

		log.Println(msg)
		return -12
	}

	startedSuccess = true
	return 0
}

//StopPublishing :
//export StopPublishing
func StopPublishing() int {
	if config.Config.Client == nil {
		log.Println("No sound is publising!")
		return -10
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

	time.Sleep(100 * time.Millisecond)
	return 0
}

// Start mixing default speaker sound with the Mic.
//export StartMixingSounds
func StartMixingSounds() int {
	if config.Config.Client == nil {
		log.Println("No sound is publising!")
		return -10
	}

	if config.Config.Client.WebRTC == nil {
		log.Println("No sound is publising!")
		return -10
	}

	success := config.Config.Client.WebRTC.StartMixingSounds()
	if !success {
		log.Println("Can not start mixing sounds!")
		return -100
	}

	return 0
}

// Stop mixing sounds.
//export StopMixingSounds
func StopMixingSounds() int {
	if config.Config.Client == nil {
		log.Println("No sound is publising!")
		return -10
	}

	if config.Config.Client.WebRTC == nil {
		log.Println("No sound is publising!")
		return -10
	}

	success := config.Config.Client.WebRTC.StopMixingSounds()
	if !success {
		log.Println("Can not stop mixing sounds!")
		return -100
	}

	return 0
}

//Stream2WebRTC audio over WebRTC
func Stream2WebRTC(client *config.Client) (string, error) {
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

////////////////////////////////////////////////////////////
////////////////////////////////////////////////////////////
////////////////////////////////////////////////////////////
////////////////////////////////////////////////////////////
////////////////////////////////////////////////////////////
////////////////////////////////////////////////////////////
////////////////////////////////////////////////////////////
////////////////////////////////////////////////////////////
///
/// tests
var iceServer = []string{
	"stun:192.168.99.48:3478",
	"turn:192.168.99.48:3478",
}
var icePasswd = "123456"
var iceUsername = "root"
var room = "123456"

//"Internal Microphone (Cirrus Logic CS8409 (AB 57))"
var mic = "Microphone Array (Realtek(R) Audio)"
var janus = "ws://192.168.99.48:8188"

var publishingUUID = "1"

func testFromGinHTTP() {
	go serveHTTP()

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

func testFromCLI() {
	// Read using Scanner
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("Enter Cmd: ")
		scanner.Scan()
		text := scanner.Text()

		if text == "q" {
			break
		} else if text == "startMixing" {
			testStartMixing()
		} else if text == "stopMixing" {
			testStopMixing()
		} else if text == "start" {
			testStart(publishingUUID)
		} else if text == "stop" {
			testStop(publishingUUID)
		}
	}

	// handle error
	if scanner.Err() != nil {
		fmt.Println("Error: ", scanner.Err())
	}
}

func testStart(uuid string) {
	if config.Config.Client != nil {
		log.Println("Already published!")
		return
	}

	display := uuid + "(AudioOnly)"
	client := config.Client{
		ID:            uuid,
		Room:          room,
		Pin:           room,
		Display:       display,
		Mic:           mic,
		Janus:         janus,
		ICEServers:    iceServer,
		ICEUsername:   iceUsername,
		ICECredential: icePasswd,
	}

	if len(client.Mic) > 0 {
		client.Mic = config.GetMD5Hash(client.Mic)
	}

	// save the client
	config.Config.Client = &client

	_, err := Stream2WebRTC(&client)
	if err != nil {
		log.Println(err)
	}
}

func testStop(uuid string) {
	if config.Config.Client == nil {
		log.Print("No client started yet, please start a client first!")
		return
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
	time.Sleep(100 * time.Millisecond)
}

func testStartMixing() {
	StartMixingSounds()
}

func testStopMixing() {
	StopMixingSounds()
}
