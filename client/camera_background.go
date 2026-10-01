package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"strings"
)

func validateCameraBackground(value string) error {
	if value == "" {
		return nil
	}
	invalid := errors.New("camera background must be a PNG or JPEG image up to 2 MiB and 2048×2048 pixels")
	if len(value) > 2800000 {
		return invalid
	}
	var raw string
	for _, prefix := range []string{"data:image/png;base64,", "data:image/jpeg;base64,"} {
		if strings.HasPrefix(value, prefix) {
			raw = strings.TrimPrefix(value, prefix)
			break
		}
	}
	if raw == "" {
		return invalid
	}
	data, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(data) > 2*1024*1024 {
		return invalid
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "png" && format != "jpeg") || config.Width < 1 || config.Height < 1 || config.Width > 2048 || config.Height > 2048 {
		return invalid
	}
	return nil
}
