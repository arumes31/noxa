//go:build !windows

package main

import (
	"context"
	"os/exec"
	"runtime"
)

func openLocalFolder(path string) error {
	program := "xdg-open"
	if runtime.GOOS == "darwin" {
		program = "open"
	}
	// The destination is an absolute locally recorded folder, passed as one
	// argument to a fixed executable without invoking a command shell.
	cmd := exec.CommandContext(context.Background(), program, path)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
