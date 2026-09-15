package knowledge

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"strings"
)

type ImagePreparePolicy struct {
	MaxBytes  int64
	MaxPixels int64
}
type PreparedImage struct {
	Bytes          []byte
	MimeType       string
	Width          int
	Height         int
	Representation string
}

func DefaultImagePreparePolicy() ImagePreparePolicy {
	return ImagePreparePolicy{MaxBytes: 20 << 20, MaxPixels: 40_000_000}
}

func PrepareImage(path, mimeType string, policy ImagePreparePolicy) (PreparedImage, error) {
	if policy.MaxBytes <= 0 || policy.MaxPixels <= 0 {
		return PreparedImage{}, fmt.Errorf("%w: positive image limits are required", ErrInvalid)
	}
	info, err := os.Stat(path)
	if err != nil {
		return PreparedImage{}, err
	}
	if info.Size() > policy.MaxBytes {
		return PreparedImage{}, fmt.Errorf("%w: image byte limit exceeded", ErrInvalid)
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return PreparedImage{}, err
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(payload))
	if err != nil {
		return PreparedImage{}, fmt.Errorf("%w: unsupported or corrupt image", ErrInvalid)
	}
	pixels := int64(config.Width) * int64(config.Height)
	if pixels > policy.MaxPixels {
		return PreparedImage{}, fmt.Errorf("%w: image pixel limit exceeded", ErrInvalid)
	}
	if strings.TrimSpace(mimeType) == "" {
		mimeType = "image/" + format
	}
	return PreparedImage{Bytes: payload, MimeType: mimeType, Width: config.Width, Height: config.Height, Representation: "original"}, nil
}
