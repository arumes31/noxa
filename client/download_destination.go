package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// A partial file belongs to one download across all server tabs. Concurrent
// writers can each verify their network bytes while corrupting shared disk
// content, so reserve both paths before opening or hashing the partial.
var activeDownloadDestinations sync.Map

func reserveDownloadDestination(path string) (func(), error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve download destination: %w", err)
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return nil, fmt.Errorf("resolve download directory: %w", err)
	}
	key := filepath.Join(dir, filepath.Base(abs))
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	keys := []string{key, key + partSuffix}
	for index, candidate := range keys {
		if _, busy := activeDownloadDestinations.LoadOrStore(candidate, struct{}{}); busy {
			for _, reserved := range keys[:index] {
				activeDownloadDestinations.Delete(reserved)
			}
			return nil, errors.New("another download is already writing to this destination")
		}
	}
	return func() {
		for _, reserved := range keys {
			activeDownloadDestinations.Delete(reserved)
		}
	}, nil
}
