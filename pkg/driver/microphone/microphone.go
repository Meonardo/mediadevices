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
	chunkChan       chan []byte
	deviceCloseFunc func()

	loopbackDevice *malgo.Device
	loopbackFloats []float32
}

func initLoopbackDeviceInfo() *malgo.DeviceInfo {
	var err error
	ctx, err = malgo.InitContext(nil, malgo.ContextConfig{}, func(message string) {
		logger.Debugf("%v\n", message)
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

func initLoopbackDevice(callbacks *malgo.DeviceCallbacks) (*malgo.Device, error) {
	//info := initLoopbackDeviceInfo()

	deviceConfig := malgo.DefaultDeviceConfig(malgo.Loopback)
	deviceConfig.Capture.Format = malgo.FormatF32
	deviceConfig.Capture.Channels = 2
	deviceConfig.SampleRate = 48000

	device, err := malgo.InitDevice(ctx.Context, deviceConfig, *callbacks)
	return device, err
}

func init() {
	backends := []malgo.Backend{
		malgo.BackendWasapi,
	}

	var err error
	ctx, err = malgo.InitContext(backends, malgo.ContextConfig{}, func(message string) {
		logger.Debugf("%v\n", message)
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
	m.chunkChan = make(chan []byte, 1)
	return nil
}

func (m *microphone) Close() error {
	if m.deviceCloseFunc != nil {
		m.deviceCloseFunc()
	}
	return nil
}

func float32SliceToByteSlice(src []float32) []byte {
	if len(src) == 0 {
		return nil
	}
	l := len(src) * 4
	ptr := unsafe.Pointer(&src[0])
	slice := unsafe.Slice((*byte)(ptr), l)
	return slice
}

// Reference: https://github.com/golang/go/wiki/cgo#turning-c-arrays-into-go-slices
func byteSliceToFloat32Slice(src []byte) []float32 {
	if len(src) == 0 {
		return nil
	}

	l := len(src) / 4
	ptr := unsafe.Pointer(&src[0])

	slice := unsafe.Slice((*float32)(ptr), l)
	return slice
	// return (*[1 << 26]float32)((*[1 << 26]float32)(ptr))[:l:l]
}

func (m *microphone) StopMixing() {

}

func (m *microphone) StartMixing() {
	if m.loopbackDevice == nil {
		var callbacks malgo.DeviceCallbacks

		var channels uint32 = 2
		// sizeInBytes := uint32(malgo.SampleSizeInBytes(malgo.FormatF32))
		onRecvChunk := func(_, pInput unsafe.Pointer, frameCount uint32) {
			sampleCount := frameCount * channels
			floats := unsafe.Slice((*float32)(pInput), sampleCount)
			m.loopbackFloats = floats
		}
		callbacks.RawData = onRecvChunk

		// init loopback device
		loopbackDevice, err := initLoopbackDevice(&callbacks)
		if err != nil {
			return
		}
		m.loopbackDevice = loopbackDevice
	}
	err := m.loopbackDevice.Start()
	if err != nil {
		return
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
	config.Capture.Channels = uint32(inputProp.ChannelCount)
	config.SampleRate = uint32(inputProp.SampleRate)
	config.PeriodSizeInMilliseconds = uint32(inputProp.Latency.Milliseconds())
	//FIX: Turn on the microphone with the current device id
	config.Capture.DeviceID = m.ID.Pointer()
	config.Capture.Format = malgo.FormatF32

	cancelCtx, cancel := context.WithCancel(context.Background())
	onRecvChunk := func(_, chunk []byte, framecount uint32) {
		select {
		case <-cancelCtx.Done():
		case m.chunkChan <- chunk:
		}
	}
	callbacks.Data = onRecvChunk

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
	m.StartMixing()

	var reader audio.Reader = audio.ReaderFunc(func() (wave.Audio, func(), error) {
		chunk, ok := <-m.chunkChan
		if !ok {
			m.deviceCloseFunc()
			logger.Debug("Microphone io.EOF!")
			return nil, func() {}, io.EOF
		}

		// here goes with mixing
		if m.loopbackFloats != nil {
			floats := byteSliceToFloat32Slice(chunk)
			length := len(floats)
			for i := 0; i < length; i++ {
				floats[i] += m.loopbackFloats[i]
			}

			chunk = float32SliceToByteSlice(floats)
		}

		decodedChunk, err := decoder.Decode(hostEndian, chunk, inputProp.ChannelCount)
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
