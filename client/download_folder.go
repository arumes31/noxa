package main

import (
	"path/filepath"
	"slices"
	"sync"
)

type downloadHistory struct {
	mu      sync.Mutex
	folders map[string]string
	order   []string
}

var revealDownloadFolder = openLocalFolder

func (cm *connManager) rememberDownload(id, destination string) {
	path, err := filepath.Abs(destination)
	if err != nil || id == "" {
		return
	}
	cm.downloads.mu.Lock()
	defer cm.downloads.mu.Unlock()
	if cm.downloads.folders == nil {
		cm.downloads.folders = make(map[string]string)
	}
	cm.downloads.order = slices.DeleteFunc(cm.downloads.order, func(old string) bool { return old == id })
	cm.downloads.order = append(cm.downloads.order, id)
	cm.downloads.folders[id] = filepath.Dir(path)
	if len(cm.downloads.order) > 100 {
		delete(cm.downloads.folders, cm.downloads.order[0])
		cm.downloads.order = cm.downloads.order[1:]
	}
}

// OpenDownloadFolderForTab accepts only a completed transfer ID. Neither the
// server nor the WebView supplies the path passed to the operating system.
func (a *App) OpenDownloadFolderForTab(tabID, transferID string) string {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return err.Error()
	}
	cm.downloads.mu.Lock()
	folder := cm.downloads.folders[transferID]
	cm.downloads.mu.Unlock()
	if folder == "" {
		return "completed download is no longer available"
	}
	if err := revealDownloadFolder(folder); err != nil {
		return err.Error()
	}
	return ""
}
