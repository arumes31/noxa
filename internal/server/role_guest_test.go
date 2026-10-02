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
	signature, err := auth.SignChallenge(private, challenge.Challenge)
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
