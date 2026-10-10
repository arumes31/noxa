package main

import (
	"hash/fnv"
	"image"
	"image/color"
	"image/draw"
	"math"
)

type overlayMistRow struct {
	width, center, phase float64
	text                 image.Rectangle
}

type overlayMist struct {
	rows []overlayMistRow
	mask *image.Alpha
}

func overlayInkWidth(mask *image.Alpha) int {
	width := 0
	for y := mask.Bounds().Min.Y; y < mask.Bounds().Max.Y; y++ {
		for x := mask.Bounds().Min.X; x < mask.Bounds().Max.X; x++ {
			if mask.AlphaAt(x, y).A != 0 {
				width = max(width, x+1)
			}
		}
	}
	return max(1, width)
}

func newMistOverlayRenderer(s GamingOverlaySnapshot, width, height int, label overlayLabel) (*overlayRenderer, error) {
	scale := float64(s.Scale) / 100
	longest := 1.0
	for _, person := range s.Speakers {
		mask, err := label(person.Name, 2400, max(1, int(26*scale)), max(1, int(16*scale)), false)
		if err != nil {
			return nil, err
		}
		longest = math.Max(longest, float64(overlayInkWidth(mask))/scale)
	}
	// Fit all rows to the selected work area, including their upper/lower motion.
	scale = math.Min(scale, math.Min(float64(width)/(88+longest), float64(height)/float64(184*max(1, len(s.Speakers)))))
	r := &overlayRenderer{scale: scale, mist: &overlayMist{rows: make([]overlayMistRow, 0, len(s.Speakers))}}
	r.base = image.NewRGBA(image.Rect(0, 0, max(1, int(math.Ceil((88+longest)*scale))), max(1, int(math.Ceil(float64(184*len(s.Speakers))*scale)))))
	for index, person := range s.Speakers {
		y := float64(index * 184)
		mask, err := label(person.Name, max(1, int(longest*scale)+4), max(1, int(26*scale)), max(1, int(16*scale)), false)
		if err != nil {
			return nil, err
		}
		nameWidth := float64(overlayInkWidth(mask)) / scale
		origin := image.Pt(int(70*scale), int((y+79)*scale))
		shadow := image.NewUniform(color.NRGBA{R: 17, G: 20, B: 23, A: 160})
		for _, offset := range []image.Point{{-1, 0}, {1, 0}, {0, -1}, {0, 1}, {0, 2}} {
			draw.DrawMask(r.base, mask.Bounds().Add(origin.Add(offset)), shadow, image.Point{}, mask, image.Point{}, draw.Over)
		}
		draw.DrawMask(r.base, mask.Bounds().Add(origin), image.NewUniform(color.NRGBA{R: 225, G: 226, B: 231, A: 255}), image.Point{}, mask, image.Point{}, draw.Over)
		r.circle(38, y+92, 22, color.NRGBA{R: 0, G: 242, B: 255, A: 25})
		r.circle(38, y+92, 20, color.NRGBA{R: 0, G: 242, B: 255, A: 255})
		r.circle(38, y+92, 18, color.NRGBA{R: 17, G: 20, B: 23, A: 255})
		if avatar := overlayAvatar(person.Avatar); avatar != nil {
			r.avatar(avatar, 38, y+92, 17)
		} else {
			initial := "?"
			if chars := []rune(person.Name); len(chars) > 0 {
				initial = string(chars[0])
			}
			if err := r.text(label, initial, 21, y+75, 34, 34, 18, true); err != nil {
				return nil, err
			}
		}
		hash := fnv.New32a()
		_, _ = hash.Write([]byte(person.ID + person.Name))
		r.mist.rows = append(r.mist.rows, overlayMistRow{
			width: 88 + nameWidth, center: y + 92, phase: float64(hash.Sum32()%628) / 100,
			text: image.Rect(origin.X, origin.Y, origin.X+int(nameWidth*scale), origin.Y+mask.Bounds().Dy()),
		})
	}
	r.frame = image.NewRGBA(r.base.Bounds())
	r.mist.mask = image.NewAlpha(r.base.Bounds())
	for _, row := range r.mist.rows {
		for y := max(0, int((row.center-92)*scale)); y < min(r.base.Bounds().Dy(), int((row.center+92)*scale)); y++ {
			for x := 2; x < min(r.base.Bounds().Dx()-2, int(row.width*scale)-2); x++ {
				px, py := float64(x)/scale, float64(y)/scale
				fx := math.Min(overlaySmooth((px-float64(row.text.Min.X)/scale+12)/8), overlaySmooth((float64(row.text.Max.X)/scale+12-px)/8))
				fy := math.Min(overlaySmooth((py-float64(row.text.Min.Y)/scale+12)/8), overlaySmooth((float64(row.text.Max.Y)/scale+12-py)/8))
				r.mist.mask.SetAlpha(x, y, color.Alpha{A: uint8(math.Floor(255 * (.65 - .55*fx*fy)))})
			}
		}
	}
	return r, nil
}

func overlaySmooth(value float64) float64 {
	x := math.Max(0, math.Min(1, value))
	return x * x * (3 - 2*x)
}

// Mist and aurora are decorative speaking activity, not measured microphone levels.
func (r *overlayRenderer) renderMist(seconds float64) *image.RGBA {
	clear(r.frame.Pix)
	for _, row := range r.mist.rows {
		t := seconds + row.phase
		strength := .8 * (.55 + .45*math.Pow(math.Sin(t*.9), 2))
		for x := 1.0; x < row.width-1; x += 1 / r.scale {
			edge := math.Pow(math.Max(0, math.Sin(math.Pi*x/row.width)), .48)
			distance := (x - 38) / 34
			span := edge * (41 + math.Exp(-distance*distance)*14)
			fade := math.Min(1, math.Min(x, row.width-x)/18)
			wave := func(phase float64) float64 { return math.Sin(x*.041-t*1.7+phase)*.72 + math.Sin(x*.09+t+phase)*.28 }
			for layer := range 5 {
				cy := row.center + span*.7*math.Sin(x*.022-t*.6+float64(layer)*1.1)
				r.mistColumn(x, cy-float64(7+layer)*edge, cy+float64(7+layer)*edge, .055*fade)
				r.mistColumn(x, cy-.55, cy+.55, .23*fade)
			}
			// Curtains leave the same three-pixel rhythm as the selected preview.
			if math.Mod(x, 3) < 1.4 {
				spread := span * (.75 + strength*.18*wave(0))
				shift := wave(1) * spread * .2
				r.mistColumn(x, row.center-spread+shift, row.center+spread+shift, (.2+.35*(.5+.5*math.Sin(x*.055-t*1.2)))*fade)
			}
			for strand := range 3 {
				cy := row.center + edge*float64(2+strand*3)*math.Sin(x*.03+t*.7+float64(strand)*1.7)
				r.mistColumn(x, cy-.5, cy+.5, .2*fade)
			}
		}
	}
	// Cap the final composite, after all overlapping layers, before drawing text.
	for pixel, mask := range r.mist.mask.Pix {
		i := pixel * 4
		for channel := range 4 {
			r.frame.Pix[i+channel] = uint8(uint16(r.frame.Pix[i+channel]) * uint16(mask) / 255)
		}
	}
	draw.Draw(r.frame, r.frame.Bounds(), r.base, image.Point{}, draw.Over)
	return r.frame
}

func (r *overlayRenderer) mistColumn(x, top, bottom, opacity float64) {
	px := int(x * r.scale)
	top, bottom = top*r.scale, bottom*r.scale
	for y := max(0, int(math.Floor(top))); y < min(r.frame.Bounds().Dy(), int(math.Ceil(bottom))); y++ {
		coverage := math.Max(0, math.Min(float64(y+1), bottom)-math.Max(float64(y), top))
		alpha := uint16(math.Min(255, coverage*opacity*255))
		i := r.frame.PixOffset(px, y)
		for channel, value := range []uint16{53, 214, 233, 255} {
			r.frame.Pix[i+channel] = uint8((value*alpha + uint16(r.frame.Pix[i+channel])*(255-alpha)) / 255)
		}
	}
}
