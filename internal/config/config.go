package config

import (
	"RTSPSender/internal/webrtc"
	"crypto/md5"
	"encoding/hex"
	"sync"
)

//Config global
var Config = Configs{}

//Configs struct
type Configs struct {
	mutex   sync.RWMutex
	Clients map[string]RTSPClient `json:"clients"`

	isMicphoneRecording bool
	LastError           error
}

type RTSPClient struct {
	URL  string `json:"url"`
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

func (element *Configs) UpdateMicphoneRecordingState(state bool) {
	element.mutex.Lock()
	defer element.mutex.Unlock()

	element.isMicphoneRecording = state
}

func (element *Configs) IsMicphoneRecording() bool {
	element.mutex.Lock()
	defer element.mutex.Unlock()

	return element.isMicphoneRecording
}

func (element *Configs) AddClient(id string, client RTSPClient) bool {
	element.mutex.Lock()
	defer element.mutex.Unlock()

	if _, ok := element.Clients[id]; !ok {
		element.Clients[id] = client
		return true
	}
	return false
}

func (element *Configs) DelClient(id string) bool {
	element.mutex.Lock()
	defer element.mutex.Unlock()

	if _, ok := element.Clients[id]; ok {
		delete(element.Clients, id)
		return true
	}
	return false
}

func (element *Configs) AddRTC2Stream(id string, WebRTC *webrtc.Muxer) bool {
	element.mutex.Lock()
	defer element.mutex.Unlock()

	if tmp, ok := element.Clients[id]; ok {
		tmp.WebRTC = WebRTC
		element.Clients[id] = tmp
		return true
	}
	return false
}

func (element *Configs) Exist(uuid string) bool {
	element.mutex.Lock()
	defer element.mutex.Unlock()
	_, ok := element.Clients[uuid]
	return ok
}

func (element *Configs) List() (string, []string) {
	element.mutex.Lock()
	defer element.mutex.Unlock()
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
