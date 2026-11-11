// resume.go implements resumable uploads (39). A client that declares the
// file's SHA-256 when it asks for an upload token gets a partial file whose
// name is derived from (uploader, scope, folder, name, size, digest). An
// interrupted transfer keeps that partial; the next token for the same file
// reports how many bytes the server already holds, and the client streams
// only the rest. The final digest still covers the whole file, so integrity
// does not depend on the resume.
package filetransfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// resumablePartialPrefix marks a resumable partial next to its final
	// blob: "<name>.part-r<32 hex>". Legacy single-shot partials use
	// "<name>.part-<16 hex>" and are never resumed.
	resumablePartialPrefix = ".part-r"
	legacyPartialPrefix    = ".part-"
	// resumablePartialTTL is how long an untouched resumable partial is kept
	// for its owner to come back. The orphan sweep removes older ones.
	resumablePartialTTL = 24 * time.Hour
	// legacyPartialTTL bounds a crashed single-shot upload's leftover. A live
	// transfer connection is capped at connTimeout, far below this.
	legacyPartialTTL = time.Hour
	// maxResumablePartialsPerUploader bounds how many interrupted uploads one
	// user may keep at a time. Further uploads still work, without resume.
	maxResumablePartialsPerUploader = 4
	// partialTakeoverWait bounds how long a resumed transfer waits for the
	// connection it replaces to release the partial file.
	partialTakeoverWait = 5 * time.Second
)

// ErrUploadInProgress reports a second transfer for the same partial while
// the first one still holds it.
var ErrUploadInProgress = errors.New("this upload is already in progress")

// partialUpload tracks one resumable partial known to this process. The
// registry is rebuilt lazily after a restart: a partial found on disk is
// adopted when its owner resumes it, and an abandoned one is removed by the
// orphan sweep after resumablePartialTTL.
type partialUpload struct {
	path     string // relative to RootDir
	uploader string
	size     int64
	// active is the transfer currently writing the partial (nil when idle).
	active *activeTransfer
	// released closes when the active transfer has closed the file.
	released chan struct{}
}

// validSHA256 reports a lowercase or uppercase 64-digit hex digest.
func validSHA256(sum string) bool {
	if len(sum) != 64 {
		return false
	}
	_, err := hex.DecodeString(sum)
	return err == nil
}

// resumablePartialPath derives the partial's path from everything that makes
// two uploads "the same": the uploader (so nobody can append into another
// user's partial), the scope and place, the declared size and the digest.
func resumablePartialPath(channelID int64, folder, name, uploader string, size int64, sum string) string {
	h := sha256.New()
	for _, part := range []string{"noxa-upload-partial-v1", strconv.FormatInt(channelID, 10), folder, name, uploader, strconv.FormatInt(size, 10), strings.ToLower(sum)} {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	key := hex.EncodeToString(h.Sum(nil))[:32]
	return blobPath(channelID, folder, name) + resumablePartialPrefix + key
}

// isResumablePartialName reports "<anything>.part-r<32 hex>".
func isResumablePartialName(base string) bool {
	i := strings.LastIndex(base, resumablePartialPrefix)
	if i <= 0 {
		return false
	}
	return isHex(base[i+len(resumablePartialPrefix):], 32)
}

// isLegacyPartialName reports "<anything>.part-<16 hex>".
func isLegacyPartialName(base string) bool {
	i := strings.LastIndex(base, legacyPartialPrefix)
	if i <= 0 {
		return false
	}
	return isHex(base[i+len(legacyPartialPrefix):], 16)
}

func isHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// InitResumableUpload is InitUpload for a client that declared the file's
// SHA-256. It returns the offset the client should resume from (0 for a new
// upload) and whether the transfer is resumable at all: when the uploader
// already keeps the maximum number of partials, or the partials would push
// them past their quota, the upload is issued as a plain single-shot one.
func (s *Server) InitResumableUpload(ctx context.Context, channelID int64, folder, name string, size int64, uploader string, uploaderQuotaMB int64, sum string) (string, string, int64, bool, error) {
	if !validSHA256(sum) || uploader == "" {
		id, token, err := s.InitUpload(ctx, channelID, folder, name, size, uploader, uploaderQuotaMB)
		return id, token, 0, false, err
	}
	sum = strings.ToLower(sum)
	cleanName, err := sanitizeName(name)
	if err != nil {
		return "", "", 0, false, err
	}
	cleanFolder, err := sanitizeFolder(folder)
	if err != nil {
		return "", "", 0, false, err
	}
	path := resumablePartialPath(channelID, cleanFolder, cleanName, uploader, size, sum)
	offset, resumable := s.partialOffset(channelID, path, uploader, size, uploaderQuotaMB)
	var complete func(*transfer)
	if resumable {
		complete = func(tr *transfer) { tr.SHA256, tr.PartialPath = sum, path }
	}
	id, token, err := s.initUpload(ctx, channelID, folder, name, size, uploader, uploaderQuotaMB, complete)
	if err != nil {
		// A refused token must not keep a reservation for a partial that
		// was never written.
		s.pruneIdlePartials(uploader, time.Now())
		return "", "", 0, false, err
	}
	if !resumable {
		return id, token, 0, false, nil
	}
	return id, token, offset, true, nil
}

// partialOffset reports the bytes already held for path and whether this
// uploader may keep another resumable partial.
func (s *Server) partialOffset(channelID int64, path, uploader string, size, uploaderQuotaMB int64) (int64, bool) {
	// Reservations whose token expired unused, or whose partial is gone,
	// must not count against this uploader's limit.
	s.pruneIdlePartials(uploader, time.Now())
	var held int64
	if root, err := s.openBlobRoot(); err == nil {
		if info, statErr := root.Lstat(path); statErr == nil && info.Mode().IsRegular() {
			held = info.Size()
			if held > size {
				// Not a prefix of this upload: start over.
				_ = root.Remove(path)
				held = 0
			}
		}
		_ = root.Close()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.channelDeletedLocked(channelID) {
		return 0, false
	}
	count := 0
	var otherBytes int64
	for p, partial := range s.partials {
		if p == path || partial.uploader != uploader {
			continue
		}
		count++
		otherBytes += partial.size
	}
	if count >= maxResumablePartialsPerUploader {
		return 0, false
	}
	// Partials are not charged to the quota. To keep users from parking
	// more half-uploaded bytes than their quota, an upload that would take
	// the pending partials past it is issued single-shot instead.
	if uploaderQuotaMB > 0 && otherBytes+size > uploaderQuotaMB<<20 {
		return 0, false
	}
	if _, ok := s.partials[path]; !ok {
		s.partials[path] = &partialUpload{path: path, uploader: uploader, size: size}
	}
	return held, true
}

// pruneIdlePartials drops registry entries of uploader ("" = everyone) that
// no transfer is writing, no unexpired token targets, and whose file no
// longer exists: reservations of refused or abandoned tokens and partials
// already removed from disk. Entries with a file on disk stay, so a client
// can still resume them until the orphan sweep's TTL. It never touches files.
func (s *Server) pruneIdlePartials(uploader string, now time.Time) {
	s.mu.Lock()
	var candidates []string
	for p, partial := range s.partials {
		if (uploader == "" || partial.uploader == uploader) && partial.active == nil && !s.pendingTokenForLocked(p, now) {
			candidates = append(candidates, p)
		}
	}
	s.mu.Unlock()
	if len(candidates) == 0 {
		return
	}
	root, err := s.openBlobRoot()
	if err != nil {
		return
	}
	var gone []string
	for _, p := range candidates {
		if _, err := root.Lstat(p); errors.Is(err, os.ErrNotExist) {
			gone = append(gone, p)
		}
	}
	_ = root.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range gone {
		// Recheck: a transfer may have claimed it, or a token been issued,
		// while the file system was consulted.
		if partial, ok := s.partials[p]; ok && partial.active == nil && !s.pendingTokenForLocked(p, now) {
			delete(s.partials, p)
		}
	}
}

// pendingTokenForLocked reports an issued, unexpired token for the partial.
func (s *Server) pendingTokenForLocked(path string, now time.Time) bool {
	for _, tr := range s.transfers {
		if tr.PartialPath == path && !now.After(tr.Expires) {
			return true
		}
	}
	return false
}

// claimPartial makes active the only writer of the partial. A transfer that
// still holds it (a connection the client already gave up on) is closed, and
// the claim waits until it has released the file.
func (s *Server) claimPartial(tr *transfer, active *activeTransfer) (func(), error) {
	s.mu.Lock()
	partial, ok := s.partials[tr.PartialPath]
	if !ok {
		partial = &partialUpload{path: tr.PartialPath, uploader: tr.Uploader, size: tr.Size}
		s.partials[tr.PartialPath] = partial
	}
	if partial.uploader != tr.Uploader {
		s.mu.Unlock()
		return nil, ErrAccessRevoked
	}
	previous, released := partial.active, partial.released
	s.mu.Unlock()
	if previous != nil && previous != active {
		_ = previous.conn.Close()
		select {
		case <-released:
		case <-time.After(partialTakeoverWait):
			return nil, ErrUploadInProgress
		}
	}
	s.mu.Lock()
	if partial.active != nil && partial.active != previous {
		s.mu.Unlock()
		return nil, ErrUploadInProgress
	}
	done := make(chan struct{})
	partial.active, partial.released = active, done
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		if partial.active == active {
			partial.active, partial.released = nil, nil
		}
		s.mu.Unlock()
		close(done)
	}, nil
}

// forgetPartial drops a partial from the registry once it was committed or
// deliberately discarded.
func (s *Server) forgetPartial(path string) {
	s.mu.Lock()
	delete(s.partials, path)
	s.mu.Unlock()
}

// partialInUse reports whether a live transfer is writing path right now.
func (s *Server) partialInUse(path string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	partial, ok := s.partials[path]
	return ok && partial.active != nil
}

// openResumablePartial opens the partial for appending at offset and returns
// a hash already fed with the bytes the server holds below offset. A client
// offset beyond the held bytes is refused; a lower one truncates, so a client
// that lost track can always restart from zero.
func openResumablePartial(root *os.Root, path string, offset int64) (*os.File, hashWriter, error) {
	if offset < 0 {
		return nil, nil, errors.New("negative resume offset")
	}
	if err := root.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, nil, fmt.Errorf("creating channel dir: %w", err)
	}
	if info, err := root.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return nil, nil, errors.New("partial upload is not a regular file")
	}
	f, err := root.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("opening partial upload: %w", err)
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, nil, errors.New("partial upload is not a regular file")
	}
	if offset > info.Size() {
		_ = f.Close()
		return nil, nil, fmt.Errorf("resume offset %d is beyond the %d bytes the server holds", offset, info.Size())
	}
	if err := f.Truncate(offset); err != nil {
		_ = f.Close()
		return nil, nil, fmt.Errorf("truncating partial upload: %w", err)
	}
	h := sha256.New()
	if _, err := io.Copy(h, io.NewSectionReader(f, 0, offset)); err != nil {
		_ = f.Close()
		return nil, nil, fmt.Errorf("reading partial upload: %w", err)
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		_ = f.Close()
		return nil, nil, fmt.Errorf("seeking partial upload: %w", err)
	}
	return f, h, nil
}

// hashWriter is the subset of hash.Hash the upload loop uses.
type hashWriter interface {
	io.Writer
	Sum([]byte) []byte
}

// keepPartialOn reports whether a failed resumable transfer keeps its bytes:
// only for interruptions. A protocol violation, a wrong digest or lost access
// discards them, because the next attempt could not build on them.
func keepPartialOn(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrAccessRevoked) || errors.Is(err, ErrChannelDeleted) {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, context.Canceled) {
		return true
	}
	return false
}
