package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/broadcast"
	"noxa/internal/config"
	"noxa/internal/server"
	"noxa/internal/state"
	"noxa/internal/tlscert"
)

// fakeAuth implements server.AuthBackend for the smoke test.
type fakeAuth struct{}

func (fakeAuth) AuthenticatePassword(_ context.Context, uniqueID, password string) (bool, error) {
	return uniqueID == "lt-uid" && password == "pw", nil
}

func (fakeAuth) AuthenticateIdentifier(_ context.Context, identifier, password string) (*auth.User, error) {
	if identifier != "lt-uid" {
		return nil, auth.ErrUserNotFound
	}
	if password != "pw" {
		return nil, nil
	}
	return &auth.User{ID: 1, UniqueID: identifier, Nickname: "loadtest"}, nil
}

func (fakeAuth) AuthenticateChallenge(context.Context, string, []byte, []byte) (bool, error) {
	return false, nil
}

func (fakeAuth) AuthenticateNickname(context.Context, string, string) (*auth.User, error) {
	return nil, auth.ErrUserNotFound
}

func (fakeAuth) LookupUser(_ context.Context, uniqueID string) (*auth.User, error) {
	return &auth.User{ID: 1, UniqueID: uniqueID, Nickname: "loadtest"}, nil
}

func (fakeAuth) LookupUserByPublicKey(context.Context, string) (*auth.User, error) {
	return nil, auth.ErrUserNotFound
}

func (fakeAuth) LookupActiveBan(context.Context, string, string) (*auth.Ban, error) {
	return nil, nil
}

func (fakeAuth) BindPublicKey(context.Context, int64, string) error {
	return nil
}

func (fakeAuth) SetE2EPublicKey(context.Context, int64, string) error {
	return nil
}

func (fakeAuth) GetE2EPublicKey(context.Context, string) (string, error) {
	return "", auth.ErrUserNotFound
}

type fakeRolePolicy struct{}

func (fakeRolePolicy) RolePolicy(context.Context) (authorization.RolePolicy, error) {
	return authorization.RolePolicy{
		Revision:   1,
		OwnerID:    1,
		EveryoneID: 10,
		Roles: []authorization.Role{{
			ID: 10, Name: "@everyone",
			Permissions: []authorization.Capability{authorization.ViewChannel, authorization.Connect, authorization.SendMessages, authorization.ReadHistory},
		}},
		Channels: []authorization.ChannelPolicy{{ChannelID: 1}},
	}, nil
}

func (fakeRolePolicy) ChangeRolePolicy(context.Context, int64, authorization.RoleChange) (authorization.RolePolicy, error) {
	return authorization.RolePolicy{}, authorization.ErrRoleForbidden
}

// TestLoadtestSmoke runs the simulator against a real in-process server and
// verifies clients connect, authenticate, and send chat.
func TestLoadtestSmoke(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	logger := zap.NewNop()
	sm := state.New(logger)
	sm.AddChannel(&state.Channel{ChannelID: 1, Name: "Load"})
	bc := broadcast.New(logger, sm)
	defer bc.Close()
	keys, kek := loadTestKeys(t)
	roles := fakeRolePolicy{}
	authority, err := authorization.NewAuthority(t.Context(), roles, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}

	srv := server.New(&config.Config{TCPAddr: addr, ChatAllowPlaintext: true}, logger, &server.Deps{
		Auth:      fakeAuth{},
		State:     sm,
		Broadcast: bc,
		Roles:     roles,
		Authority: authority,
		ScopeKeys: keys,
		ChatKEK:   kek,
	})
	if err := srv.EnsureGlobalScopeKey(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Start(ctx) }()

	// Wait for the listener.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", addr)
		if err == nil {
			_ = conn.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	var st stats
	opts := options{
		addr:     addr,
		clients:  3,
		duration: 1200 * time.Millisecond,
		ramp:     100 * time.Millisecond,
		uniqueID: "lt-uid",
		password: "pw",
		channel:  1,
	}
	if err := run(context.Background(), opts, &st); err != nil {
		t.Fatalf("run: %v auth=%d sessions=%d confirmed=%d received=%d sent=%d", err, st.authOK.Load(), st.sessionFail.Load(), st.chatParticipants.Load(), st.chatRecv.Load(), st.chatSent.Load())
	}

	if got := st.connectsOK.Load(); got != 3 {
		t.Errorf("connectsOK = %d, want 3", got)
	}
	if got := st.authOK.Load(); got != 3 {
		t.Errorf("authOK = %d, want 3", got)
	}
	if got := st.connectsFail.Load() + st.authFail.Load(); got != 0 {
		t.Errorf("failures = %d, want 0", got)
	}
	if got := st.chatSent.Load(); got == 0 {
		t.Error("no chat sent")
	}

	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("server start error: %v", err)
	}
	_ = srv.Shutdown(t.Context())
}

func TestReadRTPIdentifiers(t *testing.T) {
	sequence, timestamp, ssrc, err := readRTPIdentifiers(bytes.NewReader([]byte{
		0x01, 0x02,
		0x03, 0x04, 0x05, 0x06,
		0x07, 0x08, 0x09, 0x0a,
	}))
	if err != nil {
		t.Fatalf("readRTPIdentifiers: %v", err)
	}
	if sequence != 0x0102 || timestamp != 0x03040506 || ssrc != 0x0708090a {
		t.Fatalf("identifiers = (%#x, %#x, %#x)", sequence, timestamp, ssrc)
	}
}

func TestControlTLSConfigRestrictsInsecureMode(t *testing.T) {
	if _, _, err := controlTLSConfig(options{addr: "192.0.2.1:12333", tlsInsecure: true}); err == nil {
		t.Fatal("remote -tls-insecure address accepted")
	}

	cfg, enabled, err := controlTLSConfig(options{addr: "127.0.0.1:12333", tlsInsecure: true})
	if err != nil {
		t.Fatalf("loopback -tls-insecure: %v", err)
	}
	if !enabled || !cfg.InsecureSkipVerify || cfg.MinVersion != tls.VersionTLS13 {
		t.Fatalf("unexpected loopback TLS config: %+v", cfg)
	}
}

func TestPinnedTLSHandshake(t *testing.T) {
	cert, fingerprint, err := tlscert.Ensure(t.TempDir(), "", "", []string{"localhost"})
	if err != nil {
		t.Fatalf("generate certificate: %v", err)
	}

	for _, tc := range []struct {
		name      string
		pin       string
		wantError bool
	}{
		{name: "matching pin", pin: fingerprint},
		{name: "wrong pin", pin: strings.Repeat("00", 32), wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := fingerprintVerifiedTLSConfig(tc.pin)
			if err != nil {
				t.Fatalf("TLS config: %v", err)
			}
			listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("listen: %v", err)
			}
			t.Cleanup(func() { _ = listener.Close() })
			clientConn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(t.Context(), "tcp", listener.Addr().String())
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			t.Cleanup(func() { _ = clientConn.Close() })
			serverConn, err := listener.Accept()
			if err != nil {
				t.Fatalf("accept: %v", err)
			}
			t.Cleanup(func() {
				_ = serverConn.Close()
			})
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			serverTLS := tls.Server(serverConn, &tls.Config{
				Certificates: []tls.Certificate{cert},
				MinVersion:   tls.VersionTLS13,
			})
			serverResult := make(chan error, 1)
			go func() { serverResult <- serverTLS.HandshakeContext(ctx) }()
			clientTLS := tls.Client(clientConn, cfg)
			err = clientTLS.HandshakeContext(ctx)
			if tc.wantError {
				if err == nil || !strings.Contains(err.Error(), "TLS fingerprint =") {
					t.Fatalf("mismatched pin handshake error = %v", err)
				}
			} else if err != nil {
				t.Fatalf("matching pin handshake: %v", err)
			}
			if serverErr := <-serverResult; !tc.wantError && serverErr != nil {
				t.Fatalf("server handshake: %v", serverErr)
			}
		})
	}
}

func TestPinnedTLSConfigVerifiesExactCertificate(t *testing.T) {
	cert, fingerprint, err := tlscert.Ensure(t.TempDir(), "", "", []string{"localhost"})
	if err != nil {
		t.Fatalf("generate certificate: %v", err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}

	cfg, err := fingerprintVerifiedTLSConfig(fingerprint)
	if err != nil {
		t.Fatalf("fingerprintVerifiedTLSConfig: %v", err)
	}
	state := tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}}
	if err := cfg.VerifyConnection(state); err != nil {
		t.Fatalf("matching certificate rejected: %v", err)
	}

	wrong := strings.Repeat("00:", 31) + "00"
	cfg, err = fingerprintVerifiedTLSConfig(wrong)
	if err != nil {
		t.Fatalf("wrong pin syntax: %v", err)
	}
	if err := cfg.VerifyConnection(state); err == nil {
		t.Fatal("mismatched certificate accepted")
	}
}
