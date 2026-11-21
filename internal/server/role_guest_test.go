package server

import (
	"net"
	"testing"

	"noxa/internal/auth"
	"noxa/internal/netproto"
)

func dialNamedIdentity(t *testing.T, addr, uniqueID, public, private, nickname string, presentKey bool) (net.Conn, netproto.AuthResponse) {
	t.Helper()
	conn := dialRetry(t, addr)
	send(t, conn, netproto.MsgAuthenticate, netproto.Authenticate{
		Username: uniqueID, Nickname: nickname, Anonymous: presentKey,
		AuthorizationModels: []string{netproto.AuthorizationModelRolesV1},
	})
	var challenge netproto.AuthChallenge
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgAuthChallenge), &challenge); err != nil {
		t.Fatal(err)
	}
	signature, err := auth.SignLoginChallenge(private, loginBindingOf(conn), challenge.Challenge)
	if err != nil {
		t.Fatal(err)
	}
	message := netproto.AuthSignature{UniqueID: uniqueID, Signature: signature}
	if presentKey {
		message.PublicKey = public
	}
	send(t, conn, netproto.MsgAuthSignature, message)
	var response netproto.AuthResponse
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgAuthResponse), &response); err != nil {
		t.Fatal(err)
	}
	if !response.OK {
		_ = conn.Close()
		t.Fatalf("identity login: %+v", response)
	}
	readOfType(t, conn, netproto.MsgSnapshot)
	return conn, response
}

// A guest with a linked device has a second session without a verified key;
// lookup must find the verified one whatever the map order.
func TestRoleGuestFindsVerifiedSessionAmongSeveral(t *testing.T) {
	s := &TCPServer{clients: map[string]*Client{}}
	for i := range 8 {
		s.clients[string(rune('a'+i))] = &Client{UniqueID: "g", authed: true}
	}
	s.clients["verified"] = &Client{UniqueID: "g", authed: true, verifiedGuestKey: "key"}
	for range 20 {
		if c, ok := s.roleGuest("g"); !ok || c.verifiedGuestKey == "" {
			t.Fatal("verified guest session not found")
		}
	}
	if _, ok := s.roleGuest("other"); ok {
		t.Fatal("unknown guest found")
	}
}
