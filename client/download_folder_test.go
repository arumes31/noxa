package main

import (
	"fmt"
	"path/filepath"
	"testing"
)

func TestDownloadFolderIsRecordedAndTabScoped(t *testing.T) {
	first, second := &connManager{}, &connManager{}
	dir := t.TempDir()
	first.rememberDownload("same", filepath.Join(dir, "first", "file.txt"))
	second.rememberDownload("same", filepath.Join(dir, "second", "file.txt"))
	app := &App{activeID: "first", tabs: map[string]*tabState{
		"first": {cm: first}, "second": {cm: second},
	}}
	app.cmStore(first)
	var opened string
	old := revealDownloadFolder
	revealDownloadFolder = func(path string) error { opened = path; return nil }
	t.Cleanup(func() { revealDownloadFolder = old })
	if err := app.OpenDownloadFolderForTab("second", "same"); err == "" {
		t.Fatal("opened a background tab's destination")
	}
	if err := app.OpenDownloadFolderForTab("first", "unfinished"); err == "" || opened != "" {
		t.Fatal("opened an uncompleted or unknown transfer")
	}
	if err := app.OpenDownloadFolderForTab("first", "same"); err != "" {
		t.Fatal(err)
	}
	if opened != filepath.Join(dir, "first") {
		t.Fatalf("opened %q", opened)
	}
}

func TestDownloadFolderHistoryIsBounded(t *testing.T) {
	cm := &connManager{}
	for i := 0; i < 120; i++ {
		cm.rememberDownload(fmt.Sprint(i), filepath.Join(t.TempDir(), "file"))
	}
	if len(cm.downloads.folders) != 100 || len(cm.downloads.order) != 100 {
		t.Fatal("completed download history is not bounded")
	}
	if _, exists := cm.downloads.folders["0"]; exists {
		t.Fatal("oldest transfer was not evicted")
	}
}
