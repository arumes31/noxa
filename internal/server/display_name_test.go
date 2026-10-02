package server

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

func TestOwnerKeyLoginKeepsDisplayName(t *testing.T) {
	for _, bound := range []bool{false, true} {
		t.Run(map[bool]string{false: "account challenge", true: "bound identity"}[bound], func(t *testing.T) {
			env := startTestEnv(t, nil)
			defer env.stop()
			public, private, err := auth.GenerateIdentityKeyPair()
			if err != nil {
				t.Fatal(err)
			}
			env.auth.pubkeys["admin-uid"] = public
			env.auth.pubkeyIndex[public] = env.auth.users["admin-uid"]
			conn, response := dialNamedIdentity(t, env.addr, "admin-uid", public, private, "Daniel", bound)
			defer func() { _ = conn.Close() }()
			member, _ := env.state.GetClient(response.ClientID)
			if !response.OK || response.Nickname != "Daniel" || response.UniqueID != "admin-uid" || member.UserID != 1 {
				t.Fatalf("owner alias changed identity or was discarded: %+v, %+v", response, member)
			}
		})
	}
}

func TestAccountDisplayNamePreservesAuthenticatedIdentity(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	env.auth.nicknames["owner"] = env.auth.users["admin-uid"]
	conn := dialRetry(t, env.addr)
	defer func() { _ = conn.Close() }()
	send(t, conn, netproto.MsgAuthenticate, netproto.Authenticate{Username: "owner", Password: "pw", Nickname: " Daniel "})
	var response netproto.AuthResponse
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgAuthResponse), &response); err != nil {
		t.Fatal(err)
	}
	if !response.OK || response.UniqueID != "admin-uid" || response.Nickname != "Daniel" {
		t.Fatalf("account/display name were mixed: %+v", response)
	}
	member, ok := env.state.GetClient(response.ClientID)
	if !ok || member.UserID != 1 || member.Nickname != "Daniel" || env.auth.users["admin-uid"].Nickname != "admin" {
		t.Fatalf("display name changed the account: %+v", member)
	}
}

func TestDisplayNameChangeIsPublicAndPreservesIdentity(t *testing.T) {
	env := startTestEnvWithCapabilities(t, authorization.PokeMembers)
	defer env.stop()
	alice, aliceID := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = alice.Close() }()
	bob, bobID := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = bob.Close() }()
	before, _ := env.state.GetClient(aliceID)
	send(t, alice, netproto.MsgDisplayNameSet, netproto.DisplayNameSet{Nickname: " Daniel "})
	var saved netproto.DisplayNameSaved
	if err := netproto.Decode(readOfType(t, alice, netproto.MsgDisplayNameSaved), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.ClientID != aliceID || saved.Nickname != "Daniel" {
		t.Fatalf("rename acknowledgement = %+v", saved)
	}
	for {
		frame := readOfType(t, bob, netproto.MsgSnapshot)
		if bytes.Contains(frame.Payload, []byte(`"nickname":"Daniel"`)) {
			break
		}
	}
	after, _ := env.state.GetClient(aliceID)
	if after.Nickname != "Daniel" || after.UserID != before.UserID || after.UniqueID != before.UniqueID {
		t.Fatalf("rename changed identity: before=%+v after=%+v", before, after)
	}
	send(t, alice, netproto.MsgPoke, netproto.Poke{ClientID: bobID, Message: "hello"})
	var poke pokeEvent
	if err := json.Unmarshal(readEventOfType(t, bob, "poke"), &poke); err != nil || poke.FromNickname != "Daniel" {
		t.Fatalf("new activity used the old name: %+v, %v", poke, err)
	}
	for _, invalid := range []string{"admin", "", "Daniel\n", strings.Repeat("a", 65)} {
		send(t, alice, netproto.MsgDisplayNameSet, netproto.DisplayNameSet{Nickname: invalid})
		if err := readError(t, alice); err.Code != errCodeMalformed {
			t.Fatalf("name %q accepted: %+v", invalid, err)
		}
		current, _ := env.state.GetClient(aliceID)
		if current.Nickname != "Daniel" {
			t.Fatalf("rejected edit changed the name: %+v", current)
		}
	}
}
