// Orphaned chat uploads (504): ciphertext that reached the server while the
// message referencing it was never published. Every successful upload is
// tracked locally until its message is sent; whatever remains is deleted in a
// controlled sweep, never anything a sent message still references.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	uploadOrphanUploaded      = "uploaded"       // token returned, publish not attempted
	uploadOrphanPublishFailed = "publish_failed" // the server rejected the message
	uploadOrphanUncertain     = "uncertain"      // delivery was interrupted; the message may exist
)

// uploadOrphanGrace keeps a freshly uploaded file out of the sweep while its
// message send may still be in flight in this process.
const uploadOrphanGrace = 2 * time.Minute

// uploadOrphanRetention bounds how long an uncertain entry (or one the server
// refuses to delete) stays tracked before it is forgotten.
const uploadOrphanRetention = 7 * 24 * time.Hour

const maxUploadOrphans = 500

type uploadOrphan struct {
	Server    string `json:"server"`
	ChannelID int64  `json:"channel_id"`
	Storage   string `json:"storage"`
	Name      string `json:"name,omitempty"`
	CreatedAt int64  `json:"created_at"`
	State     string `json:"state"`
	Error     string `json:"error,omitempty"`
	// Session identifies the client process that uploaded the file. The
	// upload queue of that process may still hold the token (composing, or a
	// failed send waiting for Retry), so only entries from earlier processes
	// are ever swept.
	Session string `json:"session,omitempty"`
}

// uploadOrphanSession is this process's identity for the ledger.
var uploadOrphanSession = newUploadOrphanSession()

func newUploadOrphanSession() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return time.Now().Format(time.RFC3339Nano)
	}
	return hex.EncodeToString(raw[:])
}

// UploadOrphanCleanup is the sweep summary shown as "N orphaned uploads
// cleaned up". Kept counts uncertain entries left untouched on purpose.
type UploadOrphanCleanup struct {
	Removed int `json:"removed"`
	Failed  int `json:"failed"`
	Kept    int `json:"kept"`
}

var uploadOrphanMu sync.Mutex

func (a *App) uploadOrphanPath() string {
	base := a.settingsFile()
	if base == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(base), "upload-orphans.json")
}

func loadUploadOrphans(path string) ([]uploadOrphan, error) {
	root, name, err := openParentRoot(path)
	if errors.Is(err, os.ErrNotExist) {
		return []uploadOrphan{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	raw, err := root.ReadFile(name)
	if errors.Is(err, os.ErrNotExist) {
		return []uploadOrphan{}, nil
	}
	if err != nil {
		return nil, err
	}
	var entries []uploadOrphan
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, errors.New("invalid upload orphan file")
	}
	return entries, nil
}

func saveUploadOrphans(path string, entries []uploadOrphan) error {
	raw, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writePrivateFileAtomic(path, raw)
}

// uploadOrphanServer namespaces entries by canonical destination and pinned
// certificate so a sweep on one server never touches another's files.
func uploadOrphanServer(cm *connManager) string {
	if cm == nil {
		return ""
	}
	cm.mu.Lock()
	addr, fingerprint := cm.addr, cm.fingerprint
	cm.mu.Unlock()
	canonical, err := canonicalServerDestination(addr)
	if err != nil || canonical == "" {
		canonical = addr
	}
	if canonical == "" {
		return ""
	}
	return canonical + "|" + fingerprint
}

var fileRefTokenPattern = regexp.MustCompile(`\[file:([^\]]+)\]`)

// fileRefStorages lists the storage names referenced by [file:…] tokens in a
// message body; legacy plain references are included unchanged.
func fileRefStorages(text string) []string {
	var storages []string
	for _, match := range fileRefTokenPattern.FindAllStringSubmatch(text, -1) {
		if storage, _, _ := parseFileRef(match[1]); storage != "" {
			storages = append(storages, storage)
		}
	}
	return storages
}

var uncertainPublishPattern = regexp.MustCompile(`(?i)timeout|timed out|deadline|connection closed|not connected|offline|EOF|interrupted|identity changed|connection reset|broken pipe`)

// uncertainPublishFailure reports send errors after which the server may
// still have stored the message. Such uploads are never swept automatically.
func uncertainPublishFailure(message string) bool {
	return uncertainPublishPattern.MatchString(message)
}

func (a *App) mutateUploadOrphans(mutate func(entries []uploadOrphan) []uploadOrphan) {
	path := a.uploadOrphanPath()
	if path == "" {
		return
	}
	uploadOrphanMu.Lock()
	defer uploadOrphanMu.Unlock()
	entries, err := loadUploadOrphans(path)
	if err != nil {
		entries = []uploadOrphan{}
	}
	next := mutate(entries)
	if len(next) > maxUploadOrphans {
		next = next[len(next)-maxUploadOrphans:]
	}
	_ = saveUploadOrphans(path, next)
}

// recordUploadedAttachment tracks a token the frontend now holds but has not
// yet published.
func (a *App) recordUploadedAttachment(cm *connManager, channelID int64, token, name string) {
	server := uploadOrphanServer(cm)
	storages := fileRefStorages(token)
	if server == "" || len(storages) == 0 {
		return
	}
	now := time.Now().UnixMilli()
	a.mutateUploadOrphans(func(entries []uploadOrphan) []uploadOrphan {
		for _, storage := range storages {
			entries = append(entries, uploadOrphan{Server: server, ChannelID: channelID, Storage: storage, Name: name, CreatedAt: now, State: uploadOrphanUploaded, Session: uploadOrphanSession})
		}
		return entries
	})
}

// resolvePublishedAttachments settles tracked uploads referenced by text once
// its send completed: success forgets them, a definite rejection marks them
// for the sweep, and an interrupted delivery parks them as uncertain.
func (a *App) resolvePublishedAttachments(cm *connManager, text, failure string) {
	if !strings.Contains(text, "[file:") {
		return
	}
	server := uploadOrphanServer(cm)
	storages := fileRefStorages(text)
	if server == "" || len(storages) == 0 {
		return
	}
	a.mutateUploadOrphans(func(entries []uploadOrphan) []uploadOrphan {
		return settleUploadOrphans(entries, server, storages, failure)
	})
}

func settleUploadOrphans(entries []uploadOrphan, server string, storages []string, failure string) []uploadOrphan {
	referenced := map[string]bool{}
	for _, storage := range storages {
		referenced[storage] = true
	}
	kept := entries[:0:0]
	for _, entry := range entries {
		if entry.Server != server || !referenced[entry.Storage] {
			kept = append(kept, entry)
			continue
		}
		if failure == "" {
			continue // published: the message owns the file now
		}
		entry.Error = failure
		if uncertainPublishFailure(failure) {
			entry.State = uploadOrphanUncertain
		} else {
			entry.State = uploadOrphanPublishFailed
		}
		kept = append(kept, entry)
	}
	return kept
}

// sweepableUploadOrphans selects the entries of server that may be deleted
// now: never-published uploads past the grace window and definite publish
// rejections, but only from earlier client processes. Entries recorded by
// the running process (session) stay: its upload queue may still publish or
// retry with that token. Uncertain entries are excluded.
func sweepableUploadOrphans(entries []uploadOrphan, server, session string, now time.Time) []uploadOrphan {
	var selected []uploadOrphan
	cutoff := now.Add(-uploadOrphanGrace).UnixMilli()
	for _, entry := range entries {
		if entry.Server != server || entry.Session == session {
			continue
		}
		switch entry.State {
		case uploadOrphanUploaded:
			if entry.CreatedAt <= cutoff {
				selected = append(selected, entry)
			}
		case uploadOrphanPublishFailed:
			selected = append(selected, entry)
		}
	}
	return selected
}

// expireUploadOrphans forgets entries older than the retention window.
func expireUploadOrphans(entries []uploadOrphan, now time.Time) []uploadOrphan {
	cutoff := now.Add(-uploadOrphanRetention).UnixMilli()
	kept := entries[:0:0]
	for _, entry := range entries {
		if entry.CreatedAt > cutoff {
			kept = append(kept, entry)
		}
	}
	return kept
}

func uploadOrphanKey(entry uploadOrphan) string {
	return entry.Server + "\x00" + entry.Storage
}

// CleanupOrphanUploadsForTab deletes this server's orphaned uploads through
// the regular file deletion path (uploader-gated server side) and reports the
// outcome. A file the server already lacks counts as cleaned.
func (a *App) CleanupOrphanUploadsForTab(tabID string) (UploadOrphanCleanup, error) {
	var result UploadOrphanCleanup
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return result, err
	}
	return a.cleanupOrphanUploads(cm, func(entry uploadOrphan) string {
		return cm.fileDelete(entry.ChannelID, "", entry.Storage)
	}, time.Now()), nil
}

func (a *App) cleanupOrphanUploads(cm *connManager, remove func(uploadOrphan) string, now time.Time) UploadOrphanCleanup {
	var result UploadOrphanCleanup
	path := a.uploadOrphanPath()
	server := uploadOrphanServer(cm)
	if path == "" || server == "" {
		return result
	}
	uploadOrphanMu.Lock()
	entries, err := loadUploadOrphans(path)
	if err != nil {
		entries = []uploadOrphan{}
	}
	entries = expireUploadOrphans(entries, now)
	selected := sweepableUploadOrphans(entries, server, uploadOrphanSession, now)
	uploadOrphanMu.Unlock()
	deleted := map[string]bool{}
	failures := map[string]string{}
	for _, entry := range selected {
		message := remove(entry)
		if message == "" || strings.Contains(strings.ToLower(message), "not found") || strings.Contains(strings.ToLower(message), "no such file") {
			deleted[uploadOrphanKey(entry)] = true
			result.Removed++
		} else {
			failures[uploadOrphanKey(entry)] = message
			result.Failed++
		}
	}
	uploadOrphanMu.Lock()
	defer uploadOrphanMu.Unlock()
	entries, err = loadUploadOrphans(path)
	if err != nil {
		return result
	}
	kept := entries[:0:0]
	for _, entry := range entries {
		key := uploadOrphanKey(entry)
		if deleted[key] {
			continue
		}
		if message, failed := failures[key]; failed {
			entry.Error = message
		}
		if entry.Server == server && entry.State == uploadOrphanUncertain {
			result.Kept++
		}
		kept = append(kept, entry)
	}
	_ = saveUploadOrphans(path, expireUploadOrphans(kept, now))
	return result
}
