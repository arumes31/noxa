package main

import (
	"context"
	"encoding/base64"
	"net"
	"path/filepath"
	"strings"
	"testing"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"noxa/internal/netproto"
)

func TestChatSendForTabEncryptsCapturedScope(t *testing.T) {
	frames := make(chan *netproto.Frame, 4)
	app, cm := newPipedApp(t, func(f *netproto.Frame) (netproto.MessageType, any, bool) {
		frames <- f
		return 0, nil, false
	})
	app.tabs = map[string]*tabState{"a": {cm: cm}}
	app.activeID = "a"
	key := randKey(t)
	cm.scopeKeys.put(7, 3, key)
	for _, reply := range []int64{0, 11} {
		var err string
		if reply == 0 {
			err = app.SendChatForTab("a", "channel", "7", "private text")
		} else {
			err = app.SendChatReplyForTab("a", "channel", "7", "private text", reply)
		}
		if err != "" {
			t.Fatal(err)
		}
		var msg netproto.ChatSend
		if err := netproto.Decode(nextFrame(t, frames, netproto.MsgChatSend), &msg); err != nil {
			t.Fatal(err)
		}
		if msg.ChannelID != "7" || msg.ReplyToID != reply || !msg.Enc || msg.KeyID != 3 || msg.AckRequested {
			t.Fatalf("wrong message metadata: %+v", msg)
		}
		if plain, err := openScope(msg.Text, key); err != nil || plain != "private text" {
			t.Fatalf("wrong encrypted body %q / %v", plain, err)
		}
	}
	for _, tab := range []string{"", "missing"} {
		if app.SendChatForTab(tab, "channel", "7", "text") == "" || app.SendChatReplyForTab(tab, "channel", "7", "text", 11) == "" {
			t.Fatalf("accepted invalid tab %q", tab)
		}
	}
}

func TestChatAttachmentsRejectStaleTabBeforePreparation(t *testing.T) {
	app, cm := newPipedApp(t, func(*netproto.Frame) (netproto.MessageType, any, bool) {
		t.Error("stale request reached server")
		return 0, nil, false
	})
	app.tabs = map[string]*tabState{"a": {cm: cm}}
	app.activeID = "a"
	app.chatAttachmentSaveDialog = func(context.Context, wailsRuntime.SaveDialogOptions) (string, error) {
		t.Error("stale request opened save dialog")
		return "", nil
	}
	app.chatAttachmentFetch = func(*connManager, int64, string) ([]byte, error) {
		t.Error("stale request fetched attachment")
		return nil, nil
	}
	for _, tab := range []string{"", "missing"} {
		if _, err := app.UploadChatAttachmentForTab(tab, 7, "file.txt", "aGVsbG8="); err == nil {
			t.Fatal("accepted stale upload")
		}
		if _, err := app.DownloadChatAttachmentForTab(tab, 7, "file.txt", ""); err == nil {
			t.Fatal("accepted stale preview")
		}
		if _, err := app.SaveChatAttachmentForTab(tab, 7, "file.txt", "", "file.txt"); err == nil {
			t.Fatal("accepted stale save")
		}
	}
}

func TestChatAttachmentSaveForTabRetainsManagerThroughDialog(t *testing.T) {
	app, cm := newPipedApp(t, func(*netproto.Frame) (netproto.MessageType, any, bool) { return 0, nil, false })
	other, _ := newPipedApp(t, func(*netproto.Frame) (netproto.MessageType, any, bool) { return 0, nil, false })
	app.tabs = map[string]*tabState{"a": {cm: cm}, "b": {cm: other.cmLoad()}}
	app.activeID = "a"
	dest := filepath.Join(t.TempDir(), "saved.txt")
	app.chatAttachmentSaveDialog = func(context.Context, wailsRuntime.SaveDialogOptions) (string, error) {
		app.tabsMu.Lock()
		_, _, _, ok := app.activateLocked("b")
		app.tabsMu.Unlock()
		if !ok {
			t.Error("activation failed")
		}
		return dest, nil
	}
	app.chatAttachmentFetch = func(got *connManager, channel int64, name string) ([]byte, error) {
		if got != cm || channel != 7 || name != "file.txt" {
			t.Error("attachment retargeted")
		}
		return []byte("original bytes"), nil
	}
	writes := 0
	app.chatAttachmentWrite = func(path string, plain []byte) error {
		writes++
		if path != dest || string(plain) != "original bytes" {
			t.Error("wrong saved bytes")
		}
		return nil
	}
	if path, err := app.SaveChatAttachmentForTab("a", 7, "file.txt", "", "file.txt"); err != nil || path != dest || writes != 1 {
		t.Fatalf("save: %q / %v / %d writes", path, err, writes)
	}
	if _, err := app.DownloadChatAttachmentForTab("a", 7, "file.txt", ""); err == nil {
		t.Fatal("old tab accepted after activation")
	}
	app.chatAttachmentFetch = func(got *connManager, channel int64, name string) ([]byte, error) {
		if got != other.cmLoad() || channel != 8 || name != "other.txt" {
			t.Error("wrong preview source")
		}
		return []byte("other bytes"), nil
	}
	if b64, err := app.DownloadChatAttachmentForTab("b", 8, "other.txt", ""); err != nil || b64 != base64.StdEncoding.EncodeToString([]byte("other bytes")) {
		t.Fatalf("preview: %q / %v", b64, err)
	}
}

func TestChatAttachmentUploadForTabRetainsManagerAfterInit(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	controlClient, controlServer := net.Pipe()
	control := fixedRemoteAddrConn{Conn: controlClient, remote: fixedTransferAddr("127.0.0.1:12333")}
	cm := newConnManager(context.Background())
	cm.sink = &eventRecorder{}
	cm.conn = control
	cm.acceptingTransfers = true
	cm.transferEpoch = 1
	app := appWithCM(cm)
	go serveFrames(controlServer, func(f *netproto.Frame) (netproto.MessageType, any, bool) {
		var request netproto.FileTransferInit
		if err := netproto.Decode(f, &request); err != nil {
			t.Error(err)
		}
		if request.ChannelID != 7 || request.Direction != "upload" || request.Size <= 0 {
			t.Errorf("wrong upload request: %+v", request)
		}
		close(entered)
		<-release
		return netproto.MsgFileTransferInitResponse, netproto.FileTransferInitResponse{Token: "upload", TransferID: "upload", Port: 12334}, true
	})
	go cm.readLoop(control)
	t.Cleanup(func() { cm.disconnect(); _ = controlServer.Close() })
	other, _ := newPipedApp(t, func(*netproto.Frame) (netproto.MessageType, any, bool) {
		t.Error("upload redirected to replacement server")
		return 0, nil, false
	})
	app.tabs = map[string]*tabState{"a": {cm: cm}, "b": {cm: other.cmLoad()}}
	app.activeID = "a"
	uploadClient, uploadServer := net.Pipe()
	oldDial := transferDial
	transferDial = func(ep ftEndpoint) (net.Conn, error) {
		if ep.addr != "127.0.0.1:12334" || ep.epoch != 1 {
			t.Errorf("wrong endpoint: %+v", ep)
		}
		return uploadClient, nil
	}
	t.Cleanup(func() { transferDial = oldDial; _ = uploadClient.Close(); _ = uploadServer.Close() })
	uploaded := captureAttachmentUpload(uploadServer)
	type result struct {
		token string
		err   error
	}
	done := make(chan result, 1)
	go func() {
		token, err := app.UploadChatAttachmentForTab("a", 7, "original.txt", base64.StdEncoding.EncodeToString([]byte("original bytes")))
		done <- result{token, err}
	}()
	select {
	case <-entered:
	case <-timeoutC(t):
		close(release)
		t.Fatal("upload initialization did not start")
	}
	app.tabsMu.Lock()
	_, _, _, ok := app.activateLocked("b")
	app.tabsMu.Unlock()
	close(release)
	if !ok {
		t.Fatal("activation failed")
	}
	var got result
	select {
	case got = <-done:
	case <-timeoutC(t):
		t.Fatal("upload did not finish")
	}
	if got.err != nil {
		t.Fatal(got.err)
	}
	upload := <-uploaded
	if upload.err != nil {
		t.Fatal(upload.err)
	}
	storage, keyB64, name := parseFileRef(strings.TrimSuffix(strings.TrimPrefix(got.token, "[file:"), "]"))
	if storage != attachmentStorageName(upload.data) || name != "original.txt" {
		t.Fatalf("wrong token: %q", got.token)
	}
	rawKey, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil || len(rawKey) != 32 {
		t.Fatal("invalid attachment key")
	}
	var key [32]byte
	copy(key[:], rawKey)
	if plain, err := openFileLimited(upload.data, key, maxChatAttachmentBytes); err != nil || string(plain) != "original bytes" {
		t.Fatalf("wrong encrypted upload: %q / %v", plain, err)
	}
}
