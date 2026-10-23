package server

import (
	"encoding/binary"
	"fmt"
	"net"
	"noxa/internal/auth"
	"noxa/internal/config"
	"noxa/internal/netproto"
	"testing"
	"time"
)

func TestMalformedAuthenticationCannotPoisonLoginBudget(t *testing.T) {
	limiter := auth.NewLoginFailureLimiter(auth.LoginFailureLimiterConfig{MaxEntries: 8})
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.LoginLimiter = limiter })
	defer env.stop()
	for i := 0; i < 12; i++ {
		conn := dialRetry(t, env.addr)
		for j := 0; j < 5; j++ {
			send(t, conn, netproto.MsgAuthenticate, netproto.Authenticate{Anonymous: true, Username: fmt.Sprintf("victim-%d", i), Password: "bogus"})
			var response netproto.AuthResponse
			if err := netproto.Decode(readOfType(t, conn, netproto.MsgAuthResponse), &response); err != nil {
				t.Fatal(err)
			}
			if response.Reason != "anonymous login takes no password" {
				t.Fatalf("invalid envelope: %+v", response)
			}
		}
		_ = conn.Close()
	}
	conn := dialRetry(t, env.addr)
	defer conn.Close()
	// Negotiate publicly, then send mismatched/no-challenge signatures. Neither
	// may penalize the named victim or the source's credential budget.
	send(t, conn, netproto.MsgAuthenticate, netproto.Authenticate{Username: "seed"})
	readOfType(t, conn, netproto.MsgAuthChallenge)
	for i := 0; i < 5; i++ {
		send(t, conn, netproto.MsgAuthSignature, netproto.AuthSignature{UniqueID: "admin-uid", Signature: []byte("bad")})
		var response netproto.AuthResponse
		if err := netproto.Decode(readOfType(t, conn, netproto.MsgAuthResponse), &response); err != nil {
			t.Fatal(err)
		}
		if response.Reason != "no pending challenge" {
			t.Fatalf("signature without challenge: %+v", response)
		}
	}
	send(t, conn, netproto.MsgAuthenticate, netproto.Authenticate{Username: "admin-uid", Password: "pw"})
	var response netproto.AuthResponse
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgAuthResponse), &response); err != nil {
		t.Fatal(err)
	}
	if !response.OK {
		t.Fatalf("unrelated valid login denied after malformed flood: %+v", response)
	}
}

func TestIdleConnectionsDoNotConsumeAuthenticatedCapacity(t *testing.T) {
	env := startTestEnvFull(t, nil, func(c *config.Config) { c.MaxClients = 1 })
	defer env.stop()
	idle := dialRetry(t, env.addr)
	defer idle.Close()
	conn := dialRetry(t, env.addr)
	defer conn.Close()
	send(t, conn, netproto.MsgAuthenticate, netproto.Authenticate{Username: "admin-uid", Password: "pw"})
	var response netproto.AuthResponse
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgAuthResponse), &response); err != nil {
		t.Fatal(err)
	}
	if !response.OK {
		t.Fatalf("idle socket blocked legitimate login: %+v", response)
	}
	send(t, idle, netproto.MsgAuthenticate, netproto.Authenticate{Username: "user-uid", Password: "pw"})
	if err := netproto.Decode(readOfType(t, idle, netproto.MsgAuthResponse), &response); err != nil {
		t.Fatal(err)
	}
	if response.OK || response.Reason != "server is full" {
		t.Fatalf("authenticated capacity bypass: %+v", response)
	}
}

func TestPreauthOversizedHeaderIsRejectedWithoutBody(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	conn := dialRetry(t, env.addr)
	defer conn.Close()
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], preauthFrameLimit+1)
	if _, err := conn.Write(header[:]); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	var b [1]byte
	if _, err := conn.Read(b[:]); err == nil {
		t.Fatal("oversized frame was accepted")
	} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatal("reader waited for attacker body")
	}
}
