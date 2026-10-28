package server

import (
	"context"
	"errors"
	"slices"
	"testing"
	"testing/synctest"

	"noxa/internal/auth"
	"noxa/internal/config"
	"noxa/internal/e2ee/olmdir"
	"noxa/internal/netproto"
	"noxa/internal/store"
)

// Test the actual key-proof path, not just visibility of the guest button.
func TestWebGuestAdmission(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[allowed], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, peer, done := startPreauthPipe(t, 10, false)
				defer finishPreauthPipe(peer, done)
				s.cfg.AllowAnonymous = allowed
				s.mu.RLock()
				for _, c := range s.clients {
					c.webTransport.Store(true)
				}
				s.mu.RUnlock()
				response := keyLoginOn(t, peer, newLinkTestKeys(t))
				if response.OK != allowed {
					t.Fatalf("allowed=%v: %+v", allowed, response)
				}
				if !allowed && response.Reason != "guest login is disabled on this server" {
					t.Fatalf("wrong rejection: %+v", response)
				}
				if allowed {
					c, ok := s.clientByID(response.ClientID)
					if !ok || c.userID() != 0 || c.currentDeviceID() != "" {
						t.Fatal("guest received an account or linked device")
					}
				}
			})
		})
	}
}

func TestWebGuestStillRequiresServerPassword(t *testing.T) {
	hash, err := auth.HashPassword("test guest password")
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, func(cfg *config.Config) { cfg.AllowAnonymous = true }, func(deps *Deps) { deps.ServerPasswordHash = hash })
	defer env.stop()
	conn := dialRetry(t, env.addr)
	defer func() { _ = conn.Close() }()
	send(t, conn, netproto.MsgAuthenticate, netproto.Authenticate{Anonymous: true, Nickname: "Guest", ServerPassword: "wrong"})
	var result netproto.AuthResponse
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgAuthResponse), &result); err != nil {
		t.Fatal(err)
	}
	if result.OK || result.Reason != "invalid server password" {
		t.Fatalf("password bypass: %+v", result)
	}
}

func TestWebLoginCapabilityRequiresEnabledWeb(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
			env := startTestEnvDeps(t, nil, func(cfg *config.Config) {
				cfg.Web = config.WebConfig{Enabled: enabled, PublicURL: "https://chat.example"}
			}, func(deps *Deps) { deps.Devices = &fakeDevices{} })
			t.Cleanup(env.stop)
			conn, response := keyLogin(t, env.addr, newLinkTestKeys(t))
			t.Cleanup(func() { _ = conn.Close() })
			if !response.OK || slices.Contains(response.Capabilities, netproto.CapabilityWebLogin) != enabled {
				t.Fatalf("enabled=%v: %+v", enabled, response)
			}
		})
	}
}

// olmFakeDevices adds the Olm directory to the fake device store.
type olmFakeDevices struct{ fakeDevices }

func (*olmFakeDevices) PublishOlmBundle(context.Context, string, string, string, olmdir.Bundle) error {
	return errors.New("unused")
}
func (*olmFakeDevices) ClaimOlmKey(context.Context, string, string, string, string, string, int64, string, ...int64) (store.OlmClaim, error) {
	return store.OlmClaim{}, errors.New("unused")
}
func (*olmFakeDevices) ExpireOlmDirectory(context.Context) (int64, error) { return 0, nil }
func (*olmFakeDevices) OwnOlmBundle(context.Context, string, string) (olmdir.Bundle, error) {
	return olmdir.Bundle{}, errors.New("unused")
}

// 1c-iii-b item 5: with the Olm directory available, the admission endpoint
// is ON by default and advertised, so updated clients use only noxa-olm-v1.
func TestOlmAdmissionCapabilityDefaultOn(t *testing.T) {
	env := startTestEnvDeps(t, nil, nil, func(deps *Deps) { deps.Devices = &olmFakeDevices{} })
	t.Cleanup(env.stop)
	conn, response := keyLogin(t, env.addr, newLinkTestKeys(t))
	t.Cleanup(func() { _ = conn.Close() })
	if !response.OK || !slices.Contains(response.Capabilities, netproto.CapabilityOlmDirectory) || !slices.Contains(response.Capabilities, netproto.CapabilityOlmAdmission) {
		t.Fatalf("capabilities: %+v", response.Capabilities)
	}
	if !New(&config.Config{}, nil, &Deps{}).olmAdmissionEnabled {
		t.Fatal("admission must be enabled by default")
	}
}
