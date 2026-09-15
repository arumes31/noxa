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
	"testing"
)

func TestTrayIconAssets(t *testing.T) {
	icons := trayIcons()
	seen := make(map[string]bool)
	preview := image.NewNRGBA(image.Rect(0, 0, 6*48, 48))
	names := []string{"idle", "talking", "mic-muted", "audio-muted", "both-muted", "talking-audio-muted"}
	for mode := trayIdle; mode <= trayTalkingDeafened; mode++ {
		t.Run(names[mode], func(t *testing.T) {
			data := icons[mode]
			if len(data) == 0 || seen[string(data)] {
				t.Fatal("tray states must have distinct nonempty icons")
			}
			seen[string(data)] = true
			if &data[0] != &trayIcons()[mode][0] {
				t.Fatal("tray icon was regenerated")
			}
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
			if img.Bounds().Dx() != 32 || img.Bounds().Dy() != 32 {
				t.Fatal("expected 32px logo")
			}
			if _, _, _, a := img.At(0, 0).RGBA(); a != 0 {
				t.Fatal("logo must have transparent corners")
			}
			for y := 0; y < 48; y++ {
				for x := 0; x < 48; x++ {
					c := color.NRGBA{R: 30, G: 34, B: 40, A: 255}
					if x >= 8 && x < 40 && y >= 8 && y < 40 {
						pixel := color.NRGBAModel.Convert(img.At(x-8, y-8)).(color.NRGBA)
						a := uint32(pixel.A)
						c.R = uint8((uint32(pixel.R)*a + uint32(c.R)*(255-a)) / 255)
						c.G = uint8((uint32(pixel.G)*a + uint32(c.G)*(255-a)) / 255)
						c.B = uint8((uint32(pixel.B)*a + uint32(c.B)*(255-a)) / 255)
					}
					preview.SetNRGBA(int(mode)*48+x, y, c)
				}
			}
		})
	}
	if dir := os.Getenv("NOXA_TRAY_PREVIEW_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := png.Encode(&out, preview); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "tray-states.png"), out.Bytes(), 0644); err != nil {
			t.Fatal(err)
		}
	}
}
