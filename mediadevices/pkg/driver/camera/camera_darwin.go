package camera

import (
	"image"

	"RTSPSender/mediadevices/pkg/avfoundation"
	"RTSPSender/mediadevices/pkg/driver"
	"RTSPSender/mediadevices/pkg/frame"
	"RTSPSender/mediadevices/pkg/io/video"
	"RTSPSender/mediadevices/pkg/prop"
)

type camera struct {
	device  avfoundation.Device
	session *avfoundation.Session
}

func init() {
	devices, err := avfoundation.Devices(avfoundation.Video)
	if err != nil {
		panic(err)
	}

	for _, device := range devices {
		cam := newCamera(device)
		driver.GetManager().Register(cam, driver.Info{
			Label:        device.UID,
			Name:         device.Name,
			Manufacturer: device.Manufacturer,
			ModelID:      device.ModelID,
			DeviceType:   driver.Camera,
		})
	}
}

func newCamera(device avfoundation.Device) *camera {
	return &camera{
		device: device,
	}
}

func (cam *camera) Open() error {
	var err error
	cam.session, err = avfoundation.NewSession(cam.device)
	return err
}

func (cam *camera) Close() error {
	return cam.session.Close()
}

func (cam *camera) VideoRecord(property prop.Media) (video.Reader, error) {
	decoder, err := frame.NewDecoder(property.FrameFormat)
	if err != nil {
		return nil, err
	}

	rc, err := cam.session.Open(property)
	if err != nil {
		return nil, err
	}
	r := video.ReaderFunc(func() (image.Image, func(), error) {
		frame, _, err := rc.Read()
		if err != nil {
			return nil, func() {}, err
		}
		return decoder.Decode(frame, property.Width, property.Height)
	})
	return r, nil
}

func (cam *camera) Properties() []prop.Media {
	return cam.session.Properties()
}
