package main

import (
	"strings"
	"sync"
	"testing"
)

func TestTrayConcurrentStateUpdates(t *testing.T) {
	controller := &tray{isConnected: true, publish: func(trayPresentation, bool) {}}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			for j := 0; j < 100; j++ {
				controller.setVoiceState(j%2 == 0, j%3 == 0, j%5 == 0)
			}
		})
	}
	wg.Wait()
	controller.setVoiceState(false, true, true)
	if controller.last.icon != trayBothMuted {
		t.Fatal("final tray state was lost")
	}
}

func BenchmarkTrayUnchangedVoiceState(b *testing.B) {
	controller := &tray{isConnected: true, publish: func(trayPresentation, bool) {}}
	controller.setVoiceState(true, false, false)
	b.ReportAllocs()
	for b.Loop() {
		controller.setVoiceState(true, false, false)
	}
}

func TestTrayVoiceStates(t *testing.T) {
	tests := []struct {
		name                      string
		speaking, muted, deafened bool
		want                      trayIconState
		label                     string
	}{
		{"idle", false, false, false, trayIdle, "Microphone on"},
		{"talking", true, false, false, trayTalking, "Talking"},
		{"mic muted", true, true, false, trayMicMuted, "Microphone muted"},
		{"audio muted", false, false, true, trayDeafened, "Audio muted"},
		{"talking while deafened", true, false, true, trayTalkingDeafened, "Talking"},
		{"both muted", true, true, true, trayBothMuted, "Microphone and audio muted"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			controller := &tray{isConnected: true}
			var got trayPresentation
			updates := 0
			controller.publish = func(p trayPresentation, _ bool) { got = p; updates++ }
			controller.setVoiceState(tt.speaking, tt.muted, tt.deafened)
			if got.icon != tt.want || !strings.Contains(got.tooltip, tt.label) {
				t.Fatalf("presentation = %+v, want %v / %s", got, tt.want, tt.label)
			}
			controller.setVoiceState(tt.speaking, tt.muted, tt.deafened)
			if updates != 1 {
				t.Fatalf("unchanged state published %d times", updates)
			}
			controller.setConnected(false)
			if got.icon == trayTalking || got.icon == trayTalkingDeafened {
				t.Fatal("disconnected tray still indicates talking")
			}
		})
	}
}

func TestTrayReconnectLastOnlyEmitsWhileDisconnected(t *testing.T) {
	var event string
	controller := &tray{emit: func(got string, _ any) {
		event = got
	}}

	controller.setConnected(true)
	controller.reconnectLast()
	if event != "" {
		t.Fatalf("connected tray emitted %q", event)
	}

	controller.setConnected(false)
	controller.reconnectLast()
	if event != "tray_reconnect" {
		t.Fatalf("tray event = %q, want tray_reconnect", event)
	}

	controller = &tray{}
	controller.reconnectLast()
}

func TestTrayDisconnectOnlyEmitsWhileConnected(t *testing.T) {
	var event string
	controller := &tray{emit: func(got string, _ any) {
		event = got
	}}

	controller.setConnected(false)
	controller.disconnectActive()
	if event != "" {
		t.Fatalf("disconnected tray emitted %q", event)
	}

	controller.setConnected(true)
	controller.disconnectActive()
	if event != "tray_disconnect" {
		t.Fatalf("tray event = %q, want tray_disconnect", event)
	}
}
