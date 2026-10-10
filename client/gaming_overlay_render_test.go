package main

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

func overlayTestAvatar(t *testing.T, width int) string {
	t.Helper()
	avatar := image.NewRGBA(image.Rect(0, 0, width, width))
	for y := range width {
		for x := range width {
			avatar.Set(x, y, color.RGBA{R: 220, A: 255})
		}
	}
	var data bytes.Buffer
	if err := png.Encode(&data, avatar); err != nil {
		t.Fatal(err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(data.Bytes())
}

func TestOverlayAvatarValidation(t *testing.T) {
	for _, value := range []string{"", "https://example.org/avatar.png", "data:image/png;base64,invalid", strings.Repeat("x", 32769), overlayTestAvatar(t, 129)} {
		if overlayAvatar(value) != nil {
			t.Fatal("accepted unsafe or oversized avatar")
		}
	}
	if overlayAvatar(overlayTestAvatar(t, 64)) == nil {
		t.Fatal("rejected bounded PNG thumbnail")
	}
}

func TestOverlayRendererSeparatesPeopleAndAnimatesOnlyIndicators(t *testing.T) {
	s := normalizeGamingOverlay(GamingOverlaySnapshot{Active: true, Scale: 100, Speakers: []GamingOverlaySpeaker{
		{ID: "alice", Name: "Alice", Speaking: true, Avatar: overlayTestAvatar(t, 64)},
		{ID: "bob", Name: "Bob", Speaking: true},
		{Name: "Quiet"},
	}})
	var labels []string
	r, err := newOverlayRenderer(s, 280, 140, func(text string, width, height, _ int, _ bool) (*image.Alpha, error) {
		labels = append(labels, text)
		return image.NewAlpha(image.Rect(0, 0, width, height)), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(labels, ",") != "Alice,B,Bob" {
		t.Fatalf("labels = %v", labels)
	}
	first := bytes.Clone(r.render(0).Pix)
	second := r.render(0.2)
	if bytes.Equal(first, second.Pix) {
		t.Fatal("speaking indicator did not animate")
	}
	if got := second.RGBAAt(30, 32); got.R != 220 || got.A != 255 {
		t.Fatalf("avatar not rendered: %v", got)
	}
	if second.RGBAAt(30, 6).A == 0 {
		t.Fatal("speaking ring missing")
	}
	for y := range 140 {
		for x := range 280 {
			i := second.PixOffset(x, y)
			if !bytes.Equal(first[i:i+4], second.Pix[i:i+4]) && (x < 68 || x > 110 || y%76 < 34 || y%76 > 54) {
				t.Fatalf("animation moved avatar or label at %d,%d", x, y)
			}
			if (y >= 64 && y < 76 || x > 120) && second.RGBAAt(x, y).A != 0 {
				t.Fatalf("unexpected panel backdrop at %d,%d", x, y)
			}
		}
	}
}
