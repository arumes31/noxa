package netproto

import (
	"math"
	"strings"
	"testing"
)

func TestPositionValidation(t *testing.T) {
	valid := PositionUpdate{ChannelID: 1, Context: "map", X: 1_000_000, Y: -1_000_000}
	if !valid.ValidPosition() {
		t.Fatal("boundary position rejected")
	}
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), 1_000_001} {
		p := valid
		p.X = value
		if p.ValidPosition() {
			t.Fatal("unbounded position accepted")
		}
	}
	for _, context := range []string{"", strings.Repeat("x", 129), string([]byte{0xff})} {
		p := valid
		p.Context = context
		if p.ValidPosition() {
			t.Fatal("invalid context accepted")
		}
	}
}
