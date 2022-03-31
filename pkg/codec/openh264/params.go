package openh264

import (
	"github.com/Meonardo/mediadevices/pkg/codec"
	"github.com/Meonardo/mediadevices/pkg/io/video"
	"github.com/Meonardo/mediadevices/pkg/prop"
)

// Params stores libopenh264 specific encoding parameters.
type Params struct {
	codec.BaseParams
}

// NewParams returns default openh264 codec specific parameters.
func NewParams() (Params, error) {
	return Params{
		BaseParams: codec.BaseParams{
			BitRate: 100000,
		},
	}, nil
}

// RTPCodec represents the codec metadata
func (p *Params) RTPCodec() *codec.RTPCodec {
	return codec.NewRTPH264Codec(90000)
}

// BuildVideoEncoder builds openh264 encoder with given params
func (p *Params) BuildVideoEncoder(r video.Reader, property prop.Media) (codec.ReadCloser, error) {
	return newEncoder(r, property, *p)
}
