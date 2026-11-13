package main

import (
	"net"
	"testing"
	"time"

	"noxa/internal/netproto"
)

func TestRecommendedUpdateOnlyForOlderReleases(t *testing.T) {
	for _, tc := range []struct{ recommended, current, want string }{
		{"0.6.3", "0.6.1", "0.6.3"},
		{"0.6.3", "v0.6.1+abc", "0.6.3"},
		{"0.6.3", "0.6.3", ""},
		{"0.6.1", "0.6.3", ""},
		{"", "0.6.1", ""},
		{"0.6.3", "0.6.1-dev+gabc", ""}, // dev builds never auto-update
		{"0.6.3", "dev", ""},
		{"0.6.3", "", ""},
		{"latest", "0.6.1", ""},
		{"0.6.3-rc.1", "0.6.1", ""},
	} {
		if got := recommendedUpdate(tc.recommended, tc.current); got != tc.want {
			t.Errorf("recommendedUpdate(%q, %q) = %q, want %q", tc.recommended, tc.current, got, tc.want)
		}
	}
}

// Quitting to install an update tells the server why, so observers keep the
// member's place instead of hearing a leave.
func TestGoodbyeSaysUpdateOnlyWhenQuittingForAnUpdate(t *testing.T) {
	t.Cleanup(func() { quitForUpdate.Store(false) })
	for _, update := range []bool{false, true} {
		quitForUpdate.Store(update)
		client, server := net.Pipe()
		m := &connManager{conn: client}
		frames := make(chan *netproto.Frame, 1)
		go func() {
			_ = server.SetReadDeadline(time.Now().Add(2 * time.Second))
			f, err := netproto.ReadFrame(server)
			if err == nil {
				frames <- f
			}
			close(frames)
		}()
		m.disconnect()
		f, ok := <-frames
		_ = server.Close()
		if !ok || netproto.MessageType(f.Type) != netproto.MsgGoodbye {
			t.Fatalf("update=%v: got %v, want a goodbye", update, f)
		}
		var goodbye netproto.Goodbye
		if err := netproto.Decode(f, &goodbye); err != nil {
			t.Fatal(err)
		}
		if want := map[bool]string{true: netproto.GoodbyeReasonUpdate}[update]; goodbye.Reason != want {
			t.Fatalf("update=%v: reason %q, want %q", update, goodbye.Reason, want)
		}
	}
}
