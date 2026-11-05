package filetransfer

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func caseInsensitiveRoot(t *testing.T) *os.Root {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "probe"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "PROBE")); err != nil {
		t.Skip("temporary directory is case-sensitive")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

func TestBlobAliasDetectsDifferentlySpelledOccupant(t *testing.T) {
	root := caseInsensitiveRoot(t)
	if err := root.MkdirAll("7", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := root.WriteFile(filepath.Join("7", "Report.pdf"), []byte("victim"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias, err := blobAlias(root, filepath.Join("7", "report.pdf"), nil)
	if err != nil || !alias {
		t.Fatalf("case variant alias=%v err=%v, want alias", alias, err)
	}
	if alias, err := blobAlias(root, filepath.Join("7", "Report.pdf"), nil); err != nil || alias {
		t.Fatalf("exact spelling alias=%v err=%v", alias, err)
	}
	if alias, err := blobAlias(root, filepath.Join("7", "other.pdf"), nil); err != nil || alias {
		t.Fatalf("missing file alias=%v err=%v", alias, err)
	}
}

func TestMoveBlobRejectsAliasButAllowsCaseOnlySelfRename(t *testing.T) {
	root := caseInsensitiveRoot(t)
	if err := root.MkdirAll("7", 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"Victim.txt": "victim", "mine.txt": "mine"} {
		if err := root.WriteFile(filepath.Join("7", name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := moveBlob(root, filepath.Join("7", "mine.txt"), filepath.Join("7", "victim.txt")); !errors.Is(err, ErrNameAliasesExistingFile) {
		t.Fatalf("move onto alias err=%v", err)
	}
	if body, _ := root.ReadFile(filepath.Join("7", "Victim.txt")); string(body) != "victim" {
		t.Fatalf("victim blob replaced: %q", body)
	}
	if err := moveBlob(root, filepath.Join("7", "mine.txt"), filepath.Join("7", "Mine.txt")); err != nil {
		t.Fatalf("case-only self rename: %v", err)
	}
}

func TestBlobAliasDetectsDifferentlySpelledFolder(t *testing.T) {
	root := caseInsensitiveRoot(t)
	if err := root.MkdirAll(filepath.Join("7", "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := root.WriteFile(filepath.Join("7", "docs", "report.pdf"), []byte("victim"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The file name is listed exactly inside the folder, but the folder itself
	// is reached through a case variant whose record lives elsewhere.
	if alias, err := blobAlias(root, filepath.Join("7", "DOCS", "report.pdf"), nil); err != nil || !alias {
		t.Fatalf("folder alias over existing file alias=%v err=%v, want alias", alias, err)
	}
	if alias, err := blobAlias(root, filepath.Join("7", "DOCS", "new.pdf"), nil); err != nil || !alias {
		t.Fatalf("folder alias for new file alias=%v err=%v, want alias", alias, err)
	}
	if alias, err := blobAlias(root, filepath.Join("7", "docs", "new.pdf"), nil); err != nil || alias {
		t.Fatalf("exact folder alias=%v err=%v", alias, err)
	}
	if alias, err := blobAlias(root, filepath.Join("7", "other", "new.pdf"), nil); err != nil || alias {
		t.Fatalf("missing folder alias=%v err=%v", alias, err)
	}
	if err := root.WriteFile(filepath.Join("7", "mine.txt"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := moveBlob(root, filepath.Join("7", "mine.txt"), filepath.Join("7", "DOCS", "report.pdf")); !errors.Is(err, ErrNameAliasesExistingFile) {
		t.Fatalf("move into folder alias err=%v", err)
	}
	if body, _ := root.ReadFile(filepath.Join("7", "docs", "report.pdf")); string(body) != "victim" {
		t.Fatalf("victim blob replaced: %q", body)
	}
}
