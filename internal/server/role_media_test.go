package server

import (
	"context"
	"testing"
	"time"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/state"
	"noxa/internal/webrtc"
)

func TestRoleMediaDoesNotWaitForPolicyReconciliation(t *testing.T) {
	authority, err := authorization.NewAuthority(t.Context(), serverRoleFixture(), func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
	defer env.stop()
	done := make(chan struct{})
	blocked := false
	err = authority.WithExclusivePolicy(t.Context(), func(*authorization.RoleEvaluator) error {
		go func() {
			defer close(done)
			_ = env.srv.guardRoleMedia(webrtc.MediaDelivery{}, func() error { t.Error("media emitted during reconciliation"); return nil })
		}()
		select {
		case <-done:
		case <-time.After(time.Second):
			blocked = true
		}
		return nil
	})
	<-done
	if err != nil {
		t.Fatal(err)
	}
	if blocked {
		t.Fatal("media waited for reconciliation; closing its pacer would deadlock")
	}
}

func TestRoleMediaDeliveryChecksSlotScopesAndLiveRevocation(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect, authorization.Speak}
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
	defer env.stop()
	env.state.AddChannel(testChannel(1))
	memberConn, memberID := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = memberConn.Close() }()
	ownerConn, ownerID := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = ownerConn.Close() }()
	for _, id := range []string{memberID, ownerID} {
		if err := env.state.MoveClient(id, 1); err != nil {
			t.Fatal(err)
		}
	}
	base := webrtc.MediaDelivery{SenderID: memberID, RecipientID: ownerID, ChannelID: 1, RecipientChannelID: 1, Slot: webrtc.SlotMic}
	check := func(d webrtc.MediaDelivery, want bool) {
		t.Helper()
		written := false
		if err := env.srv.guardRoleMedia(d, func() error {
			written = true
			sender, _ := env.srv.clientByID(d.SenderID)
			if sender.roleActionMu.TryLock() {
				sender.roleActionMu.Unlock()
				t.Error("packet write did not protect source membership")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if written != want {
			t.Fatalf("delivery %+v wrote=%v want=%v", d, written, want)
		}
	}
	check(base, true)
	t.Run("concurrent packets retain shared membership leases", func(t *testing.T) {
		entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
		go func() {
			defer close(done)
			_ = env.srv.guardRoleMedia(base, func() error {
				close(entered)
				<-release
				return nil
			})
		}()
		defer func() { close(release); <-done }()
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("first packet did not enter its write")
		}
		written := false
		err := env.srv.guardRoleMedia(base, func() error { written = true; return nil })
		if err != nil || !written {
			t.Fatalf("authorized packet dropped during another packet write: wrote=%v err=%v", written, err)
		}
	})
	for _, slot := range []string{webrtc.SlotCam, webrtc.SlotScreen, webrtc.SlotScreenAudio, "unknown"} {
		d := base
		d.Slot = slot
		check(d, false)
	}
	d := base
	d.Whisper = true
	check(d, false)
	d = base
	d.ChannelID = 99
	check(d, false)
	d = base
	d.RecipientChannelID = 99
	check(d, false)
	d = base
	d.Tap = true
	d.RecipientID = "recorder"
	check(d, false)
	// The owner may publish camera, but still needs a current routing scope.
	d = base
	d.SenderID, d.RecipientID, d.Slot = ownerID, memberID, webrtc.SlotCam
	check(d, true)
	_, err = authority.ChangeRolePolicy(t.Context(), 2, authorization.RoleChange{Kind: authorization.RoleUpdate, ExpectedRevision: 1, Role: authorization.Role{ID: 10, Name: "@everyone", Permissions: []authorization.Capability{authorization.ViewChannel, authorization.Connect}}})
	if err != nil {
		t.Fatal(err)
	}
	check(base, false)
	// A receiver losing Connect cannot continue receiving the owner's media.
	_, err = authority.ChangeRolePolicy(t.Context(), 2, authorization.RoleChange{Kind: authorization.ChannelAccessSet, ExpectedRevision: 2, Channel: authorization.ChannelPolicy{ChannelID: 1, Overrides: []authorization.RoleOverride{{UserID: 1, Capability: authorization.Connect, Effect: authorization.Deny}}}})
	if err != nil {
		t.Fatal(err)
	}
	check(d, false)
}

type disconnectingRoleChannels struct {
	ChannelBackend
	disconnect func(string)
}

func (c disconnectingRoleChannels) LeaveClient(id string) (int64, error) {
	c.disconnect(id)
	return 0, state.ErrClientNotFound
}

func TestRoleMediaReconciliationAcceptsConcurrentDisconnect(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect}
	var env *testEnv
	authority, err := authorization.NewAuthority(t.Context(), backend, func(ctx context.Context, before, after *authorization.RoleEvaluator) error {
		return env.srv.reconcileRoleMedia(ctx, before, after)
	})
	if err != nil {
		t.Fatal(err)
	}
	env = startTestEnvDeps(t, nil, nil, func(d *Deps) {
		d.Authority = authority
		d.Channels = disconnectingRoleChannels{d.Channels, func(id string) { _, _ = d.State.RemoveClient(id) }}
	})
	defer env.stop()
	env.state.AddChannel(testChannel(1))
	conn, id := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = conn.Close() }()
	if err := env.state.MoveClient(id, 1); err != nil {
		t.Fatal(err)
	}
	_, err = authority.ChangeRolePolicy(t.Context(), 2, authorization.RoleChange{Kind: authorization.RoleUpdate, ExpectedRevision: 1, Role: authorization.Role{ID: 10, Name: "@everyone"}})
	if err != nil {
		t.Fatalf("ordinary disconnect disabled authority: %v", err)
	}
	if err := authority.WithAccess(t.Context(), 2, 0, authorization.ManageRoles, func(*authorization.RoleEvaluator) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestRoleMediaReconciliationDisconnectsAndStopsRevokedRecording(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect, authorization.Speak, authorization.RecordChannel}
	var env *testEnv
	authority, err := authorization.NewAuthority(t.Context(), backend, func(ctx context.Context, before, after *authorization.RoleEvaluator) error {
		return env.srv.reconcileRoleMedia(ctx, before, after)
	})
	if err != nil {
		t.Fatal(err)
	}
	env = startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
	defer env.stop()
	env.state.AddChannel(testChannel(1))
	conn, memberID := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = conn.Close() }()
	if err := env.state.MoveClient(memberID, 1); err != nil {
		t.Fatal(err)
	}
	env.state.SetSharing(memberID, true)
	env.state.SetPrioritySpeaker(memberID, true)
	send(t, conn, netproto.MsgRecordingControl, netproto.RecordingControl{ChannelID: 1, Action: "start"})
	waitFor(t, "recording owner assigned", func() bool { owner, ok := env.srv.roleRecordingOwners.Load(int64(1)); return ok && owner.(int64) == 1 })
	delivery := webrtc.MediaDelivery{SenderID: memberID, RecipientID: "recorder", ChannelID: 1, RecipientChannelID: 1, Slot: webrtc.SlotMic, Tap: true}
	writes := 0
	if err := env.srv.guardRoleMedia(delivery, func() error { writes++; return nil }); err != nil || writes != 1 {
		t.Fatalf("authorized recording: writes=%d err=%v", writes, err)
	}
	_, err = authority.ChangeRolePolicy(t.Context(), 2, authorization.RoleChange{Kind: authorization.RoleUpdate, ExpectedRevision: 1, Role: authorization.Role{ID: 10, Name: "@everyone", Permissions: []authorization.Capability{authorization.ViewChannel}}})
	if err != nil {
		t.Fatal(err)
	}
	if ch, _, _ := env.state.ClientChannelState(memberID); ch != 0 {
		t.Fatal("revoked member remained in voice")
	}
	if member, _ := env.state.GetClient(memberID); member.Sharing || member.PrioritySpeaker {
		t.Fatal("revoked presence flags survived reconciliation")
	}
	if _, ok := env.srv.roleRecordingOwners.Load(int64(1)); ok {
		t.Fatal("revoked recording retained ownership")
	}
	env.recorder.mu.Lock()
	stopped := len(env.recorder.stopped) == 1 && env.recorder.stopped[0] == 1
	env.recorder.mu.Unlock()
	if !stopped {
		t.Fatal("revoked recording was not stopped before commit acknowledgement")
	}
	if err := env.srv.guardRoleMedia(delivery, func() error { t.Fatal("revoked recording received packet"); return nil }); err != nil {
		t.Fatal(err)
	}
}
