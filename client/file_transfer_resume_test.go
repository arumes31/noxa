package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"noxa/internal/filetransfer"
	"noxa/internal/netproto"
	"noxa/internal/store"
)

// memFileStore is the smallest filetransfer.FileStore for end-to-end tests.
type memFileStore struct {
	mu    sync.Mutex
	files map[string]store.FileRecord
}

func memKey(channelID int64, folder, name string) string {
	return strconv.FormatInt(channelID, 10) + "/" + folder + "/" + name
}

func (m *memFileStore) AddFile(_ context.Context, rec store.FileRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files[memKey(rec.ChannelID, rec.Folder, rec.Name)] = rec
	return nil
}
func (m *memFileStore) GetFile(_ context.Context, channelID int64, folder, name string) (*store.FileRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.files[memKey(channelID, folder, name)]
	if !ok {
		return nil, store.ErrFileNotFound
	}
	return &rec, nil
}
func (m *memFileStore) ListFiles(context.Context, int64, string) ([]store.FileRecord, error) {
	return nil, nil
}
func (m *memFileStore) ListFileFolders(context.Context, int64) ([]string, error) { return nil, nil }
func (m *memFileStore) ListFileVersions(context.Context, int64, string, string) ([]store.FileRecord, error) {
	return nil, nil
}
func (m *memFileStore) RenameFile(context.Context, int64, string, string, string, string) error {
	return errors.New("unsupported")
}
func (m *memFileStore) MoveFile(context.Context, int64, string, string, int64, string, string) error {
	return errors.New("unsupported")
}
func (m *memFileStore) DeleteFile(context.Context, int64, string, string) error { return nil }
func (m *memFileStore) FindFileBySHA(context.Context, int64, string, string, string) (*store.FileRecord, error) {
	return nil, store.ErrFileNotFound
}
func (m *memFileStore) ChannelFileUsage(context.Context, int64) (int64, error)   { return 0, nil }
func (m *memFileStore) UploaderFileUsage(context.Context, string) (int64, error) { return 0, nil }
func (m *memFileStore) FileContentUsage(context.Context, int64, string, string) (int64, int64, error) {
	return 0, 0, nil
}
func (m *memFileStore) UploaderContentUsageExcept(context.Context, int64, string, string, string, string) (int64, error) {
	return 0, nil
}

// dropAfterConn fails its writes once limit bytes went through, like a
// network that disappears mid-upload.
type dropAfterConn struct {
	net.Conn
	limit   int
	written int
}

func (c *dropAfterConn) Write(p []byte) (int, error) {
	if c.written+len(p) > c.limit {
		_ = c.Close()
		return 0, &net.OpError{Op: "write", Net: "tcp", Err: errors.New("connection reset by peer")}
	}
	c.written += len(p)
	return c.Conn.Write(p)
}

// startResumeFixture runs a real file-transfer server and a control channel
// that issues its tokens. It returns the manager, the server, its root, and
// the offsets reported by every issued token.
func startResumeFixture(t *testing.T) (*connManager, *filetransfer.Server, string, *[]int64) {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	root := t.TempDir()
	srv := filetransfer.New(filetransfer.Config{Addr: addr, RootDir: root}, &memFileStore{files: map[string]store.FileRecord{}}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan error, 1)
	go func() { started <- srv.Start(ctx) }()
	t.Cleanup(func() { cancel(); _ = srv.Close(); <-started })
	for deadline := time.Now().Add(3 * time.Second); ; {
		c, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", addr)
		if err == nil {
			_ = c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("file-transfer server did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}

	var mu sync.Mutex
	offsets := []int64{}
	controlClient, controlServer := net.Pipe()
	cm := newConnManager(context.Background())
	cm.sink = &eventRecorder{}
	cm.conn = fixedRemoteAddrConn{Conn: controlClient, remote: fixedTransferAddr("127.0.0.1:12333")}
	cm.acceptingTransfers = true
	cm.transferEpoch = 1
	go serveFrames(controlServer, func(frame *netproto.Frame) (netproto.MessageType, any, bool) {
		var req netproto.FileTransferInit
		if err := netproto.Decode(frame, &req); err != nil {
			t.Errorf("decode init: %v", err)
		}
		id, token, offset, resumable, err := srv.InitResumableUpload(context.Background(), req.ChannelID, req.Folder, req.Name, req.Size, "uid-1", 0, req.SHA256)
		if err != nil {
			return netproto.MsgError, netproto.Error{Code: 6, Message: err.Error(), OriginType: uint16(netproto.MsgFileTransferInit)}, true
		}
		mu.Lock()
		offsets = append(offsets, offset)
		mu.Unlock()
		return netproto.MsgFileTransferInitResponse, netproto.FileTransferInitResponse{TransferID: id, Token: token, Port: srv.Port(), Resumable: resumable, ResumeOffset: offset}, true
	})
	go cm.readLoop(cm.conn)
	t.Cleanup(func() { cm.disconnect(); _ = controlServer.Close() })
	return cm, srv, root, &offsets
}

// settledPartialDelay returns a retry delay that waits until the server's
// resumable partial under root holds bytes and has stopped growing, so the
// retry sees the bytes that reached the server (production waits a second).
func settledPartialDelay(root string) func(int) time.Duration {
	return func(int) time.Duration {
		last, stable := int64(-1), 0
		for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline) && stable < 3; {
			var size int64
			_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
				if err == nil && !d.IsDir() && strings.Contains(d.Name(), ".part-r") {
					if info, statErr := d.Info(); statErr == nil {
						size = info.Size()
					}
				}
				return nil
			})
			if size > 0 && size == last {
				stable++
			} else {
				stable = 0
			}
			last = size
			time.Sleep(20 * time.Millisecond)
		}
		return time.Millisecond
	}
}

func TestUploadResumesAfterDroppedConnection(t *testing.T) {
	cm, _, root, offsets := startResumeFixture(t)
	previousDelay := uploadRetryDelay
	uploadRetryDelay = settledPartialDelay(root)
	t.Cleanup(func() { uploadRetryDelay = previousDelay })
	var dials atomic.Int32
	previous := transferDial
	transferDial = func(ep ftEndpoint) (net.Conn, error) {
		conn, err := ftDial(ep)
		if err != nil || dials.Add(1) > 1 {
			return conn, err
		}
		return &dropAfterConn{Conn: conn, limit: 100 << 10}, nil
	}
	t.Cleanup(func() { transferDial = previous })

	data := bytes.Repeat([]byte("resumable-upload-"), 20000) // ~340 KB
	if err := ftPutBytesInFolder(cm, 7, "docs", "big.bin", data); err != nil {
		t.Fatalf("upload: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "7", "docs", "big.bin"))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("stored %d bytes (%v), want the %d uploaded", len(got), err, len(data))
	}
	if dials.Load() != 2 || len(*offsets) != 2 {
		t.Fatalf("dials=%d tokens=%d, want one retry", dials.Load(), len(*offsets))
	}
	if (*offsets)[0] != 0 || (*offsets)[1] <= 0 || (*offsets)[1] >= int64(len(data)) {
		t.Fatalf("offsets = %v, want the retry to resume mid-file", *offsets)
	}
}

func TestUploadFromDiskResumesAfterDroppedConnection(t *testing.T) {
	cm, _, root, offsets := startResumeFixture(t)
	previousDelay := uploadRetryDelay
	uploadRetryDelay = settledPartialDelay(root)
	t.Cleanup(func() { uploadRetryDelay = previousDelay })
	var dials atomic.Int32
	previous := transferDial
	transferDial = func(ep ftEndpoint) (net.Conn, error) {
		conn, err := ftDial(ep)
		if err != nil || dials.Add(1) > 1 {
			return conn, err
		}
		return &dropAfterConn{Conn: conn, limit: 64 << 10}, nil
	}
	t.Cleanup(func() { transferDial = previous })

	data := bytes.Repeat([]byte{1, 2, 3, 4, 5, 6, 7}, 50000)
	path := filepath.Join(t.TempDir(), "disk.bin")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	src, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()
	sum, err := sha256Of(src, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(data)
	if sum != hex.EncodeToString(want[:]) {
		t.Fatal("sha256Of disagrees with sha256.Sum256")
	}
	var reported []int64
	req := uploadRequest{channelID: 7, name: "disk.bin", size: int64(len(data)), sum: sum}
	if err := cm.uploadWithResume("disk", req, src, nil, func(sent int64) { reported = append(reported, sent) }); err != nil {
		t.Fatalf("upload: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "7", "disk.bin"))
	if !bytes.Equal(got, data) {
		t.Fatal("stored file differs")
	}
	if len(*offsets) != 2 || (*offsets)[1] == 0 {
		t.Fatalf("offsets = %v", *offsets)
	}
	if reported[len(reported)-1] != int64(len(data)) {
		t.Fatalf("progress ended at %d, want %d", reported[len(reported)-1], len(data))
	}
}

func TestUploadGivesUpAfterRetries(t *testing.T) {
	cm, _, _, offsets := startResumeFixture(t)
	previousDelay := uploadRetryDelay
	uploadRetryDelay = func(int) time.Duration { return time.Millisecond }
	t.Cleanup(func() { uploadRetryDelay = previousDelay })
	previous := transferDial
	transferDial = func(ep ftEndpoint) (net.Conn, error) {
		conn, err := ftDial(ep)
		if err != nil {
			return nil, err
		}
		return &dropAfterConn{Conn: conn, limit: 1}, nil // never gets anywhere
	}
	t.Cleanup(func() { transferDial = previous })
	err := ftPutBytesInFolder(cm, 7, "", "a.bin", bytes.Repeat([]byte("x"), 1000))
	if err == nil || !retryableUpload(err) {
		t.Fatalf("err = %v, want the last network error", err)
	}
	if len(*offsets) != uploadResumeRetries+1 {
		t.Fatalf("tokens = %d, want %d attempts", len(*offsets), uploadResumeRetries+1)
	}
}

func TestRetryableUploadClassification(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{&net.OpError{Op: "write", Err: errors.New("connection reset by peer")}, true},
		{io.ErrUnexpectedEOF, true},
		{errors.New("transfer failed: checksum mismatch"), false},
		{errTransferCanceled, false},
		{localReadError{errors.New("disk gone")}, false},
		{errors.New("something else"), false},
		{nil, false},
	} {
		if got := retryableUpload(tc.err); got != tc.want {
			t.Errorf("retryableUpload(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

func TestRetryWaitEndsOnCancel(t *testing.T) {
	cm := newConnManager(context.Background())
	cm.conn = fixedRemoteAddrConn{remote: fixedTransferAddr("127.0.0.1:1")}
	cm.acceptingTransfers = true
	cm.transferEpoch = 3
	done := make(chan bool, 1)
	go func() { done <- cm.waitBeforeRetry("up-1", 3, time.Minute) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		cm.transfers.mu.Lock()
		n := len(cm.transfers.entries)
		cm.transfers.mu.Unlock()
		if n == 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cm.cancelTransfers("up-1")
	select {
	case retry := <-done:
		if retry {
			t.Fatal("a canceled transfer would retry")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not end the retry wait")
	}
}
