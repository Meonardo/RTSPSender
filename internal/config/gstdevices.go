package config

import "strings"

type GstDeviceProp struct {
	Api string
	GUID string
	Description string
}

type GstDevice struct {
	Name string
	Class string
	Caps       string
	Properties GstDeviceProp
}

func NewGstDeviceProp(properties []string) GstDeviceProp {
	var api, guid, description string
	for _, d := range properties {
		d = strings.TrimSpace(d)
		if len(d) > 0 {
			t := strings.Split(d, "=")
			t1 := strings.TrimSpace(t[0])
			t2 := strings.TrimSpace(t[len(t) - 1])

			if strings.Contains(t1, "api") && len(api) == 0 {
				api = t2
				continue
			}
			if strings.Contains(t1, "guid") || strings.Contains(t1, "strid") {
				guid = t2
				continue
			}
			if strings.Contains(t1, "description") {
				description = t2
				continue
			}
		}
	}
	return GstDeviceProp{Api: api, GUID: guid, Description: description}
}

func NewGstDevice(class []string, properties []string) GstDevice {
	var name, class_, caps string
	for _, d := range class {
		d = strings.TrimSpace(d)
		if len(d) > 0 {
			t := strings.Split(d, ":")
			t1 := strings.TrimSpace(t[0])
			t2 := strings.TrimSpace(t[len(t) - 1])
			if t1 == "name" {
				name = t2
			}
			if t1 == "class" {
				class_ = t2
			}
			if t1 == "caps" {
				caps = t2
			}
		}
	}

	return GstDevice{
		Name: name,  Class: class_, Caps: caps, Properties: NewGstDeviceProp(properties),
	}
}

func Index(slice []string, item string) int {
	for i := range slice {
		if strings.Contains(slice[i], item) {
			return i
		}
	}
	return -1
}

func GstDevicesFromCLI(content string) []GstDevice {
	if len(content) == 0 {
		return []GstDevice{}
	}

	r := strings.Split(content, "Device found:")
	var devices []GstDevice

	for _, line := range r {
		line = strings.TrimSpace(line)
		if len(line) == 0 {
			continue
		}

		devices_ := strings.Split(line, "\n")

		index := Index(devices_, "properties:")
		if index != -1 {
			left := devices_[:index]
			right := devices_[index + 1:]

			devices = append(devices, NewGstDevice(left, right))
		}
	}

	return devices
}

func FindWASAPIMicGUID(mic string, devices []GstDevice) string {
	if len(mic) == 0 || len(devices) == 0 {
		return ""
	}
	for _, d := range devices {
		if d.Name == mic && d.Properties.Api == "wasapi" {
			return d.Properties.GUID
		}
	}

	return ""
}