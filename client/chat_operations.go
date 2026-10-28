// Persisted composer operation IDs (new-protocol direct sends). The composer
// gives every user intent one stable operation ID so a retry after an
// uncertain send resolves the original instead of sending twice. Those IDs
// used to live only in frontend memory; a failed operation's ID is now kept
// beside the outbox (sealed with its key, bound to identity + server +
// pinned certificate), so a manual retry of the same text to the same
// destination after a restart still resolves the original operation. A
// successful send forgets it; entries expire after a day.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	composerOperationTTL = 24 * time.Hour
	composerOperationMax = 256
)

type composerOperation struct {
	ID string `json:"id"`
	At int64  `json:"at"`
}

func composerOperationKey(scope, target, text string, replyToID int64) string {
	sum := sha256.Sum256([]byte("noxa/composer-op/v1\x00" + scope + "\x00" + target + "\x00" + strconv.FormatInt(replyToID, 10) + "\x00" + text))
	return hex.EncodeToString(sum[:])
}

func (s chatOutboxStore) operationsPath() string {
	return strings.TrimSuffix(s.path, ".sealed") + ".ops.sealed"
}

func (s chatOutboxStore) loadOperations(now time.Time) (map[string]composerOperation, error) {
	out := map[string]composerOperation{}
	raw, err := os.ReadFile(s.operationsPath())
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	plain, err := openScope(string(raw), s.key)
	if err != nil || json.Unmarshal([]byte(plain), &out) != nil {
		return map[string]composerOperation{}, nil // unreadable: start over (only retries are affected)
	}
	for k, op := range out {
		if now.Sub(time.UnixMilli(op.At)) > composerOperationTTL || !validOperationID(op.ID) {
			delete(out, k)
		}
	}
	return out, nil
}

func (s chatOutboxStore) saveOperations(ops map[string]composerOperation) error {
	for len(ops) > composerOperationMax {
		oldest := ""
		for k, op := range ops {
			if oldest == "" || op.At < ops[oldest].At {
				oldest = k
			}
		}
		delete(ops, oldest)
	}
	raw, err := json.Marshal(ops)
	if err != nil {
		return err
	}
	sealed, err := sealScope(string(raw), s.key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	return writePrivateFileAtomic(s.operationsPath(), []byte(sealed))
}

// ComposerOperationForTab returns the persisted operation ID of an earlier
// failed new-protocol send of exactly this text to this destination, or "".
func (a *App) ComposerOperationForTab(tabID, scope, target, text string, replyToID int64) string {
	cm, err := a.requireTabCM(tabID)
	if err != nil || scope != "direct" || !cm.ratchetConfigured() {
		return ""
	}
	store, err := a.outboxStore(cm)
	if err != nil {
		return ""
	}
	outboxMu.Lock()
	defer outboxMu.Unlock()
	ops, err := store.loadOperations(time.Now())
	if err != nil {
		return ""
	}
	return ops[composerOperationKey(scope, target, text, replyToID)].ID
}

// recordComposerOperation keeps (failed) or forgets (sent) an operation ID.
func (a *App) recordComposerOperation(cm *connManager, scope, target, text string, replyToID int64, operationID string, sent bool) {
	if scope != "direct" || !cm.ratchetConfigured() || !validOperationID(operationID) {
		return
	}
	store, err := a.outboxStore(cm)
	if err != nil {
		return
	}
	outboxMu.Lock()
	defer outboxMu.Unlock()
	now := time.Now()
	ops, err := store.loadOperations(now)
	if err != nil {
		return
	}
	key := composerOperationKey(scope, target, text, replyToID)
	if sent {
		if _, ok := ops[key]; !ok {
			return
		}
		delete(ops, key)
	} else {
		ops[key] = composerOperation{ID: operationID, At: now.UnixMilli()}
	}
	_ = store.saveOperations(ops)
}
