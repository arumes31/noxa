package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

func TestTrayPNGToICODimensions(t *testing.T) {
	data := []byte("payload")
	for _, size := range []int{-1, 0, 1, 32, 255, 256, 257} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			ico := trayPNGToICO(data, size)
			if size < 1 || size > 256 {
				if ico != nil {
					t.Fatal("invalid icon dimension was accepted")
				}
				return
			}
			if len(ico) != 22+len(data) || !bytes.Equal(ico[22:], data) {
				t.Fatal("ICO did not preserve its payload")
			}
			if int(ico[6]) != size%256 || ico[6] != ico[7] {
				t.Fatal("incorrect ICO dimensions (256 must be encoded as zero)")
			}
			if int(binary.LittleEndian.Uint32(ico[14:])) != len(data) {
				t.Fatal("incorrect ICO payload length")
			}
		})
	}
}

func TestTrayAlphaMaskPreservesCoverage(t *testing.T) {
	source := image.NewAlpha16(image.Rect(0, 0, 4, 1))
	for x, alpha := range []uint16{0, 0x0080, 0x8000, 0xffff} {
		source.SetAlpha16(x, 0, color.Alpha16{A: alpha})
	}
	mask := trayAlphaMask(source, 4)
	c := newTrayCanvas(4)
	trayDrawLogo(c, mask, trayTalking)
	result := c.image()
	for x, want := range []uint8{0, 0, 128, 255} {
		if got := result.NRGBAAt(x, 0); got.A != want || (want > 0 && got.G != trayGreen.G) {
			t.Fatalf("pixel %d = %+v, want alpha %d in the talking tint", x, got, want)
		}
	}
	if result.NRGBAAt(0, 1).A != 0 {
		t.Fatal("pixels outside the source must stay transparent")
	}
}

func TestTrayUnreadBuckets(t *testing.T) {
	for count, want := range map[int]uint8{-5: 0, 0: 0, 1: 1, 5: 5, 9: 9, 10: 10, 11: 10, 1 << 30: 10} {
		if got := trayUnreadBucket(count); got != want {
			t.Fatalf("bucket(%d) = %d, want %d", count, got, want)
		}
	}
	if (trayIconKey{state: trayStateCount}).valid() || (trayIconKey{unread: trayUnreadBuckets}).valid() {
		t.Fatal("out-of-range icon keys must be rejected")
	}
	if !bytes.Equal(trayIconFor(trayIconKey{state: trayStateCount}), trayIcon()) {
		t.Fatal("invalid key must fall back to the brand icon")
	}
}

// decodeTrayIcon validates the platform container and returns the image.
func decodeTrayIcon(t *testing.T, data []byte) image.Image {
	t.Helper()
	if runtime.GOOS == "windows" {
		if len(data) < 22 || binary.LittleEndian.Uint16(data[2:]) != 1 ||
			binary.LittleEndian.Uint16(data[4:]) != 1 || data[6] != 32 || data[7] != 32 ||
			binary.LittleEndian.Uint32(data[18:]) != 22 || int(binary.LittleEndian.Uint32(data[14:])) != len(data)-22 {
			t.Fatal("invalid Windows ICO directory")
		}
		data = data[22:]
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != trayIconSize || img.Bounds().Dy() != trayIconSize {
		t.Fatal("expected 32px icon")
	}
	return img
}

func TestTrayIconsDistinctCachedAndBounded(t *testing.T) {
	seen := make(map[string]trayIconKey)
	for state := range trayStateCount {
		for bucket := range uint8(trayUnreadBuckets) {
			key := trayIconKey{state: state, unread: bucket}
			data := trayIconFor(key)
			if len(data) == 0 {
				t.Fatalf("%+v: empty icon", key)
			}
			if other, dup := seen[string(data)]; dup {
				t.Fatalf("%+v renders identically to %+v", key, other)
			}
			seen[string(data)] = key
			if &trayIconFor(key)[0] != &data[0] {
				t.Fatalf("%+v was regenerated", key)
			}
			img := decodeTrayIcon(t, data)
			// The bottom-left corner is never covered by a badge or counter.
			if _, _, _, a := img.At(0, trayIconSize-1).RGBA(); a != 0 {
				t.Fatalf("%+v: icon must have transparent corners", key)
			}
			_, _, _, counter := img.At(trayIconSize-2, 6).RGBA()
			if (bucket > 0) != (counter == 0xffff) {
				t.Fatalf("%+v: unread counter presence mismatch", key)
			}
			_, _, _, badge := img.At(24, 24).RGBA()
			if trayHasCornerBadge(state) && badge != 0xffff {
				t.Fatalf("%+v: mute state lacks its corner badge", key)
			}
		}
	}
	if len(seen) != int(trayStateCount)*trayUnreadBuckets {
		t.Fatalf("cache holds %d icons", len(seen))
	}
	cached := 0
	trayIconCache.Lock()
	for _, row := range trayIconCache.icons {
		for _, data := range row {
			if data != nil {
				cached++
			}
		}
	}
	trayIconCache.Unlock()
	if cached != int(trayStateCount)*trayUnreadBuckets {
		t.Fatalf("cache bound violated: %d entries", cached)
	}
}

func TestTrayIconCachedLookupDoesNotAllocate(t *testing.T) {
	key := trayIconKey{state: trayMicMuted, unread: 3}
	trayIconFor(key)
	if allocs := testing.AllocsPerRun(100, func() { trayIconFor(key) }); allocs != 0 {
		t.Fatalf("cached icon lookup allocated %.0f times", allocs)
	}
}

func TestTrayMuteBadgesAreGlyphOnly(t *testing.T) {
	// The mute badge and the taskbar overlay are the same glyph disc: a
	// colored disc, a dark outline and a dark glyph, never the logo.
	for state := range trayStateCount {
		img := trayOverlayImage(state, 32)
		disc, glyph := trayBadge(state)
		colors := map[color.NRGBA]bool{}
		for i := 0; i < len(img.Pix); i += 4 {
			p := color.NRGBA{R: img.Pix[i], G: img.Pix[i+1], B: img.Pix[i+2], A: img.Pix[i+3]}
			if p.A == 255 {
				colors[p] = true
			}
		}
		if !colors[disc] {
			t.Fatalf("state %d overlay lacks its disc color", state)
		}
		ink := trayInk
		if state == trayDisconnected {
			ink = trayHalo
		}
		if glyph != trayGlyphNone && !colors[ink] {
			t.Fatalf("state %d overlay lacks its glyph", state)
		}
		// Only a disc: everything outside its circle is transparent, which
		// rules out the ring/X/waveform of the logo.
		for _, p := range [][2]int{{0, 0}, {31, 0}, {0, 31}, {31, 31}, {3, 3}} {
			if img.NRGBAAt(p[0], p[1]).A != 0 {
				t.Fatalf("state %d overlay draws outside its disc", state)
			}
		}
	}
}

// trayDownscale halves an image with a premultiplied 2×2 box filter, which
// approximates the shell drawing a 32px icon at 16px (100% DPI).
func trayDownscale(src *image.NRGBA) *image.NRGBA {
	size := src.Bounds().Dx() / 2
	dst := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := range size {
		for x := range size {
			var r, g, b, a float64
			for _, o := range [][2]int{{0, 0}, {1, 0}, {0, 1}, {1, 1}} {
				p := src.NRGBAAt(2*x+o[0], 2*y+o[1])
				pa := float64(p.A) / 255
				r, g, b, a = r+float64(p.R)*pa, g+float64(p.G)*pa, b+float64(p.B)*pa, a+pa
			}
			if a > 0 {
				dst.SetNRGBA(x, y, color.NRGBA{R: uint8(r / a), G: uint8(g / a), B: uint8(b / a), A: uint8(a / 4 * 255)})
			}
		}
	}
	return dst
}

func trayCompose(dst *image.NRGBA, src image.Image, ox, oy, zoom int, bg color.NRGBA) {
	b := src.Bounds()
	for y := 0; y < b.Dy()*zoom; y++ {
		for x := 0; x < b.Dx()*zoom; x++ {
			p := color.NRGBAModel.Convert(src.At(b.Min.X+x/zoom, b.Min.Y+y/zoom)).(color.NRGBA)
			a := uint32(p.A)
			mix := func(f, k uint8) uint8 { return uint8((uint32(f)*a + uint32(k)*(255-a)) / 255) }
			dst.SetNRGBA(ox+x, oy+y, color.NRGBA{R: mix(p.R, bg.R), G: mix(p.G, bg.G), B: mix(p.B, bg.B), A: 255})
		}
	}
}

// TestTrayIconPreview writes tray-states.png (actual size) and
// tray-states-zoom.png (3×) when NOXA_TRAY_PREVIEW_DIR is set. Columns are
// the eight states; rows pair 32px and shell-downscaled 16px icons on the
// Windows dark (#202020) and light (#f3f3f3) taskbar colors, without and with
// counters, followed by the glyph-only 16px taskbar overlays.
func TestTrayIconPreview(t *testing.T) {
	dir := os.Getenv("NOXA_TRAY_PREVIEW_DIR")
	if dir == "" {
		t.Skip("NOXA_TRAY_PREVIEW_DIR not set")
	}
	dark, light := color.NRGBA{R: 32, G: 32, B: 32, A: 255}, color.NRGBA{R: 243, G: 243, B: 243, A: 255}
	type row struct {
		bg     color.NRGBA
		unread uint8
		half   bool
		badge  bool
	}
	var rows []row
	for _, unread := range []uint8{0, 3, trayUnreadMax} {
		for _, bg := range []color.NRGBA{dark, light} {
			rows = append(rows, row{bg, unread, false, false}, row{bg, unread, true, false})
		}
	}
	rows = append(rows, row{dark, 0, false, true}, row{light, 0, false, true})
	const cell = 40
	for _, zoom := range []int{1, 3} {
		out := image.NewNRGBA(image.Rect(0, 0, int(trayStateCount)*cell*zoom, len(rows)*cell*zoom))
		for r, spec := range rows {
			for state := range trayStateCount {
				var img image.Image
				switch {
				case spec.badge:
					img = trayOverlayImage(state, 16)
				default:
					full := trayStateImage(trayLogoMask(), trayIconKey{state: state, unread: spec.unread})
					img = full
					if spec.half {
						img = trayDownscale(full)
					}
				}
				x0, y0 := int(state)*cell*zoom, r*cell*zoom
				for y := 0; y < cell*zoom; y++ {
					for x := 0; x < cell*zoom; x++ {
						out.SetNRGBA(x0+x, y0+y, spec.bg)
					}
				}
				offset := (cell - img.Bounds().Dx()) / 2 * zoom
				trayCompose(out, img, x0+offset, y0+offset, zoom, spec.bg)
			}
		}
		name := "tray-states.png"
		if zoom > 1 {
			name = "tray-states-zoom.png"
		}
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, out); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), buf.Bytes(), 0644); err != nil {
			t.Fatal(err)
		}
	}
}
