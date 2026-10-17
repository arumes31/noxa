package channels

import (
	"context"
	"testing"
)

// Both channel loaders (startup and role reconciliation) carry the text-only
// flag from storage into state, where it refuses voice membership.
func TestLoadersCarryTextOnlyFlag(t *testing.T) {
	mgr, s, sm := testEnv(t)
	if _, err := s.DB().ExecContext(t.Context(), "DELETE FROM channels"); err != nil {
		t.Fatalf("failed to clean channels table: %v", err)
	}
	voiceID := insertChannelRow(t, s, "Lobby", 0, ChannelTypePermanent, 0)
	textID := insertChannelRow(t, s, "general", 0, ChannelTypePermanent, 0)
	if _, err := s.DB().ExecContext(t.Context(), "UPDATE channels SET text_only=TRUE WHERE id=$1", textID); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.LoadIntoState(context.Background()); err != nil {
		t.Fatalf("LoadIntoState: %v", err)
	}
	for id, want := range map[int64]bool{voiceID: false, textID: true} {
		if ch, ok := sm.GetChannel(id); !ok || ch.TextOnly != want {
			t.Fatalf("state channel %d = %+v, want TextOnly %v", id, ch, want)
		}
	}
	persisted, err := mgr.loadChannelStates(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range persisted {
		if ch.TextOnly != (ch.ChannelID == textID) {
			t.Fatalf("persisted channel %+v has the wrong text flag", ch)
		}
	}
}
