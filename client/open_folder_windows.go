package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func openLocalFolder(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("download folder is no longer a directory")
	}
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	folder, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, folder, nil, nil, 1)
}
