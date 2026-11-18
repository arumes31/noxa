package nativecapture

import (
	"errors"
	"time"
)

// ErrCameraBusy: the camera is in use in a way Windows cannot share.
var ErrCameraBusy = errors.New("camera busy")

// Studio (native): the webview keeps the scene model (studio-state.js) and
// sends each Take's program scene here. Display and camera sources are
// composed on the GPU; text and image sources arrive as transparent overlay
// images the webview renders, each placed at its position in the z-order.

// MaxStudioLayers bounds a scene (8 sources plus the overlay groups between them).
const MaxStudioLayers = 17

// Layer kinds.
const (
	LayerDisplay = "display"
	LayerCamera  = "camera"
	LayerOverlay = "overlay"
)

// Scene is one Studio program scene. Take increases with every Take; a
// scene with the live Take only replaces its overlay images.
type Scene struct {
	Take       uint64     `json:"take,string"`
	Layers     []Layer    `json:"layers"`
	Transition Transition `json:"transition"`
}

// Layer is one z-ordered element of a scene, bottom first. Overlay layers
// cover the whole canvas with image Overlay of the scene's images.
type Layer struct {
	Kind    string  `json:"kind"`
	Source  Source  `json:"source"`           // display
	Audio   bool    `json:"audio,omitempty"`  // display: stream this source's audio
	Camera  string  `json:"camera,omitempty"` // camera: device symbolic link
	Overlay int     `json:"overlay"`          // overlay: image index
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	W       float64 `json:"w"`
	H       float64 `json:"h"`
	Crop    Crop    `json:"crop"`
}

// Crop trims each edge by a fraction of the source (studio-state.js normalizeGeometry).
type Crop struct {
	L float64 `json:"l"`
	T float64 `json:"t"`
	R float64 `json:"r"`
	B float64 `json:"b"`
}

// Transition is how a Take goes on air: "cut", or "fade" over MS milliseconds.
type Transition struct {
	Kind string `json:"kind"`
	MS   int    `json:"ms"`
}

// rectF is a rectangle in fractions of the canvas or of a texture.
type rectF struct{ X, Y, W, H float64 }

// clampScene keeps untrusted scene values inside the ranges studio-state.js
// enforces, so a bad message can never address outside a texture.
func clampScene(s Scene, images int) Scene {
	unit := func(v, lo, hi float64) float64 {
		if v != v { // NaN
			return lo
		}
		return min(hi, max(lo, v))
	}
	out := Scene{Take: s.Take, Transition: s.Transition}
	if out.Transition.Kind != "cut" {
		out.Transition.Kind = "fade"
	}
	out.Transition.MS = min(1500, max(0, out.Transition.MS))
	for _, l := range s.Layers {
		if len(out.Layers) == MaxStudioLayers {
			break
		}
		switch l.Kind {
		case LayerOverlay:
			if l.Overlay < 0 || l.Overlay >= images {
				continue
			}
			out.Layers = append(out.Layers, Layer{Kind: LayerOverlay, Overlay: l.Overlay})
			continue
		case LayerDisplay:
			l.Camera = ""
			l.Source.Studio, l.Source.Region = false, nil
		case LayerCamera:
			l.Source, l.Audio = Source{}, false
		default:
			continue
		}
		l.W, l.H = unit(l.W, 0.02, 1), unit(l.H, 0.02, 1)
		l.X, l.Y = unit(l.X, 0, 1-l.W), unit(l.Y, 0, 1-l.H)
		l.Crop = Crop{unit(l.Crop.L, 0, 0.45), unit(l.Crop.T, 0, 0.45), unit(l.Crop.R, 0, 0.45), unit(l.Crop.B, 0, 0.45)}
		l.Overlay = 0
		out.Layers = append(out.Layers, l)
	}
	return out
}

// placement is where a w x h source picture of layer l is drawn on the
// canvas (fractions) and which part of the picture (texture fractions): the
// crop is removed and the rest fitted into the layer's box keeping its
// aspect, centred (studio-compositor.js fitRect).
func placement(l Layer, canvasW, canvasH, w, h int) (dst, uv rectF) {
	uv = rectF{l.Crop.L, l.Crop.T, 1 - l.Crop.L - l.Crop.R, 1 - l.Crop.T - l.Crop.B}
	box := rectF{l.X, l.Y, l.W, l.H}
	if w <= 0 || h <= 0 || canvasW <= 0 || canvasH <= 0 {
		return box, uv
	}
	cw, ch := float64(w)*uv.W, float64(h)*uv.H
	bw, bh := box.W*float64(canvasW), box.H*float64(canvasH)
	scale := min(bw/cw, bh/ch)
	fw, fh := cw*scale/float64(canvasW), ch*scale/float64(canvasH)
	return rectF{box.X + (box.W-fw)/2, box.Y + (box.H-fh)/2, fw, fh}, uv
}

// fadeProgress is a fade's position at now (0..1) and whether it is over.
func fadeProgress(start, now time.Time, ms int) (float64, bool) {
	if ms <= 0 {
		return 1, true
	}
	p := float64(now.Sub(start)) / float64(time.Duration(ms)*time.Millisecond)
	if p >= 1 {
		return 1, true
	}
	return max(0, p), false
}

// sceneSources lists the distinct display sources and cameras of scenes.
func sceneSources(scenes ...*Scene) (displays []Source, cameras []string) {
	seenD, seenC := map[Source]bool{}, map[string]bool{}
	for _, s := range scenes {
		if s == nil {
			continue
		}
		for _, l := range s.Layers {
			switch {
			case l.Kind == LayerDisplay && !seenD[l.Source]:
				seenD[l.Source] = true
				displays = append(displays, l.Source)
			case l.Kind == LayerCamera && l.Camera != "" && !seenC[l.Camera]:
				seenC[l.Camera] = true
				cameras = append(cameras, l.Camera)
			}
		}
	}
	return displays, cameras
}

// Camera is one video capture device.
type Camera struct {
	ID   string `json:"id"` // symbolic link
	Name string `json:"name"`
}

// bgraRows copies h rows of w BGRA pixels (rows stride bytes apart, bottom-up
// when negative) into a top-down buffer with alpha forced opaque.
func bgraRows(src []byte, w, h, stride int) []byte {
	row := w * 4
	abs := max(stride, -stride)
	if abs < row || len(src) < abs*(h-1)+row {
		return nil
	}
	out := make([]byte, row*h)
	for y := range h {
		sy := y
		if stride < 0 {
			sy = h - 1 - y
		}
		copy(out[y*row:(y+1)*row], src[sy*abs:sy*abs+row])
	}
	for i := 3; i < len(out); i += 4 {
		out[i] = 255
	}
	return out
}
