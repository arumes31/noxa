package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"noxa/internal/authorization"
	"noxa/internal/filetransfer"
	"noxa/internal/netproto"
	"noxa/internal/state"
	"noxa/internal/webrtc"
)

type sessionResponseConn struct {
	*blockingTCPConn
	output bytes.Buffer
}

func (c *sessionResponseConn) Write(p []byte) (int, error) { return c.output.Write(p) }

func revokedRequestClient(env *testEnv) (*Client, *sessionResponseConn) {
	conn := &sessionResponseConn{blockingTCPConn: newBlockingTCPConn()}
	client := &Client{ID: "revoked-test-session", Conn: conn}
	client.setIdentity("user-uid", "User", 2, false)
	env.state.AddClient(&state.Client{ClientID: client.ID, UserID: 2, UniqueID: "user-uid"})
	client.revokeSession()
	return client, conn
}

func readSessionError(t *testing.T, conn *sessionResponseConn) netproto.Error {
	t.Helper()
	frame, err := netproto.ReadFrame(&conn.output)
	if err != nil {
		t.Fatal(err)
	}
	if netproto.MessageType(frame.Type) != netproto.MsgError {
		t.Fatalf("unexpected response: %v", frame.Type)
	}
	var response netproto.Error
	if err := netproto.Decode(frame, &response); err != nil {
		t.Fatal(err)
	}
	return response
}

type failingKickVoice struct{ VoiceBackend }

func (v failingKickVoice) ClosePeer(id string) error {
	_ = v.VoiceBackend.ClosePeer(id)
	return errors.New("voice transport close failed")
}

func TestRoleKickRotatesKeysDespiteVoiceCleanupFailure(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect}
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority; d.Voice = failingKickVoice{d.Voice} })
	defer env.stop()
	env.state.AddChannel(testChannel(1))
	conn, id := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = conn.Close() }()
	if err := env.state.MoveClient(id, 1); err != nil {
		t.Fatal(err)
	}
	before, _, err := env.srv.chatKeys.EnsureScope(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	err = env.srv.withExclusiveRolePolicy(t.Context(), func(ctx context.Context) error {
		env.srv.roleMetadataMu.Lock()
		defer env.srv.roleMetadataMu.Unlock()
		pending, err := env.srv.kickRoleMember(ctx, ctx.Value(roleLeaseKey{}).(roleLease).evaluator, 2, "user-uid", "", id, "Leave")
		if !pending {
			t.Error("voice failure was not reported")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	after, _, err := env.srv.chatKeys.current(t.Context(), 1)
	if err != nil || after <= before {
		t.Fatalf("failed voice cleanup skipped rotation: %d -> %d: %v", before, after, err)
	}
}

func TestRoleRevokedSessionCannotCommitQueuedRequests(t *testing.T) {
	authority, err := authorization.NewAuthority(t.Context(), serverRoleFixture(), func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	keys := newFakePreKeys()
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority, d.PreKeys = authority, keys })
	defer env.stop()
	client, conn := revokedRequestClient(env)
	for _, tc := range []struct {
		name    string
		mt      netproto.MessageType
		payload any
		handler preKeyHandler
	}{
		{"key publish", netproto.MsgKeyPublish, netproto.KeyPublish{PublicKey: base64.StdEncoding.EncodeToString(make([]byte, 32))}, env.srv.handleKeyPublish},
		{"key lookup", netproto.MsgKeyRequest, netproto.KeyRequest{UniqueID: "admin-uid"}, env.srv.handleKeyRequest},
		{"prekey publish", netproto.MsgPreKeyPublish, validPreKeyPublish(t), env.srv.handlePreKeyPublish},
		{"prekey consume", netproto.MsgPreKeyQuery, netproto.PreKeyQuery{UniqueID: "admin-uid"}, env.srv.handlePreKeyQuery},
		{"direct message", netproto.MsgChatSend, netproto.ChatSend{ToUniqueID: "admin-uid", Text: "ciphertext", Enc: true}, env.srv.handleChatSend},
		{"complaint", netproto.MsgComplaint, netproto.Complaint{TargetUniqueID: "admin-uid", Reason: "Report"}, env.srv.handleComplaint},
		{"rules acceptance", netproto.MsgServerRulesAccept, netproto.ServerRulesAccept{Hash: "current"}, env.srv.handleServerRulesAccept},
		{"typing", netproto.MsgTyping, netproto.Typing{ToUniqueID: "admin-uid"}, env.srv.handleTyping},
		{"delivered receipt", netproto.MsgChatDelivered, netproto.ChatDelivered{ToUniqueID: "admin-uid", ClientMsgID: "message"}, env.srv.handleChatDelivered},
		{"read receipt", netproto.MsgChatRead, netproto.ChatRead{ToUniqueID: "admin-uid", ClientMsgID: "message"}, env.srv.handleChatRead},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frame, err := netproto.Encode(tc.mt, tc.payload)
			if err != nil {
				t.Fatal(err)
			}
			if err := tc.handler(t.Context(), client, frame); err != nil {
				t.Fatal(err)
			}
			got := readSessionError(t, conn)
			if got.Code != errCodePermissionDenied {
				t.Fatal(got)
			}
		})
	}
	if keys.publishCount() != 0 || keys.consumeCount() != 0 {
		t.Fatal("revoked session modified prekeys")
	}
	if current, _ := env.state.GetClient(client.ID); current.E2EPublicKey != "" {
		t.Fatal("revoked session published a key")
	}
}

type pausedSessionKeyAuth struct {
	AuthBackend
	entered chan struct{}
	release chan struct{}
	writes  atomic.Int32
}

func (a *pausedSessionKeyAuth) SetE2EPublicKey(ctx context.Context, id int64, key string) error {
	close(a.entered)
	select {
	case <-a.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	a.writes.Add(1)
	return a.AuthBackend.SetE2EPublicKey(ctx, id, key)
}

func TestRoleKickWaitsForKeyPublication(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel}
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	paused := &pausedSessionKeyAuth{entered: make(chan struct{}), release: make(chan struct{})}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { paused.AuthBackend = d.Auth; d.Auth, d.Authority = paused, authority })
	defer env.stop()
	conn, id := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = conn.Close() }()
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(paused.release) })
	send(t, conn, netproto.MsgKeyPublish, netproto.KeyPublish{PublicKey: base64.StdEncoding.EncodeToString(make([]byte, 32))})
	select {
	case <-paused.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("publication did not enter backend")
	}
	kicked := make(chan error, 1)
	go func() {
		kicked <- env.srv.withExclusiveRolePolicy(t.Context(), func(ctx context.Context) error {
			env.srv.roleMetadataMu.Lock()
			defer env.srv.roleMetadataMu.Unlock()
			_, err := env.srv.kickRoleMember(ctx, ctx.Value(roleLeaseKey{}).(roleLease).evaluator, 2, "user-uid", "", id, "Leave")
			return err
		})
	}()
	select {
	case err := <-kicked:
		t.Fatalf("kick crossed unfinished key persistence: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(paused.release) })
	select {
	case err := <-kicked:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("kick did not finish after publication")
	}
	if paused.writes.Load() != 1 {
		t.Fatal("kick returned before publication completed")
	}
}

func TestRoleKickQuiescesFileEffectsAndCleansOnce(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect, authorization.Speak, authorization.DownloadFiles}
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	ft := &disconnectFileTransfer{revoked: make(chan func(filetransfer.Principal, int64, string) bool, 8)}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority, d.FileTransfer = authority, ft })
	defer env.stop()
	env.state.AddChannel(testChannel(1))
	conn, id := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = conn.Close() }()
	target, _ := env.srv.clientByID(id)
	if err := env.state.MoveClient(id, 1); err != nil {
		t.Fatal(err)
	}
	principal := filetransfer.Principal{UserID: 1, SessionID: id}
	entered, release, fileDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	go func() {
		fileDone <- env.srv.guardRoleFileTransfer(t.Context(), principal, 1, "download", func(ctx context.Context) error {
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	<-entered
	kickDone := make(chan error, 1)
	go func() {
		kickDone <- env.srv.withExclusiveRolePolicy(t.Context(), func(ctx context.Context) error {
			env.srv.roleMetadataMu.Lock()
			defer env.srv.roleMetadataMu.Unlock()
			pending, err := env.srv.kickRoleMember(ctx, ctx.Value(roleLeaseKey{}).(roleLease).evaluator, 2, "user-uid", "", id, "Leave server")
			if pending {
				return errors.New("unexpected cleanup failure")
			}
			return err
		})
	}()
	select {
	case err := <-kickDone:
		t.Fatalf("kick crossed active protected effect: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(release) })
	if err := <-fileDone; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-kickDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("kick deadlocked with cleanup")
	}
	if target.isAuthed() || !target.sessionRevoked() {
		t.Fatal("removed session remained eligible")
	}
	if _, ok := env.srv.clientByID(id); ok {
		t.Fatal("removed session stayed in registry")
	}
	if _, ok := env.state.GetClient(id); ok {
		t.Fatal("removed session stayed in membership")
	}
	select {
	case allowed := <-ft.revoked:
		if allowed(principal, 1, "download") || !allowed(filetransfer.Principal{UserID: 1, SessionID: "other"}, 1, "download") {
			t.Fatal("file revocation targeted the wrong session")
		}
	default:
		t.Fatal("kick returned before file revocation")
	}
	if err := env.srv.guardRoleFileTransfer(t.Context(), principal, 1, "download", func(context.Context) error { t.Error("revoked file effect ran"); return nil }); !errors.Is(err, filetransfer.ErrAccessRevoked) {
		t.Fatal(err)
	}
	if err := env.srv.guardRoleMedia(webrtc.MediaDelivery{SenderID: id, RecipientID: id, ChannelID: 1, RecipientChannelID: 1, Slot: webrtc.SlotMic}, func() error { t.Error("revoked media delivered"); return nil }); err != nil {
		t.Fatal(err)
	}
	env.srv.onDisconnect(target)
	env.srv.onDisconnect(target)
	env.voice.mu.Lock()
	count := 0
	for _, closed := range env.voice.closed {
		if closed == id {
			count++
		}
	}
	env.voice.mu.Unlock()
	if count != 1 {
		t.Fatalf("voice teardown repeated %d times", count)
	}
	policy, err := authority.RolePolicy(t.Context())
	if err != nil || policy.Revision != 1 {
		t.Fatalf("session kick altered role policy: %+v %v", policy, err)
	}
}

func TestRoleRevokedNativeWritersCannotCommit(t *testing.T) {
	backend := serverRoleFixture()
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	var writer *memoryRoleChannels
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) {
		d.Authority = authority
		writer = &memoryRoleChannels{ChannelBackend: d.Channels, backend: backend}
		d.Channels = writer
	})
	defer env.stop()
	client, conn := revokedRequestClient(env)
	roleFrame, err := netproto.Encode(netproto.MsgRoleChange, authorization.RoleChange{Kind: authorization.RoleCreate, ExpectedRevision: 1, Role: authorization.Role{Name: "Must not exist"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := env.srv.handleRoleChange(t.Context(), client, roleFrame); err != nil {
		t.Fatal(err)
	}
	if got := readSessionError(t, conn); got.Code != errCodePermissionDenied {
		t.Fatal(got)
	}
	channelFrame, err := netproto.Encode(netproto.MsgRoleChannelChange, netproto.RoleChannelChange{Kind: authorization.ChannelCreate, ExpectedRevision: 1, ChannelType: 2, Settings: &netproto.RoleChannelSettings{Name: "Must not exist"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := env.srv.handleRoleChannelChange(t.Context(), client, channelFrame); err != nil {
		t.Fatal(err)
	}
	if got := readSessionError(t, conn); got.Code != errCodePermissionDenied {
		t.Fatal(got)
	}
	policy, err := authority.RolePolicy(t.Context())
	if err != nil || policy.Revision != 1 || writer.created != nil {
		t.Fatalf("revoked queued mutation committed: %+v %v", policy, err)
	}
}

func TestRoleServerKickNormalizesInvisibleAndMissingTargets(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.OwnerID = 3
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect}
	backend.policy.Roles = append(backend.policy.Roles, authorization.Role{ID: 20, Name: "Moderator", Position: 1, Permissions: []authorization.Capability{authorization.KickMembers}})
	backend.policy.Members = []authorization.RoleMember{{UserID: 1, RoleIDs: []int64{20}}}
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
	defer env.stop()
	env.state.AddChannel(testChannel(1))
	moderator, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = moderator.Close() }()
	member, memberID := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = member.Close() }()
	if err := env.state.MoveClient(memberID, 1); err != nil {
		t.Fatal(err)
	}
	env.state.SetStatus(memberID, "invisible", "")
	for _, id := range []string{memberID, "missing"} {
		send(t, moderator, netproto.MsgKickClient, netproto.KickClient{ClientID: id, FromServer: true})
		if got := readError(t, moderator); got.Code != errCodePermissionDenied {
			t.Fatal(got)
		}
	}
}
