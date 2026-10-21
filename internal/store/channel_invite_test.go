package store

import (
	"crypto/sha256"
	"noxa/internal/netproto"
	"strings"
	"testing"
)

func TestChannelInvitePersistenceRotationAndScope(t *testing.T) {
	s := pollTestStore(t)
	var channel int64
	if err := s.db.QueryRowContext(t.Context(), `INSERT INTO channels(name,channel_type) VALUES('Invites',2) RETURNING id`).Scan(&channel); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("A", 43)
	first := sha256.Sum256([]byte(token))
	second := sha256.Sum256([]byte("rotated"))
	create := netproto.ChannelInviteRequest{Action: "regenerate", ChannelID: channel, Access: "guest"}
	if _, err := s.ChannelInvite(t.Context(), create, first[:]); err != nil {
		t.Fatal(err)
	}
	read, err := s.ChannelInvite(t.Context(), netproto.ChannelInviteRequest{Action: "get", ChannelID: channel}, nil)
	if err != nil || !read.Active || read.Token != "" {
		t.Fatalf("%+v %v", read, err)
	}
	resolve := netproto.ChannelInviteRequest{Action: "resolve", ChannelID: channel, Token: token}
	if _, err := s.ChannelInvite(t.Context(), resolve, first[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ChannelInvite(t.Context(), create, second[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ChannelInvite(t.Context(), resolve, first[:]); err == nil {
		t.Fatal("old token survived")
	}
	if _, err := s.ChannelInvite(t.Context(), resolve, second[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ChannelInvite(t.Context(), netproto.ChannelInviteRequest{Action: "revoke", ChannelID: channel + 999}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ChannelInvite(t.Context(), resolve, second[:]); err != nil {
		t.Fatal("cross-channel revoke affected link")
	}
	if _, err := s.ChannelInvite(t.Context(), netproto.ChannelInviteRequest{Action: "revoke", ChannelID: channel}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ChannelInvite(t.Context(), resolve, second[:]); err == nil {
		t.Fatal("revoked token survived")
	}
}
