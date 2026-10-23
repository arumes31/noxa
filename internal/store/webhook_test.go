package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"noxa/internal/netproto"
	"testing"
	"time"
)

func TestWebhookRevokeSerializesWithInflightPostAndChannelScope(t *testing.T) {
	s := pollTestStore(t)
	var user, ch int64
	if err := s.db.QueryRowContext(t.Context(), `INSERT INTO users(unique_id,nickname) VALUES('hook-owner','Hook owner') RETURNING id`).Scan(&user); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(t.Context(), `INSERT INTO channels(name,channel_type) VALUES('Hooks',2) RETURNING id`).Scan(&ch); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("secret"))
	created, err := s.ManageWebhook(t.Context(), netproto.WebhookRequest{Action: "create", ChannelID: ch, Name: "CI"}, user, hash[:])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ManageWebhook(t.Context(), netproto.WebhookRequest{Action: "revoke", ChannelID: ch + 999, ID: created.ID}, user, nil); err != nil {
		t.Fatal(err)
	}
	valid, err := s.AuthenticateWebhook(t.Context(), created.ID, hash[:])
	if err != nil || !valid {
		t.Fatal("valid preflight rejected", err)
	}
	wrong := sha256.Sum256([]byte("invalid"))
	valid, err = s.AuthenticateWebhook(t.Context(), created.ID, wrong[:])
	if err != nil || valid {
		t.Fatal("invalid preflight accepted", err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	posted, revoked := make(chan error, 1), make(chan error, 1)
	go func() {
		_, _, _, _, err := s.DeliverWebhook(t.Context(), created.ID, hash[:], func(WebhookIdentity) (string, uint32, error) { close(entered); <-release; return "ciphertext", 1, nil })
		posted <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("post never entered")
	}
	// Invalid tokens must not wait for the valid delivery's row lock.
	invalidCtx, invalidCancel := context.WithTimeout(t.Context(), time.Second)
	defer invalidCancel()
	valid, err = s.AuthenticateWebhook(invalidCtx, created.ID, wrong[:])
	if err != nil || valid {
		close(release)
		t.Fatal("preflight waited for row lock", err)
	}
	_, _, _, _, err = s.DeliverWebhook(invalidCtx, created.ID, wrong[:], func(WebhookIdentity) (string, uint32, error) {
		t.Error("invalid token reached callback")
		return "", 0, nil
	})
	if !errors.Is(err, ErrWebhookDenied) {
		close(release)
		t.Fatal("invalid token waited for row lock", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	go func() {
		_, err := s.ManageWebhook(ctx, netproto.WebhookRequest{Action: "revoke", ChannelID: ch, ID: created.ID}, user, nil)
		revoked <- err
	}()
	if err := <-revoked; err == nil {
		t.Error("revoke acknowledged before inflight post released")
	}
	close(release)
	if err := <-posted; err != nil {
		t.Fatal(err)
	}
	if _, err := s.ManageWebhook(t.Context(), netproto.WebhookRequest{Action: "revoke", ChannelID: ch, ID: created.ID}, user, nil); err != nil {
		t.Fatal(err)
	}
	_, _, _, _, err = s.DeliverWebhook(t.Context(), created.ID, hash[:], func(WebhookIdentity) (string, uint32, error) {
		t.Error("revoked callback ran")
		return "cipher", 1, nil
	})
	if !errors.Is(err, ErrWebhookDenied) {
		t.Fatalf("revoked err=%v", err)
	}
}
