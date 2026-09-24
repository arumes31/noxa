//go:build integration

package grpcserver

import (
	"context"
	"encoding/base64"
	"errors"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"golang.org/x/net/websocket"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/config"
	"noxa/internal/eventbus"
	"noxa/internal/query"
	"noxa/internal/server"
	"noxa/internal/state"
	noxav1 "noxa/v1"
)

type nativeEventGRPCBackend struct {
	query.RoleIntegrationBackend
	query.RoleEventBackend
}

type roleEventReceiver interface {
	Recv() (*noxav1.Event, error)
}

type wsRoleEventReceiver struct{ conn *websocket.Conn }

func (s wsRoleEventReceiver) Recv() (*noxav1.Event, error) {
	if err := s.conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return nil, err
	}
	var data []byte
	if err := websocket.Message.Receive(s.conn, &data); err != nil {
		return nil, err
	}
	message := &noxav1.Event{}
	return message, protojson.Unmarshal(data, message)
}

func TestRoleEventsUseCurrentNativeProjectionAndAdmission(t *testing.T) {
	for _, transport := range []string{"grpc", "websocket"} {
		t.Run(transport, func(t *testing.T) { testRoleEventProjection(t, transport) })
	}
}

func testRoleEventProjection(t *testing.T, transport string) {
	db := grpcRoleTestStore(t)
	a := auth.New(db, zap.NewNop())
	const password = "event-integration-password"
	create := func(name string) auth.IntegrationPrincipal {
		t.Helper()
		uid, err := a.RegisterUser(t.Context(), name, password)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=TRUE WHERE unique_id=$1", uid); err != nil {
			t.Fatal(err)
		}
		p, err := a.AuthenticateIntegration(t.Context(), uid, password, "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	owner, member := create("event-owner"), create("event-reader")
	if _, err := db.DB().ExecContext(t.Context(), "INSERT INTO channels (id,name,channel_type,parent_id) VALUES (1,'Hidden parent',2,NULL),(2,'Visible child',2,1)"); err != nil {
		t.Fatal(err)
	}
	p, err := db.PrepareRolePolicy(t.Context(), owner.UserID())
	if err != nil {
		t.Fatal(err)
	}
	authority, err := authorization.NewAuthority(t.Context(), db, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	access := authorization.ChannelPolicy{ChannelID: 2, ParentID: 1}
	for _, cap := range []authorization.Capability{authorization.ViewChannel, authorization.Connect, authorization.Speak} {
		access.Overrides = append(access.Overrides, authorization.RoleOverride{RoleID: p.EveryoneID, Capability: cap, Effect: authorization.Allow})
	}
	p, err = authority.ChangeRolePolicy(t.Context(), owner.UserID(), authorization.RoleChange{Kind: authorization.ChannelAccessSet, ExpectedRevision: p.Revision, Channel: access})
	if err != nil {
		t.Fatal(err)
	}
	sm := state.New(zap.NewNop())
	sm.AddChannel(&state.Channel{ChannelID: 1, Name: "Hidden parent", ChannelType: 2})
	sm.AddChannel(&state.Channel{ChannelID: 2, ParentID: 1, Name: "Visible child", ChannelType: 2})
	for _, entry := range []struct {
		id      string
		channel int64
		status  string
	}{{"visible", 2, "online"}, {"invisible", 2, "invisible"}, {"hidden", 1, "online"}} {
		sm.AddClient(&state.Client{ClientID: entry.id, UserID: member.UserID() + 100, UniqueID: entry.id, Nickname: entry.id, Status: entry.status})
		if err := sm.MoveClient(entry.id, entry.channel); err != nil {
			t.Fatal(err)
		}
		sm.SetSpeaking(entry.id, true)
	}
	native := server.New(&config.Config{}, zap.NewNop(), &server.Deps{Auth: a, Authority: authority, Roles: db, State: sm})
	b := &nativeEventGRPCBackend{RoleIntegrationBackend: native, RoleEventBackend: native}
	bus := eventbus.New(zap.NewNop())
	t.Cleanup(bus.Close)
	var stream roleEventReceiver
	if transport == "grpc" {
		client := noxav1.NewEventsClient(dialGRPC(t, startGRPC(t, b, bus)))
		stream, err = client.Subscribe(roleAuthCtx(t, member.UniqueID(), password), &noxav1.SubscribeEventsRequest{})
		if err != nil {
			t.Fatal(err)
		}
	} else {
		httpServer := httptest.NewServer(eventbus.HandlerWithRoleBackend(bus, native, zap.NewNop(), nil, nil))
		t.Cleanup(httpServer.Close)
		cfg, err := websocket.NewConfig("ws"+strings.TrimPrefix(httpServer.URL, "http"), httpServer.URL)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(member.UniqueID()+":"+password)))
		cfg.Header.Set("Noxa-Authorization-Model", "roles-v1")
		conn, err := websocket.DialConfig(cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		stream = wsRoleEventReceiver{conn: conn}
	}
	initial, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	snapshot := initial.GetRoleSnapshot()
	if initial.Id != "1" || initial.Type != noxav1.EventType_EVENT_TYPE_ROLE_SNAPSHOT || len(snapshot.GetChannels()) != 1 || snapshot.Channels[0].ChannelId != 2 || snapshot.Channels[0].ParentId != 0 || len(snapshot.Clients) != 1 || snapshot.Clients[0].ClientId != "visible" {
		t.Fatalf("initial projection leaked hidden state: %v", initial)
	}
	if len(snapshot.SpeakingClientIds) != 1 || snapshot.SpeakingClientIds[0] != "visible" {
		t.Fatalf("initial speaking projection: %v", snapshot)
	}
	if stream, ok := stream.(interface{ Header() (metadata.MD, error) }); ok {
		headers, err := stream.Header()
		if err != nil || len(headers.Get("noxa-authorization-model")) != 1 || headers.Get("noxa-authorization-model")[0] != "roles-v1" {
			t.Fatalf("missing model confirmation: %v %v", headers, err)
		}
	}
	for _, kind := range roleStructuralBusTypes {
		bus.Publish(kind, []byte(`{"client_id":"hidden","reason":"private-reason","from_channel_id":1}`))
	}
	bus.Publish("speaking_changed", []byte(`{"client_id":"hidden","channel_id":1,"speaking":true}`))
	bus.Publish("speaking_changed", []byte(`{"client_id":"invisible","channel_id":2,"speaking":true}`))
	bus.Publish("speaking_changed", []byte(`{"client_id":"visible","channel_id":2,"speaking":true}`))
	activity, err := stream.Recv()
	if err != nil || activity.GetId() != "2" || activity.GetType() != noxav1.EventType_EVENT_TYPE_USER_SPEAKING || activity.GetUserSpeaking().GetUserId() != "visible" {
		t.Fatalf("hidden/duplicate events survived before visible marker: %v %v", activity, err)
	}
	muted := true
	bus.Publish("user_joined", []byte(`{"client_id":"hidden","channel_id":1}`))
	bus.Publish("speaking_changed", []byte(`{"client_id":"visible","channel_id":2,"speaking":true}`))
	marker, err := stream.Recv()
	if err != nil || marker.GetId() != "3" || marker.GetType() != noxav1.EventType_EVENT_TYPE_USER_SPEAKING {
		t.Fatalf("speaking delta exposed hidden structural timing: %v %v", marker, err)
	}
	if _, err := sm.SetServerVoiceState("visible", &muted, nil); err != nil {
		t.Fatal(err)
	}
	// Mute emits a structural change without requiring another RTP/VAD edge.
	bus.Publish("member_voice_changed", []byte(`{"client_id":"visible"}`))
	stopped, err := stream.Recv()
	if err != nil || stopped.GetId() != "4" || stopped.GetRoleSnapshot() == nil || len(stopped.GetRoleSnapshot().SpeakingClientIds) != 0 {
		t.Fatalf("mute retained speaking state: %v %v", stopped, err)
	}
	muted = false
	if _, err := sm.SetServerVoiceState("visible", &muted, nil); err != nil {
		t.Fatal(err)
	}
	sm.SetSpeaking("visible", true)
	bus.Publish("speaking_changed", []byte(`{"client_id":"visible","channel_id":2,"speaking":true}`))
	activity, err = stream.Recv()
	if err != nil || activity.GetId() != "5" || !activity.GetUserSpeaking().GetSpeaking() {
		t.Fatalf("speaking did not resume: %v %v", activity, err)
	}
	// Leave state.IsSpeaking stale deliberately: current source permission,
	// not the viewer's grant or a later media packet, governs the snapshot.
	access.Overrides[2].Effect = authorization.Deny
	p, err = authority.ChangeRolePolicy(t.Context(), owner.UserID(), authorization.RoleChange{Kind: authorization.ChannelAccessSet, ExpectedRevision: p.Revision, Channel: access})
	if err != nil {
		t.Fatal(err)
	}
	stopped, err = stream.Recv()
	if err != nil || stopped.GetId() != "6" || stopped.GetRoleSnapshot() == nil || len(stopped.GetRoleSnapshot().SpeakingClientIds) != 0 || len(stopped.GetRoleSnapshot().Clients) != 1 {
		t.Fatalf("idle source revocation retained speaking or lost visible member: %v %v", stopped, err)
	}
	access.Overrides = nil
	if _, err := authority.ChangeRolePolicy(t.Context(), owner.UserID(), authorization.RoleChange{Kind: authorization.ChannelAccessSet, ExpectedRevision: p.Revision, Channel: access}); err != nil {
		t.Fatal(err)
	}
	// No bus notification: idle refresh must still remove newly hidden data.
	revoked, err := stream.Recv()
	if err != nil || revoked.GetId() != "7" || revoked.GetType() != noxav1.EventType_EVENT_TYPE_ROLE_SNAPSHOT || len(revoked.GetRoleSnapshot().GetChannels()) != 0 || len(revoked.GetRoleSnapshot().GetClients()) != 0 {
		t.Fatalf("idle stream retained revoked visibility: %v %v", revoked, err)
	}
	bus.Publish("speaking_changed", []byte(`{"client_id":"visible","channel_id":2,"speaking":true}`))
	if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=FALSE WHERE unique_id=$1", member.UniqueID()); err != nil {
		t.Fatal(err)
	}
	event, err := stream.Recv()
	var timeout net.Error
	if err == nil || (transport == "grpc" && status.Code(err) != codes.Unauthenticated) || (errors.As(err, &timeout) && timeout.Timeout()) {
		t.Fatalf("disabled integration retained feed: %v %v", event, err)
	}
}
