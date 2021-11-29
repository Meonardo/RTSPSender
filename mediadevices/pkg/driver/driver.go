package driver

import (
	"RTSPSender/mediadevices/pkg/io/audio"
	"RTSPSender/mediadevices/pkg/io/video"
	"RTSPSender/mediadevices/pkg/prop"
)

type VideoRecorder interface {
	VideoRecord(p prop.Media) (r video.Reader, err error)
}

type AudioRecorder interface {
	AudioRecord(p prop.Media) (r audio.Reader, err error)
}

// Priority represents device selection priority level
type Priority float32

const (
	// PriorityHigh is a value for system default devices
	PriorityHigh Priority = 0.1
	// PriorityNormal is a value for normal devices
	PriorityNormal Priority = 0.0
	// PriorityLow is a value for unrecommended devices
	PriorityLow Priority = -0.1
)

type Info struct {
	Label      string
	DeviceType DeviceType
	Priority   Priority
	Name       string
	Manufacturer string
	ModelID    string
}

type Adapter interface {
	Open() error
	Close() error
	Properties() []prop.Media
}

type Driver interface {
	Adapter
	ID() string
	Info() Info
	Status() State
}
