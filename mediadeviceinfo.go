package mediadevices

import (
	"fmt"

	"github.com/pion/mediadevices/pkg/driver"
)

// MediaDeviceType enumerates type of media device.
type MediaDeviceType int

// MediaDeviceType definitions.
const (
	VideoInput MediaDeviceType = iota + 1
	AudioInput
	AudioOutput
)

// MediaDeviceInfo represents https://w3c.github.io/mediacapture-main/#dom-mediadeviceinfo
type MediaDeviceInfo struct {
	DeviceID     string
	Kind         MediaDeviceType
	Label        string
	DeviceType   driver.DeviceType
	Name         string
	Manufacturer string
	ModelID      string
}

func (mdi MediaDeviceInfo) String() string {
	return fmt.Sprintf("'%s' %s %v %d %s [%s %s]", mdi.Name, mdi.Label, mdi.DeviceID, mdi.Kind, mdi.DeviceType, mdi.Manufacturer, mdi.ModelID)
}

func (mdi MediaDeviceInfo) Serialize() string {
	return fmt.Sprintf("d/%s/%v/%s/%s/%s/%s", mdi.DeviceID, mdi.Kind, mdi.DeviceType, mdi.Name, mdi.Manufacturer, mdi.ModelID)
}
