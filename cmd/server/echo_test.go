package main

import (
	"context"
	"errors"
	"testing"

	"noxa/internal/config"
)

type echoStoreStub struct {
	calls int
	name  string
	err   error
}

func (s *echoStoreStub) EnsureEchoChannel(_ context.Context, name string) (int64, error) {
	s.calls++
	s.name = name
	return 42, s.err
}

func TestEnsureEchoChannelStartupSwitch(t *testing.T) {
	for _, cfg := range []*config.Config{{EchoChannelName: "Echo Test"}, {EchoChannelEnabled: true}} {
		db := &echoStoreStub{}
		if id, err := ensureEchoChannel(t.Context(), db, cfg); id != 0 || err != nil || db.calls != 0 {
			t.Fatalf("disabled echo touched storage: %d %v %+v", id, err, db)
		}
	}
	db := &echoStoreStub{}
	cfg := &config.Config{EchoChannelEnabled: true, EchoChannelName: "My echo"}
	if id, err := ensureEchoChannel(t.Context(), db, cfg); id != 42 || err != nil || db.name != cfg.EchoChannelName {
		t.Fatalf("enabled echo: %d %v %+v", id, err, db)
	}
	db.err = errors.New("storage unavailable")
	if _, err := ensureEchoChannel(t.Context(), db, cfg); !errors.Is(err, db.err) {
		t.Fatalf("startup ignored provisioning failure: %v", err)
	}
}
