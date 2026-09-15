package main

import (
	"bytes"
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

const (
	trayIdle trayIconState = iota
	trayTalking
	trayMicMuted
	trayDeafened
	trayBothMuted
	trayTalkingDeafened
)

// Six immutable variants are prepared once. Voice transitions only select a
// cached icon; they never decode images, allocate artwork, or start a timer.
var trayIcons = sync.OnceValue(func() map[trayIconState][]byte {
	icons := make(map[trayIconState][]byte)
	source, err := png.Decode(bytes.NewReader(trayIconPNG))
	if err != nil {
		log.Printf("tray logo unavailable: %v", err)
		for mode := trayIdle; mode <= trayTalkingDeafened; mode++ {
			icons[mode] = trayIcon()
		}
		return icons
	}
	for mode := trayIdle; mode <= trayTalkingDeafened; mode++ {
		img := trayStateImage(source, mode)
		var out bytes.Buffer
		if err := png.Encode(&out, img); err != nil {
			icons[mode] = trayIcon()
			continue
		}
		icons[mode] = out.Bytes()
		if runtime.GOOS == "windows" {
			icons[mode] = trayPNGToICO(out.Bytes(), img.Bounds().Dx())
			if icons[mode] == nil {
				icons[mode] = trayIcon()
			}
		}
	}
	return icons
})

func trayStateImage(source image.Image, mode trayIconState) *image.NRGBA {
	img := image.NewNRGBA(source.Bounds())
	tint := color.NRGBA{R: 48, G: 174, B: 191, A: 255}
	switch mode {
	case trayTalking, trayTalkingDeafened:
		tint = color.NRGBA{R: 83, G: 255, B: 174, A: 255}
	case trayMicMuted:
		tint = color.NRGBA{R: 255, G: 195, B: 92, A: 255}
	case trayDeafened:
		tint = color.NRGBA{R: 193, G: 169, B: 255, A: 255}
	case trayBothMuted:
		tint = color.NRGBA{R: 255, G: 119, B: 139, A: 255}
	}
	for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
		for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
			pixel := tint
			pixel.A = color.AlphaModel.Convert(source.At(x, y)).(color.Alpha).A
			img.SetNRGBA(x, y, pixel)
		}
	}
	if mode == trayIdle || mode == trayTalking {
		return img
	}
	// A contrasting corner badge adds a shape cue at small tray sizes:
	// slash = microphone mute, minus = audio mute, cross = both.
	ink := color.NRGBA{R: 16, G: 24, B: 32, A: 255}
	badge := tint
	if mode == trayTalkingDeafened {
		badge = color.NRGBA{R: 193, G: 169, B: 255, A: 255}
	}
	for y := 14; y < 32; y++ {
		for x := 14; x < 32; x++ {
			dx, dy := x-23, y-23
			distance := dx*dx + dy*dy
			if distance <= 81 {
				img.SetNRGBA(x, y, ink)
			}
			if distance <= 49 {
				img.SetNRGBA(x, y, badge)
			}
			if x < 19 || x > 27 || y < 19 || y > 27 {
				continue
			}
			slash := absTray(x+y-46) <= 1
			cross := slash || absTray(x-y) <= 1
			minus := y >= 22 && y <= 24
			if (mode == trayMicMuted && slash) || (mode == trayBothMuted && cross) ||
				((mode == trayDeafened || mode == trayTalkingDeafened) && minus) {
				img.SetNRGBA(x, y, ink)
			}
		}
	}
	return img
}

func absTray(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

// ICO supports PNG payloads. A single 32px RGBA entry preserves transparency
// and lets Windows scale the logo for the user's tray DPI.
func trayPNGToICO(data []byte, size int) []byte {
	if size < 1 || size > 256 || len(data) > math.MaxInt-22 {
		return nil
	}
	// ICO stores a 256px dimension as zero in its one-byte directory field.
	dimension, err := safecast.IntToUint8(size % 256)
	if err != nil {
		return nil
	}
	payloadSize, err := safecast.IntToUint32(len(data))
	if err != nil {
		return nil
	}
	ico := make([]byte, 22+len(data))
	binary.LittleEndian.PutUint16(ico[2:], 1)
	binary.LittleEndian.PutUint16(ico[4:], 1)
	ico[6], ico[7] = dimension, dimension
	binary.LittleEndian.PutUint16(ico[10:], 1)
	binary.LittleEndian.PutUint16(ico[12:], 32)
	binary.LittleEndian.PutUint32(ico[14:], payloadSize)
	binary.LittleEndian.PutUint32(ico[18:], 22)
	copy(ico[22:], data)
	return ico
}
