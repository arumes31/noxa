package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	"golang.org/x/crypto/nacl/box"

	"noxa/internal/broadcast"
	"noxa/internal/netproto"
	"noxa/internal/state"
)

func TestLoadtestRequiresSelectedAuthorizationModel(t *testing.T) {
	for _, selected := range []string{"", "roles-v1"} {
		for _, received := range []string{"", "roles-v1", "roles-v2"} {
			t.Run(fmt.Sprintf("%s/%s", selected, received), func(t *testing.T) {
				listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = listener.Close() }()
				done := make(chan error, 1)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				go func() {
					done <- func() error {
						conn, err := listener.Accept()
						if err != nil {
							return err
						}
						defer func() { _ = conn.Close() }()
						if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
							return err
						}
						f, err := netproto.ReadFrame(conn)
						if err != nil {
							return err
						}
						var request netproto.Authenticate
						if err := netproto.Decode(f, &request); err != nil {
							return err
						}
						want := []string{netproto.AuthorizationModelRolesV1}
						if !reflect.DeepEqual(request.AuthorizationModels, want) {
							return fmt.Errorf("advertised models %v, want %v", request.AuthorizationModels, want)
						}
						if err := writeMsg(conn, netproto.MsgAuthResponse, netproto.AuthResponse{OK: true, AuthorizationModel: received}); err != nil {
							return err
						}
						if received != netproto.AuthorizationModelRolesV1 {
							// A mismatch must terminate immediately, before waiting for a snapshot.
							if _, err := netproto.ReadFrame(conn); err == nil {
								return fmt.Errorf("sent protected traffic after model mismatch")
							} else if e, ok := err.(net.Error); ok && e.Timeout() {
								return fmt.Errorf("waited for session data after model mismatch")
							}
							return nil
						}
						cancel()
						return writeMsg(conn, netproto.MsgSnapshot, struct{}{})
					}()
				}()
				var st stats
				simulateClient(ctx, options{addr: listener.Addr().String(), uniqueID: "fixture", password: "pw", authorizationModel: selected}, &st, 0)
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				if received != netproto.AuthorizationModelRolesV1 && (st.authFail.Load() != 1 || st.authOK.Load() != 0) {
					t.Fatalf("model mismatch counted as authenticated: ok=%d failed=%d", st.authOK.Load(), st.authFail.Load())
				}
			})
		}
	}
}

func TestRoleJoinRequiresExactMembership(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		conn, peer := net.Pipe()
		defer func() { _ = conn.Close(); _ = peer.Close() }()
		chat, err := newLoadChat()
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			snapshot := broadcast.TreeSnapshot{RootChannels: []*broadcast.ChannelNode{
				{Channel: state.Channel{ChannelID: 1}, Clients: []*broadcast.ClientInfo{{ClientID: "other", ChannelID: 1}}},
				{Channel: state.Channel{ChannelID: 2}, Clients: []*broadcast.ClientInfo{{ClientID: "self", ChannelID: 2}}},
			}}
			if err := writeMsg(peer, netproto.MsgSnapshot, snapshot); err != nil {
				done <- err
				return
			}
			done <- writeMsg(peer, netproto.MsgError, netproto.Error{Code: 4, Message: "private diagnostic"})
		}()
		if err := awaitRoleJoin(conn, chat, "self", 1); err == nil {
			t.Fatal("another client or channel counted as a successful join")
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}

func TestRoleLoadResultRequiresEveryClientConfirmation(t *testing.T) {
	var st stats
	st.connectsOK.Store(2)
	st.authOK.Store(2)
	opts := options{clients: 2, authorizationModel: "roles-v1"}
	for _, confirmed := range []int64{0, 1} {
		st.chatParticipants.Store(confirmed)
		if err := st.result(opts); err == nil {
			t.Fatalf("only %d clients confirmed, but run passed", confirmed)
		}
	}
	st.chatParticipants.Store(2)
	if err := st.result(opts); err != nil {
		t.Fatal(err)
	}
	if err := run(t.Context(), options{authorizationModel: "future"}, &stats{}); err == nil {
		t.Fatal("unknown model accepted")
	}
}

func loadTestSealedKey(t *testing.T, c *loadChat, id uint32) netproto.ChannelKey {
	t.Helper()
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatal(err)
	}
	sealed, err := box.SealAnonymous(nil, key[:], c.public, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return netproto.ChannelKey{KeyID: id, SealedKey: base64.StdEncoding.EncodeToString(sealed)}
}

func TestLoadChatRequiresDecryptableCorrelatedEcho(t *testing.T) {
	c, err := newLoadChat()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.message(); err == nil {
		t.Fatal("sent without scope key")
	}
	key := loadTestSealedKey(t, c, 1)
	if err := c.install(key); err != nil {
		t.Fatal(err)
	}
	message, err := c.message()
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name                        string
		kind                        string
		mutate                      func(*netproto.ChatBroadcast)
		received, confirmed, failed bool
	}{
		{"own echo", "chat", func(*netproto.ChatBroadcast) {}, true, true, false},
		{"another sender", "chat", func(m *netproto.ChatBroadcast) { m.FromClientID = "other" }, true, false, false},
		{"unrelated event", "user_joined", func(*netproto.ChatBroadcast) {}, false, false, false},
		{"different request", "chat", func(m *netproto.ChatBroadcast) { m.ClientMsgID = "other" }, false, false, false},
		{"plaintext", "chat", func(m *netproto.ChatBroadcast) { m.Enc = false }, false, false, true},
		{"corrupt ciphertext", "chat", func(m *netproto.ChatBroadcast) { m.Text = "invalid" }, false, false, true},
		{"unknown key", "chat", func(m *netproto.ChatBroadcast) { m.KeyID = 42 }, false, false, true},
		{"direct message", "chat", func(m *netproto.ChatBroadcast) { m.Direct = true }, false, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := netproto.ChatBroadcast{FromClientID: "self", Text: message.Text, KeyID: message.KeyID, Enc: true, ClientMsgID: message.ClientMsgID}
			tt.mutate(&m)
			frame, err := netproto.Encode(netproto.MsgEvent, struct {
				Type string                 `json:"type"`
				Data netproto.ChatBroadcast `json:"data"`
			}{tt.kind, m})
			if err != nil {
				t.Fatal(err)
			}
			received, confirmed, err := c.receive(frame, "self")
			if received != tt.received || confirmed != tt.confirmed || (err != nil) != tt.failed {
				t.Fatalf("received=%v confirmed=%v err=%v", received, confirmed, err)
			}
		})
	}
	for id := uint32(2); id < 20; id++ {
		if err := c.install(loadTestSealedKey(t, c, id)); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.install(key); err != nil {
		t.Fatal(err)
	}
	next, err := c.message()
	if err != nil || next.KeyID != 19 || next.ClientMsgID == message.ClientMsgID || len(c.keys) != 2 {
		t.Fatalf("rotation lost current key or grew cache: %v %+v keys=%d", err, next, len(c.keys))
	}
}

func TestLoadSetupReadersRetainGlobalKeyRotations(t *testing.T) {
	for _, stage := range []string{"snapshot", "media"} {
		t.Run(stage, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c, err := newLoadChat()
				if err != nil {
					t.Fatal(err)
				}
				key := loadTestSealedKey(t, c, 7)
				conn, peer := net.Pipe()
				defer func() { _ = conn.Close(); _ = peer.Close() }()
				done := make(chan error, 1)
				go func() {
					if err := writeMsg(peer, netproto.MsgChannelKey, key); err != nil {
						done <- err
						return
					}
					if stage == "snapshot" {
						done <- writeMsg(peer, netproto.MsgSnapshot, struct{}{})
					} else {
						done <- writeMsg(peer, netproto.MsgWebRTCAnswer, netproto.WebRTCAnswer{SDP: "answer"})
					}
				}()
				if stage == "snapshot" {
					_, err = readOfType(conn, netproto.MsgSnapshot, time.Second, c.observe)
				} else {
					_, _, _, err = readWebRTCAnswer(conn, time.Second, c.observe)
				}
				if err != nil {
					t.Fatal(err)
				}
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				message, err := c.message()
				if err != nil || message.KeyID != 7 {
					t.Fatalf("setup dropped rotated key: %v key=%d", err, message.KeyID)
				}
			})
		})
	}
}
