package webrtc

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestCheckRemoteSDPBudget(t *testing.T) {
	var sdp strings.Builder
	sdp.WriteString("v=0\r\nm=video 9 UDP/TLS/RTP/SAVPF 96\r\n")
	for i := range maxRemoteSSRCAttributes {
		fmt.Fprintf(&sdp, "a=ssrc:%d cname:x\r\n", i+1)
	}
	if err := checkRemoteSDPBudget(sdp.String()); err != nil {
		t.Fatalf("description within budget rejected: %v", err)
	}
	sdp.WriteString("a=ssrc-group:FID 1 2\r\n")
	if err := checkRemoteSDPBudget(sdp.String()); !errors.Is(err, ErrSDPTooManySSRCs) {
		t.Fatalf("over-budget description err=%v", err)
	}
}
