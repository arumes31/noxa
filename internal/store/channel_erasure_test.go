package store

import (
	"testing"
)

func TestDeletedChannelChatErasure(t *testing.T) {
	s := testDBStore(t)
	ctx := t.Context()
	scope := testScope(t, s)
	other := testScope(t, s)
	for _, id := range []int64{scope, other, 0} {
		if existing, err := s.CurrentScopeKey(ctx, id); err != nil {
			t.Fatal(err)
		} else if existing != nil {
			continue
		}
		key, err := s.AllocScopeKeyID(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.InsertScopeKey(ctx, id, key, wrappedFixture(1), 1); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO chat_messages(scope,channel_id,from_unique_id,from_nickname,body_enc,key_id) VALUES(1,$1,'u','User','ciphertext',1)`, scope); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().ExecContext(ctx, `DELETE FROM channels WHERE id=$1`, scope); err != nil {
		t.Fatal(err)
	}
	for _, table := range []struct{ name, column string }{{"chat_messages", "channel_id"}, {"chat_scope_keys", "scope_id"}, {"chat_scope_seq", "scope_id"}} {
		var n int
		if err := s.DB().QueryRowContext(ctx, "SELECT count(*) FROM "+table.name+" WHERE "+table.column+"=$1", scope).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s retained: %d %v", table.name, n, err)
		}
	}
	if _, err := s.AllocScopeKeyID(ctx, scope); err == nil {
		t.Fatal("deleted scope recreated")
	}
	if err := s.InsertScopeKey(ctx, scope, 2, wrappedFixture(2), 1); err == nil {
		t.Fatal("deleted key recreated")
	}
	for _, id := range []int64{other, 0} {
		if k, err := s.CurrentScopeKey(ctx, id); err != nil || k == nil {
			t.Fatalf("unrelated scope erased: %d %v", id, err)
		}
	}
}
