package server

import (
	"errors"
	"testing"
)

func TestDeletedScopeCannotReloadOrMint(t *testing.T) {
	m, _, _ := testKeyManager(t, nil)
	ctx := t.Context()
	if _, _, err := m.EnsureScope(ctx, 42); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.rotate(ctx, 42); err != nil {
		t.Fatal(err)
	}
	retained := m.entry(42)
	m.forgetDeletedScope(42)
	if !retained.deleted || retained.curKey != [32]byte{} || len(retained.old) != 0 {
		t.Fatal("retained keys survived erasure")
	}
	if _, _, err := m.EnsureScope(ctx, 42); !errors.Is(err, ErrNoScopeKey) {
		t.Fatalf("mint after deletion: %v", err)
	}
	if _, _, err := m.rotate(ctx, 42); !errors.Is(err, ErrNoScopeKey) {
		t.Fatalf("rotate after deletion: %v", err)
	}
	if _, err := m.at(ctx, 42, 1); !errors.Is(err, ErrNoScopeKey) {
		t.Fatalf("archive after deletion: %v", err)
	}
	m.forgetDeletedScope(0)
	if _, _, err := m.EnsureScope(ctx, 0); err != nil {
		t.Fatal("global scope erased", err)
	}
}
