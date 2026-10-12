//go:build windows

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUpdateHandoffProcessHandleChecksCreationAndBoundsWait(t *testing.T) {
	identity := updateProcessIdentity{PID: uint32(os.Getpid())}
	process, err := openUpdateProcess(identity)
	if err != nil {
		t.Fatal(err)
	}
	defer process.close()
	if process.identity.Created == 0 || !process.alive() || !filepath.IsAbs(process.image) {
		t.Fatal("did not retain the actual live process identity")
	}
	identity.Created = process.identity.Created + 1
	stale, err := openUpdateProcess(identity)
	if stale != nil {
		stale.close()
	}
	if err == nil {
		t.Fatal("accepted recycled process identity")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if err := process.wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait was not bounded: %v", err)
	}
	if !process.alive() {
		t.Fatal("read-only wait changed the live process")
	}
}

func TestUpdateHandoffBackupMustBeSameDirectoryAndOwnedName(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "noxa.exe")
	valid := filepath.Join(dir, ".noxa-previous-owned.exe")
	if !updaterOwnedBackup(target, valid) {
		t.Fatal("rejected updater-owned backup")
	}
	for _, previous := range []string{target, ".noxa-previous-relative.exe", filepath.Join(dir, "other.exe"), filepath.Join(dir, "subdir", ".noxa-previous-owned.exe")} {
		if updaterOwnedBackup(target, previous) {
			t.Fatalf("accepted unrelated backup %q", previous)
		}
	}
}

func TestUpdateHandoffCopiesIndependentVerifiedRollback(t *testing.T) {
	dir := t.TempDir()
	source, target := filepath.Join(dir, ".noxa-previous-parent.exe"), filepath.Join(dir, "noxa.exe")
	content := []byte("disposable authenticated fixture bytes, never executable")
	if err := os.WriteFile(source, content, 0600); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(content)
	first, identity, err := copyVerifiedUpdateBackup(source, target, hash)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := copyVerifiedUpdateBackup(source, target, hash)
	if err != nil {
		t.Fatal(err)
	}
	if sameUpdatePath(first, source) || sameUpdatePath(first, second) || !updaterOwnedBackup(target, first) {
		t.Fatal("rollback copy did not receive an exclusive updater-owned name")
	}
	for _, name := range []string{source, first, second} {
		actual, err := hashUpdateExecutable(name)
		if err != nil || actual != hash {
			t.Fatalf("changed rollback bytes at %s: %v", name, err)
		}
	}
	removeVerifiedUpdateSnapshot(first, identity)
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Fatalf("owned snapshot not cleaned: %v", err)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatal("original legacy parent's backup was touched")
	}
}

func TestUpdateHandoffCopyRejectsHashMismatchWithoutLeavingSnapshot(t *testing.T) {
	dir := t.TempDir()
	source, target := filepath.Join(dir, ".noxa-previous-parent.exe"), filepath.Join(dir, "noxa.exe")
	if err := os.WriteFile(source, []byte("changed bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := copyVerifiedUpdateBackup(source, target, sha256.Sum256([]byte("authenticated bytes"))); err == nil {
		t.Fatal("accepted mismatched rollback bytes")
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Name() != filepath.Base(source) {
		t.Fatalf("failed copy left owned snapshot: %v", files)
	}
}

func TestUpdateHandoffSnapshotCleanupRejectsReplacedIdentity(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, ".noxa-previous-snapshot.exe")
	if err := os.WriteFile(name, []byte("ours"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := file.Stat()
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	// Retaining the old inode under another name prevents file-ID reuse.
	if err := os.Rename(name, name+".retained"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	removeVerifiedUpdateSnapshot(name, identity)
	content, err := os.ReadFile(name)
	if err != nil || string(content) != "replacement" {
		t.Fatalf("removed an unowned replacement: %v", err)
	}
}

func TestUpdateHandoffPlanRejectsChangedFilesAndIdentities(t *testing.T) {
	dir := t.TempDir()
	target, previous := filepath.Join(dir, "noxa.exe"), filepath.Join(dir, ".noxa-previous-test.exe")
	if err := os.WriteFile(target, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(previous, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	plan := updateSupervisorPlan{Version: 1, Target: target, Previous: previous, NewHash: sha256.Sum256([]byte("new")), OldHash: sha256.Sum256([]byte("old")), Parent: updateProcessIdentity{PID: 1, Created: 2}, Browser: updateProcessIdentity{PID: 3, Created: 4}}
	if err := plan.validate(target); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*updateSupervisorPlan)
	}{
		{"version", func(p *updateSupervisorPlan) { p.Version = 2 }},
		{"zero creation", func(p *updateSupervisorPlan) { p.Parent.Created = 0 }},
		{"same process", func(p *updateSupervisorPlan) { p.Browser.PID = p.Parent.PID }},
		{"unverified legacy", func(p *updateSupervisorPlan) { p.LegacyParent = &updateProcessIdentity{PID: 5, Created: 6} }},
		{"changed target", func(p *updateSupervisorPlan) { p.NewHash[0] ^= 1 }},
		{"changed backup", func(p *updateSupervisorPlan) { p.OldHash[0] ^= 1 }},
		{"unowned backup", func(p *updateSupervisorPlan) { p.Previous = filepath.Join(dir, "other.exe") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := plan
			test.mutate(&changed)
			if err := changed.validate(target); err == nil {
				t.Fatal("accepted invalid supervisor plan")
			}
		})
	}
	if err := plan.validate(filepath.Join(dir, "other.exe")); err == nil {
		t.Fatal("accepted a different supervisor executable")
	}
}

func TestUpdateHandoffLegacyCompatibilityRequiresPublishedDigest(t *testing.T) {
	decoded, err := hex.DecodeString(legacyUpdateExecutableSHA256)
	if err != nil {
		t.Fatal(err)
	}
	verified := [sha256.Size]byte(decoded)
	if !legacyUpdateHash(verified) {
		t.Fatal("signed release digest rejected")
	}
	verified[0] ^= 1
	if legacyUpdateHash(verified) {
		t.Fatal("unverified executable accepted for legacy policy")
	}
}

func TestUpdateHandoffRejectsMalformedNativePlanBeforeProcessActions(t *testing.T) {
	for _, input := range []string{"{}", `{"unexpected":true}`, `{} {}`, strings.Repeat("x", 65537)} {
		if err := runUpdateSupervisor(strings.NewReader(input)); err == nil {
			t.Fatal("malformed native plan accepted")
		}
	}
}

func TestUpdateHandoffRestoredRetryIsBoundedAndRechecksHash(t *testing.T) {
	for _, mode := range []string{"transient", "persistent", "changed", "still running", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "restored.fixture")
			content := []byte("authenticated original")
			if err := os.WriteFile(path, content, 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			err := retryRestoredUpdate(ctx, path, sha256.Sum256(content), func(ctx context.Context, _ time.Duration) error { return ctx.Err() }, func(context.Context, string) error {
				calls++
				switch mode {
				case "transient":
					if calls == 3 {
						return nil
					}
				case "changed":
					if err := os.WriteFile(path, []byte("unrelated replacement"), 0600); err != nil {
						t.Fatal(err)
					}
				case "still running":
					return &restoredUpdateStillRunningError{cause: errors.New("startup"), stop: errors.New("owned child not reaped")}
				case "deadline":
					cancel()
				}
				return errors.New("temporary profile lock")
			})
			if mode == "transient" {
				if err != nil || calls != 3 {
					t.Fatalf("transient recovery: calls=%d err=%v", calls, err)
				}
				return
			}
			if err == nil {
				t.Fatal("failed restoration reported ready")
			}
			want := 1
			if mode == "persistent" {
				want = 6
			}
			if calls != want {
				t.Fatalf("unsafe or unbounded attempts: got %d want %d", calls, want)
			}
		})
	}
}

func TestUpdateHandoffRestoredRetrySpansNaturalBrowserExitWindow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restored.fixture")
	content := []byte("verified original")
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Duration(0)
	var pauses []time.Duration
	calls := 0
	err := retryRestoredUpdate(t.Context(), path, sha256.Sum256(content), func(_ context.Context, delay time.Duration) error {
		pauses = append(pauses, delay)
		elapsed += delay
		return nil
	}, func(context.Context, string) error {
		calls++
		if elapsed >= 5*time.Second {
			return nil
		}
		return errors.New("previous browser has not exited yet")
	})
	if err != nil || calls != 4 || elapsed != 7*time.Second {
		t.Fatalf("did not span realistic natural exit: attempts=%d virtualElapsed=%s pauses=%v err=%v", calls, elapsed, pauses, err)
	}
}
