package main

import (
	"context"
	"errors"
	"net"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"noxa/internal/netproto"
)

func testUpdateEnvelope(t *testing.T) updateReconnectEnvelope {
	t.Helper()
	return updateReconnectEnvelope{Version: 1, Expires: time.Now().Add(5 * time.Minute).Unix(), Addr: "127.0.0.1:1", Nickname: "account", DisplayName: "Daniel", Bookmark: "home", ChannelID: 42, Identity: *mustTempIdentity(t), Passwords: loginPasswords{Password: "account-secret", ServerPassword: "server-secret"}, Muted: true, Deafened: true, MutedBeforeDeafen: false}
}

func TestUpdateReconnectEnvelopeValidation(t *testing.T) {
	// The codec is portable; the production protection primitive is DPAPI.
	copyBytes := func(b []byte) ([]byte, error) { return append([]byte(nil), b...), nil }
	saved := testUpdateEnvelope(t)
	encoded, err := encodeUpdateReconnect(saved, copyBytes)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeUpdateReconnect(encoded, time.Now(), copyBytes)
	if err != nil || !reflect.DeepEqual(*decoded, saved) {
		t.Fatalf("handoff did not round trip: %v", err)
	}
	for _, bad := range []string{"not base64", strings.Repeat("a", updateReconnectMaxBytes+1)} {
		if _, err := decodeUpdateReconnect(bad, time.Now(), copyBytes); err == nil {
			t.Fatal("invalid envelope accepted")
		}
	}
	for _, change := range []func(*updateReconnectEnvelope){
		func(s *updateReconnectEnvelope) { s.Expires = time.Now().Add(-time.Minute).Unix() },
		func(s *updateReconnectEnvelope) { s.Expires = time.Now().Add(time.Hour).Unix() },
		func(s *updateReconnectEnvelope) { s.Version = 2 },
		func(s *updateReconnectEnvelope) { s.Identity.PublicKey = "invalid" },
		func(s *updateReconnectEnvelope) { s.ChannelID = -1 },
		func(s *updateReconnectEnvelope) { s.Addr = "" },
	} {
		bad := saved
		change(&bad)
		encoded, _ := encodeUpdateReconnect(bad, copyBytes)
		if _, err := decodeUpdateReconnect(encoded, time.Now(), copyBytes); err == nil {
			t.Fatal("invalid connection accepted")
		}
	}
	fail := func([]byte) ([]byte, error) { return nil, errors.New("OS protection failed") }
	if _, err := encodeUpdateReconnect(saved, fail); err == nil {
		t.Fatal("protection failure ignored")
	}
	if _, err := decodeUpdateReconnect(encoded, time.Now(), fail); err == nil {
		t.Fatal("unlock failure ignored")
	}
}

func TestUpdateReconnectSnapshotUsesActiveConnection(t *testing.T) {
	a := newTabApp(t)
	id := mustTempIdentity(t)
	active, tab := a.newTabWithIdentity(id)
	a.newTabWithIdentity(mustTempIdentity(t))
	tab.info.Addr, tab.info.Nickname, tab.bookmark, tab.lastVoiceChannel = "server.test:1234", "account", "home", 42
	tab.loginPasswords = &loginPasswords{Password: "native-password", ServerPassword: "native-server-password"}
	local, remote := net.Pipe()
	defer local.Close()
	defer remote.Close()
	tab.cm.conn = local
	tab.cm.displayName = "Daniel"
	a.activate(active)
	got := a.snapshotUpdateConnection()
	if got == nil || got.ChannelID != 42 || got.Nickname != "account" || got.DisplayName != "Daniel" || got.Bookmark != "home" || got.Identity.PublicKey != id.PublicKey || got.Passwords != *tab.loginPasswords {
		t.Fatal("active connection snapshot incomplete")
	}
	tab.reconnectSuppressed = true
	if a.snapshotUpdateConnection() != nil {
		t.Fatal("intentional disconnect was captured")
	}
	tab.reconnectSuppressed = false
	tab.cm.closed = true
	if a.snapshotUpdateConnection() != nil {
		t.Fatal("closed socket was captured")
	}
}

func TestUpdateReconnectRequiresUpdateLaunchAndConsumesOnce(t *testing.T) {
	previous := startedAfterUpdate
	defer func() { startedAfterUpdate = previous }()
	for _, afterUpdate := range []bool{false, true} {
		startedAfterUpdate = afterUpdate
		t.Setenv(updateReconnectEnv, "invalid")
		a := newTabApp(t)
		got := a.RestoreUpdateConnection()
		if got["attempted"] != afterUpdate {
			t.Fatalf("unexpected restore for update=%v: %v", afterUpdate, got)
		}
		if os.Getenv(updateReconnectEnv) != "" {
			t.Fatal("handoff remained in process environment")
		}
		if a.RestoreUpdateConnection()["attempted"] != false {
			t.Fatal("handoff consumed twice")
		}
	}
}

func TestUpdateRestartEnvironmentIsScopedToLaunch(t *testing.T) {
	launch, quit := restartLaunch, wailsQuit
	defer func() { restartLaunch, wailsQuit = launch, quit }()
	t.Setenv(updateReconnectEnv, "previous")
	for _, fail := range []bool{false, true} {
		a := newTabApp(t)
		a.ctx = context.Background()
		a.updateReconnect = "protected-connection"
		restartLaunch = func(string) error {
			if os.Getenv(updateReconnectEnv) != "protected-connection" {
				t.Error("successor did not inherit handoff")
			}
			if fail {
				return errors.New("launch failed")
			}
			return nil
		}
		wailsQuit = func(context.Context) {
			if fail {
				t.Error("failed launch quit running client")
			}
		}
		result := a.ApplyAndRestart()
		if (result != "") != fail {
			t.Fatalf("restart result: %q", result)
		}
		if os.Getenv(updateReconnectEnv) != "previous" || a.updateReconnect != "" {
			t.Fatal("restart leaked handoff or failed to consume it")
		}
	}
}

func TestUpdateReconnectAuthenticatesAndRejoinsWithPrivacyBeforeReplay(t *testing.T) {
	for _, denied := range []bool{false, true} {
		t.Run(map[bool]string{false: "allowed", true: "denied"}[denied], func(t *testing.T) {
			joins := make(chan netproto.JoinChannel, 1)
			addr, requests, release := gatedReconnectServer(t, func(frame *netproto.Frame) (netproto.MessageType, any, bool) {
				if netproto.MessageType(frame.Type) != netproto.MsgJoinChannel {
					return 0, nil, false
				}
				var join netproto.JoinChannel
				if err := netproto.Decode(frame, &join); err != nil {
					t.Error(err)
				}
				joins <- join
				if denied {
					return netproto.MsgError, netproto.Error{Code: 4, Message: "permission denied", OriginType: frame.Type}, true
				}
				return netproto.MsgChannelJoined, netproto.ChannelJoined{ClientID: "recovered-client", ChannelID: join.ChannelID}, true
			})
			a := newTabApp(t)
			saved := testUpdateEnvelope(t)
			saved.Addr = addr
			var mu sync.Mutex
			var events []string
			a.eventEmit = func(name string, payload any) {
				mu.Lock()
				defer mu.Unlock()
				events = append(events, name)
				if name == "update_connection_restoring" {
					audio := payload.(map[string]bool)
					if !audio["muted"] || !audio["deafened"] || audio["muted_before_deafen"] {
						t.Error("audio state not restored")
					}
				}
			}
			result := make(chan map[string]any, 1)
			go func() { result <- a.restoreUpdateConnection(&saved) }()
			req := waitReconnect(t, requests)
			if req.Username != saved.Nickname || req.Nickname != saved.DisplayName || req.Password != saved.Passwords.Password || req.ServerPassword != saved.Passwords.ServerPassword || req.PublicKey != saved.Identity.PublicKey {
				t.Fatal("restored authentication changed identity or credentials")
			}
			close(release)
			join := waitReconnect(t, joins)
			if join.ChannelID != 42 || !join.AckRequested {
				t.Fatalf("join: %+v", join)
			}
			got := waitReconnect(t, result)
			tabID, ok := got["tab_id"].(string)
			if !ok || got["error"] != nil || (got["warning"] != nil) != denied {
				t.Fatalf("restore failed: %v", got)
			}
			defer a.CloseTab(tabID)
			mu.Lock()
			defer mu.Unlock()
			if len(events) == 0 || events[0] != "update_connection_restoring" {
				t.Fatal("privacy flags did not precede events")
			}
			restored, reset := -1, -1
			for i, event := range events {
				if event == "tab_voice_restored" {
					restored = i
				}
				if event == "tab_reset" {
					reset = i
				}
			}
			if reset < 0 || (!denied && (restored < 0 || restored > reset)) {
				t.Fatalf("wrong restore/replay order: %v", events)
			}
		})
	}
}

func TestUpdateReconnectCancelledDuringAuthentication(t *testing.T) {
	addr, requests, release := gatedReconnectServer(t)
	a := newTabApp(t)
	saved := testUpdateEnvelope(t)
	saved.Addr = addr
	result := make(chan map[string]any, 1)
	go func() { result <- a.restoreUpdateConnection(&saved) }()
	waitReconnect(t, requests)
	manual, _ := a.newTabWithIdentity(mustTempIdentity(t))
	a.activate(manual)
	close(release)
	got := waitReconnect(t, result)
	if got["tab_id"] != nil || got["error"] == nil || a.activeID != manual || len(a.ListTabs()) != 1 {
		t.Fatal("restoration overrode manual connection")
	}
	a.CloseTab(manual)
}
