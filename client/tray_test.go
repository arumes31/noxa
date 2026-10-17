package main

import (
	"strings"
	"sync"
	"testing"
)

func TestTrayConcurrentStateUpdates(t *testing.T) {
	controller := &tray{status: trayStatus{connected: true}, publish: func(trayPresentation, bool) {}}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			for j := 0; j < 100; j++ {
				controller.setVoiceState(j%2 == 0, j%3 == 0, j%5 == 0, j%7 != 0)
				controller.setUnread(j % 13)
			}
		})
	}
	wg.Wait()
	controller.setVoiceState(false, true, true, true)
	controller.setUnread(4)
	if controller.last.icon != (trayIconKey{state: trayBothMuted, unread: 4}) {
		t.Fatal("final tray state was lost")
	}
}

func BenchmarkTrayUnchangedVoiceState(b *testing.B) {
	controller := &tray{status: trayStatus{connected: true}, publish: func(trayPresentation, bool) {}}
	controller.setVoiceState(true, false, false, true)
	b.ReportAllocs()
	for b.Loop() {
		controller.setVoiceState(true, false, false, true)
	}
}

func TestTrayVoiceStates(t *testing.T) {
	tests := []struct {
		name                                 string
		speaking, muted, deafened, inChannel bool
		want                                 trayIconState
		label                                string
	}{
		{"in channel", false, false, false, true, trayIdle, "In a channel"},
		{"talking", true, false, false, true, trayTalking, "Talking"},
		{"not in channel", false, false, false, false, trayNoChannel, "not in a channel"},
		{"mic muted", true, true, false, true, trayMicMuted, "Microphone muted"},
		{"audio muted", false, false, true, true, trayDeafened, "Audio muted"},
		{"talking while deafened", true, false, true, true, trayTalkingDeafened, "Talking"},
		{"both muted", true, true, true, true, trayBothMuted, "Microphone and audio muted"},
		{"mute wins over no channel", false, true, false, false, trayMicMuted, "Microphone muted"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			controller := &tray{status: trayStatus{connected: true}}
			var got trayPresentation
			updates := 0
			controller.publish = func(p trayPresentation, _ bool) { got = p; updates++ }
			controller.setVoiceState(tt.speaking, tt.muted, tt.deafened, tt.inChannel)
			if got.icon.state != tt.want || !strings.Contains(got.tooltip, tt.label) {
				t.Fatalf("presentation = %+v, want %v / %s", got, tt.want, tt.label)
			}
			controller.setVoiceState(tt.speaking, tt.muted, tt.deafened, tt.inChannel)
			if updates != 1 {
				t.Fatalf("unchanged state published %d times", updates)
			}
			controller.setConnected(false)
			if got.icon.state != trayDisconnected || !strings.Contains(got.tooltip, "Not connected") {
				t.Fatalf("disconnected tray shows %+v", got)
			}
		})
	}
}

func TestTrayUnreadCounter(t *testing.T) {
	controller := &tray{status: trayStatus{connected: true}}
	var got trayPresentation
	var iconChanges, updates int
	controller.publish = func(p trayPresentation, iconChanged bool) {
		got = p
		updates++
		if iconChanged {
			iconChanges++
		}
	}
	controller.setUnread(0)
	controller.setUnread(3)
	if got.icon.unread != 3 || !strings.Contains(got.tooltip, "3 unread message(s)") {
		t.Fatalf("presentation = %+v", got)
	}
	controller.setUnread(12)
	controller.setUnread(15)
	if got.icon.unread != trayUnreadMax || !strings.Contains(got.tooltip, "15 unread") {
		t.Fatalf("presentation = %+v", got)
	}
	// 12 -> 15 stays in the "9+" bucket: tooltip only, no new icon.
	if iconChanges != 3 || updates != 4 {
		t.Fatalf("icon changes = %d, updates = %d", iconChanges, updates)
	}
	controller.setUnread(15)
	if updates != 4 {
		t.Fatal("unchanged count republished")
	}
	controller.setUnread(-2)
	if got.icon.unread != 0 || strings.Contains(got.tooltip, "unread message") {
		t.Fatalf("cleared counter still shown: %+v", got)
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
