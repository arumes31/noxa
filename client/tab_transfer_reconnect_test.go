package main

import (
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
)

func TestReconnectRetainsTransferReplayAndCompletedFolder(t *testing.T) {
	app := newTabApp(t)
	id, source := app.newTab()
	otherID, other := app.newTab()
	dir := t.TempDir()
	source.cm.rememberDownload("done", filepath.Join(dir, "original", "file.txt"))
	other.cm.rememberDownload("done", filepath.Join(dir, "other-server", "file.txt"))
	progress := []ftProgress{
		{ID: "done", Direction: "download", Name: "file.txt", Status: "done", Transferred: 100, Total: 100},
		{ID: "retry", Direction: "download", Name: "retry.txt", Status: "canceled", Transferred: 30, Total: 100},
		{ID: "interrupted", Direction: "download", Name: "partial.txt", Status: "active", Transferred: 45, Total: 100, BytesPerSec: 500, Resumed: 10},
	}
	for _, row := range progress {
		source.recordTransfer(row)
	}
	candidate := &tabState{cm: newConnManager(t.Context())}
	app.tabsMu.Lock()
	candidate.inheritTransferHistory(source)
	app.tabs[id] = candidate
	app.tabsMu.Unlock()
	got := activateTransferSnapshot(t, app, id)
	if len(got) != 3 || !reflect.DeepEqual(got[:2], progress[:2]) {
		t.Fatalf("reconnect discarded terminal download rows: %+v", got)
	}
	interrupted := got[2]
	if interrupted.ID != "interrupted" || interrupted.Status != "canceled" || interrupted.Error == "" || interrupted.BytesPerSec != 0 || interrupted.Transferred != 45 || interrupted.Total != 100 || interrupted.Resumed != 10 {
		t.Fatalf("disconnected transfer remained active or lost its resume position: %+v", interrupted)
	}
	if source.transfers[2].Status != "active" {
		t.Fatal("replacement mutated the old manager's snapshot")
	}
	// The retained path remains native-owned and isolated from another server,
	// and from later writes by the now-unpublished old manager.
	source.cm.rememberDownload("done", filepath.Join(dir, "late-old", "file.txt"))
	var opened string
	oldReveal := revealDownloadFolder
	revealDownloadFolder = func(path string) error { opened = path; return nil }
	t.Cleanup(func() { revealDownloadFolder = oldReveal })
	if err := app.OpenDownloadFolderForTab(id, "done"); err != "" || opened != filepath.Join(dir, "original") {
		t.Fatalf("completed folder unavailable after reconnect: opened=%q err=%q", opened, err)
	}
	if err := app.OpenDownloadFolderForTab(otherID, "done"); err == "" {
		t.Fatal("retained download path escaped the active server tab")
	}
	if len(candidate.cm.transfers.entries) != 0 {
		t.Fatal("replacement inherited old transfer sockets")
	}
}

func TestReconnectKeepsTransferHistoryBoundedAndIndependent(t *testing.T) {
	source := &tabState{cm: newConnManager(t.Context())}
	dir := t.TempDir()
	for i := range 130 {
		id := fmt.Sprint(i)
		source.recordTransfer(ftProgress{ID: id, Status: "active", Transferred: int64(i)})
		source.cm.rememberDownload(id, filepath.Join(dir, id, "file.txt"))
	}
	candidate := &tabState{cm: newConnManager(t.Context())}
	candidate.inheritTransferHistory(source)
	if len(candidate.transfers) != recentTransferCount || candidate.transfers[0].ID != "110" || candidate.transfers[19].ID != "129" {
		t.Fatalf("reconnected history is not bounded to recent transfers: %+v", candidate.transfers)
	}
	if len(candidate.cm.downloads.order) != 100 || len(candidate.cm.downloads.folders) != 100 {
		t.Fatal("download folder history lost its existing bound")
	}
	candidate.cm.rememberDownload("new", filepath.Join(dir, "new", "file.txt"))
	if _, present := source.cm.downloads.folders["new"]; present || source.cm.downloads.order[0] != "30" {
		t.Fatal("new manager's download history aliases the old manager")
	}
}
