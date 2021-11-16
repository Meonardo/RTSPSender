package mmal

import (
	"RTSPSender/mediadevices/pkg/codec"
	"RTSPSender/mediadevices/pkg/io/video"
	"RTSPSender/mediadevices/pkg/prop"
)

// Params stores libmmal specific encoding parameters.
type Params struct {
	codec.BaseParams
}

// NewParams returns default mmal codec specific parameters.
func NewParams() (Params, error) {
	return Params{
		BaseParams: codec.BaseParams{
			KeyFrameInterval: 60,
		},
	}, nil
}

// RTPCodec represents the codec metadata
func (p *Params) RTPCodec() *codec.RTPCodec {
	return codec.NewRTPH264Codec(90000)
}

// BuildVideoEncoder builds mmal encoder with given params
func (p *Params) BuildVideoEncoder(r video.Reader, property prop.Media) (codec.ReadCloser, error) {
	return newEncoder(r, property, *p)
}
