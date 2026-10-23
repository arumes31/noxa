package main

import (
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	"image/draw"
	"math"
)

const (
	overlayMotionSpeed   = 2.4
	overlayCombAmplitude = 8.0
	overlayContourGap    = 1.2
)

type overlayContourColumn struct{ x, top, bottom float64 }
type overlayHaloPixel struct {
	x, y     int
	distance float64
}
type overlayAnimationRow struct {
	width, center, phase float64
	text                 image.Rectangle
	tint                 color.NRGBA
	contour              []overlayContourColumn
	halo                 []overlayHaloPixel
	speaking             bool // quiet rows keep only their static avatar and name
}
type overlayAnimation struct{ rows []overlayAnimationRow }

// overlayBand is a horizontal slice of the static layer: one per row, then one
// per chat peek line. Together they partition the surface, so drawing every
// band at full opacity equals drawing the whole static layer at once.
type overlayBand struct {
	area  image.Rectangle
	alpha float64
}

func overlayInkWidth(mask *image.Alpha) int {
	width := 0
	for y := mask.Bounds().Min.Y; y < mask.Bounds().Max.Y; y++ {
		for x := mask.Bounds().Min.X; x < mask.Bounds().Max.X; x++ {
			if mask.AlphaAt(x, y).A != 0 {
				width = max(width, x-mask.Bounds().Min.X+1)
			}
		}
	}
	return max(1, width)
}

// Cache the real glyph edges, including descenders, once per snapshot. Empty
// columns use their nearest ink edge so the comb spans spaces and the full name.
func overlayNameContour(mask *image.Alpha, origin image.Point, scale float64) []overlayContourColumn {
	width := overlayInkWidth(mask)
	tops, bottoms, nearest := make([]int, width), make([]int, width), make([]int, width)
	last := -1
	for x := range width {
		tops[x], bottoms[x] = -1, -1
		for y := mask.Bounds().Min.Y; y < mask.Bounds().Max.Y; y++ {
			if mask.AlphaAt(mask.Bounds().Min.X+x, y).A > 48 {
				if tops[x] < 0 {
					tops[x] = y - mask.Bounds().Min.Y
				}
				bottoms[x] = y - mask.Bounds().Min.Y + 1
			}
		}
		if tops[x] >= 0 {
			last = x
		}
		nearest[x] = last
	}
	last = -1
	for x := width - 1; x >= 0; x-- {
		if tops[x] >= 0 {
			last = x
		}
		if last >= 0 && (nearest[x] < 0 || last-x < x-nearest[x]) {
			nearest[x] = last
		}
	}
	columns := make([]overlayContourColumn, 0, int(float64(width)/scale/2.5)+1)
	for x := 0.0; x < float64(width)/scale; x += 2.5 {
		pixel := min(width-1, int(math.Round(x*scale)))
		ink := nearest[pixel]
		if ink >= 0 {
			columns = append(columns, overlayContourColumn{
				x:   float64(origin.X)/scale + x,
				top: float64(origin.Y+tops[ink]) / scale, bottom: float64(origin.Y+bottoms[ink]) / scale,
			})
		}
	}
	return columns
}

func overlaySpeakerPhase(id, name string) float64 {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(id + name))
	return float64(hash.Sum32()%628) / 100
}

func newOverlayRenderer(s GamingOverlaySnapshot, width, height int, label overlayLabel) (*overlayRenderer, error) {
	if width < 1 || height < 1 || s.Scale < 1 {
		return nil, fmt.Errorf("invalid overlay dimensions %dx%d at scale %d", width, height, s.Scale)
	}
	scale := float64(s.Scale) / 100
	longest := 1.0
	for _, person := range s.Speakers {
		mask, err := label(person.Name, 2400, max(1, int(26*scale)), max(1, int(16*scale)), false)
		if err != nil {
			return nil, err
		}
		longest = math.Max(longest, float64(overlayInkWidth(mask))/scale)
	}
	contentWidth, stackHeight := overlayContentSize(len(s.Speakers), longest, s.ChatPeek)
	scale = math.Min(scale, math.Min(float64(width)/contentWidth, float64(height)/stackHeight))
	r := &overlayRenderer{scale: scale, layer: 1, animation: &overlayAnimation{rows: make([]overlayAnimationRow, 0, len(s.Speakers))}}
	r.base = image.NewRGBA(image.Rect(0, 0, max(1, min(width, int(math.Ceil(contentWidth*scale)))), max(1, min(height, int(math.Ceil(stackHeight*scale))))))
	for index, person := range s.Speakers {
		y := float64(index * 72)
		mask, err := label(person.Name, max(1, int(longest*scale)+4), max(1, int(26*scale)), max(1, int(16*scale)), false)
		if err != nil {
			return nil, err
		}
		inkWidth := overlayInkWidth(mask)
		nameWidth := float64(inkWidth) / scale
		origin := image.Pt(int(70*scale), int((y+79)*scale))
		shadow := image.NewUniform(color.NRGBA{R: 17, G: 20, B: 23, A: 160})
		for _, offset := range []image.Point{{-1, 0}, {1, 0}, {0, -1}, {0, 1}, {0, 2}} {
			draw.DrawMask(r.base, mask.Bounds().Add(origin.Add(offset)), shadow, image.Point{}, mask, image.Point{}, draw.Over)
		}
		draw.DrawMask(r.base, mask.Bounds().Add(origin), image.NewUniform(color.NRGBA{R: 225, G: 226, B: 231, A: 255}), image.Point{}, mask, image.Point{}, draw.Over)
		tint := overlayTint(person.Color)
		r.circle(38, y+92, 20, tint)
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
		row := overlayAnimationRow{
			width: 88 + nameWidth, center: y + 92, phase: overlaySpeakerPhase(person.ID, person.Name), tint: tint,
			text:    image.Rect(origin.X, origin.Y, origin.X+inkWidth, origin.Y+mask.Bounds().Dy()),
			contour: overlayNameContour(mask, origin, scale), speaking: person.Speaking,
		}
		// Distance samples are reused by the breathing halo; no per-frame masks.
		area := image.Rect(0, int((row.center-38)*scale), int(math.Ceil(76*scale)), int(math.Ceil((row.center+38)*scale))).Intersect(r.base.Bounds())
		for py := area.Min.Y; py < area.Max.Y; py++ {
			for px := area.Min.X; px < area.Max.X; px++ {
				distance := math.Hypot((float64(px)+.5)/scale-38, (float64(py)+.5)/scale-row.center)
				if distance <= 37 {
					row.halo = append(row.halo, overlayHaloPixel{px, py, distance})
				}
			}
		}
		r.animation.rows = append(r.animation.rows, row)
	}
	if err := r.peek(s, label); err != nil {
		return nil, err
	}
	r.layoutBands(len(s.Speakers), len(s.Peek))
	r.frame = image.NewRGBA(r.base.Bounds())
	return r, nil
}

// peek rasterizes the chat lines once per snapshot, in the overlay's own
// label style: the same face, light text and compact dark halo as the names,
// with no panel behind them. The message text is slightly dimmer than the name.
func (r *overlayRenderer) peek(s GamingOverlaySnapshot, label overlayLabel) error {
	top := overlayPeekTop(len(s.Speakers))
	height, font := max(1, int(overlayPeekLine*r.scale)), max(1, int(overlayPeekFont*r.scale))
	for index, line := range s.Peek {
		y := int((top + float64(index)*overlayPeekLine) * r.scale)
		name, err := label(line.Name, max(1, int(overlayPeekNameMax*r.scale)), height, font, false)
		if err != nil {
			return err
		}
		left := int(overlayPeekLeft * r.scale)
		r.label(name, image.Pt(left, y), 255)
		textX := left + overlayInkWidth(name) + int(8*r.scale)
		textWidth := int((overlayPeekLeft+overlayPeekWidth)*r.scale) - textX
		if textWidth < int(16*r.scale) {
			continue
		}
		text, err := label(line.Text, textWidth, height, font, false)
		if err != nil {
			return err
		}
		r.label(text, image.Pt(textX, y), 205)
	}
	return nil
}

// label draws a cached glyph mask with the overlay's text halo.
func (r *overlayRenderer) label(mask *image.Alpha, origin image.Point, opacity uint8) {
	shadow := image.NewUniform(color.NRGBA{R: 17, G: 20, B: 23, A: uint8((160 * int(opacity) / 255) & 255)})
	for _, offset := range []image.Point{{-1, 0}, {1, 0}, {0, -1}, {0, 1}, {0, 2}} {
		draw.DrawMask(r.base, mask.Bounds().Add(origin.Add(offset)), shadow, image.Point{}, mask, image.Point{}, draw.Over)
	}
	draw.DrawMask(r.base, mask.Bounds().Add(origin), image.NewUniform(color.NRGBA{R: 225, G: 226, B: 231, A: opacity}), image.Point{}, mask, image.Point{}, draw.Over)
}

// layoutBands splits the static layer between rows (at the midpoints between
// avatar centers) and peek lines, so each can fade on its own.
func (r *overlayRenderer) layoutBands(rows, lines int) {
	bounds := r.base.Bounds()
	edges := make([]int, 0, rows+lines+1)
	edges = append(edges, bounds.Min.Y)
	for index := 1; index < rows; index++ {
		edges = append(edges, int(float64(56+index*72)*r.scale))
	}
	top := overlayPeekTop(rows)
	for index := range lines {
		if rows > 0 || index > 0 {
			edges = append(edges, int((top+float64(index)*overlayPeekLine)*r.scale))
		}
	}
	edges = append(edges, bounds.Max.Y)
	r.bands = make([]overlayBand, 0, len(edges)-1)
	for index := 0; index+1 < len(edges); index++ {
		low, high := min(max(edges[index], bounds.Min.Y), bounds.Max.Y), min(max(edges[index+1], bounds.Min.Y), bounds.Max.Y)
		r.bands = append(r.bands, overlayBand{area: image.Rect(bounds.Min.X, low, bounds.Max.X, max(low, high)), alpha: 1})
	}
}

// apply sets per-row speaking state and the row/peek opacities for the next
// frame. A speaking change needs no new rasterization: quiet rows only skip
// the animated halo and comb.
func (r *overlayRenderer) apply(scene overlayScene) {
	for index := range r.animation.rows {
		if index < len(scene.snapshot.Speakers) {
			r.animation.rows[index].speaking = scene.snapshot.Speakers[index].Speaking
		}
	}
	alphas := append(append([]float64(nil), scene.rowAlpha...), scene.peekAlpha...)
	if len(alphas) == 0 && len(r.bands) == 1 {
		return // A single band without rows or lines (empty preview layout).
	}
	for index := range r.bands {
		if index < len(alphas) {
			r.bands[index].alpha = overlayUnit(alphas[index])
		}
	}
}

func overlayUnit(value float64) float64 { return math.Max(0, math.Min(1, value)) }

// Speaking activity is decorative, never a measured microphone level. The
// selected light halo and glyph-hugging comb use the same 2.4x / 8px preset.
func (r *overlayRenderer) render(seconds float64) *image.RGBA {
	clear(r.frame.Pix)
	for index, row := range r.animation.rows {
		if !row.speaking {
			continue
		}
		r.layer = r.bandAlpha(index)
		if r.layer <= 0 {
			continue
		}
		t := seconds*overlayMotionSpeed + row.phase
		pulse := .5 + .5*math.Sin(t*4)
		radius := (23 + 3*pulse) * 1.25 // Preserve the selected shape around a 40px avatar.
		outer := radius + 3.75
		for _, pixel := range row.halo {
			d := pixel.distance
			r.animationPixel(pixel.x, pixel.y, overlayUnit((23.75-d)*r.scale+.5)*(.12+.08*pulse), row.tint)
			if d > 20 && d < outer {
				q := (d - 20) / (outer - 20)
				glow := math.Min(q/.45, (1-q)/.55) * (160.0 / 255)
				r.animationPixel(pixel.x, pixel.y, glow, row.tint)
			}
			distance := math.Abs(d - radius)
			r.animationPixel(pixel.x, pixel.y, overlayUnit((2.4375-distance)*r.scale+.5), color.NRGBA{R: 6, G: 17, B: 27, A: 255})
			r.animationPixel(pixel.x, pixel.y, overlayUnit((1.625-distance)*r.scale+.5), row.tint)
		}
		for _, column := range row.contour {
			// Anchor the wave to the name, as in the selectable preview.
			x := column.x - float64(row.text.Min.X)/r.scale + 68
			a := .15 + .55*(.5+.5*math.Sin(t*4.2-x*.15)) + .3*(.5+.5*math.Sin(t*2.1+x*.072))
			length := 1 + overlayCombAmplitude*a
			r.animationRect(column.x-.7, column.top, 1.4, column.bottom-column.top, .13+.14*a, row.tint)
			for _, edge := range [][2]float64{{column.top - overlayContourGap - length, length}, {column.bottom + overlayContourGap, length}} {
				r.animationRect(column.x-1.45, edge[0], 2.9, edge[1], .88, color.NRGBA{R: 6, G: 17, B: 27, A: 255})
				r.animationRect(column.x-.7, edge[0], 1.4, edge[1], .88, row.tint)
			}
		}
	}
	r.layer = 1
	for _, band := range r.bands {
		switch {
		case band.alpha >= 1:
			draw.Draw(r.frame, band.area, r.base, band.area.Min, draw.Over)
		case band.alpha > 0:
			mask := image.NewUniform(color.Alpha{A: uint8(band.alpha * 255)})
			draw.DrawMask(r.frame, band.area, r.base, band.area.Min, mask, image.Point{}, draw.Over)
		}
	}
	return r.frame
}

func (r *overlayRenderer) bandAlpha(index int) float64 {
	if index < len(r.bands) {
		return r.bands[index].alpha
	}
	return 1
}

func (r *overlayRenderer) animationRect(x, y, width, height, opacity float64, tint color.NRGBA) {
	left, top, right, bottom := x*r.scale, y*r.scale, (x+width)*r.scale, (y+height)*r.scale
	area := image.Rect(int(math.Floor(left)), int(math.Floor(top)), int(math.Ceil(right)), int(math.Ceil(bottom))).Intersect(r.frame.Bounds())
	for py := area.Min.Y; py < area.Max.Y; py++ {
		fy := math.Max(0, math.Min(float64(py+1), bottom)-math.Max(float64(py), top))
		for px := area.Min.X; px < area.Max.X; px++ {
			fx := math.Max(0, math.Min(float64(px+1), right)-math.Max(float64(px), left))
			r.animationPixel(px, py, fx*fy*opacity, tint)
		}
	}
}

func (r *overlayRenderer) animationPixel(x, y int, opacity float64, tint color.NRGBA) {
	alpha := uint16(overlayUnit(opacity*r.layer) * 255)
	if alpha == 0 {
		return
	}
	i := r.frame.PixOffset(x, y)
	for channel, value := range [4]uint16{uint16(tint.R), uint16(tint.G), uint16(tint.B), 255} {
		r.frame.Pix[i+channel] = uint8(((value*alpha + uint16(r.frame.Pix[i+channel])*(255-alpha)) / 255) & 255)
	}
}
