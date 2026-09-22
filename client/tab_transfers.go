package main

import "encoding/json"

const recentTransferCount = 20

// recordTransfer retains one update per ID and the latest finished transfers.
// Active entries remain until completion, proportional to active work. The
// caller holds tabsMu, so activation can capture a consistent replay batch.
func (ts *tabState) recordTransfer(progress ftProgress) {
	if progress.ID == "" {
		return
	}
	kept := ts.transfers[:0]
	for _, previous := range ts.transfers {
		if previous.ID != progress.ID {
			kept = append(kept, previous)
		}
	}
	kept = append(kept, progress)
	ts.transfers = kept
	finished := 0
	for _, p := range ts.transfers {
		if p.Status != "active" {
			finished++
		}
	}
	kept = ts.transfers[:0]
	for _, p := range ts.transfers {
		if p.Status != "active" && finished > recentTransferCount {
			finished--
			continue
		}
		kept = append(kept, p)
	}
	clear(ts.transfers[len(kept):])
	ts.transfers = kept
}

// transferSnapshot copies state into an immutable journal payload before
// tabsMu is released. Empty snapshots clear stale frontend transfer rows too.
func (ts *tabState) transferSnapshot() journalEntry {
	data, err := json.Marshal(struct {
		Transfers []ftProgress `json:"transfers"`
	}{Transfers: append([]ftProgress{}, ts.transfers...)})
	if err != nil {
		// ftProgress contains only JSON scalar fields; retain a clearing
		// snapshot if its representation ever changes to permit marshal errors.
		return journalEntry{name: "ft_snapshot", payload: `{"transfers":[]}`}
	}
	return journalEntry{name: "ft_snapshot", payload: string(data)}
}
