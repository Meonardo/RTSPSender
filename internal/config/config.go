package config

import (
	"RTSPSender/internal/webrtc"
	"crypto/md5"
	"encoding/hex"
	"sync"
)

//Config global
var Config = Configs{}

// `(rtsp[s]?):\/\/((25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\.){3}(25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)`
const RTSPReg = `(rtsp):\/\/((25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\.){3}(25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)`

//Configs struct
type Configs struct {
	Mutex   sync.RWMutex
	Client  *Client
	Clients map[string]Client `json:"clients"`

	LastError error
}

type Client struct {
	ID   string `json:"id"`
	Room string `json:"room"`
	Pin  string `json:"pin"`

	Display       string   `json:"display"`
	Mic           string   `json:"mic"`
	Janus         string   `json:"janus"`
	ICEServers    []string `json:"ice_servers"`
	ICEUsername   string   `json:"ice_username"`
	ICECredential string   `json:"ice_credential"`

	WebRTC *webrtc.Muxer
}

func (element *Configs) CopyClient() Client {
	var c = Client{
		ID:            element.Client.ID,
		Room:          element.Client.Room,
		Pin:           element.Client.Pin,
		Display:       element.Client.Display,
		Mic:           element.Client.Mic,
		Janus:         element.Client.Janus,
		ICEServers:    element.Client.ICEServers,
		ICEUsername:   element.Client.ICEUsername,
		ICECredential: element.Client.ICECredential,
	}
	return c
}

func (element *Configs) AddClient(id string, client Client) bool {
	element.Mutex.Lock()
	defer element.Mutex.Unlock()

	if _, ok := element.Clients[id]; !ok {
		element.Clients[id] = client
		return true
	}
	return false
}

func (element *Configs) DelClient(id string) bool {
	element.Mutex.Lock()
	defer element.Mutex.Unlock()

	if _, ok := element.Clients[id]; ok {
		delete(element.Clients, id)
		return true
	}
	return false
}

func (element *Configs) AddRTC2Stream(id string, WebRTC *webrtc.Muxer) bool {
	element.Mutex.Lock()
	defer element.Mutex.Unlock()

	if tmp, ok := element.Clients[id]; ok {
		tmp.WebRTC = WebRTC
		element.Clients[id] = tmp
		return true
	}
	return false
}

func (element *Configs) Exist(uuid string) bool {
	element.Mutex.Lock()
	defer element.Mutex.Unlock()
	_, ok := element.Clients[uuid]
	return ok
}

func (element *Configs) List() (string, []string) {
	element.Mutex.Lock()
	defer element.Mutex.Unlock()
	var res []string
	var fist string
	for k := range element.Clients {
		if fist == "" {
			fist = k
		}
		res = append(res, k)
	}
	return fist, res
}

func GetMD5Hash(text string) string {
	hash := md5.Sum([]byte(text))
	return hex.EncodeToString(hash[:])
}
