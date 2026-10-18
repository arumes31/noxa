package main

import (
	"image"
	"image/color"
	"math"
)

// trayCanvas is a tiny straight-alpha compositor for tray and taskbar
// artwork. Shapes are signed-distance functions in pixel units (negative
// inside), so every edge receives exact one-pixel analytic anti-aliasing at
// any output size without supersampling or font dependencies.
type trayCanvas struct {
	size int
	px   []float64 // premultiplied RGBA, 0..1
}

type traySDF func(x, y float64) float64

func newTrayCanvas(size int) *trayCanvas {
	return &trayCanvas{size: size, px: make([]float64, size*size*4)}
}

// blend composites color c with coverage alpha over pixel (x, y).
func (c *trayCanvas) blend(x, y int, col color.NRGBA, alpha float64) {
	if alpha <= 0 {
		return
	}
	a := alpha * float64(col.A) / 255
	i := (y*c.size + x) * 4
	keep := 1 - a
	c.px[i] = float64(col.R)/255*a + c.px[i]*keep
	c.px[i+1] = float64(col.G)/255*a + c.px[i+1]*keep
	c.px[i+2] = float64(col.B)/255*a + c.px[i+2]*keep
	c.px[i+3] = a + c.px[i+3]*keep
}

// fill paints shape with an analytic one-pixel edge ramp, sampled at pixel
// centers. Only the pixels inside the bounding box are evaluated.
func (c *trayCanvas) fill(shape traySDF, col color.NRGBA, bounds image.Rectangle) {
	bounds = bounds.Intersect(image.Rect(0, 0, c.size, c.size))
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			d := shape(float64(x)+0.5, float64(y)+0.5)
			c.blend(x, y, col, trayClamp01(0.5-d))
		}
	}
}

// image converts the premultiplied canvas into straight-alpha NRGBA.
func (c *trayCanvas) image() *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, c.size, c.size))
	for i := 0; i < len(c.px); i += 4 {
		a := c.px[i+3]
		if a <= 0 {
			continue
		}
		img.Pix[i] = trayByte(c.px[i] / a)
		img.Pix[i+1] = trayByte(c.px[i+1] / a)
		img.Pix[i+2] = trayByte(c.px[i+2] / a)
		img.Pix[i+3] = trayByte(a)
	}
	return img
}

func trayClamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }

func trayByte(v float64) uint8 { return uint8(math.Round(trayClamp01(v) * 255)) }

func trayCircle(cx, cy, r float64) traySDF {
	return func(x, y float64) float64 { return math.Hypot(x-cx, y-cy) - r }
}

// traySegment is a stroked line segment with round caps (a capsule).
func traySegment(ax, ay, bx, by, halfWidth float64) traySDF {
	return func(x, y float64) float64 {
		px, py, dx, dy := x-ax, y-ay, bx-ax, by-ay
		h := trayClamp01((px*dx + py*dy) / (dx*dx + dy*dy))
		return math.Hypot(px-dx*h, py-dy*h) - halfWidth
	}
}

// trayBox is an axis-aligned rectangle with corner radius r. Pixel-aligned
// edges with r == 0 produce exact area coverage, which keeps the bitmap
// digits crisp.
func trayBox(x0, y0, x1, y1, r float64) traySDF {
	cx, cy := (x0+x1)/2, (y0+y1)/2
	hx, hy := (x1-x0)/2-r, (y1-y0)/2-r
	return func(x, y float64) float64 {
		qx, qy := math.Abs(x-cx)-hx, math.Abs(y-cy)-hy
		outside := math.Hypot(math.Max(qx, 0), math.Max(qy, 0))
		return outside + math.Min(math.Max(qx, qy), 0) - r
	}
}

// trayArc strokes the circle arc from angle a0 to a1 (radians, clockwise in
// screen space starting at +x) with round caps.
func trayArc(cx, cy, r, a0, a1, halfWidth float64) traySDF {
	mid, span := (a0+a1)/2, (a1-a0)/2
	ex0, ey0 := cx+r*math.Cos(a0), cy+r*math.Sin(a0)
	ex1, ey1 := cx+r*math.Cos(a1), cy+r*math.Sin(a1)
	return func(x, y float64) float64 {
		angle := math.Atan2(y-cy, x-cx)
		delta := math.Remainder(angle-mid, 2*math.Pi)
		if math.Abs(delta) <= span {
			return math.Abs(math.Hypot(x-cx, y-cy)-r) - halfWidth
		}
		return math.Min(math.Hypot(x-ex0, y-ey0), math.Hypot(x-ex1, y-ey1)) - halfWidth
	}
}

// trayPolygon is the exact signed distance to a simple polygon.
func trayPolygon(points ...[2]float64) traySDF {
	return func(x, y float64) float64 {
		d := math.Inf(1)
		inside := false
		for i, j := 0, len(points)-1; i < len(points); j, i = i, i+1 {
			ax, ay := points[j][0], points[j][1]
			bx, by := points[i][0], points[i][1]
			ex, ey := bx-ax, by-ay
			wx, wy := x-ax, y-ay
			h := trayClamp01((wx*ex + wy*ey) / (ex*ex + ey*ey))
			d = math.Min(d, math.Hypot(wx-ex*h, wy-ey*h))
			if (ay > y) != (by > y) && x < ax+(y-ay)*ex/ey {
				inside = !inside
			}
		}
		if inside {
			return -d
		}
		return d
	}
}

func trayUnion(shapes ...traySDF) traySDF {
	return func(x, y float64) float64 {
		d := math.Inf(1)
		for _, shape := range shapes {
			d = math.Min(d, shape(x, y))
		}
		return d
	}
}

// trayGrow dilates (positive) or erodes (negative) a shape, used for
// outlines and for the gap that separates a slash from the glyph below it.
func trayGrow(shape traySDF, amount float64) traySDF {
	return func(x, y float64) float64 { return shape(x, y) - amount }
}

func traySubtract(shape, hole traySDF) traySDF {
	return func(x, y float64) float64 { return math.Max(shape(x, y), -hole(x, y)) }
}
