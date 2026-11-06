package server

import (
	"testing"
	"time"

	"go.uber.org/zap"

	"noxa/internal/broadcast"
	"noxa/internal/config"
	"noxa/internal/state"
)

func TestAnnounceShutdownBroadcastsRoundedSeconds(t *testing.T) {
	b := broadcast.New(zap.NewNop(), state.New(zap.NewNop()))
	var events []string
	b.SetEventTap(func(eventType string, payload []byte) {
		events = append(events, eventType+" "+string(payload))
	})
	s := New(&config.Config{}, zap.NewNop(), &Deps{Broadcast: b})

	s.AnnounceShutdown(29*time.Second + time.Millisecond) // rounds up
	s.AnnounceShutdown(400 * time.Millisecond)            // still at least one second
	s.AnnounceShutdown(0)                                 // nothing left to announce

	want := []string{
		`server_shutdown_scheduled {"seconds":30}`,
		`server_shutdown_scheduled {"seconds":1}`,
	}
	if len(events) != len(want) {
		t.Fatalf("events = %q, want %q", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("event %d = %q, want %q", i, events[i], want[i])
		}
	}
}
