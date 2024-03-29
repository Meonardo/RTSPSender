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
	"path/filepath"
	"regexp"
	"sync"
	"syscall"

	"golang.org/x/sys/windows"
)

const DEBUG = false
const UsingCLI = true

var globalMutex = sync.RWMutex{}

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
	config.Config.Clients = make(map[string]config.RTSPClient)

	if runtime.GOOS == "windows" && !DEBUG {
		home := os.Getenv("HOMEDRIVE") + os.Getenv("HOMEPATH")
		if home == "" {
			home = os.Getenv("USERPROFILE")
		}

		// check if can detect the executebale file dir
		ex, err := os.Executable()
		if err == nil {
			home = filepath.Dir(ex)
		}

		logPath := home + "\\RTSPSender"
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
	threadId := windows.GetCurrentThreadId()
	log.Printf("============== Calling StartPublishing, current thead: %d", threadId)

	globalMutex.Lock()
	defer func() {
		globalMutex.Unlock()
	}()

	// Config first
	makeConfig()
	var startedSuccess = false

	c := strings.Fields(C.GoString(p))
	configs := strings.Join(c, "")
	log.Printf("StartPublishing..., Configs = %s", configs)

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

	rtsp := client.URL
	var validURL = regexp.MustCompile(config.RTSPReg)
	if !validURL.MatchString(rtsp) {
		log.Println("Please input validate RTSP camera URL!")
		return -9
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

	defer func() {
		if !startedSuccess {
			config.Config.DelClient(uuid)
		}
	}()

	if !config.Config.AddClient(uuid, client) {
		log.Printf("Camera(%s) add failed!", id)
		return -11
	}

	msg, err := Stream2WebRTC(uuid)

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
func StopPublishing(ID int64, Room int64) int {
	threadId := windows.GetCurrentThreadId()
	log.Printf("============== Calling StopPublishing, current thead: %d", threadId)

	globalMutex.Lock()
	defer func() {
		globalMutex.Unlock()
	}()

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

	time.Sleep(100 * time.Millisecond)
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

// test

var iceServer = []string{
	"stun:192.168.99.48:3478",
	"turn:192.168.99.48:3478",
}
var testCameras = map[string]string{
	"1": "rtsp://192.168.99.169/1",
	"8": "rtsp://192.168.99.23/2",
	// "3": "rtsp://192.168.99.16/1",
	// "4": "rtsp://192.168.99.18/1",
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
		} else if text == "1" {
			testSwitch("1")
		} else if text == "2" {
			testSwitch("2")
		} else if text == "start" {
			testStart(publishingUUID)
		} else if text == "stop" {
			testStop(publishingUUID)
		} else if text == "startAll" {
			testStartAll()
		} else if text == "stopAll" {
			testStopAll()
		}
	}

	// handle error
	if scanner.Err() != nil {
		fmt.Println("Error: ", scanner.Err())
	}
}

func testStart(uuid string) {
	url := testCameras[uuid]
	var validURL = regexp.MustCompile(config.RTSPReg)
	if !validURL.MatchString(url) {
		log.Println("Please input validate RTSP camera URL!")
		return
	}

	display := url

	client := config.RTSPClient{
		URL:           url,
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

	if !config.Config.AddClient(uuid, client) {
		return
	}

	_, err := Stream2WebRTC(uuid)
	if err != nil {
		log.Println(err)
	}
}

func testSwitch(uuid string) {
	if publishingUUID == uuid {
		return
	}
	testStop(publishingUUID)
	testStart(uuid)
	publishingUUID = uuid
}

func testStop(uuid string) {
	if !config.Config.Exist(uuid) {
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

	time.Sleep(100 * time.Millisecond)
}

func testStartAll() {
	for uuid, _ := range testCameras {
		testStart(uuid)
	}
}

func testStopAll() {
	for uuid, _ := range testCameras {
		testStop(uuid)
	}
}
