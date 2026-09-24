package webrtc

import (
	"github.com/pion/rtp"
	"testing"
)

func TestMediaPacerAccountsRTPPadding(t *testing.T) {
	p := &mediaPacer{streams: map[uint32]*pacedStream{1: {active: true}}}
	header := rtp.Header{Version: 2, SSRC: 1, Padding: true, PaddingSize: 200}
	n, err := p.Write(&header, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := (&rtp.Packet{Header: header}).MarshalSize()
	if n != want || p.bytes != want {
		t.Fatalf("accepted=%d queued=%d, wire uses %d bytes", n, p.bytes, want)
	}
}
