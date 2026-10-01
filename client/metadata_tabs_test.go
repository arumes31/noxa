package main

import (
	"reflect"
	"testing"
)

func TestCachedMetadataRejectsNativeTabSwitch(t *testing.T) {
	first := &connManager{motd: "first", lastSubscriptions: `{"channel_ids":[1,3]}`}
	second := &connManager{motd: "second", lastSubscriptions: `{"channel_ids":[2]}`}
	app := &App{activeID: "a", tabs: map[string]*tabState{"a": {cm: first}, "b": {cm: second}, "offline": {}}}
	app.cmStore(first)
	check := func(tab, wantMOTD string, wantSubscriptions []int64) {
		t.Helper()
		if got, err := app.MOTDForTab(tab); err != nil || got != wantMOTD {
			t.Fatalf("MOTD = %q, %v; want %q", got, err, wantMOTD)
		}
		got, err := app.SubscriptionsForTab(tab)
		if err != nil || !reflect.DeepEqual(got, wantSubscriptions) {
			t.Fatalf("subscriptions = %v, %v; want %v", got, err, wantSubscriptions)
		}
		if len(got) > 0 {
			got[0] = 99
			again, err := app.SubscriptionsForTab(tab)
			if err != nil || !reflect.DeepEqual(again, wantSubscriptions) {
				t.Fatalf("caller changed cached subscriptions: %v, %v", again, err)
			}
		}
	}
	check("a", "first", []int64{1, 3})
	app.tabsMu.Lock()
	_, _, _, ok := app.activateLocked("b")
	app.tabsMu.Unlock()
	if !ok {
		t.Fatal("native activation failed")
	}
	for _, tab := range []string{"", "a", "missing", "offline"} {
		if _, err := app.MOTDForTab(tab); err == nil {
			t.Fatalf("accepted stale MOTD read for %q", tab)
		}
		if _, err := app.SubscriptionsForTab(tab); err == nil {
			t.Fatalf("accepted stale subscriptions read for %q", tab)
		}
	}
	check("b", "second", []int64{2})
}
