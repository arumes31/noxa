package netproto

import (
	"math"
	"unicode/utf8"
)

// ValidPosition bounds untrusted world coordinates to one million meters.
// Context identifies a shared game/map coordinate system, never a file path.
func (p PositionUpdate) ValidPosition() bool {
	if len(p.Context) == 0 || len(p.Context) > 128 || !utf8.ValidString(p.Context) {
		return false
	}
	for _, v := range []float64{p.X, p.Y, p.Z} {
		if math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > 1_000_000 {
			return false
		}
	}
	return true
}
