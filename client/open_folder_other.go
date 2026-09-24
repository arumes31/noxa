//go:build !windows

package main

import (
	"context"
	"os/exec"
	"runtime"
)

func openLocalFolder(path string) error {
	// The destination is an absolute locally recorded folder, passed as one
	// argument to a fixed executable without invoking a command shell.
	cmd := exec.CommandContext(context.Background(), "xdg-open", path)
	if runtime.GOOS == "darwin" {
		cmd = exec.CommandContext(context.Background(), "open", path)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
