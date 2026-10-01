//go:build integration

package server

import (
	"context"
	"errors"
	"testing"

	"go.uber.org/zap"
	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/config"
	"noxa/internal/netproto"
	"noxa/internal/state"
	"noxa/internal/webrtc"
)

type banRecordingBackend struct {
	RecordingBackend
	fail bool
}

func (r *banRecordingBackend) StopContext(ctx context.Context, channelID int64) error {
	if r.fail {
		return errors.New("recording cleanup unavailable")
	}
	return r.RecordingBackend.Stop(channelID)
}

func (r *banRecordingBackend) Stop(channelID int64) error {
	return r.StopContext(context.Background(), channelID)
}

func TestRoleBanRemovesEveryCanonicalSession(t *testing.T) {
	db := integrationManagementStore(t)
	a := auth.New(db, zap.NewNop())
	create := func(name string) auth.IntegrationPrincipal {
		t.Helper()
		uid, err := a.RegisterUser(t.Context(), name, "integration-password")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=TRUE WHERE unique_id=$1", uid); err != nil {
			t.Fatal(err)
		}
		p, err := a.AuthenticateIntegration(t.Context(), uid, "integration-password", "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	owner, member := create("ban-owner"), create("ban-member")
	backend := serverRoleFixture()
	backend.policy.OwnerID = 1000000
	backend.policy.Roles = append(backend.policy.Roles, authorization.Role{ID: 20, Name: "Ban moderator", Position: 1, Permissions: []authorization.Capability{authorization.BanMembers}})
	backend.policy.Members = []authorization.RoleMember{{UserID: owner.UserID(), RoleIDs: []int64{20}}}
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect, authorization.Speak, authorization.RecordChannel}
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	sm := state.New(zap.NewNop())
	sm.AddChannel(testChannel(1))
	recording := &banRecordingBackend{RecordingBackend: &fakeRecorder{}, fail: true}
	srv := New(&config.Config{FileRoot: t.TempDir()}, zap.NewNop(), &Deps{Authority: authority, Auth: a, Bans: db, Groups: db, State: sm, Recorder: recording})
	add := func(id, uid string, userID int64) *Client {
		c := &Client{ID: id, Conn: newBlockingTCPConn()}
		c.setIdentity(uid, id, userID, false)
		srv.register(c)
		sm.AddClient(&state.Client{ClientID: id, UniqueID: uid, UserID: userID, Nickname: id})
		if err := sm.MoveClient(id, 1); err != nil {
			t.Fatal(err)
		}
		return c
	}
	actor := add("owner", owner.UniqueID(), owner.UserID())
	actorConn := &sessionResponseConn{blockingTCPConn: newBlockingTCPConn()}
	actor.Conn = actorConn
	target := add("target", member.UniqueID(), member.UserID())
	second := add("second", member.UniqueID(), member.UserID())
	guest := add("guest", "guest-other", 0)
	otherGuest := add("other-guest", "guest-unrelated", 0)
	sm.SetStatus(target.ID, "invisible", "")
	for _, id := range []string{target.ID, "missing"} {
		frame, err := netproto.Encode(netproto.MsgKickClient, netproto.KickClient{ClientID: id, Ban: true})
		if err != nil {
			t.Fatal(err)
		}
		if err := srv.handleKickClient(t.Context(), actor, frame); err != nil {
			t.Fatal(err)
		}
		if response := readSessionError(t, actorConn); response.Code != errCodePermissionDenied {
			t.Fatal(response)
		}
	}
	sm.SetStatus(target.ID, "online", "")
	for _, seconds := range []int64{-1, maxRoleBanSeconds + 1} {
		frame, err := netproto.Encode(netproto.MsgKickClient, netproto.KickClient{ClientID: target.ID, Ban: true, DurationSeconds: seconds})
		if err != nil {
			t.Fatal(err)
		}
		if err := srv.handleKickClient(t.Context(), actor, frame); err != nil {
			t.Fatal(err)
		}
		if response := readSessionError(t, actorConn); response.Code != errCodeMalformed {
			t.Fatal(response)
		}
		if !target.isAuthed() || !second.isAuthed() {
			t.Fatal("invalid duration revoked sessions")
		}
	}
	srv.roleRecordingOwners.Store(int64(1), member.UserID())
	frame, err := netproto.Encode(netproto.MsgKickClient, netproto.KickClient{ClientID: target.ID, Ban: true, DurationSeconds: 60, Reason: "Account suspended"})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.handleKickClient(t.Context(), actor, frame); err != nil {
		t.Fatal(err)
	}
	if response := readSessionError(t, actorConn); response.Code != errCodeUnavailable {
		t.Fatal("recording failure must report saved/pending", response)
	}
	if _, exists := srv.roleRecordingOwners.Load(int64(1)); !exists {
		t.Fatal("recording failure lost retry ownership")
	}
	written := false
	if err := srv.guardRoleMedia(webrtc.MediaDelivery{SenderID: actor.ID, ChannelID: 1, Slot: webrtc.SlotMic, Tap: true}, func() error { written = true; return nil }); err != nil || written {
		t.Fatalf("banned account recording still received packets: %v", err)
	}
	recording.fail = false
	if err := authority.WithExclusivePolicy(t.Context(), func(e *authorization.RoleEvaluator) error { return srv.reconcileRoleMedia(t.Context(), e, e) }); err != nil {
		t.Fatal(err)
	}
	if _, exists := srv.roleRecordingOwners.Load(int64(1)); exists {
		t.Fatal("retry did not clear revoked recording")
	}
	for _, c := range []*Client{target, second} {
		if _, exists := sm.GetClient(c.ID); exists || c.isAuthed() {
			t.Errorf("banned session survived: %s", c.ID)
		}
		if _, exists := srv.clientByID(c.ID); exists {
			t.Errorf("banned session remained registered: %s", c.ID)
		}
	}
	if !guest.isAuthed() || !actor.isAuthed() {
		t.Fatal("ban removed an unrelated identity")
	}
	if err := a.ValidateIntegration(t.Context(), member); !errors.Is(err, auth.ErrIntegrationDenied) {
		t.Fatalf("banned integration remains admitted: %v", err)
	}
	ban, err := a.LookupActiveBan(t.Context(), member.UniqueID(), "127.0.0.1")
	if err != nil || ban == nil {
		t.Fatalf("ban not durable: %+v %v", ban, err)
	}
	var count int
	if err := db.DB().QueryRowContext(t.Context(), "SELECT COUNT(*) FROM audit_log WHERE action='ban' AND actor_unique_id=$1 AND target=$2", owner.UniqueID(), member.UniqueID()).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("account ban must have one canonical audit, got %d", count)
	}
	// A failed insert cannot be reported as saved, but an uncertain commit
	// must not leave the authorized target active. Guest ID zero is not a group.
	if _, err := db.DB().ExecContext(t.Context(), "ALTER TABLE bans ADD CONSTRAINT reject_guest_test CHECK (value <> 'guest-other')"); err != nil {
		t.Fatal(err)
	}
	failed, err := netproto.Encode(netproto.MsgKickClient, netproto.KickClient{ClientID: guest.ID, Ban: true, Reason: "Rejected insert"})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.handleKickClient(t.Context(), actor, failed); err != nil {
		t.Fatal(err)
	}
	if response := readSessionError(t, actorConn); response.Code != errCodeUnavailable {
		t.Fatal(response)
	}
	if guest.isAuthed() || !otherGuest.isAuthed() {
		t.Fatal("failed ban did not isolate the exact guest UID")
	}
	if ban, err := a.LookupActiveBan(t.Context(), "guest-other", ""); err != nil || ban != nil {
		t.Fatalf("rejected insert became a ban: %+v %v", ban, err)
	}
}
