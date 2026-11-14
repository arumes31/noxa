package main

import (
	"crypto/tls"
	"net"
	"testing"
	"time"

	"noxa/internal/auth"
	"noxa/internal/netproto"
	"noxa/internal/tlscert"
)

// A password login proves its identity key with a signature over a challenge
// fetched on the same connection; a server that answers the probe without a
// challenge (no proof support needed) still logs the client in.
func TestPasswordLoginProvesIdentityKey(t *testing.T) {
	for _, challenge := range []bool{true, false} {
		name := "server without challenge"
		if challenge {
			name = "proof"
		}
		t.Run(name, func(t *testing.T) {
			cert, _, err := tlscert.Ensure(t.TempDir(), "", "", nil)
			if err != nil {
				t.Fatal(err)
			}
			listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = listener.Close() }()
			result := make(chan string, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					result <- err.Error()
					return
				}
				defer func() { _ = conn.Close() }()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				read := func() netproto.Authenticate {
					var msg netproto.Authenticate
					if frame, err := netproto.ReadFrame(conn); err == nil {
						_ = netproto.Decode(frame, &msg)
					}
					return msg
				}
				ok := netproto.AuthResponse{OK: true, AuthorizationModel: netproto.AuthorizationModelRolesV1, ClientID: "c1", UniqueID: "u1"}
				probe := read()
				if probe.Password != "" || probe.PublicKey != "" || probe.Username != "alice" {
					result <- "the probe carried the password or key"
					return
				}
				if challenge {
					nonce, err := auth.GenerateChallenge()
					if err != nil {
						result <- err.Error()
						return
					}
					_ = netproto.WriteFrame(conn, mustEncode(netproto.MsgAuthChallenge, netproto.AuthChallenge{Challenge: nonce, Scheme: netproto.AuthChallengeSchemeV1}))
					login := read()
					if login.Password != "pw" || auth.VerifyLoginChallenge(login.PublicKey, nonce, login.PublicKeyProof) != nil {
						result <- "the password login did not prove its key"
						return
					}
				}
				_ = netproto.WriteFrame(conn, mustEncode(netproto.MsgAuthResponse, ok))
				result <- ""
				for {
					if _, err := netproto.ReadFrame(conn); err != nil {
						return
					}
				}
			}()
			cm, _ := newTestBackend(t)
			defer cm.disconnect()
			if reason := cm.connect(listener.Addr().String(), "alice", "pw", ""); reason != "" {
				t.Fatalf("connect: %s", reason)
			}
			select {
			case problem := <-result:
				if problem != "" {
					t.Fatal(problem)
				}
			case <-time.After(6 * time.Second):
				t.Fatal("server did not finish")
			}
		})
	}
}

// readLoginFrame reads a login like a current server: a password login's
// key-proof probe (no password, not anonymous) gets a challenge first, and
// the frame returned is the login that follows it.
func readLoginFrame(conn net.Conn) (*netproto.Frame, error) {
	frame, err := netproto.ReadFrame(conn)
	if err != nil {
		return nil, err
	}
	var request netproto.Authenticate
	if netproto.Decode(frame, &request) != nil || request.Password != "" || request.Anonymous {
		return frame, nil
	}
	nonce, err := auth.GenerateChallenge()
	if err != nil {
		return nil, err
	}
	if err := netproto.WriteFrame(conn, mustEncode(netproto.MsgAuthChallenge, netproto.AuthChallenge{Challenge: nonce, Scheme: netproto.AuthChallengeSchemeV1})); err != nil {
		return nil, err
	}
	return netproto.ReadFrame(conn)
}
