package main

import "testing"

func TestDMIdentityChangesPublishRevision(t *testing.T) {
	a := identityTestApp(t, "off")
	owner, err := a.DMHistoryContextForTab("")
	if err != nil {
		t.Fatal(err)
	}
	var revisions []string
	a.eventEmit = func(name string, payload any) {
		if name == "dm_history_identity_changed" {
			revision, ok := payload.(string)
			if !ok {
				t.Errorf("revision is not a lossless string: %T", payload)
				return
			}
			revisions = append(revisions, revision)
		}
	}
	if err := a.CreateIdentity("second"); err != "" {
		t.Fatal(err)
	}
	if len(revisions) != 0 {
		t.Fatal("creating an unselected identity invalidated ownership")
	}
	if err := a.SwitchIdentity("second"); err != "" {
		t.Fatal(err)
	}
	switched, err := a.DMHistoryContextForTab("")
	if err != nil {
		t.Fatal(err)
	}
	if len(revisions) != 1 || revisions[0] != switched.IdentityRevision || switched.IdentityRevision == owner.IdentityRevision {
		t.Fatalf("switch revision: %v / %+v", revisions, switched)
	}
	regeneratedUID := a.RegenerateIdentity()
	regenerated, err := a.DMHistoryContextForTab("")
	if err != nil {
		t.Fatal(err)
	}
	if regeneratedUID != regenerated.IdentityUID || len(revisions) != 2 || revisions[1] != regenerated.IdentityRevision || regenerated.IdentityRevision == switched.IdentityRevision {
		t.Fatalf("regeneration revision: %v / %+v", revisions, regenerated)
	}
	if _, err := a.DMHistoryLoadForContext(switched, "peer"); err == nil {
		t.Fatal("regeneration kept old context valid")
	}
}

func TestSelectedIdentityChangePreservesExistingTabIdentity(t *testing.T) {
	a := identityTestApp(t, "off")
	_, cm := dmContextTestApp(t)
	a.cmStore(cm)
	a.tabs = map[string]*tabState{"a": {cm: cm}}
	a.activeID = "a"
	original, err := cm.identity()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.CreateIdentity("second"); err != "" {
		t.Fatal(err)
	}
	if err := a.SwitchIdentity("second"); err != "" {
		t.Fatal(err)
	}
	if got, err := cm.identity(); err != nil || got != original {
		t.Fatalf("selection replaced established tab identity: %p / %v", got, err)
	}
	_, next, err := a.newIdentityTab()
	if err != nil {
		t.Fatal(err)
	}
	nextID, err := next.cm.identity()
	if err != nil {
		t.Fatal(err)
	}
	nextUID, err := nextID.uniqueID()
	if err != nil || nextUID != a.IdentityUID() {
		t.Fatalf("new tab ignored selected identity: %q / %v", nextUID, err)
	}
	regeneratedUID := a.RegenerateIdentity()
	if got, err := cm.identity(); err != nil || got != original {
		t.Fatalf("regeneration replaced established tab identity: %p / %v", got, err)
	}
	if got, err := next.cm.identity(); err != nil || got != nextID {
		t.Fatalf("regeneration replaced background tab identity: %p / %v", got, err)
	}
	_, fresh, err := a.newIdentityTab()
	if err != nil {
		t.Fatal(err)
	}
	freshID, err := fresh.cm.identity()
	if err != nil {
		t.Fatal(err)
	}
	freshUID, err := freshID.uniqueID()
	if err != nil || freshUID != regeneratedUID {
		t.Fatalf("new tab ignored regenerated identity: %q / %v", freshUID, err)
	}
	var order []string
	originalUID, err := original.uniqueID()
	if err != nil {
		t.Fatal(err)
	}
	a.eventEmit = func(name string, payload any) {
		order = append(order, name)
		if name == "tab_identity" {
			info, ok := payload.(map[string]string)
			if !ok || info["tab_id"] != "a" || info["identity_uid"] != originalUID {
				t.Errorf("wrong replay identity: %+v", payload)
			}
		}
	}
	a.tabs["a"].journal = []journalEntry{{name: "event", payload: "DM replay fixture"}}
	a.activate("a")
	if len(order) < 3 || order[0] != "tab_reset" || order[1] != "tab_identity" || order[2] != "event" {
		t.Fatalf("identity did not precede replay: %v", order)
	}
}
