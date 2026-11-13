// transfer.go implements the per-connection transfer flow: token validation,
// upload receive with size and checksum enforcement, and download send.
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
	"time"

	"go.uber.org/zap"

	"noxa/internal/netproto"
	"noxa/internal/store"
)

// connTimeout bounds a single transfer connection.
const connTimeout = 10 * time.Minute

// uploadIdleTimeout bounds the wait for each upload frame; a var for tests.
var uploadIdleTimeout = 30 * time.Second

// serve handles one file-transfer connection: validate the init frame's
// token, then run the upload or download flow.
func (s *Server) serve(ctx context.Context, conn net.Conn) {
	defer func() {
		if err := conn.Close(); err != nil {
			s.logger.Debug("closing file transfer connection", zap.Error(err))
		}
	}()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	f, err := netproto.ReadFrameLimit(conn, 4096)
	if err != nil {
		return
	}
	if f.Type != frameInit {
		writeStatus(conn, false, "expected init frame")
		return
	}
	var init initMsg
	if err := decodeJSON(f, &init); err != nil {
		writeStatus(conn, false, "malformed init frame")
		return
	}

	tr, active, err := s.beginTransfer(ctx, init.Token, init.TransferID, conn)
	if err != nil {
		writeStatus(conn, false, err.Error())
		return
	}
	defer s.deactivateTransfer(active)
	s.releasePreauth(conn)
	connDeadline := time.Now().Add(connTimeout)
	_ = conn.SetDeadline(connDeadline)

	if tr.Direction == "upload" {
		err = s.receiveUpload(ctx, conn, tr, active, init.Offset, connDeadline)
	} else {
		err = s.sendDownload(ctx, conn, tr, init.Offset)
	}
	if s.OnTransferComplete != nil {
		result := "ok"
		if err != nil {
			result = "error"
		}
		s.OnTransferComplete(tr.Direction, result)
	}
	if err != nil {
		s.logger.Warn("transfer failed",
			zap.String("transfer_id", tr.ID),
			zap.String("direction", tr.Direction),
			zap.Error(err),
		)
		writeStatus(conn, false, err.Error())
		return
	}
	writeStatus(conn, true, "")
}

// receiveUpload reads chunk frames into a partial file, verifies the declared
// size and SHA-256 digest, and atomically moves the file into place with a
// files-table record. A single-shot upload writes an exclusive, randomly named
// partial that is removed on any failure. A resumable upload (39) appends to
// its deterministic partial from offset and keeps it when the connection is
// interrupted, so the next token for the same file can continue.
func (s *Server) receiveUpload(ctx context.Context, conn net.Conn, tr *transfer, active *activeTransfer, offset int64, connDeadline time.Time) error {
	root, err := s.openBlobRoot()
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	finalPath := blobPath(tr.ChannelID, tr.Folder, tr.Name)
	if err := root.MkdirAll(filepath.Dir(finalPath), 0o700); err != nil {
		return fmt.Errorf("creating channel dir: %w", err)
	}

	resumable := tr.PartialPath != ""
	var (
		f        *os.File
		h        hashWriter
		tmpPath  string
		received int64
	)
	if resumable {
		release, err := s.claimPartial(tr, active)
		if err != nil {
			return err
		}
		defer release()
		tmpPath = tr.PartialPath
		if s.partialIsStoredFile(ctx, tr.ChannelID, tr.Folder, tmpPath) {
			return errors.New("partial upload path is a stored file")
		}
		if f, h, err = openResumablePartial(root, tmpPath, offset); err != nil {
			return err
		}
		received = offset
	} else {
		if offset != 0 {
			return errors.New("this upload cannot resume")
		}
		tmpID, err := randomHex(8)
		if err != nil {
			return fmt.Errorf("generating temporary upload name: %w", err)
		}
		tmpPath = finalPath + legacyPartialPrefix + tmpID
		if f, err = root.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600); err != nil {
			return fmt.Errorf("creating temp file: %w", err)
		}
		h = sha256.New()
	}
	// discard removes the partial; a resumable one also leaves the registry.
	discard := func() {
		_ = root.Remove(tmpPath)
		if resumable {
			s.forgetPartial(tmpPath)
		}
	}
	// On an error path: close the partial, and remove it unless a resumable
	// transfer was merely interrupted.
	fail := func(err error) error {
		if resumable && keepPartialOn(err) {
			_ = f.Sync()
			_ = f.Close()
			return err
		}
		_ = f.Close()
		discard()
		return err
	}

	limiter := s.limiterFor(time.Now())

	for {
		deadline := time.Now().Add(uploadIdleTimeout)
		if connDeadline.Before(deadline) {
			deadline = connDeadline
		}
		_ = conn.SetReadDeadline(deadline)
		frame, err := netproto.ReadFrame(conn)
		if err != nil {
			return fail(fmt.Errorf("reading chunk: %w", err))
		}

		switch frame.Type {
		case frameChunk:
			received += int64(len(frame.Payload))
			if received > tr.Size {
				return fail(errors.New("received more bytes than the declared size"))
			}
			if err := s.withTransferAccess(ctx, tr, func(context.Context) error {
				_, err := f.Write(frame.Payload)
				return err
			}); err != nil {
				return fail(fmt.Errorf("writing file: %w", err))
			}
			_, _ = h.Write(frame.Payload)
			if err := limiter.wait(ctx, len(frame.Payload)); err != nil {
				return fail(err)
			}

		case frameDigest:
			var digest digestMsg
			if err := decodeJSON(frame, &digest); err != nil {
				return fail(errors.New("malformed digest frame"))
			}
			if received != tr.Size {
				return fail(fmt.Errorf("size mismatch: got %d bytes, declared %d", received, tr.Size))
			}
			sum := hex.EncodeToString(h.Sum(nil))
			if digest.SHA256 != sum || (resumable && tr.SHA256 != sum) {
				return fail(errors.New("checksum mismatch"))
			}
			if err := f.Sync(); err != nil {
				_ = f.Close()
				discard()
				return fmt.Errorf("syncing file: %w", err)
			}
			if err := f.Close(); err != nil {
				discard()
				return fmt.Errorf("closing file: %w", err)
			}
			if err := s.withTransferAccess(ctx, tr, func(ctx context.Context) error {
				return s.finalizeUpload(ctx, tr, root, tmpPath, finalPath, received, sum)
			}); err != nil {
				discard()
				return err
			}
			if resumable {
				s.forgetPartial(tmpPath)
			}
			s.logger.Info("upload complete",
				zap.String("transfer_id", tr.ID),
				zap.Int64("channel_id", tr.ChannelID),
				zap.String("folder", tr.Folder),
				zap.String("name", tr.Name),
				zap.Int64("size", received),
			)
			return nil

		default:
			return fail(fmt.Errorf("unexpected frame type %d", frame.Type))
		}
	}
}

// maxFileVersions is how many old versions are kept on overwrite (264).
const maxFileVersions = 3

// finalizeUpload places a verified upload: it rotates the replaced file into
// <name>.v1..v3 (264), hard-links to an identical existing blob when one
// exists (275 dedup), and records the row. A ".vcx" upload is flagged
// (91-135) so the file browser can refuse to hand out a blob it has no key
// for, and its name must be the truncated digest of the ciphertext: that is
// what makes the name unforgeable, so an upload cannot displace the blob an
// older message still points at.
func (s *Server) finalizeUpload(ctx context.Context, tr *transfer, root *os.Root, tmpPath, finalPath string, size int64, sum string) error {
	s.fileOpsMu.Lock()
	defer s.fileOpsMu.Unlock()
	published := false
	if tr.revoked.Load() {
		return ErrAccessRevoked
	}
	if s.channelDeleted(tr.ChannelID) {
		_ = root.Remove(tmpPath)
		return ErrChannelDeleted
	}
	encrypted := isEncryptedAttachment(tr.Name)
	if err := s.checkFileOwner(ctx, tr.ChannelID, tr.Folder, tr.Name); err != nil {
		return err
	}
	if encrypted && tr.Name != sum[:encryptedNameLen]+encryptedSuffix {
		_ = root.Remove(tmpPath)
		return fmt.Errorf("encrypted attachment name is not its content digest")
	}
	// Issuance does not reserve bytes. Another upload may have committed since
	// then, so check fresh usage while holding the publication/mutation lock.
	if err := s.checkUploadCommitQuota(ctx, tr, size, sum); err != nil {
		return err
	}

	// Identical re-upload of the current file: keep the blob, refresh the row.
	if cur, err := s.store.GetFile(ctx, tr.ChannelID, tr.Folder, tr.Name); err == nil && cur.SHA256 == sum {
		if info, statErr := root.Lstat(finalPath); statErr == nil && info.Mode().IsRegular() {
			_ = root.Remove(tmpPath)
			return s.store.AddFile(ctx, store.FileRecord{
				ChannelID: tr.ChannelID, Folder: tr.Folder, Name: tr.Name,
				Size: size, SHA256: sum, Uploader: tr.Uploader, Encrypted: encrypted,
			})
		}
	}

	// An object reachable at finalPath under another spelling belongs to a
	// different metadata record, which the exact-name owner check above did
	// not cover. Refuse before rotation, so a refused upload moves nothing.
	// The exactly named current file is listed exactly and is not an alias.
	if alias, err := blobAlias(root, finalPath, nil); err != nil {
		_ = root.Remove(tmpPath)
		return err
	} else if alias {
		_ = root.Remove(tmpPath)
		return ErrNameAliasesExistingFile
	}

	// Content-derived .vcx names never collide, so rotation can only burn four
	// store lookups on a guaranteed miss for them (91-135).
	if !encrypted {
		// Rotation also replaces/deletes names. A member cannot overwrite
		// somebody else's file by claiming the corresponding base name.
		for version := 1; version <= maxFileVersions; version++ {
			if err := s.checkFileOwner(ctx, tr.ChannelID, tr.Folder, tr.Name+".v"+strconv.Itoa(version)); err != nil {
				return err
			}
		}
		finishRotation, undoRotation, err := s.rotateVersions(ctx, tr, root)
		if err != nil {
			_ = root.Remove(tmpPath)
			return err
		}
		// Until the new file is published, a failure restores every version.
		defer func() {
			if published {
				finishRotation()
			} else {
				undoRotation()
			}
		}()
	}

	// Dedup (275): point at an identical existing blob instead of storing a
	// second copy.
	linked := false
	if ex, err := s.store.FindFileBySHA(ctx, tr.ChannelID, sum, tr.Folder, tr.Name); err == nil && ex != nil {
		existingPath := blobPath(tr.ChannelID, ex.Folder, ex.Name)
		if info, statErr := root.Lstat(existingPath); statErr == nil && info.Mode().IsRegular() {
			if err := root.Link(existingPath, finalPath); err == nil {
				linked = true
				_ = root.Remove(tmpPath)
			}
		}
	}
	if !linked {
		if err := root.Rename(tmpPath, finalPath); err != nil {
			_ = root.Remove(tmpPath)
			return fmt.Errorf("finalizing file: %w", err)
		}
	}
	if err := s.store.AddFile(ctx, store.FileRecord{
		ChannelID: tr.ChannelID, Folder: tr.Folder, Name: tr.Name,
		Size: size, SHA256: sum, Uploader: tr.Uploader, Encrypted: encrypted,
	}); err != nil {
		// Free the name so the rotation undo can put the previous file back.
		_ = root.Remove(finalPath)
		return err
	}
	published = true
	return nil
}

// checkUploadCommitQuota runs under fileOpsMu, before rotation or publication.
// Existing content is already charged; version removal can only reduce usage.
func (s *Server) checkUploadCommitQuota(ctx context.Context, tr *transfer, size int64, sum string) error {
	if s.cfg.ChannelQuotaMB <= 0 && tr.UploaderQuotaMB <= 0 {
		return nil
	}
	channelContent, uploaderContent, err := s.store.FileContentUsage(ctx, tr.ChannelID, sum, tr.Uploader)
	if err != nil {
		return fmt.Errorf("checking content quota usage: %w", err)
	}
	if s.cfg.ChannelQuotaMB > 0 {
		q, err := s.ChannelQuotaState(ctx, tr.ChannelID)
		if err != nil {
			return fmt.Errorf("checking channel quota: %w", err)
		}
		if q.Exceeded(max(0, size-channelContent)) {
			return fmt.Errorf("%w: %d MiB", ErrQuotaExceeded, s.cfg.ChannelQuotaMB)
		}
	}
	if tr.UploaderQuotaMB > 0 {
		q, err := s.UploaderQuotaState(ctx, tr.Uploader, tr.UploaderQuotaMB)
		if err != nil {
			return fmt.Errorf("checking upload quota: %w", err)
		}
		if q.Exceeded(max(0, size-uploaderContent)) {
			return fmt.Errorf("%w: %d MiB", ErrUploaderQuotaExceeded, tr.UploaderQuotaMB)
		}
	}
	return nil
}

// rotateVersions makes room for a new upload of tr.Name: name -> .v1 -> .v2
// ... and the oldest version is set aside. Every step moves the row and the
// blob together; a step that cannot (disk check, row rename or blob rename
// failure) aborts the upload. undo restores every completed step; finish
// drops the set-aside oldest version and runs only once the new file is
// published, so a failed upload changes no retained version.
func (s *Server) rotateVersions(ctx context.Context, tr *transfer, root *os.Root) (finish, undo func(), err error) {
	type step struct{ from, to string }
	var done []step
	folder := tr.Folder
	rollbackCtx := func() (context.Context, context.CancelFunc) {
		// The upload's context may already be canceled (client gone).
		return context.WithTimeout(context.WithoutCancel(ctx), rotateRollbackTimeout)
	}
	undo = func() {
		rctx, cancel := rollbackCtx()
		defer cancel()
		for i := len(done) - 1; i >= 0; i-- {
			st := done[i]
			if err := rotateRename(root, blobPath(tr.ChannelID, folder, st.to), blobPath(tr.ChannelID, folder, st.from)); err != nil && !errors.Is(err, os.ErrNotExist) {
				s.logger.Error("version rotate undo (disk) failed", zap.String("from", st.to), zap.Error(err))
			}
			if err := s.store.RenameFile(rctx, tr.ChannelID, folder, st.to, folder, st.from); err != nil {
				s.logger.Error("version rotate undo (db) failed", zap.String("from", st.to), zap.Error(err))
			}
		}
		done = nil
	}
	rename := func(from, to string) error {
		if _, err := s.store.GetFile(ctx, tr.ChannelID, folder, from); errors.Is(err, store.ErrFileNotFound) {
			return nil // nothing to rotate
		} else if err != nil {
			return fmt.Errorf("rotating %q: %w", from, err)
		}
		oldPath := blobPath(tr.ChannelID, folder, from)
		newPath := blobPath(tr.ChannelID, folder, to)
		if info, err := root.Lstat(oldPath); err == nil && !info.Mode().IsRegular() {
			return fmt.Errorf("rotating %q: not a regular file", from)
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("rotating %q: %w", from, err)
		}
		if err := s.store.RenameFile(ctx, tr.ChannelID, folder, from, folder, to); err != nil {
			return fmt.Errorf("rotating %q to %q: %w", from, to, err)
		}
		if err := rotateRename(root, oldPath, newPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			// Row and blob must name the same place, or the sweep would later
			// delete the "unreferenced" blob and lose this version.
			rctx, cancel := rollbackCtx()
			defer cancel()
			if rbErr := s.store.RenameFile(rctx, tr.ChannelID, folder, to, folder, from); rbErr != nil {
				s.logger.Error("version rotate rollback (db) failed", zap.String("from", from), zap.Error(rbErr))
			}
			return fmt.Errorf("rotating %q to %q: %w", from, to, err)
		}
		done = append(done, step{from, to})
		return nil
	}
	fail := func(err error) (func(), func(), error) {
		undo()
		return nil, nil, err
	}

	// The oldest version is set aside under a unique name, not deleted yet.
	oldest := tr.Name + ".v" + strconv.Itoa(maxFileVersions)
	aside := ""
	if _, err := s.store.GetFile(ctx, tr.ChannelID, folder, oldest); err == nil {
		suffix, err := randomHex(8)
		if err != nil {
			return nil, nil, err
		}
		aside = oldest + ".rotating-" + suffix
		if err := rename(oldest, aside); err != nil {
			return fail(err)
		}
	} else if !errors.Is(err, store.ErrFileNotFound) {
		return nil, nil, fmt.Errorf("rotating %q: %w", oldest, err)
	}
	for v := maxFileVersions - 1; v >= 1; v-- {
		if err := rename(tr.Name+".v"+strconv.Itoa(v), tr.Name+".v"+strconv.Itoa(v+1)); err != nil {
			return fail(err)
		}
	}
	if err := rename(tr.Name, tr.Name+".v1"); err != nil {
		return fail(err)
	}
	finish = func() {
		if aside == "" {
			return
		}
		rctx, cancel := rollbackCtx()
		defer cancel()
		if err := s.store.DeleteFile(rctx, tr.ChannelID, folder, aside); err != nil {
			s.logger.Warn("dropping oldest version (db) failed", zap.String("name", aside), zap.Error(err))
			return // the sweep must not see a blob without its row
		}
		_ = root.Remove(blobPath(tr.ChannelID, folder, aside))
	}
	return finish, undo, nil
}

// rotateRollbackTimeout bounds rotation rollback and cleanup, which run on a
// context detached from the (possibly canceled) upload.
const rotateRollbackTimeout = 10 * time.Second

// rotateRename renames a blob during version rotation; tests replace it to
// simulate a rename the OS refuses (a file held open on Windows).
var rotateRename = func(root *os.Root, oldPath, newPath string) error {
	return root.Rename(oldPath, newPath)
}

// sendDownload streams the file in chunk frames followed by the digest
// frame carrying its SHA-256. offset > 0 resumes an interrupted download
// (259): the prefix the client already holds is folded into the hash but not
// re-sent, so the digest still covers the whole file and a resumed download
// is verified exactly as strictly as a fresh one.
func (s *Server) sendDownload(ctx context.Context, conn net.Conn, tr *transfer, offset int64) (retErr error) {
	root, err := s.openBlobRoot()
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	f, err := openRegularBlob(root, blobPath(tr.ChannelID, tr.Folder, tr.Name))
	if err != nil {
		return fmt.Errorf("opening file: %w", err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("closing file: %w", err))
		}
	}()

	h := sha256.New()
	if offset > 0 {
		if offset > tr.Size {
			return fmt.Errorf("resume offset %d is past the end of %s (%d bytes)", offset, tr.Name, tr.Size)
		}
		if _, err := io.CopyN(h, f, offset); err != nil {
			return fmt.Errorf("hashing resumed prefix: %w", err)
		}
	}
	limiter := s.limiterFor(time.Now())
	buf := make([]byte, chunkSize)

	for {
		n, readErr := f.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			if err := s.withTransferAccess(ctx, tr, func(context.Context) error {
				if tr.Principal != nil {
					_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				}
				return netproto.WriteFrame(conn, &netproto.Frame{Type: frameChunk, Payload: chunk})
			}); err != nil {
				return fmt.Errorf("writing chunk: %w", err)
			}
			_, _ = h.Write(chunk)
			if err := limiter.wait(ctx, n); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fmt.Errorf("reading file: %w", readErr)
		}
	}

	if err := s.withTransferAccess(ctx, tr, func(context.Context) error {
		if tr.Principal != nil {
			_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		}
		return writeJSON(conn, frameDigest, digestMsg{SHA256: hex.EncodeToString(h.Sum(nil))})
	}); err != nil {
		return fmt.Errorf("writing digest: %w", err)
	}
	s.logger.Info("download complete",
		zap.String("transfer_id", tr.ID),
		zap.Int64("channel_id", tr.ChannelID),
		zap.String("name", tr.Name),
		zap.Int64("resume_offset", offset),
	)
	return nil
}
