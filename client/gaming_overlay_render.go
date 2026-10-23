package main

import (
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"strconv"
	"strings"
)

// Labels and avatars are rasterized once per snapshot, not once per animation
// frame. The native window owns this renderer and its reusable frame buffer.
type overlayRenderer struct {
	base, frame *image.RGBA
	scale       float64
	animation   *overlayAnimation
	bands       []overlayBand
	layer       float64 // opacity of the row being animated (1 unless fading)
}

type overlayLabel func(text string, width, height, fontSize int, centered bool) (*image.Alpha, error)

func overlayTint(value string) color.NRGBA {
	if len(value) == 7 && value[0] == '#' {
		if rgb, err := strconv.ParseUint(value[1:], 16, 24); err == nil {
			r, g, b := uint8((rgb>>16)&255), uint8((rgb>>8)&255), uint8(rgb&255)
			return color.NRGBA{R: r, G: g, B: b, A: 255}
		}
	}
	return color.NRGBA{R: 54, G: 157, B: 199, A: 255}
}

func normalizeOverlayColor(value string) string {
	tint := overlayTint(value)
	return fmt.Sprintf("#%02x%02x%02x", tint.R, tint.G, tint.B)
}

func overlayAvatar(value string) image.Image {
	const prefix = "data:image/png;base64,"
	if len(value) > 32768 || !strings.HasPrefix(value, prefix) {
		return nil
	}
	data := value[len(prefix):]
	config, err := png.DecodeConfig(base64.NewDecoder(base64.StdEncoding, strings.NewReader(data)))
	if err != nil || config.Width < 1 || config.Height < 1 || config.Width > 128 || config.Height > 128 {
		return nil
	}
	avatar, err := png.Decode(base64.NewDecoder(base64.StdEncoding, strings.NewReader(data)))
	if err != nil {
		return nil
	}
	return avatar
}

func (r *overlayRenderer) text(label overlayLabel, text string, x, y, width, height, fontSize float64, centered bool) error {
	mask, err := label(text, int(width*r.scale), int(height*r.scale), int(fontSize*r.scale), centered)
	if err != nil {
		return err
	}
	origin := image.Pt(int(x*r.scale), int(y*r.scale))
	// A compact dark halo keeps names legible over bright games without a panel.
	shadow := image.NewUniform(color.NRGBA{17, 20, 23, 160})
	for _, offset := range []image.Point{{-1, 0}, {1, 0}, {0, -1}, {0, 1}, {0, 2}} {
		draw.DrawMask(r.base, mask.Bounds().Add(origin.Add(offset)), shadow, image.Point{}, mask, image.Point{}, draw.Over)
	}
	draw.DrawMask(r.base, mask.Bounds().Add(origin), image.NewUniform(color.NRGBA{225, 226, 231, 255}), image.Point{}, mask, image.Point{}, draw.Over)
	return nil
}

func (r *overlayRenderer) circle(x, y, radius float64, fill color.NRGBA) {
	cx, cy, size := x*r.scale, y*r.scale, radius*r.scale
	area := image.Rect(int(cx-size-1), int(cy-size-1), int(cx+size+1), int(cy+size+1)).Intersect(r.base.Bounds())
	mask := image.NewAlpha(area)
	for py := area.Min.Y; py < area.Max.Y; py++ {
		for px := area.Min.X; px < area.Max.X; px++ {
			coverage := math.Max(0, math.Min(1, size+0.5-math.Hypot(float64(px)+0.5-cx, float64(py)+0.5-cy)))
			mask.SetAlpha(px, py, color.Alpha{uint8(coverage * 255)})
		}
	}
	draw.DrawMask(r.base, area, image.NewUniform(fill), image.Point{}, mask, area.Min, draw.Over)
}

func (r *overlayRenderer) avatar(avatar image.Image, x, y, radius float64) {
	cx, cy, size := x*r.scale, y*r.scale, radius*r.scale
	area := image.Rect(int(cx-size), int(cy-size), int(cx+size+1), int(cy+size+1)).Intersect(r.base.Bounds())
	bounds := avatar.Bounds()
	crop := min(bounds.Dx(), bounds.Dy())
	for py := area.Min.Y; py < area.Max.Y; py++ {
		for px := area.Min.X; px < area.Max.X; px++ {
			if math.Hypot(float64(px)+0.5-cx, float64(py)+0.5-cy) > size {
				continue
			}
			sx := bounds.Min.X + (bounds.Dx()-crop)/2 + min(crop-1, int((float64(px)+0.5-cx+size)/(size*2)*float64(crop)))
			sy := bounds.Min.Y + (bounds.Dy()-crop)/2 + min(crop-1, int((float64(py)+0.5-cy+size)/(size*2)*float64(crop)))
			pixel := image.NewUniform(avatar.At(sx, sy))
			draw.Draw(r.base, image.Rect(px, py, px+1, py+1), pixel, image.Point{}, draw.Over)
		}
	}
}
