package main

import (
	"maps"
	"slices"
)

// inheritTransferHistory preserves local transfer history when an authenticated
// replacement takes over the same server tab. The caller holds tabsMu and the
// candidate is not published yet. Live transfer sockets remain with source.
func (candidate *tabState) inheritTransferHistory(source *tabState) {
	for _, progress := range source.transfers {
		if progress.Status == "active" {
			progress.Status = "canceled"
			progress.Error = "connection lost during transfer"
			progress.BytesPerSec = 0
		}
		candidate.recordTransfer(progress)
	}

	// Download history never holds its mutex while acquiring tabsMu. Clone its
	// containers so late completions on the old manager cannot change this tab.
	source.cm.downloads.mu.Lock()
	candidate.cm.downloads.folders = maps.Clone(source.cm.downloads.folders)
	candidate.cm.downloads.order = slices.Clone(source.cm.downloads.order)
	source.cm.downloads.mu.Unlock()
}
