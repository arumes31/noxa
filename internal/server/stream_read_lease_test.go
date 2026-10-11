package server

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"noxa/internal/authorization"
	"noxa/internal/config"
	"noxa/internal/netproto"
	"noxa/internal/state"
	"noxa/internal/webrtc"
)

type streamReadBlockedConn struct {
	*blockingTCPConn
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *streamReadBlockedConn) Write(p []byte) (int, error) {
	c.once.Do(func() { close(c.entered) })
	<-c.release
	return len(p), nil
}

type streamReadVoice struct {
	streamDiagnosticVoice
	refreshes int
}

func (v *streamReadVoice) SetPublisherGuard(guard webrtc.PublisherGuard) {
	v.refreshes++
	v.fakeVoice.SetPublisherGuard(guard)
	v.videoRouter().SetPublisherGuard(guard)
}

func TestStreamReadReplyDoesNotDiscardAuthorizedMedia(t *testing.T) {
	for _, action := range []string{"list", "preview", "diagnostics"} {
		t.Run(action, func(t *testing.T) {
			backend := serverRoleFixture()
			backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.Connect, authorization.ViewChannel, authorization.ShareScreen, authorization.Speak}
			authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			sm := state.New(zap.NewNop())
			sm.AddChannel(testChannel(1))
			voice := &streamReadVoice{}
			srv := New(&config.Config{}, zap.NewNop(), &Deps{Authority: authority, State: sm, Voice: voice})
			conn := &streamReadBlockedConn{blockingTCPConn: newBlockingTCPConn(), entered: make(chan struct{}), release: make(chan struct{})}
			reader := &Client{ID: "reader", Conn: conn}
			other := &Client{ID: "other", Conn: newBlockingTCPConn()}
			for _, client := range []*Client{reader, other} {
				client.setIdentity(client.ID, client.ID, 1, false)
				srv.register(client)
				sm.AddClient(&state.Client{ClientID: client.ID, UserID: 1, UniqueID: client.ID})
				if err := sm.MoveClient(client.ID, 1); err != nil {
					t.Fatal(err)
				}
				voice.videoRouter().JoinChannel(1, client.ID)
			}
			if err := authority.WithPolicy(t.Context(), func(e *authorization.RoleEvaluator) error { srv.refreshRolePublishers(e); return nil }); err != nil {
				t.Fatal(err)
			}
			refreshes := voice.refreshes
			generation, err := voice.PublishVideo(other.ID, webrtc.SlotScreen, 0, true)
			if err != nil {
				t.Fatal(err)
			}
			request := netproto.VideoStreamControl{Action: action, PublisherID: other.ID, Slot: webrtc.SlotScreen, Generation: generation, Session: voice.VideoWatchSession(reader.ID)}
			frame, err := netproto.Encode(netproto.MsgVideoStreamControl, request)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- srv.handleVideoStreamControl(t.Context(), reader, frame) }()
			defer func() {
				close(conn.release)
				if err := <-done; err != nil {
					t.Error(err)
				}
			}()
			select {
			case <-conn.entered:
			case <-time.After(time.Second):
				t.Fatal("read-only stream reply did not reach blocked TCP writer")
			}
			// A real response is paused inside its socket write, not merely queued
			// in a goroutine. Membership and metadata must remain pinned there.
			if reader.roleActionMu.TryLock() {
				reader.roleActionMu.Unlock()
				t.Error("reply released membership lease")
			}
			if srv.roleMetadataMu.TryLock() {
				srv.roleMetadataMu.Unlock()
				t.Error("reply released metadata lease")
			}
			for _, slot := range []string{webrtc.SlotMic, webrtc.SlotScreen} {
				for _, pair := range [][2]string{{reader.ID, other.ID}, {other.ID, reader.ID}} {
					written := false
					err := srv.guardRoleMedia(webrtc.MediaDelivery{SenderID: pair[0], RecipientID: pair[1], ChannelID: 1, RecipientChannelID: 1, Slot: slot}, func() error { written = true; return nil })
					if err != nil || !written {
						t.Errorf("blocked %s reply discarded authorized %s packet %s -> %s: wrote=%v err=%v", action, slot, pair[0], pair[1], written, err)
					}
				}
			}
			if voice.refreshes != refreshes {
				t.Error("read-only stream request refreshed publisher/watch state")
			}
		})
	}
}

func TestStreamReadLeaseRejectsRevokedOrIneligibleSessions(t *testing.T) {
	for _, action := range []string{"list", "preview", "diagnostics", "telemetry"} {
		for _, condition := range []string{"rules blocked", "session revoked", "membership removed", "lobby", "connect denied", "authority unavailable"} {
			t.Run(action+"/"+condition, func(t *testing.T) {
				backend := serverRoleFixture()
				backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.Connect, authorization.ViewChannel}
				if condition == "connect denied" {
					backend.policy.Roles[0].Permissions = nil
				}
				authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
				if err != nil {
					t.Fatal(err)
				}
				sm := state.New(zap.NewNop())
				sm.AddChannel(testChannel(1))
				voice := &streamReadVoice{}
				srv := New(&config.Config{}, zap.NewNop(), &Deps{Authority: authority, State: sm, Voice: voice})
				refreshes := voice.refreshes
				conn := &sessionResponseConn{blockingTCPConn: newBlockingTCPConn()}
				client := &Client{ID: "reader", Conn: conn}
				client.setIdentity("reader", "reader", 1, false)
				srv.register(client)
				sm.AddClient(&state.Client{ClientID: client.ID, UserID: 1, UniqueID: "reader"})
				if err := sm.MoveClient(client.ID, 1); err != nil {
					t.Fatal(err)
				}
				switch condition {
				case "rules blocked":
					client.setRulesPending(true)
				case "session revoked":
					client.revokeSession()
				case "membership removed":
					_, _ = sm.RemoveClient(client.ID)
				case "lobby":
					if err := sm.LeaveChannel(client.ID); err != nil {
						t.Fatal(err)
					}
				case "authority unavailable":
					srv.deps.Authority = nil
				}
				if action == "telemetry" {
					frame, err := netproto.Encode(netproto.MsgVoiceTelemetry, voiceTelemetryFixture())
					if err != nil {
						t.Fatal(err)
					}
					err = srv.handleVoiceTelemetry(t.Context(), client, frame)
					if err != nil {
						t.Fatal(err)
					}
					if client.voiceTelemetry != nil {
						t.Fatal("ineligible session changed retained telemetry")
					}
				} else {
					frame, err := netproto.Encode(netproto.MsgVideoStreamControl, netproto.VideoStreamControl{Action: action, PublisherID: "other", Slot: webrtc.SlotScreen, Generation: 1, Session: 1})
					if err != nil {
						t.Fatal(err)
					}
					if err := srv.handleVideoStreamControl(t.Context(), client, frame); err != nil {
						t.Fatal(err)
					}
				}
				response := readSessionError(t, conn)
				if response.Code != errCodePermissionDenied && response.Code != errCodeUnavailable {
					t.Fatalf("unexpected admission result: %+v", response)
				}
				if voice.refreshes != refreshes {
					t.Fatal("denied read mutated publisher visibility")
				}
			})
		}
	}
}
