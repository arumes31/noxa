//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// MoveFileW rejects an existing destination atomically; os.Rename instead uses
// MoveFileEx with REPLACE_EXISTING. All recovery paths are on the same volume.
func renameClientUpdateRecoveryNoReplace(source, destination string) error {
	fromPath, err := recoveryWindowsExtendedPath(source)
	if err != nil {
		return err
	}
	toPath, err := recoveryWindowsExtendedPath(destination)
	if err != nil {
		return err
	}
	from, err := windows.UTF16PtrFromString(fromPath)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(toPath)
	if err != nil {
		return err
	}
	if err := windows.MoveFile(from, to); err != nil {
		return &os.LinkError{Op: "rename", Old: source, New: destination, Err: err}
	}
	return nil
}

// Keep Go's long-path support when calling the no-replace native primitive.
func recoveryWindowsExtendedPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(absolute, `\\?\`) {
		return absolute, nil
	}
	if strings.HasPrefix(absolute, `\\`) {
		return `\\?\UNC\` + absolute[2:], nil
	}
	return `\\?\` + absolute, nil
}
