package main

import (
	"strings"
	"testing"
)

// Exhaustive priority: not connected > mute states > not in a channel >
// in-channel idle/talking.
func TestTrayStatePriority(t *testing.T) {
	for bits := range 32 {
		s := trayStatus{
			connected: bits&1 != 0, inChannel: bits&2 != 0,
			speaking: bits&4 != 0, muted: bits&8 != 0, deafened: bits&16 != 0,
		}
		got := trayStateFor(s)
		var want trayIconState
		talking := s.inChannel && s.speaking && !s.muted
		switch {
		case !s.connected:
			want = trayDisconnected
		case s.muted && s.deafened:
			want = trayBothMuted
		case s.muted:
			want = trayMicMuted
		case s.deafened && talking:
			want = trayTalkingDeafened
		case s.deafened:
			want = trayDeafened
		case !s.inChannel:
			want = trayNoChannel
		case talking:
			want = trayTalking
		default:
			want = trayIdle
		}
		if got != want {
			t.Fatalf("%+v -> %d, want %d", s, got, want)
		}
	}
}

func TestTrayPresentationLabels(t *testing.T) {
	seen := map[string]bool{}
	for state := range trayStateCount {
		label := trayStateLabels[state]
		if label == "" || seen[label] {
			t.Fatalf("state %d needs a unique label", state)
		}
		seen[label] = true
	}
	p := trayPresent(trayStatus{connected: true, inChannel: true, unread: 27, mentions: 2})
	if p.icon != (trayIconKey{state: trayIdle, unread: trayUnreadMax}) {
		t.Fatalf("icon = %+v", p.icon)
	}
	for _, want := range []string{"In a channel", "27 unread message(s)", "2 unread mention(s)"} {
		if !strings.Contains(p.tooltip, want) {
			t.Fatalf("tooltip %q lacks %q", p.tooltip, want)
		}
	}
	if p := trayPresent(trayStatus{}); p.icon.state != trayDisconnected || !strings.Contains(p.title, "Not connected") {
		t.Fatalf("disconnected presentation = %+v", p)
	}
}

func TestTrayPresentDoesNotAllocateWithoutCounts(t *testing.T) {
	s := trayStatus{connected: true, inChannel: true, speaking: true}
	if allocs := testing.AllocsPerRun(100, func() { _ = trayPresent(s) }); allocs != 0 {
		t.Fatalf("presentation allocated %.0f times", allocs)
	}
}
