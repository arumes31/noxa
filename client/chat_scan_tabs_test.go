package main

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"noxa/internal/netproto"
)

type scanProgressSink struct{ values chan any }

func (s scanProgressSink) Emit(_ string, value any) { s.values <- value }

func TestChatScansBindTabAndCorrelateProgress(t *testing.T) {
	for _, kind := range []string{"search", "export"} {
		t.Run(kind, func(t *testing.T) {
			var callsA, callsB atomic.Int32
			entered, release := make(chan struct{}), make(chan struct{})
			app, cm := newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) {
				callsA.Add(1)
				var request netproto.ChatHistory
				if f.Type != uint16(netproto.MsgChatHistory) || netproto.Decode(f, &request) != nil || request.ChannelID != 7 || request.Limit != chatSearchPage {
					t.Error("incorrect scan page")
				}
				if request.BeforeID != 0 {
					if request.BeforeID != 1 {
						t.Errorf("cursor = %d", request.BeforeID)
					}
					return netproto.MsgChatHistoryResponse, netproto.ChatHistoryResponse{ChannelID: 7}, true
				}
				close(entered)
				<-release
				entries := make([]netproto.ChatHistoryEntry, chatSearchPage)
				for i := range entries {
					entries[i] = netproto.ChatHistoryEntry{ID: int64(chatSearchPage - i), Deleted: true}
				}
				return netproto.MsgChatHistoryResponse, netproto.ChatHistoryResponse{ChannelID: 7, Messages: entries}, true
			})
			other, _ := newPipedApp(t, func(*netproto.Frame) (netproto.MessageType, any, bool) {
				callsB.Add(1)
				return netproto.MsgChatHistoryResponse, netproto.ChatHistoryResponse{}, true
			})
			app.tabs = map[string]*tabState{"a": {cm: cm}, "b": {cm: other.cmLoad()}}
			app.activeID = "a"
			progress := make(chan any, 2)
			cm.sink = scanProgressSink{values: progress}
			call := func(tab, requestID string) error {
				if kind == "search" {
					_, err := app.ChatSearchForTab(tab, requestID, 7, "needle", 1000)
					return err
				}
				_, err := app.ChatExportHistoryForTab(tab, requestID, 7, 1000)
				return err
			}
			for _, id := range []string{"", strings.Repeat("x", 129)} {
				if err := call("a", id); err == nil {
					t.Fatal("invalid scan ID accepted")
				}
			}
			for _, tab := range []string{"", "missing", "b"} {
				if err := call(tab, "scan-1"); err == nil {
					t.Fatal("nonactive tab accepted")
				}
			}
			if callsA.Load() != 0 || callsB.Load() != 0 {
				t.Fatal("invalid input read history")
			}
			done := make(chan error, 1)
			go func() { done <- call("a", "scan-1") }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("scan did not start")
			}
			app.tabsMu.Lock()
			_, _, _, ok := app.activateLocked("b")
			app.tabsMu.Unlock()
			close(release)
			if !ok {
				t.Fatal("activation failed")
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("scan did not finish")
			}
			if callsA.Load() != 2 || callsB.Load() != 0 {
				t.Fatalf("scan pages A/B: %d/%d", callsA.Load(), callsB.Load())
			}
			select {
			case value := <-progress:
				if got, ok := value.(chatScanProgress); !ok || got.RequestID != "scan-1" || got.Scanned != chatSearchPage {
					t.Fatalf("uncorrelated progress: %#v", value)
				}
			default:
				t.Fatal("missing progress")
			}
			if err := call("a", "scan-2"); err == nil {
				t.Fatal("stale tab accepted after activation")
			}
		})
	}
}
