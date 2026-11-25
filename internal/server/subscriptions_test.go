package server

import (
	"context"
	"net"
	"reflect"
	"strings"
	"testing"

	"noxa/internal/authorization"
	"noxa/internal/broadcast"
	"noxa/internal/netproto"
	"noxa/internal/state"
)

// TestChannelScopedBroadcastTargetsReverseIndex verifies broadcastChannelScoped
// reaches a channel's members and subscribers (deduped) and no one else, using
// the reverse index rather than every connected client.
func TestChannelScopedBroadcastTargetsReverseIndex(t *testing.T) {
	backend := serverRoleFixture() // @everyone (role 10) on channel 1
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel}
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	sm := state.New(testLogger())
	sm.AddChannel(&state.Channel{ChannelID: 1, Name: "ch"})
	bc := broadcast.New(testLogger(), sm)
	srv := &TCPServer{deps: &Deps{State: sm, Broadcast: bc, Authority: authority}, clients: map[string]*Client{}}

	recv := map[string]<-chan []byte{}
	for cid, uid := range map[string]int64{"m1": 11, "m2": 12, "sub": 13, "x1": 14, "x2": 15} {
		sm.AddClient(&state.Client{ClientID: cid, UserID: uid, UniqueID: cid + "-uid"})
		ch, err := bc.Register(cid)
		if err != nil {
			t.Fatal(err)
		}
		recv[cid] = ch
		srv.clients[cid] = &Client{ID: cid, UserID: uid, UniqueID: cid + "-uid"}
	}
	if err := sm.MoveClient("m1", 1); err != nil {
		t.Fatal(err)
	}
	if err := sm.MoveClient("m2", 1); err != nil {
		t.Fatal(err)
	}
	// A member that also subscribed must still be delivered to exactly once.
	sm.Subscribe("m2", []int64{1})
	sm.Subscribe("sub", []int64{1})

	srv.broadcastChannelScoped(t.Context(), 1, []byte(`{"type":"user_joined"}`))

	for _, cid := range []string{"m1", "m2", "sub"} {
		select {
		case <-recv[cid]:
		default:
			t.Errorf("%s did not receive channel-scoped broadcast", cid)
		}
		select {
		case <-recv[cid]:
			t.Errorf("%s received the broadcast more than once", cid)
		default:
		}
	}
	for _, cid := range []string{"x1", "x2"} {
		select {
		case <-recv[cid]:
			t.Errorf("%s received a broadcast it should not have", cid)
		default:
		}
	}
}

func TestSubscriptionSetIsAuthoritativeDeduplicatedAndSorted(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	conn, clientID := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = conn.Close() }()
	client, ok := env.srv.clientByID(clientID)
	if !ok {
		t.Fatal("authenticated client is not registered")
	}
	env.state.AddChannel(&state.Channel{ChannelID: 7, Name: "seven"})
	env.state.AddChannel(&state.Channel{ChannelID: 3, Name: "three"})
	env.state.Subscribe(clientID, []int64{7, 3, 7})
	if got, want := env.srv.subscriptionSet(client), []int64{3, 7}; !reflect.DeepEqual(got, want) {
		t.Fatalf("subscription set = %v, want %v", got, want)
	}
	env.state.Unsubscribe(clientID, []int64{3})
	if got, want := env.srv.subscriptionSet(client), []int64{7}; !reflect.DeepEqual(got, want) {
		t.Fatalf("subscription set after unsubscribe = %v, want %v", got, want)
	}
}

func TestChannelSubscribeRejectsMissingKeyAndOver64Targets(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	conn, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = conn.Close() }()
	ids := make([]int64, maxSubscribeTargets+1)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	send(t, conn, netproto.MsgChannelSubscribe, netproto.ChannelSubscribe{Subscribe: true, ChannelIDs: ids})
	if got := readError(t, conn); got.Code != errCodeMalformed {
		t.Fatalf("65 targets error = %+v, want malformed", got)
	}
	env.state.AddChannel(&state.Channel{ChannelID: 99, Name: "target"})
	send(t, conn, netproto.MsgChannelSubscribe, netproto.ChannelSubscribe{Subscribe: true, ChannelIDs: []int64{99}})
	if got := readError(t, conn); got.Code != errCodePermissionDenied {
		t.Fatalf("missing encryption key error = %+v, want permission denied", got)
	}
}

func TestChannelSubscribeDeliversKeysHonorsCurrentChannelAndRevokes(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.OwnerID = 1
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel}
	backend.policy.Channels = []authorization.ChannelPolicy{
		{ChannelID: 40},
		{ChannelID: 41, Overrides: []authorization.RoleOverride{{UserID: 2, Capability: authorization.ViewChannel, Effect: authorization.Deny}}},
	}
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(deps *Deps) { deps.Authority = authority })
	defer env.stop()
	env.state.AddChannel(&state.Channel{ChannelID: 40, Name: "forty"})
	env.state.AddChannel(&state.Channel{ChannelID: 41, Name: "forty-one"})
	pub, _ := testX25519(t)
	conn, authResp := dialSubscriptionClient(t, env.addr, "user-uid", pub)
	defer func() { _ = conn.Close() }()

	// A mixed request must accept the entitled channel, refuse denied and absent
	// targets on the wire, and still deliver the accepted channel's key.
	send(t, conn, netproto.MsgChannelSubscribe, netproto.ChannelSubscribe{Subscribe: true, ChannelIDs: []int64{40, 41, 404}})
	reply, keys, errs := readSubscriptionReply(t, conn)
	if !reflect.DeepEqual(reply.ChannelIDs, []int64{40}) || !reflect.DeepEqual(env.state.Subscriptions(authResp.ClientID), []int64{40}) {
		t.Fatalf("subscription reply/state = %v/%v, want [40]", reply.ChannelIDs, env.state.Subscriptions(authResp.ClientID))
	}
	if len(keys) != 1 || keys[0].ChannelID != 40 {
		t.Fatalf("delivered keys = %+v, want exactly channel 40", keys)
	}
	if len(errs) != 1 || errs[0].Code != errCodePermissionDenied || !strings.Contains(errs[0].Message, "unavailable") {
		t.Fatalf("mixed-target refusal = %+v, want generic permission denial", errs)
	}
	if got := env.deps.ScopeKeys.(*fakeScopeKeys).countFor(40); got != 1 {
		t.Fatalf("scope keys for accepted target = %d, want 1", got)
	}
	if err := env.state.MoveClient(authResp.ClientID, 40); err != nil {
		t.Fatal(err)
	}
	env.state.Unsubscribe(authResp.ClientID, []int64{40})
	send(t, conn, netproto.MsgChannelSubscribe, netproto.ChannelSubscribe{Subscribe: true, ChannelIDs: []int64{40}})
	reply, keys, errs = readSubscriptionReply(t, conn)
	if !reflect.DeepEqual(reply.ChannelIDs, []int64{40}) || len(env.state.Subscriptions(authResp.ClientID)) != 0 {
		t.Fatalf("current channel reply/state = %v/%v, want implicit [40] only", reply.ChannelIDs, env.state.Subscriptions(authResp.ClientID))
	}
	if len(keys) != 0 || len(errs) != 0 {
		t.Fatalf("current channel request delivered unexpected keys/errors = %+v/%+v", keys, errs)
	}
	// An explicit unsubscribe cannot remove the implicit current-channel entry.
	send(t, conn, netproto.MsgChannelSubscribe, netproto.ChannelSubscribe{Subscribe: false, ChannelIDs: []int64{40}})
	reply, keys, errs = readSubscriptionReply(t, conn)
	if !reflect.DeepEqual(reply.ChannelIDs, []int64{40}) || len(keys) != 0 || len(errs) != 0 {
		t.Fatalf("current channel unsubscribe = %+v, keys/errors=%+v/%+v", reply, keys, errs)
	}
	if err := env.state.LeaveChannel(authResp.ClientID); err != nil {
		t.Fatal(err)
	}
	env.state.Subscribe(authResp.ClientID, []int64{40})
	backend.mu.Lock()
	backend.policy.Revision++
	backend.policy.Channels[0].Overrides = []authorization.RoleOverride{{UserID: 2, Capability: authorization.ViewChannel, Effect: authorization.Deny}}
	backend.mu.Unlock()
	if err := authority.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := env.srv.channelSubscribers(context.Background(), 40); len(got) != 0 {
		t.Fatalf("revoked subscribers = %v, want none", got)
	}
	if got := env.state.Subscriptions(authResp.ClientID); len(got) != 0 {
		t.Fatalf("revoked subscription remains = %v", got)
	}
	if pushed, _, pushedErrs := readSubscriptionReply(t, conn); len(pushed.ChannelIDs) != 0 || len(pushedErrs) != 0 {
		t.Fatalf("revocation state = %+v, errors=%+v, want empty state without error", pushed, pushedErrs)
	}
}

func TestChannelSubscribeAllows64AndRejects65thStandingSubscription(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.OwnerID = 1
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel}
	backend.policy.Channels = nil
	for channelID := int64(1000); channelID < 1000+maxSubscriptions; channelID++ {
		backend.policy.Channels = append(backend.policy.Channels, authorization.ChannelPolicy{ChannelID: channelID})
	}
	backend.policy.Channels = append(backend.policy.Channels, authorization.ChannelPolicy{ChannelID: 2000})
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(deps *Deps) { deps.Authority = authority })
	defer env.stop()
	pub, _ := testX25519(t)
	conn, authResp := dialSubscriptionClient(t, env.addr, "admin-uid", pub)
	defer func() { _ = conn.Close() }()
	ids := make([]int64, maxSubscriptions)
	for i := range ids {
		ids[i] = int64(1000 + i)
		env.state.AddChannel(&state.Channel{ChannelID: ids[i], Name: "target"})
	}
	// A full legal request is accepted on the wire, not merely via a seeded
	// state fixture. It also exercises the 64-key delivery fan-out.
	send(t, conn, netproto.MsgChannelSubscribe, netproto.ChannelSubscribe{Subscribe: true, ChannelIDs: ids})
	reply, keys, errs := readSubscriptionReply(t, conn)
	if len(reply.ChannelIDs) != maxSubscriptions || len(keys) != maxSubscriptions || len(errs) != 0 {
		t.Fatalf("64 target request reply/keys/errors = %+v/%d/%+v", reply, len(keys), errs)
	}
	env.state.AddChannel(&state.Channel{ChannelID: 2000, Name: "overflow"})
	send(t, conn, netproto.MsgChannelSubscribe, netproto.ChannelSubscribe{Subscribe: true, ChannelIDs: []int64{2000}})
	if got := readError(t, conn); got.Code != errCodePermissionDenied {
		t.Fatalf("65th standing subscription error = %+v, want permission denied", got)
	}
	if got := env.state.Subscriptions(authResp.ClientID); len(got) != maxSubscriptions {
		t.Fatalf("standing subscriptions = %d, want %d", len(got), maxSubscriptions)
	}
}

// dialSubscriptionClient consumes the authentication-time authoritative
// subscription state. Every later state frame can therefore be associated
// with the request under test, rather than being an auth-time leftover.
func dialSubscriptionClient(t *testing.T, addr, uniqueID string, pub [32]byte) (net.Conn, netproto.AuthResponse) {
	t.Helper()
	conn, response := dialAuthedX25519(t, addr, uniqueID, pub)
	initial, _, errs := readSubscriptionReply(t, conn)
	if len(initial.ChannelIDs) != 0 || len(errs) != 0 {
		_ = conn.Close()
		t.Fatalf("initial subscription state = %+v, errors=%+v, want empty", initial, errs)
	}
	return conn, response
}

// readSubscriptionReply reads the key/error frames that precede an
// authoritative subscription state. The protocol deliberately replies with
// a state even when selected targets were refused, so callers need both.
func readSubscriptionReply(t *testing.T, conn net.Conn) (netproto.SubscriptionState, []netproto.ChannelKey, []netproto.Error) {
	t.Helper()
	var keys []netproto.ChannelKey
	var errs []netproto.Error
	for {
		frame := readFrame(t, conn)
		switch netproto.MessageType(frame.Type) {
		case netproto.MsgChannelKey:
			var key netproto.ChannelKey
			if err := netproto.Decode(frame, &key); err != nil {
				t.Fatalf("decode channel key: %v", err)
			}
			keys = append(keys, key)
		case netproto.MsgError:
			var responseErr netproto.Error
			if err := netproto.Decode(frame, &responseErr); err != nil {
				t.Fatalf("decode subscription error: %v", err)
			}
			errs = append(errs, responseErr)
		case netproto.MsgSubscriptionState:
			var state netproto.SubscriptionState
			if err := netproto.Decode(frame, &state); err != nil {
				t.Fatalf("decode subscription state: %v", err)
			}
			return state, keys, errs
		}
	}
}
