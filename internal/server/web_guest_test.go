package server

import (
	"testing"
	"testing/synctest"

	"noxa/internal/auth"
	"noxa/internal/config"
	"noxa/internal/netproto"
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
