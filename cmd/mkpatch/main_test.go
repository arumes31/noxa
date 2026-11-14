package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"noxa/internal/updatepatch"
)

func TestMkpatchOutputAppliesToNewFile(t *testing.T) {
	dir := t.TempDir()
	oldFile := bytes.Repeat([]byte("old executable section "), 50000)
	newFile := append(append([]byte(nil), oldFile[:len(oldFile)/2]...), "changed code"...)
	newFile = append(newFile, oldFile[len(oldFile)/2:]...)
	oldPath, newPath, out := filepath.Join(dir, "old"), filepath.Join(dir, "new"), filepath.Join(dir, "p.patch")
	if err := os.WriteFile(oldPath, oldFile, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, newFile, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"-old", oldPath, "-new", newPath, "-out", out}); err != nil {
		t.Fatal(err)
	}
	patch, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	got, err := updatepatch.Apply(oldFile, bytes.NewReader(patch), int64(len(newFile)))
	if err != nil || !bytes.Equal(got, newFile) {
		t.Fatalf("applying mkpatch output: %v", err)
	}
	if err := run([]string{"-old", oldPath}); err == nil {
		t.Fatal("accepted missing arguments")
	}
}
