package main

import (
	"context"
	"encoding/base64"
	"errors"
	"net"
	"noxa/internal/netproto"
	"strings"
	"testing"
)

func TestProgressAttachmentUsesEncryptedStorageAndPlaintextSize(t *testing.T) {
	var cipher []byte
	token, err := uploadChatAttachmentUsing("report.txt", base64.StdEncoding.EncodeToString([]byte("private content")), func(storage string, blob []byte) error {
		if storage != attachmentStorageName(blob) {
			t.Fatal("storage is not derived from encrypted content")
		}
		cipher = blob
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_, key, name, meta := splitFileRef(strings.TrimSuffix(strings.TrimPrefix(token, "[file:"), "]"))
	if key == "" || name != "report.txt" || meta.Size != 15 || strings.Contains(string(cipher), "private content") {
		t.Fatalf("invalid encrypted upload: %s", token)
	}
	failure := errors.New("failed upload")
	if token, err := uploadChatAttachmentUsing("file", "YQ==", func(string, []byte) error { return failure }); token != "" || !errors.Is(err, failure) {
		t.Fatal("failed upload returned a token")
	}
}

func TestProgressAttachmentCancellationBeforeHandshake(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	control := chatUploadCancellation{cancel: cancel}
	_ = control.Close()
	if err := uploadChatCiphertext(ctx, nil, "id", 1, "name", nil, nil); !errors.Is(err, errTransferCanceled) {
		t.Fatal(err)
	}
	a := &App{}
	if _, err := a.UploadChatAttachmentProgressForTab("", "", 1, "name", "YQ=="); err == nil {
		t.Fatal("empty transfer identifier accepted")
	}
}

func TestProgressAttachmentCancelDuringInitDoesNotDialOrReturnToken(t *testing.T) {
	controlClient, controlServer := net.Pipe()
	control := fixedRemoteAddrConn{Conn: controlClient, remote: fixedTransferAddr("127.0.0.1:12333")}
	cm := newConnManager(context.Background())
	cm.sink = &eventRecorder{}
	cm.conn = control
	cm.acceptingTransfers = true
	cm.transferEpoch = 1
	app := appWithCM(cm)
	app.tabs = map[string]*tabState{"a": {cm: cm}}
	app.activeID = "a"
	entered, release := make(chan struct{}), make(chan struct{})
	go serveFrames(controlServer, func(*netproto.Frame) (netproto.MessageType, any, bool) {
		close(entered)
		<-release
		return netproto.MsgFileTransferInitResponse, netproto.FileTransferInitResponse{Token: "upload", TransferID: "upload", Port: 12334}, true
	})
	go cm.readLoop(control)
	t.Cleanup(func() { cm.disconnect(); _ = controlServer.Close() })
	previous := transferDial
	transferDial = func(ftEndpoint) (net.Conn, error) {
		t.Error("canceled upload dialed data port")
		return nil, errors.New("unexpected dial")
	}
	t.Cleanup(func() { transferDial = previous })
	result := make(chan error, 1)
	go func() {
		token, err := app.UploadChatAttachmentProgressForTab("a", "chat-cancel", 7, "private.txt", "YQ==")
		if token != "" {
			result <- errors.New("canceled upload returned attachment token")
			return
		}
		result <- err
	}()
	select {
	case <-entered:
	case <-timeoutC(t):
		close(release)
		t.Fatal("upload did not initialize")
	}
	if err := app.CancelTransferForTab("a", "chat-cancel"); err != "" {
		close(release)
		t.Fatal(err)
	}
	close(release)
	select {
	case err := <-result:
		if !errors.Is(err, errTransferCanceled) {
			t.Fatal(err)
		}
	case <-timeoutC(t):
		t.Fatal("canceled upload did not finish")
	}
}
