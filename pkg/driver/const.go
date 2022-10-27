package driver

// DeviceType represents human readable device type. DeviceType
// can be useful to filter the drivers too.
type DeviceType string

const (
	// Camera represents camera devices
	Camera DeviceType = "camera"
	// Microphone represents microphone devices
	Microphone = "microphone"
	// Speaker represents speaker devices
	Speaker = "speaker"
	// Screen represents screen devices
	Screen = "screen"
)
