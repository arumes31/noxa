//go:build darwin

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

func renameClientUpdateRecoveryNoReplace(source, destination string) error {
	if err := unix.RenamexNp(source, destination, unix.RENAME_EXCL); err != nil {
		return &os.LinkError{Op: "rename", Old: source, New: destination, Err: err}
	}
	return nil
}
