package main

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

func TestCommunicationPresentationBounds(t *testing.T) {
	s := DefaultSettings()
	if s.GamingOverlayScale != 100 || s.GamingOverlayOpacity != 88 || s.CameraBlurStrength != 14 || !s.CameraMirrorPreview {
		t.Fatal("unexpected presentation defaults")
	}
	s.GamingOverlayScale, s.GamingOverlayOpacity, s.CameraBlurStrength = 900, 0, 200
	s.GamingOverlayX, s.GamingOverlayY = -5, 999
	s.UserShareVolumes = map[string]int{"a": -5, "b": 999}
	s = normalizeSettings(s)
	if s.GamingOverlayScale != 200 || s.GamingOverlayOpacity != 20 || s.CameraBlurStrength != 30 || s.GamingOverlayX != 0 || s.GamingOverlayY != 100 || s.UserShareVolumes["a"] != 0 || s.UserShareVolumes["b"] != 200 {
		t.Fatal("presentation preferences were not bounded")
	}
}

func TestCameraBackgroundValidationAcceptsLocalImagesAndRejectsUnsafeInputs(t *testing.T) {
	encode := func(width, height int, format string) string {
		t.Helper()
		var data bytes.Buffer
		pixels := image.NewNRGBA(image.Rect(0, 0, width, height))
		var err error
		if format == "jpeg" {
			err = jpeg.Encode(&data, pixels, nil)
		} else {
			err = png.Encode(&data, pixels)
		}
		if err != nil {
			t.Fatal(err)
		}
		return "data:image/" + format + ";base64," + base64.StdEncoding.EncodeToString(data.Bytes())
	}
	for _, tc := range []struct {
		name, value string
		valid       bool
	}{
		{"empty", "", true},
		{"png", encode(64, 32, "png"), true},
		{"jpeg", encode(1280, 720, "jpeg"), true},
		{"remote URL", "https://example.com/background.jpg", false},
		{"file URL", "file:///C:/private/background.png", false},
		{"SVG markup", "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)), false},
		{"invalid base64", "data:image/png;base64,invalid%%%", false},
		{"non-image bytes", "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("not an image")), false},
		{"oversized width", encode(2049, 1, "png"), false},
		{"oversized height", encode(1, 2049, "png"), false},
		{"oversized encoded payload", "data:image/png;base64," + strings.Repeat("A", 2800000), false},
		{"oversized decoded payload", "data:image/png;base64," + base64.StdEncoding.EncodeToString(make([]byte, 2*1024*1024+1)), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCameraBackground(tc.value)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%t, validation returned %v", tc.valid, err)
			}
		})
	}
}
