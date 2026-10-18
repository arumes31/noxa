//go:build integration

package store

import (
	"errors"
	"testing"

	"noxa/internal/authorization"
)

// Migration 042 keeps every existing channel a voice channel, and a text
// channel created through the role lifecycle persists its flag.
func TestTextChannelFlagPersistsAndDefaultsToVoice(t *testing.T) {
	s, owner, _ := roleTestStore(t)
	p, err := s.PrepareRolePolicy(t.Context(), owner)
	if err != nil {
		t.Fatal(err)
	}
	existing := p.Channels[0].ChannelID
	_, text, err := s.ChangeRoleChannel(t.Context(), owner, authorization.ChannelTreeChange{
		Kind: authorization.ChannelCreate, ExpectedRevision: p.Revision,
		Access: authorization.ChannelPolicy{Synced: true},
	}, &RoleChannelCreate{Name: "general", ChannelType: 2, TextOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[int64]bool{existing: false, text: true} {
		var got bool
		if err := s.db.QueryRowContext(t.Context(), `SELECT text_only FROM channels WHERE id=$1`, id).Scan(&got); err != nil || got != want {
			t.Fatalf("channel %d text_only = %v, want %v (%v)", id, got, want, err)
		}
	}
}

// Text channels are reached through chat subscriptions, which never check a
// channel password: storage refuses to give one a password, at creation or
// later, while clearing stays possible.
func TestTextChannelRefusesPassword(t *testing.T) {
	s, owner, _ := roleTestStore(t)
	p, err := s.PrepareRolePolicy(t.Context(), owner)
	if err != nil {
		t.Fatal(err)
	}
	create := func(rec RoleChannelCreate) (authorization.RolePolicy, int64, error) {
		return s.ChangeRoleChannel(t.Context(), owner, authorization.ChannelTreeChange{
			Kind: authorization.ChannelCreate, ExpectedRevision: p.Revision,
			Access: authorization.ChannelPolicy{Synced: true},
		}, &rec)
	}
	if _, _, err := create(RoleChannelCreate{Name: "locked", ChannelType: 2, TextOnly: true, PasswordHash: "hash"}); !errors.Is(err, authorization.ErrRoleInvalid) {
		t.Fatalf("passworded text channel created: %v", err)
	}
	p, text, err := create(RoleChannelCreate{Name: "general", ChannelType: 2, TextOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	hash, empty := "hash", ""
	if _, err := s.EditRoleChannel(t.Context(), owner, text, p.Revision, RoleChannelSettings{Name: "general", PasswordHash: &hash}); !errors.Is(err, authorization.ErrRoleInvalid) {
		t.Fatalf("password set on text channel: %v", err)
	}
	var stored string
	if err := s.db.QueryRowContext(t.Context(), `SELECT COALESCE(password_hash,'') FROM channels WHERE id=$1`, text).Scan(&stored); err != nil || stored != "" {
		t.Fatalf("text channel password = %q (%v)", stored, err)
	}
	if _, err := s.EditRoleChannel(t.Context(), owner, text, p.Revision, RoleChannelSettings{Name: "general", PasswordHash: &empty}); err != nil {
		t.Fatalf("clearing text channel password: %v", err)
	}
}
