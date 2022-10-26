package microphone

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/gen2brain/malgo"
	"github.com/pion/mediadevices/internal/logging"
	"github.com/pion/mediadevices/pkg/driver"
	"github.com/pion/mediadevices/pkg/io/audio"
	"github.com/pion/mediadevices/pkg/prop"
	"github.com/pion/mediadevices/pkg/wave"
)

const (
	maxDeviceIDLength = 20
	// TODO: should replace this with a more flexible approach
	sampleRateStep    = 1000
	initialBufferSize = 1024
)

var logger = logging.NewLogger("mediadevices/driver/microphone")
var ctx *malgo.AllocatedContext
var hostEndian binary.ByteOrder
var (
	errUnsupportedFormat = errors.New("the provided audio format is not supported")
)

type microphone struct {
	malgo.DeviceInfo
	chunkChan       chan unsafe.Pointer
	deviceCloseFunc func()

	loopbackDevice *malgo.Device
	loopbackFloats []float32
	isMixing       bool
}

func defaultPlaybackDeviceInfo() *malgo.DeviceInfo {
	var err error
	ctx, err = malgo.InitContext(nil, malgo.ContextConfig{}, func(message string) {
		fmt.Printf("Playback device message: %s\n", message)
	})
	if err != nil {
		panic(err)
	}

	devices, err := ctx.Devices(malgo.Playback)
	if err != nil {
		panic(err)
	}

	for _, device := range devices {
		if device.IsDefault > 0 {
			info, err := ctx.DeviceInfo(malgo.Playback, device.ID, malgo.Shared)
			if err == nil {
				return &info
			}
		}

	}
	return nil
}

func init() {
	backends := []malgo.Backend{
		malgo.BackendWasapi,
	}

	var err error
	ctx, err = malgo.InitContext(backends, malgo.ContextConfig{}, func(message string) {
		fmt.Printf("Playback device message: %s\n", message)
	})
	if err != nil {
		panic(err)
	}

	devices, err := ctx.Devices(malgo.Capture)
	if err != nil {
		panic(err)
	}

	for _, device := range devices {
		info, err := ctx.DeviceInfo(malgo.Capture, device.ID, malgo.Shared)
		if err == nil {
			priority := driver.PriorityNormal
			if info.IsDefault > 0 {
				priority = driver.PriorityHigh
			}

			name := device.Name()
			name = strings.Trim(name, "\x00")

			driver.GetManager().Register(newMicrophone(info), driver.Info{
				Label:        device.ID.String(),
				DeviceType:   driver.Microphone,
				Priority:     priority,
				Name:         name,
				Manufacturer: "",
				ModelID:      "",
			})
		}
	}

	// Decide which endian
	switch v := *(*uint16)(unsafe.Pointer(&([]byte{0x12, 0x34}[0]))); v {
	case 0x1234:
		hostEndian = binary.BigEndian
	case 0x3412:
		hostEndian = binary.LittleEndian
	default:
		panic(fmt.Sprintf("failed to determine host endianness: %x", v))
	}
}

func newMicrophone(info malgo.DeviceInfo) *microphone {
	return &microphone{
		DeviceInfo: info,
	}
}

func (m *microphone) Open() error {
	m.chunkChan = make(chan unsafe.Pointer, 1)
	return nil
}

func (m *microphone) Close() error {
	// close the playback device first
	m.ClosePlaybackDevice()
	if m.deviceCloseFunc != nil {
		m.deviceCloseFunc()
	}
	return nil
}

func (m *microphone) StopMixing() bool {
	if !m.isMixing || m.loopbackDevice == nil {
		return false
	}
	m.isMixing = false

	// stop the device
	err := m.loopbackDevice.Stop()
	if err != nil {
		fmt.Println("StopMixing failed:", err)
		return false
	}

	return true
}

func (m *microphone) StartMixing() bool {
	if m.isMixing {
		return false
	}
	if m.loopbackDevice == nil {
		// init loopback device
		var callbacks malgo.DeviceCallbacks
		var channels uint32 = 2

		onRecvChunk := func(_, pInput unsafe.Pointer, frameCount uint32) {
			sampleCount := frameCount * channels
			floats := unsafe.Slice((*float32)(pInput), sampleCount)
			m.loopbackFloats = floats
		}
		callbacks.RawData = onRecvChunk

		onDeviceStop := func() {
			go func() {
				if m.isMixing {
					m.RestartMixing()
				} else {
					m.ClosePlaybackDevice()
				}
			}()
		}
		callbacks.Stop = onDeviceStop

		deviceConfig := malgo.DefaultDeviceConfig(malgo.Loopback)
		deviceConfig.Capture.Format = malgo.FormatF32
		deviceConfig.Capture.Channels = 2
		deviceConfig.Capture.DeviceID = nil
		deviceConfig.Playback.DeviceID = nil
		loopbackDevice, err := malgo.InitDevice(ctx.Context, deviceConfig, callbacks)
		if err != nil {
			return false
		}
		loopbackDevice.SetAllowPlaybackAutoStreamRouting(true)
		m.loopbackDevice = loopbackDevice
	}

	// start the device
	err := m.loopbackDevice.Start()
	if err != nil {
		return false
	}

	m.isMixing = true
	return true
}

// restart mixing sounds
func (m *microphone) RestartMixing() {
	fmt.Println("Mix restarting...")

	m.loopbackFloats = nil
	m.ClosePlaybackDevice()
	success := m.StartMixing()
	if success {
		fmt.Println("Mixing sounds restarted successfully.")
	}
}

func (m *microphone) ClosePlaybackDevice() {
	// stop mixing if necessary
	m.StopMixing()
	if m.loopbackDevice != nil {
		// destory playback device
		//m.loopbackDevice.Uninit()
		m.loopbackDevice = nil
	}
}

func (m *microphone) AudioRecord(inputProp prop.Media) (audio.Reader, error) {
	var config malgo.DeviceConfig
	var callbacks malgo.DeviceCallbacks

	decoder, err := wave.NewDecoder(&wave.RawFormat{
		SampleSize:  inputProp.SampleSize,
		IsFloat:     inputProp.IsFloat,
		Interleaved: inputProp.IsInterleaved,
	})
	if err != nil {
		return nil, err
	}

	config.DeviceType = malgo.Capture
	config.PerformanceProfile = malgo.LowLatency
	config.Capture.Channels = 2
	config.PeriodSizeInMilliseconds = uint32(inputProp.Latency.Milliseconds())
	//FIX: Turn on the microphone with the current device id
	config.Capture.DeviceID = m.ID.Pointer()
	config.Capture.Format = malgo.FormatF32

	cancelCtx, cancel := context.WithCancel(context.Background())
	onRecvChunk := func(_, chunk unsafe.Pointer, framecount uint32) {
		select {
		case <-cancelCtx.Done():
		case m.chunkChan <- chunk:
		}
	}
	callbacks.RawData = onRecvChunk

	sizeInBytes := uint32(malgo.SampleSizeInBytes(malgo.FormatF32))

	device, err := malgo.InitDevice(ctx.Context, config, callbacks)
	if err != nil {
		cancel()
		return nil, err
	}

	err = device.Start()
	if err != nil {
		cancel()
		return nil, err
	}

	var closeDeviceOnce sync.Once
	m.deviceCloseFunc = func() {
		closeDeviceOnce.Do(func() {
			cancel() // Unblock onRecvChunk
			device.Uninit()

			if m.chunkChan != nil {
				close(m.chunkChan)
				m.chunkChan = nil
			}
		})
	}

	// test mixing
	// m.StartMixing()

	var reader audio.Reader = audio.ReaderFunc(func() (wave.Audio, func(), error) {
		chunk, ok := <-m.chunkChan
		if !ok {
			m.deviceCloseFunc()
			logger.Debug("Microphone io.EOF!")
			return nil, func() {}, io.EOF
		}

		var inputSamples []byte
		sampleCount := uint32(inputProp.SampleRate/100) * uint32(inputProp.ChannelCount)

		// here goes with mixing
		if m.loopbackFloats != nil && m.isMixing && m.loopbackFloats[0] != 0 {
			floats := unsafe.Slice((*float32)(chunk), sampleCount)
			length := len(floats)
			for i := 0; i < length; i++ {
				floats[i] += m.loopbackFloats[i]
			}
			// inputSamples = unsafe.Slice((*byte)(chunk), sampleCount*sizeInBytes)
		}
		inputSamples = unsafe.Slice((*byte)(chunk), sampleCount*sizeInBytes)

		decodedChunk, err := decoder.Decode(hostEndian, inputSamples, inputProp.ChannelCount)
		// FIXME: the decoder should also fill this information
		switch decodedChunk := decodedChunk.(type) {
		case *wave.Float32Interleaved:
			decodedChunk.Size.SamplingRate = inputProp.SampleRate
		case *wave.Int16Interleaved:
			decodedChunk.Size.SamplingRate = inputProp.SampleRate
		default:
			panic("unsupported format")
		}
		return decodedChunk, func() {}, err
	})

	return reader, nil
}

func (m *microphone) Properties() []prop.Media {
	var supportedProps []prop.Media
	logger.Debug("Querying properties")

	var isBigEndian bool
	// miniaudio only uses the host endian
	if hostEndian == binary.BigEndian {
		isBigEndian = true
	}

	for ch := m.MinChannels; ch <= m.MaxChannels; ch++ {
		// FIXME: Currently support 48kHz only. We need to implement a resampler first.
		// for sampleRate := m.MinSampleRate; sampleRate <= m.MaxSampleRate; sampleRate += sampleRateStep {
		sampleRate := 48000
		for i := 0; i < int(m.FormatCount); i++ {
			format := m.Formats[i]

			supportedProp := prop.Media{
				Audio: prop.Audio{
					ChannelCount: int(ch),
					SampleRate:   int(sampleRate),
					IsBigEndian:  isBigEndian,
					// miniaudio only supports interleaved at the moment
					IsInterleaved: true,
					// FIXME: should change this to a less discrete value
					Latency: time.Millisecond * 20,
				},
			}

			switch malgo.FormatType(format) {
			case malgo.FormatF32:
				supportedProp.SampleSize = 4
				supportedProp.IsFloat = true
			case malgo.FormatS16:
				supportedProp.SampleSize = 2
				supportedProp.IsFloat = false
			}

			supportedProps = append(supportedProps, supportedProp)
		}
		// }
	}
	return supportedProps
}
