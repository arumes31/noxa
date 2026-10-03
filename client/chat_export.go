// Chat transcript assembly and passphrase-encrypted export.
package main

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/nacl/secretbox"

	"noxa/internal/netproto"
)

// chatExportDefaultMax bounds a full-history export. It is far above a
// search's cap on purpose: an export that quietly stops after 2000 messages is
// the very "only what the view happened to hold" gap 125 exists to close.
const chatExportDefaultMax = 100000

// ChatExportResult is a rendered full-channel transcript, ready to hand to
// ExportChat or ExportChatEncrypted.
type ChatExportResult struct {
	Text          string `json:"text"`
	Messages      int    `json:"messages"`
	Undecryptable int    `json:"undecryptable"`
	Complete      bool   `json:"complete"`
}

// ChatExportHistory pages and decrypts a scope's ENTIRE stored history and
// renders it oldest-first, instead of exporting the window the chat view
// happens to hold (125). It runs on the search pager, so export and search can
// never disagree about which generations they could open. Progress lands on
// the "chatexport:progress" event as the running scanned count.
func (a *App) ChatExportHistory(channelID int64, maxMessages int) (ChatExportResult, error) {
	m, err := a.requireCM()
	if err != nil {
		return ChatExportResult{}, err
	}
	return chatExportHistoryWith(m, "", channelID, maxMessages)
}

// ChatExportHistoryForTab captures the confirmed source before the first page.
func (a *App) ChatExportHistoryForTab(tabID, requestID string, channelID int64, maxMessages int) (ChatExportResult, error) {
	if len(requestID) == 0 || len(requestID) > 128 {
		return ChatExportResult{}, fmt.Errorf("invalid chat scan request ID")
	}
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return ChatExportResult{}, err
	}
	return chatExportHistoryWith(m, requestID, channelID, maxMessages)
}

func chatExportHistoryWith(m *connManager, requestID string, channelID int64, maxMessages int) (ChatExportResult, error) {
	if maxMessages <= 0 {
		maxMessages = chatExportDefaultMax
	}
	var lines []string
	undecryptable := 0
	scanned, complete, err := chatScanWith(m, channelID, maxMessages, "chatexport:progress", requestID, func(m netproto.ChatHistoryEntry) {
		if !m.Deleted && !m.EncVerified {
			undecryptable++
		}
		lines = append(lines, chatExportLine(m))
	})
	if err != nil {
		return ChatExportResult{}, err
	}
	// chatScanWith walks newest-first; a transcript reads oldest-first.
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	if !complete || undecryptable > 0 {
		// The notice rides INSIDE the file: a caller that writes Text straight
		// out must not be able to produce a partial transcript that looks whole.
		reach := "complete"
		if !complete {
			reach = "TRUNCATED at the export limit"
		}
		lines = append([]string{fmt.Sprintf(
			"# noXa export — %d messages, %d unreadable (no key), %s",
			scanned, undecryptable, reach)}, lines...)
	}
	text := ""
	if len(lines) > 0 {
		text = strings.Join(lines, "\n") + "\n"
	}
	return ChatExportResult{
		Text: text, Messages: scanned, Undecryptable: undecryptable, Complete: complete,
	}, nil
}

// chatExportLine renders one transcript line. A body this client could not
// open keeps its placeholder rather than vanishing, so a gap in the export is
// visible as a gap instead of looking like a quiet channel.
func chatExportLine(m netproto.ChatHistoryEntry) string {
	body := m.Body
	if m.Deleted {
		body = "(deleted)"
	}
	if m.EditedAt != 0 {
		body += " (edited)"
	}
	name := m.FromNickname
	if name == "" {
		name = m.FromUniqueID
	}
	return "[" + time.Unix(m.SentAt, 0).Format("2006-01-02 15:04:05") + "] " + name + ": " + body
}

// DMExportHistory renders a stored DM conversation into the same transcript
// shape as ChatExportHistory, so exporting a PM tab reaches the whole local
// log rather than the loaded window (122/125).
func (a *App) DMExportHistory(peer string) (ChatExportResult, error) {
	msgs, err := a.DMHistoryLoad(peer)
	if err != nil {
		return ChatExportResult{}, err
	}
	return dmExportMessages(msgs), nil
}

func dmExportMessages(msgs []DMEntry) ChatExportResult {
	lines := make([]string, 0, len(msgs))
	for _, m := range msgs {
		lines = append(lines, chatExportLine(netproto.ChatHistoryEntry{
			FromUniqueID: m.FromUniqueID,
			FromNickname: m.FromNickname,
			Body:         m.Body,
			SentAt:       m.SentAt,
		}))
	}
	text := ""
	if len(lines) > 0 {
		text = strings.Join(lines, "\n") + "\n"
	}
	return ChatExportResult{Text: text, Messages: len(msgs), Complete: true}
}

// ExportChat saves text to a user-chosen file via the native save dialog.
// Returns "" on success or when the user cancels, or the error.
func (a *App) ExportChat(defaultName, contents string) string {
	path, err := wailsRuntime.SaveFileDialog(a.ctx, wailsRuntime.SaveDialogOptions{
		DefaultFilename: defaultName,
		Filters: []wailsRuntime.FileFilter{
			{DisplayName: "Text / HTML", Pattern: "*.txt;*.html"},
		},
	})
	if err != nil || path == "" {
		return "" // cancelled
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		return err.Error()
	}
	return ""
}

// Encrypted export container: magic || salt || nonce || secretbox. The key is
// derived from the passphrase with argon2id, so the file is worth no more than
// the passphrase — but a plaintext export of an encrypted chat is worth far
// less than that (125).
// Keep the format identifier stable so existing encrypted exports remain readable.
var exportMagic = []byte("VOICXCHAT1")

const (
	exportSaltLen = 16
	// argon2id parameters: one pass over 64 MiB, 4 lanes. Interactive cost on
	// a desktop, far past a GPU-friendly KDF.
	exportArgonTime    = 1
	exportArgonMemory  = 64 * 1024
	exportArgonThreads = 4
)

// sealExport builds the encrypted export container.
func sealExport(contents, passphrase string) ([]byte, error) {
	if passphrase == "" {
		return nil, errors.New("a passphrase is required for an encrypted export")
	}
	salt := make([]byte, exportSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	var key [32]byte
	copy(key[:], argon2.IDKey([]byte(passphrase), salt, exportArgonTime, exportArgonMemory, exportArgonThreads, 32))

	out := make([]byte, 0, len(exportMagic)+exportSaltLen+24+len(contents)+secretbox.Overhead)
	out = append(out, exportMagic...)
	out = append(out, salt...)
	out = append(out, nonce[:]...)
	return secretbox.Seal(out, []byte(contents), &nonce, &key), nil
}

// openExport reverses sealExport (the import half and the round-trip test).
func openExport(blob []byte, passphrase string) (string, error) {
	head := len(exportMagic) + exportSaltLen + 24
	if len(blob) < head || !bytes.Equal(blob[:len(exportMagic)], exportMagic) {
		return "", errors.New("not a noxa encrypted chat export")
	}
	salt := blob[len(exportMagic) : len(exportMagic)+exportSaltLen]
	var nonce [24]byte
	copy(nonce[:], blob[len(exportMagic)+exportSaltLen:head])
	var key [32]byte
	copy(key[:], argon2.IDKey([]byte(passphrase), salt, exportArgonTime, exportArgonMemory, exportArgonThreads, 32))

	plain, ok := secretbox.Open(nil, blob[head:], &nonce, &key)
	if !ok {
		return "", errors.New("wrong passphrase or corrupt export")
	}
	return string(plain), nil
}

// ExportChatEncrypted saves the transcript passphrase-encrypted as
// .noxachat, so an export of an encrypted chat does not become the plaintext
// copy the whole design exists to prevent. Returns "" on success or cancel.
func (a *App) ExportChatEncrypted(defaultName, contents, passphrase string) (string, error) {
	blob, err := sealExport(contents, passphrase)
	if err != nil {
		return "", err
	}
	path, err := wailsRuntime.SaveFileDialog(a.ctx, wailsRuntime.SaveDialogOptions{
		DefaultFilename: defaultName,
		Filters: []wailsRuntime.FileFilter{
			{DisplayName: "Encrypted chat export", Pattern: "*.noxachat"},
		},
	})
	if err != nil || path == "" {
		return "", nil // cancelled
	}
	if err := os.WriteFile(path, blob, 0o600); err != nil {
		return "", err
	}
	return path, nil
}
