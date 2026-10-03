package main

import (
	"crypto/tls"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"

	"noxa/internal/netproto"
	"noxa/internal/tlscert"
)

// Real TLS/auth/control frames exercise recovery without production services.
func restartFixture(t *testing.T, addr, rejection, joinRejection string, moveGate ...<-chan struct{}) (string, <-chan netproto.JoinChannel) {
	t.Helper()
	cert, _, err := tlscert.Ensure(t.TempDir(), "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := tls.Listen("tcp", addr, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13})
	if err != nil {
		t.Fatal(err)
	}
	joins := make(chan netproto.JoinChannel, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		if _, err = netproto.ReadFrame(conn); err != nil {
			return
		}
		response, _ := netproto.Encode(netproto.MsgAuthResponse, netproto.AuthResponse{OK: rejection == "", Reason: rejection, AuthorizationModel: netproto.AuthorizationModelRolesV1, ClientID: "recovered", UniqueID: "original-user"})
		if netproto.WriteFrame(conn, response) != nil || rejection != "" {
			return
		}
		for {
			frame, err := netproto.ReadFrame(conn)
			if err != nil {
				return
			}
			if netproto.MessageType(frame.Type) == netproto.MsgJoinChannel {
				var join netproto.JoinChannel
				if netproto.Decode(frame, &join) != nil {
					return
				}
				joins <- join
				var reply *netproto.Frame
				if joinRejection != "" {
					reply, _ = netproto.Encode(netproto.MsgError, netproto.Error{OriginType: uint16(netproto.MsgJoinChannel), Message: joinRejection})
				} else {
					reply, _ = netproto.Encode(netproto.MsgChannelJoined, netproto.ChannelJoined{ClientID: "recovered", ChannelID: join.ChannelID})
					if len(moveGate) > 0 {
						if netproto.WriteFrame(conn, reply) != nil {
							return
						}
						<-moveGate[0]
					}
					moved, _ := json.Marshal(map[string]any{"type": "user_moved", "data": map[string]any{"client_id": "recovered", "channel_id": join.ChannelID}})
					if netproto.WriteFrame(conn, &netproto.Frame{Type: uint16(netproto.MsgEvent), Payload: moved}) != nil {
						return
					}
					if len(moveGate) > 0 {
						continue
					}
				}
				if netproto.WriteFrame(conn, reply) != nil {
					return
				}
			}
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); <-done })
	return listener.Addr().String(), joins
}

func TestReconnectAfterProlongedOutageRestoresAuthorizedChannel(t *testing.T) {
	// Reserve then close a local endpoint to reproduce a stopped server.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	a, tabID, source, _ := prepareReconnect(t, addr)
	source.cm.clientID = "original"
	source.cm.sink.Emit("event", `{"type":"user_moved","data":{"client_id":"original","channel_id":7}}`)
	source.cm.sink.Emit("event", `{"type":"server_shutdown","data":{}}`)
	source.cm.sink.Emit("disconnected", nil)
	for i := 0; i < 8; i++ {
		if result := a.ReconnectTab(tabID, "password", ""); result.Error == "" || result.Terminal {
			t.Fatalf("offline attempt %d = %+v", i, result)
		}
	}
	_, joins := restartFixture(t, addr, "", "")
	// Fixture cleanup must run after the recovered client closes its socket.
	t.Cleanup(func() { a.CloseTab(tabID) })
	result := a.ReconnectTab(tabID, "password", "")
	if result.Error != "" || result.Warning != "" || result.TabID != tabID {
		t.Fatalf("recovery = %+v", result)
	}
	if join := waitReconnect(t, joins); join.ChannelID != 7 || !join.AckRequested || join.Password != "" {
		t.Fatalf("join = %+v", join)
	}
	if a.tabs[tabID].lastVoiceChannel != 7 {
		t.Fatal("restored membership not retained")
	}
}

func TestReconnectTerminalAuthenticationAndDeniedChannel(t *testing.T) {
	for _, reason := range []string{"invalid credentials", "banned", "invalid server password", "too many failed logins, try again later"} {
		t.Run(reason, func(t *testing.T) {
			addr, _ := restartFixture(t, "127.0.0.1:0", reason, "")
			a, tabID, _, _ := prepareReconnect(t, addr)
			result := a.ReconnectTab(tabID, "password", "")
			if result.Error != reason || result.Terminal != (reason != "too many failed logins, try again later") {
				t.Fatalf("result = %+v", result)
			}
		})
	}
	t.Run("previous channel now needs permission", func(t *testing.T) {
		addr, _ := restartFixture(t, "127.0.0.1:0", "", "channel password required")
		a, tabID, source, _ := prepareReconnect(t, addr)
		source.lastVoiceChannel = 7
		result := a.ReconnectTab(tabID, "password", "")
		if result.Error != "" || result.Warning == "" || !a.tabs[tabID].cm.connected() {
			t.Fatalf("result = %+v", result)
		}
	})
}

func TestReconnectForgetsRemovedMembership(t *testing.T) {
	for _, event := range []string{
		`{"type":"user_moved","data":{"client_id":"self","channel_id":0}}`,
		`{"type":"kicked","data":{"client_id":"self"}}`,
		`{"type":"channel_deleted","data":{"channel_id":7}}`,
		`{"type":"channel_deleted","data":{"channel_id":1,"channel_ids":[1,7]}}`,
	} {
		t.Run(event, func(t *testing.T) {
			tab := &tabState{lastVoiceChannel: 7}
			rememberReconnectChannel(tab, "event", event, "self")
			if tab.lastVoiceChannel != 0 {
				t.Fatalf("retained removed channel: %d", tab.lastVoiceChannel)
			}
		})
	}
}

func TestReconnectChangedCertificateIsTerminalAndNeverTrusted(t *testing.T) {
	addr, _ := restartFixture(t, "127.0.0.1:0", "", "")
	a, tabID, _, _ := prepareReconnect(t, addr)
	a.knownServers = loadKnownServersAt(filepath.Join(t.TempDir(), "known_servers.json"))
	oldFingerprint := tlscert.FingerprintDER([]byte("previous certificate"))
	if err := a.knownServers.trust(addr, oldFingerprint); err != nil {
		t.Fatal(err)
	}
	result := a.ReconnectTab(tabID, "password", "")
	if result.Error == "" || !result.Terminal {
		t.Fatalf("result = %+v", result)
	}
	if status, err := a.knownServers.verify(addr, oldFingerprint); err != nil || status != trustOK {
		t.Fatal("recovery changed the trusted certificate")
	}
}

func TestReconnectMarksVoiceRestoreBeforeDelayedMembership(t *testing.T) {
	gate := make(chan struct{})
	addr, _ := restartFixture(t, "127.0.0.1:0", "", "", gate)
	a, tabID, source, _ := prepareReconnect(t, addr)
	defer close(gate)
	source.lastVoiceChannel = 7
	events := make(chan string, 20)
	a.eventEmit = func(name string, _ any) {
		if name == "tab_voice_restored" || name == "tab_reset" || name == "event" {
			events <- name
		}
	}
	result := a.ReconnectTab(tabID, "password", "")
	if result.Error != "" {
		t.Fatalf("recovery = %+v", result)
	}
	if first := waitReconnect(t, events); first != "tab_voice_restored" {
		t.Fatalf("first = %s", first)
	}
	if next := waitReconnect(t, events); next != "tab_reset" {
		t.Fatalf("next = %s", next)
	}
	select {
	case name := <-events:
		t.Fatalf("membership escaped gate: %s", name)
	default:
	}
}
