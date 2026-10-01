package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestCustomMetadataPageBoundsAndIsolation(t *testing.T) {
	s := testDedicatedDBStore(t)
	uid := fmt.Sprintf("custom-page-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := s.db.ExecContext(ctx, "DELETE FROM client_custom WHERE unique_id=$1 OR unique_id=$2", uid, uid+"-other"); err != nil {
			t.Error(err)
		}
	})
	for _, key := range []string{"a", "b", "c"} {
		if err := s.CustomSet(t.Context(), uid, key, "value-"+key); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CustomSet(t.Context(), uid+"-other", "a", "other"); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListCustomPage(t.Context(), uid, "", 1)
	if err != nil || len(rows) != 2 || rows[0].Key != "a" || rows[1].Key != "b" {
		t.Fatalf("first page/lookahead: %v %v", rows, err)
	}
	rows, err = s.ListCustomPage(t.Context(), uid, "b", 1)
	if err != nil || len(rows) != 1 || rows[0].Key != "c" {
		t.Fatalf("exclusive cursor: %v %v", rows, err)
	}
	if err := s.CustomSet(t.Context(), uid, "a", strings.Repeat("x", 4097)); err != nil {
		t.Fatal(err)
	}
	if rows, err := s.ListCustomPage(t.Context(), uid, "", 1); err == nil || rows != nil {
		t.Fatal("oversized legacy value escaped bound")
	}
	if err := s.CustomDel(t.Context(), uid, "a"); err != nil {
		t.Fatal(err)
	}
	if err := s.CustomSet(t.Context(), uid, strings.Repeat("a", 129), "value"); err != nil {
		t.Fatal(err)
	}
	if rows, err := s.ListCustomPage(t.Context(), uid, "", 1); err == nil || rows != nil {
		t.Fatal("oversized legacy key escaped bound")
	}
}
