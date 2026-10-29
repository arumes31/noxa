package ratchet

import (
	"context"
	"testing"
)

func TestRetirePublishedUnsubmittedKey(t *testing.T) {
	ctx := context.Background()
	e, err := New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close(ctx) }()
	key := make([]byte, 32)
	a, err := e.Apply(ctx, Request{Op: NewAccount, Key: key, Count: 100})
	if err != nil {
		t.Fatal(err)
	}
	next, err := e.Apply(ctx, Request{Op: "retire_prekey", Key: key, Account: a.Account, OneTimeKey: a.Bundle.OneTimeKeys[0].Key})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Apply(ctx, Request{Op: PreKeys, Key: key, Account: next.Account, Count: 1}); err != nil {
		t.Fatal("retirement did not free actual private key", err)
	}
	if _, err = e.Apply(ctx, Request{Op: "retire_prekey", Key: key, Account: next.Account, OneTimeKey: a.Bundle.OneTimeKeys[0].Key}); err == nil {
		t.Fatal("missing key silently removed")
	}
	if _, err = e.Apply(ctx, Request{Op: PreKeys, Key: key, Account: a.Account, Count: 1}); err == nil {
		t.Fatal("old candidate mutated")
	}
}
