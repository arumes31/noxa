package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"noxa/internal/netproto"
)

func TestTransferInitializationRejectsNativeTabSwitch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.txt")
	if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name string
		want netproto.FileTransferInit
		call func(*App, string) string
	}{
		{"bytes", netproto.FileTransferInit{ChannelID: 3, Direction: "upload", Folder: "docs", Name: "report.txt", Size: 4},
			func(a *App, tab string) string {
				return a.UploadFileProgressForTab(tab, "up", 3, "docs", "report.txt", "ZGF0YQ==")
			}},
		{"path", netproto.FileTransferInit{ChannelID: 3, Direction: "upload", Folder: "docs", Name: "report.txt", Size: 4},
			func(a *App, tab string) string { return a.UploadPathProgressForTab(tab, "up", 3, "docs", path) }},
		{"download", netproto.FileTransferInit{ChannelID: 3, Direction: "download", Folder: "docs", Name: "report.txt"},
			func(a *App, tab string) string {
				return a.DownloadFileProgressForTab(tab, "dl", 3, "docs", "report.txt", path, 4)
			}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var writesA, writesB atomic.Int32
			backend := func(count *atomic.Int32) frameHandler {
				return func(frame *netproto.Frame) (netproto.MessageType, any, bool) {
					count.Add(1)
					var got netproto.FileTransferInit
					if frame.Type != uint16(netproto.MsgFileTransferInit) {
						t.Errorf("unexpected frame: %d", frame.Type)
					}
					if err := netproto.Decode(frame, &got); err != nil || !reflect.DeepEqual(got, tt.want) {
						t.Errorf("initialization = %+v, %v; want %+v", got, err, tt.want)
					}
					return netproto.MsgError, netproto.Error{Code: 6, Message: "fixture denial", OriginType: uint16(netproto.MsgFileTransferInit)}, true
				}
			}
			app, _ := newPipedApp(t, backend(&writesA))
			other, _ := newPipedApp(t, backend(&writesB))
			app.activeID = "a"
			app.tabs = map[string]*tabState{"a": {cm: app.cmLoad()}, "b": {cm: other.cmLoad()}}
			if err := tt.call(app, "a"); !strings.Contains(err, "fixture denial") {
				t.Fatalf("current request = %q", err)
			}
			app.tabsMu.Lock()
			app.activateLocked("b")
			app.tabsMu.Unlock()
			for _, tab := range []string{"a", "", "missing"} {
				if err := tt.call(app, tab); !strings.Contains(err, "server tab changed") {
					t.Fatalf("stale tab %q = %q", tab, err)
				}
			}
			if writesA.Load() != 1 || writesB.Load() != 0 {
				t.Fatalf("stale requests reached transport: a=%d b=%d", writesA.Load(), writesB.Load())
			}
			if err := tt.call(app, "b"); !strings.Contains(err, "fixture denial") {
				t.Fatalf("new current request = %q", err)
			}
		})
	}
}

func TestTransferCancellationRejectsNativeTabSwitch(t *testing.T) {
	first, second := newTestConnManager(), newTestConnManager()
	clientA, peerA := transferPipe(t)
	clientB, peerB := transferPipe(t)
	first.trackTransfer("same", clientA)
	second.trackTransfer("same", clientB)
	app := appWithCM(second)
	app.activeID = "b"
	app.tabs = map[string]*tabState{"a": {cm: first}, "b": {cm: second}}
	for _, tab := range []string{"a", "", "missing"} {
		if err := app.CancelTransferForTab(tab, "same"); !strings.Contains(err, "server tab changed") {
			t.Fatalf("stale cancellation %q = %q", tab, err)
		}
	}
	for _, cm := range []*connManager{first, second} {
		cm.transfers.mu.Lock()
		count := len(cm.transfers.entries)
		cm.transfers.mu.Unlock()
		if count != 1 {
			t.Fatal("stale cancellation altered a registry")
		}
	}
	if err := app.CancelTransferForTab("b", "same"); err != "" {
		t.Fatal(err)
	}
	requireTransferClosed(t, peerB)
	first.cancelTransfers("same")
	requireTransferClosed(t, peerA)
}
