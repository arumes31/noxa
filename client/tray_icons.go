package main

import (
	"bytes"
	_ "embed"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"runtime"
	"sync"

	"noxa/internal/safecast"
)

type trayIconState uint8

// The original six values keep their numbers; new states are appended so
// stored or logged indices keep their meaning. Iterate with trayStateCount.
const (
	trayIdle            trayIconState = iota // in a channel, microphone on
	trayTalking                              // in a channel, speech detected
	trayMicMuted                             // microphone muted
	trayDeafened                             // audio (output) muted
	trayBothMuted                            // microphone and audio muted
	trayTalkingDeafened                      // speech detected while audio is muted
	trayDisconnected                         // program open, active tab not connected
	trayNoChannel                            // connected, not in a voice channel
	trayStateCount
)

// Unread counter buckets: 0 hides the counter, 1-9 are exact, 10 is "9+".
const (
	trayUnreadMax     = 10
	trayUnreadBuckets = trayUnreadMax + 1
	trayIconSize      = 32
)

// trayICOSizes are the images in a Windows tray ICO. The artwork is designed
// at trayIconSize; the others are rendered natively so the shell can pick the
// size that matches the display scale (LoadImage takes the closest entry to
// SM_CXICON: 32px at 100%, 40/48/64px at 125/150/200%) instead of resampling
// one 32px image.
var trayICOSizes = [...]int{16, 20, 24, trayIconSize, 48, 64}

func trayUnreadBucket(count int) uint8 {
	switch {
	case count <= 0:
		return 0
	case count >= trayUnreadMax:
		return trayUnreadMax
	default:
		return uint8(count)
	}
}

// trayIconKey identifies one cached tray/taskbar image.
type trayIconKey struct {
	state  trayIconState
	unread uint8 // trayUnreadBucket
}

func (k trayIconKey) valid() bool { return k.state < trayStateCount && k.unread < trayUnreadBuckets }

var (
	trayInk      = color.NRGBA{R: 16, G: 24, B: 32, A: 255}
	trayHalo     = color.NRGBA{R: 236, G: 239, B: 243, A: 255}
	trayHaloSoft = color.NRGBA{R: 214, G: 219, B: 226, A: 255}
	trayCyan     = color.NRGBA{R: 48, G: 174, B: 191, A: 255}
	trayGreen    = color.NRGBA{R: 83, G: 255, B: 174, A: 255}
	trayAmber    = color.NRGBA{R: 255, G: 195, B: 92, A: 255}
	trayViolet   = color.NRGBA{R: 193, G: 169, B: 255, A: 255}
	trayRed      = color.NRGBA{R: 255, G: 119, B: 139, A: 255}
	trayGray     = color.NRGBA{R: 146, G: 153, B: 164, A: 255}
	trayBlack    = color.NRGBA{R: 22, G: 24, B: 28, A: 255}
	trayCountRed = color.NRGBA{R: 220, G: 38, B: 52, A: 255}
	trayWhite    = color.NRGBA{R: 255, G: 255, B: 255, A: 255}
)

// trayLogoTint is the color of the full-size logo. Mute states keep their
// own tint (not the in-channel cyan): the whole icon then carries the state
// color, which is still readable at 16px where the badge glyph is tiny, and
// it matches the colors users already know from earlier builds.
func trayLogoTint(state trayIconState) color.NRGBA {
	switch state {
	case trayTalking, trayTalkingDeafened:
		return trayGreen
	case trayMicMuted:
		return trayAmber
	case trayDeafened:
		return trayViolet
	case trayBothMuted:
		return trayRed
	case trayNoChannel:
		return trayGray
	case trayDisconnected:
		return trayBlack
	default:
		return trayCyan
	}
}

type trayGlyph uint8

const (
	trayGlyphNone trayGlyph = iota
	trayGlyphMic
	trayGlyphMicMuted
	trayGlyphSpeakerMuted
	trayGlyphHeadsetMuted
	trayGlyphCross
)

// trayBadge describes the glyph-only status disc. The tray shows it in the
// bottom-right corner for mute states; the taskbar overlay shows it for every
// state (the overlay is already drawn in the corner of the taskbar button).
func trayBadge(state trayIconState) (disc color.NRGBA, glyph trayGlyph) {
	switch state {
	case trayMicMuted:
		return trayAmber, trayGlyphMicMuted
	case trayDeafened, trayTalkingDeafened:
		return trayViolet, trayGlyphSpeakerMuted
	case trayBothMuted:
		return trayRed, trayGlyphHeadsetMuted
	case trayTalking:
		return trayGreen, trayGlyphMic
	case trayNoChannel:
		return trayGray, trayGlyphNone
	case trayDisconnected:
		return trayBlack, trayGlyphCross
	default:
		return trayCyan, trayGlyphMic
	}
}

func trayHasCornerBadge(state trayIconState) bool {
	switch state {
	case trayMicMuted, trayDeafened, trayBothMuted, trayTalkingDeafened:
		return true
	}
	return false
}

//go:embed build/windows/icon.ico
var trayIconWindows []byte

//go:embed frontend/public/branding/favicon-32.png
var trayIconPNG []byte

// trayIcon returns the embedded brand mark in the platform's native format.
func trayIcon() []byte {
	if runtime.GOOS == "windows" {
		return trayIconWindows
	}
	return trayIconPNG
}

//go:embed frontend/public/branding/icon-192.png
var trayIconLargePNG []byte

// trayLogoMask is the 32px logo coverage, decoded once.
var trayLogoMask = sync.OnceValue(func() []float64 {
	source, err := png.Decode(bytes.NewReader(trayIconPNG))
	if err != nil {
		log.Printf("tray logo unavailable: %v", err)
		return nil
	}
	return trayAlphaMask(source, trayIconSize)
})

// trayLogoMasks holds the logo coverage for every other ICO size, area-sampled
// once from the large brand mark (the 32px mask keeps its hand-tuned source).
var trayLogoMasks = sync.OnceValue(func() map[int][]float64 {
	masks := make(map[int][]float64, len(trayICOSizes))
	source, err := png.Decode(bytes.NewReader(trayIconLargePNG))
	if err != nil {
		log.Printf("large tray logo unavailable: %v", err)
		return masks
	}
	for _, size := range trayICOSizes {
		if size != trayIconSize {
			masks[size] = trayAreaMask(source, size)
		}
	}
	return masks
})

// trayLogoMaskAt returns the logo coverage at size, or nil when unavailable.
func trayLogoMaskAt(size int) []float64 {
	if size == trayIconSize {
		return trayLogoMask()
	}
	return trayLogoMasks()[size]
}

// trayAreaMask box-filters source coverage into a size×size mask: every
// source pixel contributes to the target pixel its center falls in.
func trayAreaMask(source image.Image, size int) []float64 {
	b := source.Bounds()
	w, h := b.Dx(), b.Dy()
	if size <= 0 || w <= 0 || h <= 0 {
		return nil
	}
	sum := make([]float64, size*size)
	count := make([]float64, size*size)
	for y := range h {
		ty := (2*y + 1) * size / (2 * h)
		for x := range w {
			tx := (2*x + 1) * size / (2 * w)
			_, _, _, a := source.At(b.Min.X+x, b.Min.Y+y).RGBA()
			sum[ty*size+tx] += float64(a) / 0xffff
			count[ty*size+tx]++
		}
	}
	for i := range sum {
		if count[i] > 0 {
			sum[i] /= count[i]
		}
	}
	return sum
}

// trayAlphaMask samples source coverage into a size×size mask; pixels
// outside the source bounds are transparent.
func trayAlphaMask(source image.Image, size int) []float64 {
	mask := make([]float64, size*size)
	b := source.Bounds()
	for y := 0; y < size && b.Min.Y+y < b.Max.Y; y++ {
		for x := 0; x < size && b.Min.X+x < b.Max.X; x++ {
			_, _, _, a := source.At(b.Min.X+x, b.Min.Y+y).RGBA()
			mask[y*size+x] = float64(a) / 0xffff
		}
	}
	return mask
}

// Icons are generated lazily per (state, unread bucket) and kept for the
// process lifetime: at most trayStateCount×trayUnreadBuckets (88) entries,
// each one ICO holding len(trayICOSizes) PNG images on Windows.
// After first use a transition is a mutex-guarded array read; it never
// decodes, renders, allocates or starts a timer.
var trayIconCache struct {
	sync.Mutex
	icons [trayStateCount][trayUnreadBuckets][]byte
}

func trayIconFor(key trayIconKey) []byte {
	if !key.valid() {
		return trayIcon()
	}
	trayIconCache.Lock()
	defer trayIconCache.Unlock()
	if data := trayIconCache.icons[key.state][key.unread]; data != nil {
		return data
	}
	data := trayEncodeIcon(key)
	if data == nil {
		data = trayIcon()
	}
	trayIconCache.icons[key.state][key.unread] = data
	return data
}

// trayEncodeIcon returns the 32px PNG, or on Windows a multi-size ICO.
func trayEncodeIcon(key trayIconKey) []byte {
	encode := func(size int) []byte {
		var out bytes.Buffer
		if err := png.Encode(&out, trayStateImage(size, key)); err != nil {
			return nil
		}
		return out.Bytes()
	}
	if runtime.GOOS != "windows" {
		return encode(trayIconSize)
	}
	images := make([]trayICOImage, 0, len(trayICOSizes))
	for _, size := range trayICOSizes {
		data := encode(size)
		if data == nil {
			return nil
		}
		images = append(images, trayICOImage{size: size, png: data})
	}
	return trayICO(images...)
}

// trayStateImage renders the tray icon at size: the tinted logo, a glyph badge
// in the bottom-right corner for mute states, and the unread counter in the
// top-right corner. Geometry is designed in trayIconSize units and scaled.
func trayStateImage(size int, key trayIconKey) *image.NRGBA {
	c := newTrayCanvas(size)
	s := float64(size) / trayIconSize
	trayDrawLogo(c, trayLogoMaskAt(size), key.state)
	if trayHasCornerBadge(key.state) {
		// Centered on an even pixel boundary so vertical strokes stay crisp
		// at 32px and after the shell's 2:1 downscale to 16px.
		trayDrawBadge(c, key.state, 22*s, 22*s, 8.5*s, 1.25*s)
	}
	if key.unread > 0 {
		trayDrawCounter(c, key.unread, s)
	}
	return c.image()
}

// trayOverlayImage renders the glyph-only status disc that fills a taskbar
// overlay icon of the given size. It never contains the logo: Windows already
// draws the overlay in the corner of the (logo) taskbar button.
func trayOverlayImage(state trayIconState, size int) *image.NRGBA {
	c := newTrayCanvas(size)
	s := float64(size)
	outline := math.Max(1, s/16)
	trayDrawBadge(c, state, s/2, s/2, s/2-outline-0.25, outline)
	return c.image()
}

func trayDrawLogo(c *trayCanvas, logo []float64, state trayIconState) {
	if len(logo) != c.size*c.size {
		return
	}
	tint := trayLogoTint(state)
	if state == trayDisconnected {
		// A near-black mark disappears on a dark taskbar; a thin light halo
		// (the mask dilated by ~1.3px) keeps its silhouette visible there.
		for y := 0; y < c.size; y++ {
			for x := 0; x < c.size; x++ {
				halo := 0.0
				for dy := -2; dy <= 2; dy++ {
					for dx := -2; dx <= 2; dx++ {
						sx, sy := x+dx, y+dy
						if sx < 0 || sy < 0 || sx >= c.size || sy >= c.size {
							continue
						}
						weight := trayClamp01(1.8 - math.Hypot(float64(dx), float64(dy)))
						halo = math.Max(halo, logo[sy*c.size+sx]*weight)
					}
				}
				c.blend(x, y, trayHaloSoft, halo*0.95)
			}
		}
	}
	for i, a := range logo {
		c.blend(i%c.size, i/c.size, tint, a)
	}
}

// trayDrawBadge paints a status disc of inner radius r with an outline, and
// its glyph in dark ink. Glyph geometry is in disc units (r = 1).
func trayDrawBadge(c *trayCanvas, state trayIconState, cx, cy, r, outline float64) {
	disc, glyph := trayBadge(state)
	ring, ink := trayInk, trayInk
	if state == trayDisconnected {
		ring, ink = trayHalo, trayHalo
	}
	pad := int(math.Ceil(r+outline)) + 2
	bounds := image.Rect(int(cx)-pad, int(cy)-pad, int(cx)+pad, int(cy)+pad)
	c.fill(trayCircle(cx, cy, r+outline), ring, bounds)
	c.fill(trayCircle(cx, cy, r), disc, bounds)
	if glyph == trayGlyphNone {
		return
	}
	// Strokes stay at least ~1.4px wide so they survive a 2:1 downscale.
	u := func(v float64) float64 { return v * r }
	w := func(v float64) float64 { return math.Max(v*r, 0.7) }
	pt := func(x, y float64) (float64, float64) { return cx + x*r, cy + y*r }
	seg := func(x0, y0, x1, y1, hw float64) traySDF {
		ax, ay := pt(x0, y0)
		bx, by := pt(x1, y1)
		return traySegment(ax, ay, bx, by, w(hw))
	}
	var shape traySDF
	slashed := true
	switch glyph {
	case trayGlyphMic, trayGlyphMicMuted:
		slashed = glyph == trayGlyphMicMuted
		mx, my := pt(0, -0.12)
		shape = trayUnion(
			seg(0, -0.42, 0, -0.12, 0.24), // capsule
			trayArc(mx, my, u(0.48), 0.2, math.Pi-0.2, w(0.118)),
			seg(0, 0.36, 0, 0.66, 0.118), // stem
		)
	case trayGlyphSpeakerMuted:
		// Windows' own "volume muted" shape: a speaker with an × beside it.
		slashed = false
		p := func(x, y float64) [2]float64 { a, b := pt(x, y); return [2]float64{a, b} }
		shape = trayUnion(
			trayPolygon(p(-0.68, -0.22), p(-0.4, -0.22), p(0.04, -0.6), p(0.04, 0.6), p(-0.4, 0.22), p(-0.68, 0.22)),
			seg(0.24, -0.2, 0.62, 0.2, 0.1), seg(0.24, 0.2, 0.62, -0.2, 0.1),
		)
	case trayGlyphHeadsetMuted:
		// Cups sit clear of the diagonal, so the slash crosses only the band.
		hx, hy := pt(0, 0.02)
		cup := func(x0 float64) traySDF {
			ax, ay := pt(x0, -0.04)
			bx, by := pt(x0+0.3, 0.42)
			return trayBox(ax, ay, bx, by, u(0.1))
		}
		shape = trayUnion(
			trayArc(hx, hy, u(0.53), math.Pi, 2*math.Pi, w(0.12)),
			cup(-0.68), cup(0.38),
		)
	case trayGlyphCross:
		slashed = false
		shape = trayUnion(seg(-0.36, -0.36, 0.36, 0.36, 0.14), seg(-0.36, 0.36, 0.36, -0.36, 0.14))
	}
	if slashed {
		// Material-style cut: the glyph is cleared along the upper-right side
		// of the slash only, so the slash reads as a separate stroke without
		// slicing a tiny glyph into stripes.
		half := w(0.11)
		var cut traySDF
		if glyph == trayGlyphMicMuted {
			const k = 0.7071
			gap := math.Max(u(0.08), 0.6)
			off := half + gap
			ax, ay := pt(-0.8, -0.8)
			bx, by := pt(0.8, 0.8)
			cut = traySegment(ax+off*k, ay-off*k, bx+off*k, by-off*k, gap)
		} else {
			// Wide glyphs keep both halves; a thin symmetric gap suffices.
			cut = trayGrow(seg(-0.8, -0.8, 0.8, 0.8, 0.11), math.Max(u(0.07), 0.6))
		}
		shape = traySubtract(shape, cut)
		shape = trayUnion(shape, seg(-0.62, -0.62, 0.62, 0.62, 0.11))
	}
	c.fill(shape, ink, bounds)
}

// 3×5 bitmap digits, one string per row; '#' is a lit cell.
var trayDigitFont = [10][5]string{
	{"###", "#.#", "#.#", "#.#", "###"},
	{".#.", "##.", ".#.", ".#.", "###"},
	{"###", "..#", "###", "#..", "###"},
	{"###", "..#", ".##", "..#", "###"},
	{"#.#", "#.#", "###", "..#", "..#"},
	{"###", "#..", "###", "..#", "###"},
	{"###", "#..", "###", "#.#", "###"},
	{"###", "..#", "..#", ".#.", ".#."},
	{"###", "#.#", "###", "#.#", "###"},
	{"###", "#.#", "###", "..#", "###"},
}

var trayPlusGlyph = [5]string{"...", ".#.", "###", ".#.", "..."}

// trayDrawCounter paints the unread pill in the top-right corner, in 32px
// design units scaled by s. Cells are 2 units on even coordinates, so the 32px
// and 16px images get exact 2px and 1px cells.
func trayDrawCounter(c *trayCanvas, bucket uint8, s float64) {
	cell, top, height := 2*s, 2*s, 14*s
	left := 18 * s
	if bucket >= trayUnreadMax {
		left = 14 * s
	}
	right := float64(c.size)
	all := image.Rect(int(left)-1, 0, c.size, int(math.Ceil(height))+1)
	// A rounded square rather than a full pill keeps the digit corners inside.
	c.fill(trayBox(left, 0, right, height, 5*s), trayInk, all)
	c.fill(trayBox(left+1.25*s, 1.25*s, right-1.25*s, height-1.25*s, 3.75*s), trayCountRed, all)
	glyphs := [][5]string{trayDigitFont[min(int(bucket), 9)]}
	x := 22 * s
	if bucket >= trayUnreadMax {
		glyphs = append(glyphs, trayPlusGlyph)
		x = 16 * s
	}
	for _, glyph := range glyphs {
		for row, bits := range glyph {
			for col, bit := range bits {
				if bit != '#' {
					continue
				}
				x0, y0 := x+float64(col)*cell, top+float64(row)*cell
				c.fill(trayBox(x0, y0, x0+cell, y0+cell, 0), trayWhite, image.Rect(int(x0)-1, int(y0)-1, int(math.Ceil(x0+cell))+1, int(math.Ceil(y0+cell))+1))
			}
		}
		x += 4 * cell
	}
}

// trayICOImage is one PNG entry of an ICO file.
type trayICOImage struct {
	size int
	png  []byte
}

// trayICO packs PNG payloads into one ICO, which preserves transparency and
// lets Windows choose the entry that matches the user's display scale. It
// returns nil for an empty set, a dimension outside 1..256 or an oversized
// payload.
func trayICO(images ...trayICOImage) []byte {
	if len(images) == 0 || len(images) > math.MaxUint16 {
		return nil
	}
	header := 6 + 16*len(images)
	total := header
	for _, img := range images {
		if img.size < 1 || img.size > 256 || len(img.png) > math.MaxInt32-total {
			return nil
		}
		total += len(img.png)
	}
	count, err := safecast.Int64ToUint16(int64(len(images)))
	if err != nil {
		return nil
	}
	ico := make([]byte, header, total)
	binary.LittleEndian.PutUint16(ico[2:], 1)
	binary.LittleEndian.PutUint16(ico[4:], count)
	for i, img := range images {
		// ICO stores a 256px dimension as zero in its one-byte directory field.
		dimension, err := safecast.IntToUint8(img.size % 256)
		if err != nil {
			return nil
		}
		payloadSize, err := safecast.IntToUint32(len(img.png))
		if err != nil {
			return nil
		}
		offset, err := safecast.IntToUint32(len(ico))
		if err != nil {
			return nil
		}
		entry := ico[6+16*i:]
		entry[0], entry[1] = dimension, dimension
		binary.LittleEndian.PutUint16(entry[4:], 1)
		binary.LittleEndian.PutUint16(entry[6:], 32)
		binary.LittleEndian.PutUint32(entry[8:], payloadSize)
		binary.LittleEndian.PutUint32(entry[12:], offset)
		ico = append(ico, img.png...)
	}
	return ico
}

// trayICOEntry returns the payload of the smallest ICO entry at least size
// pixels wide, or of the largest entry when none is; nil for a malformed ICO.
func trayICOEntry(ico []byte, size int) []byte {
	if len(ico) < 6 || binary.LittleEndian.Uint16(ico[2:]) != 1 {
		return nil
	}
	count := int(binary.LittleEndian.Uint16(ico[4:]))
	if count == 0 || len(ico) < 6+16*count {
		return nil
	}
	best, bestDim := -1, 0
	for i := range count {
		entry := ico[6+16*i:]
		dim := int(entry[0])
		if dim == 0 {
			dim = 256
		}
		better := best < 0 ||
			(dim >= size && (bestDim < size || dim < bestDim)) ||
			(dim < size && bestDim < size && dim > bestDim)
		if better {
			best, bestDim = i, dim
		}
	}
	entry := ico[6+16*best:]
	length := uint64(binary.LittleEndian.Uint32(entry[8:]))
	offset := uint64(binary.LittleEndian.Uint32(entry[12:]))
	if length == 0 || offset+length > uint64(len(ico)) {
		return nil
	}
	return ico[offset : offset+length]
}
