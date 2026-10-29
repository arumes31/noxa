package server

import (
	"strings"
	"testing"

	"noxa/internal/auth"
	"noxa/internal/config"
	"noxa/internal/netproto"
	"noxa/internal/version"
)

func TestClientMinimumNativeAdmission(t *testing.T) {
	env := startTestEnvDeps(t, nil, func(c *config.Config) { c.ClientMinVersion = "0.5.94" }, nil)
	defer env.stop()
	for _, tc := range []struct {
		version string
		allow   bool
	}{
		{"0.5.59", false}, {"0.5.93", false}, {"0.5.94", true}, {"0.5.95", true},
		{"", false}, {"unknown", false}, {"0.5.94-rc.1", false}, {"0.5.94+private", true},
	} {
		t.Run(tc.version, func(t *testing.T) {
			conn := dialRetry(t, env.addr)
			defer func() { _ = conn.Close() }()
			send(t, conn, netproto.MsgAuthenticate, netproto.Authenticate{Username: "user-uid", Password: "pw", ClientVersion: tc.version})
			var response netproto.AuthResponse
			if err := netproto.Decode(readOfType(t, conn, netproto.MsgAuthResponse), &response); err != nil {
				t.Fatal(err)
			}
			if response.OK != tc.allow {
				t.Fatalf("response=%+v wantAllow=%v", response, tc.allow)
			}
			if tc.allow {
				readOfType(t, conn, netproto.MsgSnapshot)
				if _, ok := env.state.GetClient(response.ClientID); !ok {
					t.Fatal("accepted client not published")
				}
			} else {
				if !strings.Contains(response.Reason, "required >=0.5.94") || !strings.Contains(response.Reason, "update noXa") {
					t.Fatalf("reason=%q", response.Reason)
				}
				send(t, conn, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 1})
				if got := readError(t, conn); got.Code != errCodeNotAuthenticated {
					t.Fatalf("join bypass: %+v", got)
				}
			}
		})
	}
}

func TestClientMinimumRejectsEveryInitialAuthPath(t *testing.T) {
	env := startTestEnvDeps(t, nil, func(c *config.Config) { c.ClientMinVersion = "0.5.94" }, nil)
	defer env.stop()
	for name, request := range map[string]netproto.Authenticate{
		"password":             {Username: "user-uid", Password: "pw"},
		"registered signature": {Username: "user-uid"},
		"guest signature":      {Username: "guest-id", Anonymous: true, Nickname: "Guest"},
		"ephemeral guest":      {Anonymous: true, Nickname: "Guest"},
		"reserved token":       {Token: "old-session-token", Anonymous: true, Nickname: "Guest"},
	} {
		t.Run(name, func(t *testing.T) {
			conn := dialRetry(t, env.addr)
			defer func() { _ = conn.Close() }()
			request.ClientVersion = "0.5.59"
			send(t, conn, netproto.MsgAuthenticate, request)
			var response netproto.AuthResponse
			if err := netproto.Decode(readOfType(t, conn, netproto.MsgAuthResponse), &response); err != nil {
				t.Fatal(err)
			}
			if response.OK || !strings.Contains(response.Reason, "installed 0.5.59; required >=0.5.94") {
				t.Fatalf("response=%+v", response)
			}
			env.srv.mu.RLock()
			defer env.srv.mu.RUnlock()
			for _, client := range env.srv.clients {
				if client.isAuthed() {
					t.Fatal("rejected client authenticated")
				}
				if _, ok := env.state.GetClient(client.ID); ok {
					t.Fatal("rejected client published")
				}
			}
		})
	}
}

func TestClientMinimumChallengeCannotBypassFinalAdmission(t *testing.T) {
	for _, mode := range []string{"registered", "guest", "rejected new attempt"} {
		t.Run(mode, func(t *testing.T) {
			env := startTestEnvDeps(t, nil, func(c *config.Config) { c.ClientMinVersion = "0.5.94" }, nil)
			defer env.stop()
			pub, private, err := auth.GenerateIdentityKeyPair()
			if err != nil {
				t.Fatal(err)
			}
			identity := "user-uid"
			env.auth.pubkeys[identity] = pub
			guest := mode == "guest"
			if guest {
				identity, err = auth.UniqueIDFromPublicKey(pub)
				if err != nil {
					t.Fatal(err)
				}
			}
			conn := dialRetry(t, env.addr)
			defer func() { _ = conn.Close() }()
			send(t, conn, netproto.MsgAuthenticate, netproto.Authenticate{Username: identity, Anonymous: guest, Nickname: "Guest", ClientVersion: "0.5.94"})
			var challenge netproto.AuthChallenge
			if err := netproto.Decode(readOfType(t, conn, netproto.MsgAuthChallenge), &challenge); err != nil {
				t.Fatal(err)
			}
			signature, err := auth.SignChallenge(private, challenge.Challenge) // legacy (pre-domain) client: raw form still accepted
			if err != nil {
				t.Fatal(err)
			}
			if mode == "rejected new attempt" {
				send(t, conn, netproto.MsgAuthenticate, netproto.Authenticate{Username: identity, ClientVersion: "0.5.59"})
				readOfType(t, conn, netproto.MsgAuthResponse)
			} else {
				// Simulate a policy change during the challenge to exercise the common
				// final admission guard, independently of the early request check.
				env.srv.configMu.Lock()
				env.srv.cfg.ClientMinVersion = "0.5.95"
				env.srv.configMu.Unlock()
			}
			reply := netproto.AuthSignature{UniqueID: identity, Signature: signature}
			if guest {
				reply.PublicKey = pub
			}
			send(t, conn, netproto.MsgAuthSignature, reply)
			var response netproto.AuthResponse
			if err := netproto.Decode(readOfType(t, conn, netproto.MsgAuthResponse), &response); err != nil {
				t.Fatal(err)
			}
			if response.OK {
				t.Fatalf("signature bypassed minimum: %+v", response)
			}
			send(t, conn, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 1})
			if got := readError(t, conn); got.Code != errCodeNotAuthenticated {
				t.Fatalf("join bypass: %+v", got)
			}
		})
	}
}

func TestClientMinimumSentinelAndExplicitAnyNative(t *testing.T) {
	for _, tc := range []struct {
		name, minimum, client string
		allow                 bool
	}{
		{"sentinel old", "9.9.9", "0.0.0", false},
		{"sentinel same", "9.9.9", version.Short(), true},
		{"sentinel newer", "9.9.9", "99.0.0", true},
		{"any missing", "0.0.0", "", true},
		{"any descriptive", "0.0.0", "legacy Windows build", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := startTestEnvDeps(t, nil, func(c *config.Config) { c.ClientMinVersion = tc.minimum }, nil)
			defer env.stop()
			conn := dialRetry(t, env.addr)
			defer func() { _ = conn.Close() }()
			send(t, conn, netproto.MsgAuthenticate, netproto.Authenticate{Username: "user-uid", Password: "pw", ClientVersion: tc.client})
			var response netproto.AuthResponse
			if err := netproto.Decode(readOfType(t, conn, netproto.MsgAuthResponse), &response); err != nil {
				t.Fatal(err)
			}
			if response.OK != tc.allow {
				t.Fatalf("response=%+v wantAllow=%v", response, tc.allow)
			}
		})
	}
}
