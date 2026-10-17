//go:build integration

package store

import (
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
