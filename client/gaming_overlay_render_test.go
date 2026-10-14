package main

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"math"
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

func TestDefaultOverlayCachesLabelsAndKeepsAvatarsStill(t *testing.T) {
	s := normalizeGamingOverlay(GamingOverlaySnapshot{Active: true, Scale: 100, Speakers: []GamingOverlaySpeaker{
		{ID: "alice", Name: "Alice", Speaking: true, Avatar: overlayTestAvatar(t, 64)},
		{ID: "bob", Name: "Bob", Speaking: true},
		{Name: "Quiet"},
	}})
	var labels []string
	r, err := newOverlayRenderer(s, 800, 800, func(text string, width, height, font int, centered bool) (*image.Alpha, error) {
		labels = append(labels, text)
		return mistTestLabel(text, width, height, font, centered)
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(labels, ",") != "Alice,Bob,Alice,Bob,B" {
		t.Fatalf("labels = %v", labels)
	}
	cached := len(labels)
	first := bytes.Clone(r.render(0).Pix)
	second := r.render(0.2)
	if len(labels) != cached {
		t.Fatal("animation rasterized labels again")
	}
	if bytes.Equal(first, second.Pix) {
		t.Fatal("speaking indicator did not animate")
	}
	if got := second.RGBAAt(38, 92); got.R != 220 || got.A != 255 {
		t.Fatalf("avatar not rendered: %v", got)
	}
	if second.RGBAAt(38, 72).A == 0 {
		t.Fatal("speaking ring missing")
	}
	for _, row := range r.animation.rows {
		center := int(row.center)
		for y := center - 15; y < center+15; y++ {
			for x := 24; x < 52; x++ {
				if math.Hypot(float64(x)-38, float64(y-center)) > 16 {
					continue
				}
				i := second.PixOffset(x, y)
				if !bytes.Equal(first[i:i+4], second.Pix[i:i+4]) {
					t.Fatalf("animation moved avatar at %d,%d", x, y)
				}
			}
		}
	}
}
