// Encrypted chat attachments, bounded previews, and safe local saves.
package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// attachmentStorageName derives the upload name from the CIPHERTEXT. Content
// addressing is what makes per-file keys viable: files has UNIQUE (channel_id,
// name) and same-name uploads rotate through .v1..v3, so with a random key per
// file a second "image.png" would leave every older message holding a key that
// fails Poly1305 against the new blob. Random keys make two uploads of the
// same bytes distinct, so a chat attachment never collides and never rotates.
func attachmentStorageName(blob []byte) string {
	sum := sha256.Sum256(blob)
	return hex.EncodeToString(sum[:])[:32] + ".vcx"
}

// safeDisplayName strips the two characters that would break the token
// grammar. The name is only ever shown, never used to address storage.
func safeDisplayName(name string) string {
	return strings.NewReplacer("]", "_", "#", "_").Replace(name)
}

// parseFileRef splits a [file:<capture>] body token. It is total: zero or one
// separators means a legacy plain reference, which keeps pre-encryption
// messages rendering. chat-ui.js mirrors this exactly.
func parseFileRef(capture string) (storage, keyB64, name string) {
	i := strings.Index(capture, "#")
	if i < 0 {
		return capture, "", capture // legacy [file:photo.png]
	}
	j := strings.Index(capture[i+1:], "#")
	if j < 0 {
		return capture, "", capture // malformed -> treat as plain
	}
	j += i + 1
	return capture[:i], capture[i+1 : j], capture[j+1:]
}

// UploadChatAttachment seals data with a fresh random 32-byte key, uploads
// the ciphertext under a content-derived name, and returns the body token
// "[file:<storage>#<keyB64>#<display>]" for embedding in the chat message.
// The key only ever exists inside the (encrypted) message body, so the file
// gets exactly the protection the message text gets.
func (a *App) UploadChatAttachment(channelID int64, name, dataBase64 string) (string, error) {
	cm, err := a.requireCM()
	if err != nil {
		return "", err
	}
	return uploadChatAttachmentWith(cm, channelID, name, dataBase64)
}

// UploadChatAttachmentForTab captures the server before preparing encrypted bytes.
func (a *App) UploadChatAttachmentForTab(tabID string, channelID int64, name, dataBase64 string) (string, error) {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return "", err
	}
	return uploadChatAttachmentWith(cm, channelID, name, dataBase64)
}

func uploadChatAttachmentWith(cm *connManager, channelID int64, name, dataBase64 string) (string, error) {
	data, err := io.ReadAll(io.LimitReader(
		base64.NewDecoder(base64.StdEncoding, strings.NewReader(dataBase64)),
		int64(maxChatAttachmentBytes)+1,
	))
	if err != nil {
		return "", errors.New("invalid file data")
	}
	if len(data) > maxChatAttachmentBytes {
		return "", errors.New("attachment exceeds 25 MiB limit")
	}
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		return "", err
	}
	blob, err := sealFile(data, key)
	if err != nil {
		return "", err
	}
	storage := attachmentStorageName(blob)
	if err := ftPutBytesWith(cm, channelID, storage, blob); err != nil {
		return "", err
	}
	return "[file:" + storage + "#" + base64.StdEncoding.EncodeToString(key[:]) +
		"#" + safeDisplayName(name) + "]", nil
}

const maxChatAttachmentBytes = 25 << 20

// Sealed attachments include a header plus one length-prefixed AES-GCM record
// per plaintext chunk. They may therefore be slightly larger on the wire while
// still representing at most 25 MiB after decryption.
var maxSealedChatAttachmentBytes = sealedAttachmentSizeForPlaintext(maxChatAttachmentBytes)

const maxInlineAttachmentBase64Bytes = 8 << 20

var errChatAttachmentPreviewTooLarge = errors.New("attachment is too large to preview")

// chatAttachmentBytesForCM fetches and opens one attachment with the captured
// manager. A native save dialog may suspend the UI; retaining this manager
// prevents a concurrent tab switch from redirecting the later download to a
// different server.
func (a *App) chatAttachmentBytesForCM(cm *connManager, channelID int64, storage, keyB64 string) ([]byte, error) {
	// A caller that hands over the raw capture instead of its parts is parsed
	// here too, so a token can never be mistaken for a file name.
	if keyB64 == "" && strings.Contains(storage, "#") {
		storage, keyB64, _ = parseFileRef(storage)
	}
	if storage == "" {
		return nil, errors.New("invalid attachment storage")
	}
	var key [32]byte
	if keyB64 != "" {
		raw, err := base64.StdEncoding.DecodeString(keyB64)
		if err != nil || len(raw) != len(key) {
			return nil, errors.New("invalid attachment key")
		}
		copy(key[:], raw)
	}
	var (
		blob []byte
		err  error
	)
	if a.chatAttachmentFetch != nil {
		blob, err = a.chatAttachmentFetch(cm, channelID, storage)
	} else {
		maxWireBytes := maxChatAttachmentBytes
		if keyB64 != "" {
			maxWireBytes = maxSealedChatAttachmentBytes
		}
		blob, err = a.ftGetBytesForCMWithLimit(cm, channelID, storage, int64(maxWireBytes))
	}
	if err != nil {
		return nil, err
	}
	if keyB64 == "" && len(blob) > maxChatAttachmentBytes {
		return nil, errors.New("attachment exceeds 25 MiB limit")
	}
	if keyB64 == "" {
		return blob, nil
	}
	if bytes.HasPrefix(blob, attachmentGCMHeader) {
		if len(blob) > maxSealedChatAttachmentBytes {
			return nil, errors.New("attachment exceeds 25 MiB limit")
		}
	} else if len(blob) > maxChatAttachmentBytes {
		// The larger encrypted wire allowance is exclusively for the chunked
		// GCM format. Legacy secretbox attachments retain their former cap.
		return nil, errors.New("attachment exceeds 25 MiB limit")
	}
	plain, err := openFileLimited(blob, key, maxChatAttachmentBytes)
	if err != nil {
		return nil, err
	}
	return plain, nil
}

func (a *App) chatAttachmentBytes(channelID int64, storage, keyB64 string) ([]byte, error) {
	cm, err := a.requireCM()
	if err != nil {
		return nil, err
	}
	return a.chatAttachmentBytesForCM(cm, channelID, storage, keyB64)
}

// DownloadChatAttachment fetches <storage> and unseals it with keyB64,
// returning bounded base64 plaintext for inline preview. An empty keyB64
// downloads a plain legacy [file:photo.png] reference.
func (a *App) DownloadChatAttachment(channelID int64, storage, keyB64 string) (string, error) {
	plain, err := a.chatAttachmentBytes(channelID, storage, keyB64)
	return inlineChatAttachment(plain, err)
}

// DownloadChatAttachmentForTab binds an inline preview to its source server.
func (a *App) DownloadChatAttachmentForTab(tabID string, channelID int64, storage, keyB64 string) (string, error) {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return "", err
	}
	plain, err := a.chatAttachmentBytesForCM(cm, channelID, storage, keyB64)
	return inlineChatAttachment(plain, err)
}

func inlineChatAttachment(plain []byte, err error) (string, error) {
	if err != nil {
		return "", err
	}
	if base64.StdEncoding.EncodedLen(len(plain)) > maxInlineAttachmentBase64Bytes {
		return "", errChatAttachmentPreviewTooLarge
	}
	return base64.StdEncoding.EncodeToString(plain), nil
}

var errChatAttachmentDestinationExists = errors.New("the selected file already exists")

// chatAttachmentLink is replaceable in tests that model a filename appearing
// after the dialog/preflight check. os.Link is an atomic no-overwrite publish
// on a same-directory temporary file, including on Windows NTFS.
var chatAttachmentLink = os.Link

func safeAttachmentFilename(defaultName string) string {
	name := strings.TrimSpace(defaultName)
	if i := strings.LastIndexAny(name, "/\\"); i >= 0 {
		name = name[i+1:]
	}
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	if name == "" || name == "." || name == ".." {
		return "attachment"
	}
	return name
}

func chatAttachmentDestination(dest string) (string, error) {
	dest = filepath.Clean(dest)
	base := filepath.Base(dest)
	if dest == "." || base == "." || base == string(filepath.Separator) {
		return "", errors.New("invalid attachment destination")
	}
	info, err := os.Stat(filepath.Dir(dest))
	if err != nil {
		return "", fmt.Errorf("opening attachment destination directory: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("attachment destination parent is not a directory")
	}
	if _, err := os.Lstat(dest); err == nil {
		return "", errChatAttachmentDestinationExists
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("checking attachment destination: %w", err)
	}
	return dest, nil
}

func (a *App) chooseChatAttachmentPath(defaultName string) (string, error) {
	options := wailsRuntime.SaveDialogOptions{
		Title:           "Save chat attachment",
		DefaultFilename: safeAttachmentFilename(defaultName),
	}
	if a.chatAttachmentSaveDialog != nil {
		return a.chatAttachmentSaveDialog(a.ctx, options)
	}
	if a.ctx == nil {
		return "", errors.New("save dialog is unavailable")
	}
	return wailsRuntime.SaveFileDialog(a.ctx, options)
}

func writeChatAttachmentAtomically(dest string, data []byte) (err error) {
	dest, err = chatAttachmentDestination(dest)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(dest), "."+filepath.Base(dest)+".tmp-")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer func() {
		if temp != "" {
			_ = os.Remove(temp)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if n, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	} else if n != len(data) {
		_ = f.Close()
		return io.ErrShortWrite
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := chatAttachmentLink(temp, dest); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return errChatAttachmentDestinationExists
		}
		return fmt.Errorf("publishing attachment: %w", err)
	}
	if err := os.Remove(temp); err != nil {
		return fmt.Errorf("cleaning attachment temporary file: %w", err)
	}
	temp = ""
	return nil
}

// SaveChatAttachment opens the native save dialog before downloading. It
// decrypts only in Go and publishes a fully verified new destination without
// replacing an existing file. Cancel returns an empty path and no error.
func (a *App) SaveChatAttachment(channelID int64, storage, keyB64, defaultName string) (string, error) {
	// Capture the active manager before the native dialog. A tab switch while
	// the dialog is open must not redirect the selected attachment to another
	// server; no network operation occurs until after the user chooses a path.
	cm := a.cmLoad()
	return a.saveChatAttachmentWith(cm, channelID, storage, keyB64, defaultName)
}

// SaveChatAttachmentForTab rejects stale tabs before opening the native dialog.
func (a *App) SaveChatAttachmentForTab(tabID string, channelID int64, storage, keyB64, defaultName string) (string, error) {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return "", err
	}
	return a.saveChatAttachmentWith(cm, channelID, storage, keyB64, defaultName)
}

func (a *App) saveChatAttachmentWith(cm *connManager, channelID int64, storage, keyB64, defaultName string) (string, error) {
	dest, err := a.chooseChatAttachmentPath(defaultName)
	if err != nil || dest == "" {
		return dest, err
	}
	dest, err = chatAttachmentDestination(dest)
	if err != nil {
		return "", err
	}
	if cm == nil {
		return "", errors.New("not connected")
	}
	plain, err := a.chatAttachmentBytesForCM(cm, channelID, storage, keyB64)
	if err != nil {
		return "", err
	}
	write := a.chatAttachmentWrite
	if write == nil {
		write = writeChatAttachmentAtomically
	}
	if err := write(dest, plain); err != nil {
		return "", err
	}
	return dest, nil
}
