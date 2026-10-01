package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"noxa/internal/netproto"
)

type downloadProgressSignal struct {
	once  sync.Once
	wrote chan struct{}
}

func (s *downloadProgressSignal) Emit(name string, payload any) {
	if progress, ok := payload.(ftProgress); name == "ft_progress" && ok && progress.Transferred > 0 {
		s.once.Do(func() { close(s.wrote) })
	}
}

func diskDownloadManager(t *testing.T) *connManager {
	t.Helper()
	cm := newTestConnManager()
	control, _ := transferPipe(t)
	cm.conn = control
	cm.transferEpoch = 1
	cm.acceptingTransfers = true
	return cm
}

func diskDownloadServer(t *testing.T, conn net.Conn, content []byte, finish <-chan struct{}) <-chan int64 {
	t.Helper()
	offsets := make(chan int64, 1)
	go func() {
		defer func() { _ = conn.Close() }()
		frame, err := netproto.ReadFrame(conn)
		if err != nil {
			return
		}
		var request struct {
			Offset int64 `json:"offset"`
		}
		if err := json.Unmarshal(frame.Payload, &request); err != nil {
			return
		}
		offsets <- request.Offset
		if request.Offset > int64(len(content)) {
			payload, _ := json.Marshal(map[string]any{"ok": false, "error": fmt.Sprintf("resume offset %d is past the end of file (%d bytes)", request.Offset, len(content))})
			_ = netproto.WriteFrame(conn, &netproto.Frame{Type: ftStatus, Payload: payload})
			return
		}
		if err := netproto.WriteFrame(conn, &netproto.Frame{Type: ftChunk, Payload: content[request.Offset:]}); err != nil {
			return
		}
		if finish != nil {
			<-finish
		}
		payload, _ := json.Marshal(map[string]string{"sha256": transferDigest(content)})
		if err := netproto.WriteFrame(conn, &netproto.Frame{Type: ftDigest, Payload: payload}); err != nil {
			return
		}
		_ = netproto.WriteFrame(conn, &netproto.Frame{Type: ftStatus, Payload: []byte(`{"ok":true}`)})
	}()
	return offsets
}

func TestProgressDownloadSerializesSharedDestinationAcrossManagers(t *testing.T) {
	originalDial := transferDial
	t.Cleanup(func() { transferDial = originalDial })
	first, second := diskDownloadManager(t), diskDownloadManager(t)
	written := make(chan struct{})
	first.sink = &downloadProgressSignal{wrote: written}
	firstConn, firstPeer := transferPipe(t)
	secondConn, secondPeer := transferPipe(t)
	release := make(chan struct{})
	defer close(release)
	diskDownloadServer(t, firstPeer, []byte("first"), release)
	diskDownloadServer(t, secondPeer, []byte("first-corrupted"), nil)
	var dials atomic.Int32
	transferDial = func(ftEndpoint) (net.Conn, error) {
		if dials.Add(1) == 1 {
			return firstConn, nil
		}
		return secondConn, nil
	}
	dest := filepath.Join(t.TempDir(), "shared.bin")
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- first.ftDownloadProgress("first", ftEndpoint{epoch: 1}, "token", "first", dest, &ftProgress{Total: 5})
	}()
	<-written
	err := second.ftDownloadProgress("second", ftEndpoint{epoch: 1}, "token", "second", dest, &ftProgress{Total: 15})
	if err == nil || !strings.Contains(err.Error(), "already") {
		t.Errorf("overlapping download was not rejected before touching the destination: %v", err)
	}
	if dials.Load() != 1 {
		t.Errorf("overlapping download reached the data connection: %d dials", dials.Load())
	}
	// Release the first server before asserting its stored bytes. Both streams
	// have valid individual hashes, so their shared writer must be excluded.
	release <- struct{}{}
	if err := <-firstDone; err != nil {
		t.Fatalf("original download: %v", err)
	}
	data, err := os.ReadFile(dest)
	if err != nil || string(data) != "first" {
		t.Fatalf("verified destination = %q, %v; want first", data, err)
	}
	if err := second.ftDownloadProgress("retry", ftEndpoint{epoch: 1}, "token", "retry", dest, &ftProgress{Total: 15}); err != nil {
		t.Fatalf("destination did not become available after completion: %v", err)
	}
}

func TestProgressDownloadClearsRejectedResumePrefix(t *testing.T) {
	originalDial := transferDial
	t.Cleanup(func() { transferDial = originalDial })
	cm := diskDownloadManager(t)
	dest := filepath.Join(t.TempDir(), "shrunk.bin")
	if err := os.WriteFile(dest, []byte("original destination"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest+partSuffix, []byte("old oversized partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	for attempt := range 2 {
		conn, peer := transferPipe(t)
		offsets := diskDownloadServer(t, peer, []byte("new"), nil)
		transferDial = func(ftEndpoint) (net.Conn, error) { return conn, nil }
		err := cm.ftDownloadProgress("resume", ftEndpoint{epoch: 1}, "token", "resume", dest, &ftProgress{Total: 99})
		offset := <-offsets
		if attempt == 0 {
			if err == nil {
				t.Fatal("rejected oversized prefix unexpectedly succeeded")
			}
			if _, err := os.Stat(dest + partSuffix); !os.IsNotExist(err) {
				t.Errorf("rejected prefix survived and would poison retry: %v", err)
			}
			if data, err := os.ReadFile(dest); err != nil || string(data) != "original destination" {
				t.Fatalf("failed resume replaced existing download: %q, %v", data, err)
			}
		} else {
			if err != nil || offset != 0 {
				t.Fatalf("clean retry offset=%d, error=%v", offset, err)
			}
			if data, err := os.ReadFile(dest); err != nil || string(data) != "new" {
				t.Fatalf("downloaded bytes = %q, %v", data, err)
			}
		}
	}
}

func TestProgressDownloadPreservesResumeAfterTransportFailure(t *testing.T) {
	originalDial := transferDial
	t.Cleanup(func() { transferDial = originalDial })
	cm := diskDownloadManager(t)
	dest := filepath.Join(t.TempDir(), "interrupted.bin")
	prefix, whole := []byte("prefix"), []byte("prefix and suffix")
	if err := os.WriteFile(dest+partSuffix, prefix, 0o600); err != nil {
		t.Fatal(err)
	}
	conn, peer := transferPipe(t)
	_ = peer.Close()
	transferDial = func(ftEndpoint) (net.Conn, error) { return conn, nil }
	if err := cm.ftDownloadProgress("interrupted", ftEndpoint{epoch: 1}, "token", "interrupted", dest, &ftProgress{Total: int64(len(whole))}); err == nil {
		t.Fatal("closed transport unexpectedly succeeded")
	}
	if data, err := os.ReadFile(dest + partSuffix); err != nil || string(data) != string(prefix) {
		t.Fatalf("transport failure discarded resumable prefix: %q, %v", data, err)
	}
	conn, peer = transferPipe(t)
	offsets := diskDownloadServer(t, peer, whole, nil)
	transferDial = func(ftEndpoint) (net.Conn, error) { return conn, nil }
	progress := &ftProgress{Total: int64(len(whole))}
	if err := cm.ftDownloadProgress("retry", ftEndpoint{epoch: 1}, "token", "retry", dest, progress); err != nil {
		t.Fatal(err)
	}
	if offset := <-offsets; offset != int64(len(prefix)) || progress.Resumed != offset {
		t.Fatalf("resume offset=%d, progress=%d", offset, progress.Resumed)
	}
	if data, err := os.ReadFile(dest); err != nil || string(data) != string(whole) {
		t.Fatalf("resumed destination=%q, %v", data, err)
	}
}

func TestDownloadStreamEarlyStatusStillRequiresDigest(t *testing.T) {
	for _, success := range []bool{false, true} {
		t.Run(fmt.Sprint(success), func(t *testing.T) {
			conn, peer := transferPipe(t)
			payload, _ := json.Marshal(map[string]any{"ok": success, "error": "resume offset rejected"})
			_, done := serveTransferDownload(t, peer, nil, "", &netproto.Frame{Type: ftStatus, Payload: payload})
			_, err := ftDownloadTo(conn, "token", "download", &transferRecordingWriter{}, 0)
			<-done
			if err == nil {
				t.Fatal("download without a digest succeeded")
			}
			if !success && (!errors.Is(err, errFileTransferRejected) || !strings.Contains(err.Error(), "resume offset rejected")) {
				t.Fatalf("early server rejection lost its type or reason: %v", err)
			}
		})
	}
}

func TestDownloadDestinationReservationNormalizesPathAliases(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "report.bin")
	release, err := reserveDownloadDestination(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	aliases := []string{dir + string(filepath.Separator) + "child" + string(filepath.Separator) + ".." + string(filepath.Separator) + "report.bin"}
	aliases = append(aliases, dest+partSuffix)
	if runtime.GOOS == "windows" {
		aliases = append(aliases, strings.ToUpper(dest))
	}
	for _, alias := range aliases {
		if done, err := reserveDownloadDestination(alias); err == nil {
			done()
			t.Fatalf("download alias bypassed destination ownership: %s", alias)
		}
	}
}

func TestDownloadDestinationReservationRejectsAnActivePartialAsTarget(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "report.bin")
	release, err := reserveDownloadDestination(dest + partSuffix)
	if err != nil {
		t.Fatal(err)
	}
	if done, err := reserveDownloadDestination(dest); err == nil {
		done()
		t.Fatal("destination reservation overwrote an active partial target")
	}
	release()
	done, err := reserveDownloadDestination(dest)
	if err != nil {
		t.Fatalf("rejected reservation leaked its destination lock: %v", err)
	}
	done()
}
