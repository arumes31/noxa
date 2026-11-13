package main

import (
	"net"
	"testing"
	"time"

	"noxa/internal/netproto"
)

func TestBindingsRequireManagerOffline(t *testing.T) {
	a := &App{}
	if got := a.FileDelete(1, "", "file"); got != "not connected" {
		t.Fatalf("FileDelete offline = %q", got)
	}
	if _, err := a.FileList(1, ""); err == nil || err.Error() != "not connected" {
		t.Fatalf("FileList offline error = %v", err)
	}
	if got := a.ChatDeleteMessage(1); got != "not connected" {
		t.Fatalf("ChatDeleteMessage offline = %q", got)
	}
	if got := a.SetStatus("online", ""); got != "not connected" {
		t.Fatalf("SetStatus offline = %q", got)
	}
}

func TestFileBindingStaysWithCapturedManagerAcrossTabSwitch(t *testing.T) {
	clientA, serverA := net.Pipe()
	clientB, serverB := net.Pipe()
	t.Cleanup(func() {
		_ = clientA.Close()
		_ = serverA.Close()
		_ = clientB.Close()
		_ = serverB.Close()
	})
	cmA, cmB := newTestConnManager(), newTestConnManager()
	cmA.mu.Lock()
	cmA.conn = clientA
	cmA.mu.Unlock()
	cmB.mu.Lock()
	cmB.conn = clientB
	cmB.mu.Unlock()
	go cmA.readLoop(clientA)
	go cmB.readLoop(clientB)
	a := appWithCM(cmA)

	done := make(chan error, 1)
	go func() {
		_, err := a.FileList(7, "")
		done <- err
	}()
	request := readFrame(t, serverA)
	if got := netproto.MessageType(request.Type); got != netproto.MsgFileList {
		t.Fatalf("request sent to A = %v", got)
	}
	a.cmStore(cmB)
	if err := netproto.WriteFrame(serverA, mustEncode(netproto.MsgFileListResponse, netproto.FileListResponse{})); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("FileList on captured manager: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("captured FileList did not complete")
	}
	_ = serverB.SetReadDeadline(time.Now().Add(25 * time.Millisecond))
	if _, err := netproto.ReadFrame(serverB); err == nil {
		t.Fatal("tab switch redirected FileList request to manager B")
	}
}
