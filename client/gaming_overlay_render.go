package main

import (
	"encoding/base64"
	"hash/fnv"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"strings"
)

// Labels and avatars are rasterized once per snapshot, not once per animation
// frame. The native window owns this renderer and its reusable frame buffer.
type overlayRenderer struct {
	base, frame *image.RGBA
	scale       float64
	phases      []float64
}

type overlayLabel func(text string, width, height, fontSize int, centered bool) (*image.Alpha, error)

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

func newOverlayRenderer(s GamingOverlaySnapshot, width, height int, label overlayLabel) (*overlayRenderer, error) {
	r := &overlayRenderer{base: image.NewRGBA(image.Rect(0, 0, width, height)), scale: float64(s.Scale) / 100}
	for index, speaker := range s.Speakers {
		y := float64(index * 76)
		// Transparent space separates each person. Only the avatar has a backing.
		r.circle(30, y+32, 28, color.NRGBA{0, 242, 255, 25})
		r.circle(30, y+32, 25, color.NRGBA{0, 242, 255, 255})
		r.circle(30, y+32, 23, color.NRGBA{17, 20, 23, 255})
		avatar := overlayAvatar(speaker.Avatar)
		if avatar != nil {
			r.avatar(avatar, 30, y+32, 22)
		} else {
			initial := "?"
			if name := []rune(speaker.Name); len(name) > 0 {
				initial = strings.ToUpper(string(name[0]))
			}
			if err := r.text(label, initial, 8, y+10, 44, 44, 22, true); err != nil {
				return nil, err
			}
		}
		if err := r.text(label, speaker.Name, 68, y+8, 208, 26, 16, false); err != nil {
			return nil, err
		}
		hash := fnv.New32a()
		_, _ = hash.Write([]byte(speaker.ID + speaker.Name))
		r.phases = append(r.phases, float64(hash.Sum32()%628)/100)
	}
	r.frame = image.NewRGBA(r.base.Bounds())
	return r, nil
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

func (r *overlayRenderer) render(seconds float64) *image.RGBA {
	copy(r.frame.Pix, r.base.Pix)
	cyan := image.NewUniform(color.NRGBA{0, 242, 255, 255})
	for row, phase := range r.phases {
		for bar := range 11 {
			// Activity animation, not an audio-level meter. Each speaker has a
			// distinct phase and keeps moving without frontend/IPC frame updates.
			envelope := 0.35 + 0.65*math.Sin(float64(bar)*math.Pi/10)
			height := (3 + 14*envelope*(0.5+0.5*math.Sin(seconds*9+phase-float64(bar)*0.8))) * r.scale
			x := int(float64(68+bar*4) * r.scale)
			cy := float64(row*76+44) * r.scale
			rect := image.Rect(x, int(cy-height/2), x+max(1, int(2*r.scale)), int(cy+height/2)+1)
			draw.Draw(r.frame, rect, cyan, image.Point{}, draw.Over)
		}
	}
	return r.frame
}
