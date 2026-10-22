//go:build integration

package store

import (
	"errors"
	"testing"

	"noxa/internal/authorization"
)

func homeIDs(p authorization.RolePolicy) (int64, []int64) {
	var home int64
	var public []int64
	for _, ch := range p.Channels {
		if ch.SystemKind == "home" {
			home = ch.ChannelID
		}
		if ch.SystemKind == "public" {
			public = append(public, ch.ChannelID)
		}
	}
	return home, public
}
func TestHomePoolGrowShrinkAndProtection(t *testing.T) {
	s, before := echoTestStore(t)
	p, err := s.EnsureHomeChannels(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	home, public := homeIDs(p)
	if home == 0 || len(public) != 2 {
		t.Fatalf("missing initial rooms: %+v", p.Channels)
	}
	if len(p.Channels) != len(before.Channels)+3 {
		t.Fatal("unrelated channels changed")
	}
	again, err := s.EnsureHomeChannels(t.Context())
	if err != nil || again.Revision != p.Revision {
		t.Fatalf("not idempotent: %v", err)
	}
	if _, err = s.DB().ExecContext(t.Context(), `DELETE FROM channels WHERE id=$1`, home); err == nil {
		t.Fatal("home was deletable")
	}
	if _, _, err = s.ChangeRoleChannel(t.Context(), p.OwnerID, authorization.ChannelTreeChange{Kind: authorization.ChannelDelete, ChannelID: home, ExpectedRevision: p.Revision}, nil); !errors.Is(err, authorization.ErrRoleForbidden) {
		t.Fatalf("owner delete: %v", err)
	}
	occupied := map[int64]bool{public[0]: true}
	p, err = s.ReconcilePublicChannels(t.Context(), p.Revision, occupied)
	if err != nil {
		t.Fatal(err)
	}
	_, rooms := homeIDs(p)
	if len(rooms) != 3 {
		t.Fatalf("one used requires two spare: %v", rooms)
	}
	occupied[public[1]] = true
	p, err = s.ReconcilePublicChannels(t.Context(), p.Revision, occupied)
	if err != nil {
		t.Fatal(err)
	}
	_, rooms = homeIDs(p)
	if len(rooms) != 4 {
		t.Fatalf("two used requires two spare: %v", rooms)
	}
	if _, err = s.ReconcilePublicChannels(t.Context(), p.Revision, occupied); !errors.Is(err, authorization.ErrLifecycleUnchanged) {
		t.Fatalf("idle pool wrote: %v", err)
	}
	// Keep a higher-numbered room occupied: cleanup must preserve its identity.
	busy := rooms[len(rooms)-1]
	occupied = map[int64]bool{busy: true}
	p, err = s.ReconcilePublicChannels(t.Context(), p.Revision, occupied)
	if err != nil {
		t.Fatal(err)
	}
	_, rooms = homeIDs(p)
	if len(rooms) != 3 {
		t.Fatal("surplus empty rooms survived")
	}
	found := false
	for _, id := range rooms {
		found = found || id == busy
	}
	if !found {
		t.Fatal("occupied room removed")
	}
	p, err = s.ReconcilePublicChannels(t.Context(), p.Revision, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, rooms = homeIDs(p)
	if len(rooms) != 2 {
		t.Fatal("empty pool did not return to two")
	}
	e, err := authorization.NewRoleEvaluator(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range rooms {
		if !e.Evaluate(0, id, authorization.Connect).Allowed || !e.Evaluate(0, id, authorization.SendMessages).Allowed {
			t.Fatal("public not usable")
		}
	}
	if e.Evaluate(p.OwnerID, home, authorization.SendMessages).Allowed {
		t.Fatal("home chat enabled")
	}
}

func TestHomePoolAdoptsExistingLobbyAndKeepsCustomChannels(t *testing.T) {
	s, p := echoTestStore(t)
	create := func(name string, parent int64) int64 {
		t.Helper()
		next, id, err := s.ChangeRoleChannel(t.Context(), p.OwnerID, authorization.ChannelTreeChange{Kind: authorization.ChannelCreate, ExpectedRevision: p.Revision, ParentID: parent, Access: authorization.ChannelPolicy{ParentID: parent, Synced: true}}, &RoleChannelCreate{Name: name, ChannelType: 2})
		if err != nil {
			t.Fatal(err)
		}
		p = next
		return id
	}
	lobby := create("Lobby", 0)
	first := create("Public1", lobby)
	second := create("Public2", lobby)
	custom := create("Gaming", lobby)
	p, err := s.EnsureHomeChannels(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	home, rooms := homeIDs(p)
	if home != lobby || len(rooms) != 2 || rooms[0] != first || rooms[1] != second {
		t.Fatalf("existing IDs changed: %d %v", home, rooms)
	}
	for _, ch := range p.Channels {
		if ch.ChannelID == custom && ch.SystemKind != "" {
			t.Fatal("custom room adopted")
		}
	}
	p, err = s.EnsureHomeChannels(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	home, rooms = homeIDs(p)
	if home != lobby || len(rooms) != 2 {
		t.Fatal("restart duplicated rooms")
	}
}
