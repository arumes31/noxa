package main

import (
	"testing"
	"time"

	"noxa/internal/netproto"
)

func TestChatReadRetainsCapturedManagerAndKeys(t *testing.T) {
	for _, pins := range []bool{false, true} {
		name := "history"
		if pins {
			name = "pins"
		}
		t.Run(name, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			key := randKey(t)
			var cm *connManager
			app, cm := newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) {
				close(entered)
				<-release
				entry := netproto.ChatHistoryEntry{ID: 11, BodyEnc: mustSeal(t, "original server", key), KeyID: 3, SentAt: 1}
				keys := []netproto.ChannelKey{sealKeyFor(t, cm, 7, 3, key)}
				if pins {
					return netproto.MsgChatPinsResponse, netproto.ChatPinsResponse{ChannelID: 7, Pins: []netproto.ChatPinEntry{{MessageID: 11, Message: &entry}}, Keys: keys}, true
				}
				return netproto.MsgChatHistoryResponse, netproto.ChatHistoryResponse{ChannelID: 7, Messages: []netproto.ChatHistoryEntry{entry}, Keys: keys}, true
			})
			other, _ := newPipedApp(t, func(*netproto.Frame) (netproto.MessageType, any, bool) {
				t.Error("read redirected to replacement server")
				return 0, nil, false
			})
			app.tabs = map[string]*tabState{"a": {cm: cm}, "b": {cm: other.cmLoad()}}
			app.activeID = "a"
			type result struct {
				entry netproto.ChatHistoryEntry
				keys  []netproto.ChannelKey
				err   error
			}
			done := make(chan result, 1)
			go func() {
				if pins {
					resp, err := app.ChatPinsForTab("a", 7)
					r := result{keys: resp.Keys, err: err}
					if len(resp.Pins) == 1 && resp.Pins[0].Message != nil {
						r.entry = *resp.Pins[0].Message
					}
					done <- r
				} else {
					resp, err := app.ChatHistoryForTab("a", 7, 0, 50)
					r := result{keys: resp.Keys, err: err}
					if len(resp.Messages) == 1 {
						r.entry = resp.Messages[0]
					}
					done <- r
				}
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("read did not start")
			}
			app.tabsMu.Lock()
			_, _, _, ok := app.activateLocked("b")
			app.tabsMu.Unlock()
			close(release)
			if !ok {
				t.Fatal("activation failed")
			}
			select {
			case got := <-done:
				if got.err != nil || got.entry.Body != "original server" || !got.entry.EncVerified || got.entry.BodyEnc != "" || got.entry.KeyID != 0 || got.keys != nil {
					t.Fatalf("decrypted result: %+v", got)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("read did not finish")
			}
			if _, ok := other.cmLoad().scopeKeys.get(7, 3); ok {
				t.Fatal("original server keys installed in replacement")
			}
		})
	}
}
