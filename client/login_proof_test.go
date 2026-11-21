package main

import (
	"crypto/tls"
	"net"
	"slices"
	"strings"
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
			cert, fingerprint, err := tlscert.Ensure(t.TempDir(), "", "", nil)
			if err != nil {
				t.Fatal(err)
			}
			// What the server verifies against: its own certificate.
			binding, err := ratchetBindingFor(fingerprint, "")
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
					_ = netproto.WriteFrame(conn, mustEncode(netproto.MsgAuthChallenge, netproto.AuthChallenge{Challenge: nonce, Scheme: netproto.AuthChallengeSchemeV2}))
					login := read()
					if login.Password != "pw" || auth.VerifyLoginChallenge(login.PublicKey, binding, nonce, login.PublicKeyProof) != nil {
						result <- "the password login did not prove its key for this server"
						return
					}
					if auth.VerifyLoginChallenge(login.PublicKey, "tls-sha256:"+strings.Repeat("00", 32), nonce, login.PublicKeyProof) == nil {
						result <- "the key proof verifies for another server"
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

// A key login is bound to the certificate of the client's own connection: a
// challenge that came from another server yields a proof only this server
// accepts. Every login announces the bound proof.
func TestKeyLoginProofNamesTheConnectedServer(t *testing.T) {
	cert, fingerprint, err := tlscert.Ensure(t.TempDir(), "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := ratchetBindingFor(fingerprint, "")
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
		var login netproto.Authenticate
		if frame, err := netproto.ReadFrame(conn); err == nil {
			_ = netproto.Decode(frame, &login)
		}
		if !slices.Contains(login.ClientFeatures, netproto.ClientFeatureBoundLogin) {
			result <- "the login did not announce the bound proof"
			return
		}
		// As if relayed: the challenge is not this server's own.
		foreign, _ := auth.GenerateChallenge()
		_ = netproto.WriteFrame(conn, mustEncode(netproto.MsgAuthChallenge, netproto.AuthChallenge{Challenge: foreign, Scheme: netproto.AuthChallengeSchemeV2}))
		var signed netproto.AuthSignature
		if frame, err := netproto.ReadFrame(conn); err == nil {
			_ = netproto.Decode(frame, &signed)
		}
		switch {
		case auth.VerifyLoginChallenge(signed.PublicKey, binding, foreign, signed.Signature) != nil:
			result <- "the proof does not verify for the connected server"
		case auth.VerifyLoginChallenge(signed.PublicKey, "tls-sha256:"+strings.Repeat("00", 32), foreign, signed.Signature) == nil,
			auth.VerifyChallenge(signed.PublicKey, foreign, signed.Signature) == nil,
			auth.VerifyChallenge(signed.PublicKey, append([]byte("noxa-auth-challenge-v1\n"), foreign...), signed.Signature) == nil:
			result <- "the proof verifies without this server's identity"
		default:
			result <- ""
		}
		_ = netproto.WriteFrame(conn, mustEncode(netproto.MsgAuthResponse, netproto.AuthResponse{Reason: "done"}))
	}()
	cm, _ := newTestBackend(t)
	defer cm.disconnect()
	if reason := cm.connect(listener.Addr().String(), "guest", "", ""); reason != "done" {
		t.Fatalf("connect: %s", reason)
	}
	if problem := <-result; problem != "" {
		t.Fatal(problem)
	}
}

// Without TLS there is no server identity to bind a proof to: a password
// login sends no key proof (and no probe), and a key login signs nothing.
func TestPlaintextConnectionMakesNoLoginProof(t *testing.T) {
	for _, password := range []string{"pw", ""} {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		result := make(chan string, 1)
		go func() {
			// The client tries TLS first; the login arrives on its plaintext retry.
			var conn net.Conn
			var login netproto.Authenticate
			for {
				var err error
				if conn, err = listener.Accept(); err != nil {
					result <- err.Error()
					return
				}
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				if frame, err := netproto.ReadFrame(conn); err == nil && netproto.Decode(frame, &login) == nil {
					break
				}
				_ = conn.Close()
			}
			defer func() { _ = conn.Close() }()
			if password != "" {
				if login.Password != password || len(login.PublicKeyProof) != 0 {
					result <- "the plaintext password login probed for or sent a key proof"
					return
				}
				_ = netproto.WriteFrame(conn, mustEncode(netproto.MsgAuthResponse, netproto.AuthResponse{Reason: "done"}))
				result <- ""
				return
			}
			nonce, _ := auth.GenerateChallenge()
			_ = netproto.WriteFrame(conn, mustEncode(netproto.MsgAuthChallenge, netproto.AuthChallenge{Challenge: nonce, Scheme: netproto.AuthChallengeSchemeV2}))
			if frame, err := netproto.ReadFrame(conn); err == nil {
				result <- "the client answered a challenge over plaintext with message type " + netproto.MessageType(frame.Type).String()
				return
			}
			result <- ""
		}()
		cm, _ := newTestBackend(t)
		cm.allowPlaintext = true
		reason := cm.connect(listener.Addr().String(), "alice", password, "")
		if want := map[bool]string{true: "done", false: auth.ErrNoServerBinding.Error()}[password != ""]; reason != want {
			t.Fatalf("password=%q: connect = %q, want %q", password, reason, want)
		}
		if problem := <-result; problem != "" {
			t.Fatal(problem)
		}
		cm.disconnect()
		_ = listener.Close()
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
	if err := netproto.WriteFrame(conn, mustEncode(netproto.MsgAuthChallenge, netproto.AuthChallenge{Challenge: nonce, Scheme: netproto.AuthChallengeSchemeV2})); err != nil {
		return nil, err
	}
	return netproto.ReadFrame(conn)
}
