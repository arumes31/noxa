package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"noxa/internal/netproto"
)

// Resumable uploads (39): the client declares the file's SHA-256 when it asks
// for an upload token. A server that supports resume answers with the offset
// it already holds; after a dropped connection the client asks for a new
// token and streams only the missing bytes. The digest frame always covers
// the whole file. Servers without resume ignore the digest and the upload
// behaves exactly as before, including a full restart on retry.

// uploadResumeRetries bounds automatic retries after a transient failure.
const uploadResumeRetries = 3

// uploadRetryDelay is the wait before retry n (1-based): 1 s, 2 s, 4 s.
var uploadRetryDelay = func(n int) time.Duration { return time.Second << (n - 1) }

// uploadRequest describes one upload independent of its data source.
type uploadRequest struct {
	channelID int64
	folder    string
	name      string
	size      int64
	sum       string // lowercase hex SHA-256 of the whole file
}

// sha256Of hashes a source without holding it in memory.
func sha256Of(src io.ReaderAt, size int64) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, io.NewSectionReader(src, 0, size)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// initUpload asks the control channel for an upload token.
func (cm *connManager) initUpload(req uploadRequest) (netproto.FileTransferInitResponse, error) {
	var init netproto.FileTransferInitResponse
	f, err := cm.request(netproto.MsgFileTransferInit, netproto.MsgFileTransferInitResponse,
		netproto.FileTransferInit{ChannelID: req.channelID, Folder: req.folder, Direction: "upload", Name: req.name, Size: req.size, SHA256: req.sum},
		10*time.Second)
	if err != nil {
		return init, err
	}
	err = decodeJSON(f, &init)
	return init, err
}

// uploadWithResume streams src through first (an already issued token, may be
// nil) and retries transient failures, resuming where the server stopped.
// trackID names the transfer for cancellation ("" uses the transfer ID);
// progress, when set, receives the bytes the server holds plus those sent.
// canceled, when set, is checked before every attempt.
func (cm *connManager) uploadWithResume(trackID string, req uploadRequest, src io.ReaderAt, first *netproto.FileTransferInitResponse, progress func(sent int64), canceled ...func() bool) error {
	isCanceled := func() bool { return len(canceled) > 0 && canceled[0] != nil && canceled[0]() }
	var lastErr error
	for attempt := 0; ; attempt++ {
		if isCanceled() {
			return errTransferCanceled
		}
		init := first
		first = nil
		if init == nil {
			issued, err := cm.initUpload(req)
			if err != nil {
				if lastErr != nil {
					return lastErr
				}
				return err
			}
			init = &issued
		}
		ep, err := cm.ftTarget(*init)
		if err != nil {
			return err
		}
		if isCanceled() {
			return errTransferCanceled // canceled while the token was issued
		}
		id := trackID
		if id == "" {
			id = init.TransferID
		}
		offset := int64(0)
		if init.Resumable && init.ResumeOffset > 0 && init.ResumeOffset <= req.size {
			offset = init.ResumeOffset
		}
		err = cm.sendUploadRange(id, ep, *init, offset, src, req, progress)
		if err == nil {
			return nil
		}
		if isCanceled() {
			return errTransferCanceled
		}
		lastErr = err
		if attempt >= uploadResumeRetries || !retryableUpload(err) {
			return err
		}
		if !cm.waitBeforeRetry(id, ep.epoch, uploadRetryDelay(attempt+1)) {
			return errTransferCanceled
		}
	}
}

// sendUploadRange runs one transfer connection: init (with the resume offset
// for a resumable token), chunks from offset to the end, digest, status.
func (cm *connManager) sendUploadRange(id string, ep ftEndpoint, init netproto.FileTransferInitResponse, offset int64, src io.ReaderAt, req uploadRequest, progress func(int64)) error {
	conn, err := transferDial(ep)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	defer clearTransferDeadlines(conn)
	untrack, tracked := cm.trackTransferAt(id, ep.epoch, conn)
	if !tracked {
		return errTransferCanceled
	}
	defer untrack()

	initFrame := map[string]any{"token": init.Token, "transfer_id": init.TransferID}
	if init.Resumable && offset > 0 {
		initFrame["offset"] = offset
	}
	if err := ftWriteJSON(conn, ftInit, initFrame); err != nil {
		return asCanceled(err)
	}
	buf := make([]byte, 32*1024)
	sent := offset
	if progress != nil {
		progress(sent)
	}
	for sent < req.size {
		n, readErr := src.ReadAt(buf[:min(int64(len(buf)), req.size-sent)], sent)
		if n > 0 {
			if err := ftWriteFrame(conn, &netproto.Frame{Type: ftChunk, Payload: buf[:n]}); err != nil {
				return asCanceled(err)
			}
			sent += int64(n)
			if progress != nil {
				progress(sent)
			}
		}
		if readErr != nil && (!errors.Is(readErr, io.EOF) || sent != req.size) {
			return fmt.Errorf("reading %s: %w", req.name, localReadError{readErr})
		}
	}
	if err := ftWriteJSON(conn, ftDigest, map[string]string{"sha256": req.sum}); err != nil {
		return asCanceled(err)
	}
	return asCanceled(ftReadStatus(conn))
}

// localReadError marks a failure to read the local source; retrying cannot
// help when the file itself is unreadable.
type localReadError struct{ err error }

func (e localReadError) Error() string { return e.err.Error() }
func (e localReadError) Unwrap() error { return e.err }

// asCanceled maps an operation on a socket the user closed to the cancel
// error, so it is neither retried nor shown as a network failure.
func asCanceled(err error) error {
	if err != nil && isClosedConn(err) {
		return errTransferCanceled
	}
	return err
}

// retryableUpload reports a transient failure: the network, not the server's
// verdict ("transfer failed: ..."), the user, or the local file.
func retryableUpload(err error) bool {
	if err == nil || errors.Is(err, errTransferCanceled) {
		return false
	}
	var local localReadError
	if errors.As(err, &local) {
		return false
	}
	if strings.HasPrefix(err.Error(), "transfer failed: ") {
		return false
	}
	var netErr net.Error
	return errors.As(err, &netErr) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		strings.Contains(err.Error(), "connection reset") || strings.Contains(err.Error(), "broken pipe")
}

// waitBeforeRetry sleeps for d unless the user cancels the transfer (or the
// control connection changes) meanwhile. It reports whether to retry.
func (cm *connManager) waitBeforeRetry(id string, epoch uint64, d time.Duration) bool {
	signal := newCancelSignal()
	untrack, tracked := cm.trackTransferAt(id, epoch, signal)
	if !tracked {
		return false
	}
	defer untrack()
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-signal.closed:
		return false
	}
}

// cancelSignal stands in for a transfer socket while no socket exists: the
// cancel path closes tracked connections, and closing this one ends a wait.
type cancelSignal struct {
	net.Conn // never used; only Close is called
	once     sync.Once
	closed   chan struct{}
}

func newCancelSignal() *cancelSignal { return &cancelSignal{closed: make(chan struct{})} }

func (c *cancelSignal) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}
