package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"testing"
)

func activateTransferSnapshot(t *testing.T, app *App, tabID string) []ftProgress {
	t.Helper()
	var names []string
	var raw string
	app.eventEmit = func(name string, payload any) {
		names = append(names, name)
		if name == "ft_snapshot" {
			raw, _ = payload.(string)
		}
	}
	app.activate(tabID)
	var snapshot struct {
		Transfers []ftProgress `json:"transfers"`
	}
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		t.Fatalf("missing/invalid transfer snapshot %q: %v", raw, err)
	}
	reset, state, done := -1, -1, -1
	for i, name := range names {
		switch name {
		case "tab_reset":
			reset = i
		case "ft_snapshot":
			state = i
		case "tab_replay_done":
			done = i
		}
	}
	if reset < 0 || state <= reset || done <= state {
		t.Fatalf("incorrect replay order: %v", names)
	}
	return snapshot.Transfers
}

func TestTransferProgressReplayRetainsLatestStatePerTab(t *testing.T) {
	app := newTabApp(t)
	a, _ := app.newTab()
	b, _ := app.newTab()
	app.activate(a)
	active := ftProgress{ID: "same", Direction: "download", Name: "a.txt", Status: "active", Total: 10, Transferred: 2}
	app.relayTabEvent(a, "ft_progress", active)
	app.activate(b)
	var leaked bool
	app.eventEmit = func(name string, _ any) { leaked = leaked || name == "ft_progress" }
	done := active
	done.Status, done.Transferred = "done", 10
	app.relayTabEvent(a, "ft_progress", done)
	if leaked {
		t.Fatal("background progress leaked to active tab")
	}
	other := ftProgress{ID: "same", Direction: "upload", Name: "b.txt", Status: "error", Error: "fixture failure"}
	app.relayTabEvent(b, "ft_progress", other)
	// A fresh channel snapshot must not erase independent transfer state.
	app.relayTabEvent(a, "snapshot", `{"root_channels":[]}`)
	if got := activateTransferSnapshot(t, app, a); !reflect.DeepEqual(got, []ftProgress{done}) {
		t.Fatalf("A replay = %+v", got)
	}
	if got := activateTransferSnapshot(t, app, b); !reflect.DeepEqual(got, []ftProgress{other}) {
		t.Fatalf("B replay = %+v", got)
	}
	app.CloseTab(a)
	app.relayTabEvent(a, "ft_progress", active)
	if got := activateTransferSnapshot(t, app, b); !reflect.DeepEqual(got, []ftProgress{other}) {
		t.Fatalf("closed tab altered B replay = %+v", got)
	}
}

func TestTransferProgressReplayBoundsFinishedHistoryAndKeepsActive(t *testing.T) {
	app := newTabApp(t)
	id, _ := app.newTab()
	for i := range 25 {
		app.relayTabEvent(id, "ft_progress", ftProgress{ID: fmt.Sprintf("active-%d", i), Direction: "download", Status: "active"})
	}
	for i := range 40 {
		app.relayTabEvent(id, "ft_progress", ftProgress{ID: fmt.Sprintf("done-%d", i), Direction: "download", Status: "done"})
	}
	// Repeated progress and a retried ID each remain one cached record.
	for range 100 {
		app.relayTabEvent(id, "ft_progress", ftProgress{ID: "active-0", Direction: "download", Status: "active", Transferred: 8})
	}
	app.relayTabEvent(id, "ft_progress", ftProgress{ID: "done-39", Direction: "download", Status: "active"})
	got := activateTransferSnapshot(t, app, id)
	if len(got) != 45 {
		t.Fatalf("cache contains %d records, want 26 active + 19 finished", len(got))
	}
	seen := map[string]bool{}
	for _, p := range got {
		if seen[p.ID] {
			t.Fatalf("duplicate cached transfer %q", p.ID)
		}
		seen[p.ID] = true
	}
	for i := range 25 {
		if !seen[fmt.Sprintf("active-%d", i)] {
			t.Fatalf("lost active transfer %d", i)
		}
	}
	for i := range 20 {
		if seen[fmt.Sprintf("done-%d", i)] {
			t.Fatalf("retained expired finished transfer %d", i)
		}
	}
	empty, _ := app.newTab()
	if got := activateTransferSnapshot(t, app, empty); got == nil || len(got) != 0 {
		t.Fatalf("empty tab snapshot = %#v", got)
	}
}

func TestTransferProgressReplayConcurrentActivation(t *testing.T) {
	app := newTabApp(t)
	a, _ := app.newTab()
	b, _ := app.newTab()
	var workers sync.WaitGroup
	for _, tabID := range []string{a, b} {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := range 100 {
				app.relayTabEvent(tabID, "ft_progress", ftProgress{ID: "shared", Name: tabID, Direction: "download", Status: "active", Transferred: int64(i)})
			}
			app.relayTabEvent(tabID, "ft_progress", ftProgress{ID: "shared", Name: tabID, Direction: "download", Status: "done", Transferred: 100})
		}()
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		for range 100 {
			app.activate(a)
			app.activate(b)
		}
	}()
	workers.Wait()
	for _, tabID := range []string{a, b} {
		got := activateTransferSnapshot(t, app, tabID)
		want := []ftProgress{{ID: "shared", Name: tabID, Direction: "download", Status: "done", Transferred: 100}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("concurrent replay = %+v, want %+v", got, want)
		}
	}
}
