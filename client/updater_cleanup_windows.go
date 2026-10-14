//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func removeUpdateBackupFile(backup *verifiedUpdateBackup) error {
	name, err := windows.UTF16PtrFromString(backup.path)
	if err != nil {
		return err
	}
	// Hold the file against writes and renames while verifying and deleting.
	// Delete this verified handle, not a pathname that could be substituted.
	handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.DELETE, windows.FILE_SHARE_READ, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(handle), backup.path)
	defer func() { _ = file.Close() }()
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("update backup became a reparse point")
	}
	if err := backup.verify(file); err != nil {
		return err
	}
	// FILE_DISPOSITION_INFO has one BOOLEAN. Deletion completes on Close.
	disposition := byte(1)
	if err := windows.SetFileInformationByHandle(handle, windows.FileDispositionInfo, &disposition, 1); err != nil {
		return err
	}
	return file.Close()
}

func retryableUpdateCleanup(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_ACCESS_DENIED)
}
