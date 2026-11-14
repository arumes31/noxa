package main

import (
	"bytes"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
)

// Bytes that differ from the signed manifest hash (an asset swapped after
// verification) are refused before anything is written next to the target.
func TestApplyClientUpdateRefusesBytesOtherThanSigned(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		t.Fatal(err)
	}
	before, err := hashUpdateExecutable(exe)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyClientUpdate(bytes.NewReader([]byte("swapped")), sha256.Sum256([]byte("signed"))); err == nil {
		t.Fatal("asset with a different hash was applied")
	}
	if clientUpdateRecovery.pending != nil {
		t.Fatal("refused update left a pending recovery")
	}
	if after, err := hashUpdateExecutable(exe); err != nil || after != before {
		t.Fatal("target changed", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(exe), "."+filepath.Base(exe)+".new")); !os.IsNotExist(err) {
		t.Fatal("staged binary written", err)
	}
}
