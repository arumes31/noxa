package main

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
)

func updateCleanupFixture(t *testing.T) (string, string, *verifiedUpdateBackup, [sha256.Size]byte) {
	t.Helper()
	dir := t.TempDir()
	target, previous := filepath.Join(dir, "noxa.exe"), filepath.Join(dir, ".noxa-previous-owned.exe")
	for path, data := range map[string]string{target: "verified new", previous: "verified old"} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	backup, err := captureUpdateBackup(target, previous, sha256.Sum256([]byte("verified old")))
	if err != nil {
		t.Fatal(err)
	}
	return target, previous, backup, sha256.Sum256([]byte("verified new"))
}

func TestUpdateSuccessCleanupOnlyRemovesOwnedBackup(t *testing.T) {
	target, previous, backup, hash := updateCleanupFixture(t)
	unrelated := filepath.Join(filepath.Dir(target), ".noxa-previous-other-session.exe")
	profile := filepath.Join(filepath.Dir(target), "profile.fixture")
	for _, name := range []string{unrelated, profile} {
		if err := os.WriteFile(name, []byte("untouched"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := backup.remove(target, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(previous); !os.IsNotExist(err) {
		t.Fatalf("successful update retained backup: %v", err)
	}
	for _, name := range []string{unrelated, profile} {
		if got, err := os.ReadFile(name); err != nil || string(got) != "untouched" {
			t.Fatalf("unowned file changed: %s %v", name, err)
		}
	}
	if err := backup.remove(target, hash); err != nil {
		t.Fatalf("already cleaned backup: %v", err)
	}
}

func TestUpdateSuccessCleanupRetainsChangedFiles(t *testing.T) {
	for _, scenario := range []string{"new contents", "old contents", "old identity", "missing target", "symlink"} {
		t.Run(scenario, func(t *testing.T) {
			target, previous, backup, hash := updateCleanupFixture(t)
			switch scenario {
			case "new contents":
				if err := os.WriteFile(target, []byte("other update"), 0600); err != nil {
					t.Fatal(err)
				}
			case "old contents":
				if err := os.WriteFile(previous, []byte("other backup"), 0600); err != nil {
					t.Fatal(err)
				}
			case "old identity", "symlink":
				// Preserve the original inode to prohibit file-ID reuse.
				if err := os.Rename(previous, previous+".saved"); err != nil {
					t.Fatal(err)
				}
				if scenario == "symlink" {
					if err := os.Symlink(previous+".saved", previous); err != nil {
						t.Skipf("symlinks unavailable: %v", err)
					}
				} else if err := os.WriteFile(previous, []byte("verified old"), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing target":
				if err := os.Remove(target); err != nil {
					t.Fatal(err)
				}
			}
			if err := backup.remove(target, hash); err == nil {
				t.Fatal("changed transaction was accepted for cleanup")
			}
			if _, err := os.Lstat(previous); err != nil {
				t.Fatalf("unverified backup was removed: %v", err)
			}
		})
	}
}

func TestUpdateSuccessCleanupCapturesOnlyVerifiedOwnedPaths(t *testing.T) {
	target, previous, _, _ := updateCleanupFixture(t)
	expected := sha256.Sum256([]byte("verified old"))
	for _, path := range []string{target, filepath.Base(previous), filepath.Join(filepath.Dir(target), "other.exe")} {
		if _, err := captureUpdateBackup(target, path, expected); err == nil {
			t.Fatalf("accepted unowned path: %s", path)
		}
	}
	if _, err := captureUpdateBackup(target, previous, sha256.Sum256([]byte("wrong"))); err == nil {
		t.Fatal("accepted wrong release hash")
	}
}

func TestUpdateSuccessCleanupIncludesIndependentLegacySnapshot(t *testing.T) {
	target, previous, backup, hash := updateCleanupFixture(t)
	snapshot := filepath.Join(filepath.Dir(target), ".noxa-previous-bootstrap-independent.exe")
	if err := os.WriteFile(snapshot, []byte("verified old"), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := captureUpdateBackup(target, snapshot, backup.hash)
	if err != nil {
		t.Fatal(err)
	}
	// The final supervisor owns both exact files after the legacy host and
	// temporary compatibility frontend have exited and normal startup passes.
	finishUpdateBackupCleanup(target, hash, second, nil)
	finishUpdateBackupCleanup(target, hash, backup, nil)
	for _, path := range []string{previous, snapshot} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("legacy backup retained: %s %v", path, err)
		}
	}
}
