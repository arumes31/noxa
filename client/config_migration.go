package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// migrateLegacyConfig moves the complete profile before any startup code opens
// settings, identities, trust pins or logs. An existing noxa profile wins.
func migrateLegacyConfig(base string) error {
	destination := filepath.Join(base, "noxa")
	if info, err := os.Stat(destination); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("profile is not a directory: %s", destination)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	legacy := filepath.Join(base, "voicx")
	info, err := os.Stat(legacy)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("legacy profile is not a directory: %s", legacy)
	}
	return os.Rename(legacy, destination)
}
