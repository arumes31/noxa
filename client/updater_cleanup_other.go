//go:build !windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func removeUpdateBackupFile(backup *verifiedUpdateBackup) error {
	root, err := os.OpenRoot(filepath.Dir(backup.path))
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	name := filepath.Base(backup.path)
	file, err := root.Open(name)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	if err := backup.verify(file); err != nil {
		return err
	}
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || !os.SameFile(backup.identity, info) {
		return fmt.Errorf("update backup identity changed")
	}
	return root.Remove(name)
}

func retryableUpdateCleanup(error) bool { return false }
