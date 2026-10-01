package server

import (
	"context"
	"errors"
	"net"
	"runtime"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"noxa/internal/authorization"
	"noxa/internal/broadcast"
	"noxa/internal/config"
	"noxa/internal/netproto"
)

// Publication tests isolate the post-commit phase; they do not claim backend
// application or durable media configuration, which remain separate work.
func publishTestMediaLimits(t *testing.T, s *TCPServer, limits netproto.MediaLimits) {
	t.Helper()
	s.configSaveMu.Lock()
	defer s.configSaveMu.Unlock()
	if err := s.publishMediaLimitsLocked(limits); err != nil {
		t.Fatal(err)
	}
}

// Caller holds configMu exclusively, so the writer cannot finish its snapshot.
func awaitMediaWriterLock(t *testing.T, client *Client) {
	t.Helper()
	deadline := time.After(time.Second)
	for client.wmu.TryLock() {
		client.wmu.Unlock()
		select {
		case <-deadline:
			t.Fatal("media configuration read precedes ownership of its writer")
		default:
			runtime.Gosched()
		}
	}
}

type pausedMediaWrite struct {
	net.Conn
	entered, release chan struct{}
	once             sync.Once
}

func (c *pausedMediaWrite) Write(p []byte) (int, error) {
	c.once.Do(func() { close(c.entered); <-c.release })
	return c.Conn.Write(p)
}

func TestMediaLimitsAuthenticationOrdersConcurrentNotification(t *testing.T) {
	bc := broadcast.New(zap.NewNop(), nil)
	defer bc.Close()
	srv := New(&config.Config{}, zap.NewNop(), &Deps{Broadcast: bc})
	publishTestMediaLimits(t, srv, netproto.MediaLimits{VideoMaxBitrate: 1_000_000})
	sender, receiver := net.Pipe()
	defer func() { _ = sender.Close(); _ = receiver.Close() }()
	paused := &pausedMediaWrite{Conn: sender, entered: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	defer release.Do(func() { close(paused.release) })
	client := &Client{ID: "user", Conn: paused}
	client.setIdentity("uid", "name", 1, false)
	srv.register(client)
	out, err := bc.Register(client.ID)
	if err != nil {
		t.Fatal(err)
	}
	client.mediaLimitsReady = true
	writerDone := make(chan struct{})
	go func() { defer close(writerDone); srv.broadcastWriter(client, out) }()
	authDone := make(chan error, 1)
	go func() {
		authDone <- srv.writeAuthenticationResponse(t.Context(), client, netproto.AuthResponse{OK: true})
	}()
	select {
	case <-paused.entered:
	case <-time.After(time.Second):
		t.Fatal("auth reply did not start")
	}
	// This must not wait on the held socket write or lose the post-auth update.
	published := make(chan error, 1)
	go func() {
		srv.configSaveMu.Lock()
		defer srv.configSaveMu.Unlock()
		published <- srv.publishMediaLimitsLocked(netproto.MediaLimits{VideoMaxBitrate: 2_000_000})
	}()
	select {
	case err := <-published:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("socket write held configuration publication")
	}
	release.Do(func() { close(paused.release) })
	_ = receiver.SetReadDeadline(time.Now().Add(3 * time.Second))
	frame, err := netproto.ReadFrame(receiver)
	if err != nil {
		t.Fatal(err)
	}
	var response netproto.AuthResponse
	if frame.Type != uint16(netproto.MsgAuthResponse) || netproto.Decode(frame, &response) != nil || response.MediaLimitsRevision != 1 || response.MediaLimits.VideoMaxBitrate != 1_000_000 {
		t.Fatalf("auth reply = %+v, frame=%+v", response, frame)
	}
	frame, err = netproto.ReadFrame(receiver)
	if err != nil {
		t.Fatal(err)
	}
	var changed netproto.MediaLimitsChanged
	if frame.Type != uint16(netproto.MsgMediaLimitsChanged) || netproto.Decode(frame, &changed) != nil || changed.Revision != 2 || changed.VideoMaxBitrate != 2_000_000 {
		t.Fatalf("notification = %+v, frame=%+v", changed, frame)
	}
	if err := <-authDone; err != nil {
		t.Fatal(err)
	}
	bc.Unregister(client.ID)
	select {
	case <-writerDone:
	case <-time.After(time.Second):
		t.Fatal("broadcast writer did not finish")
	}
}

func TestMediaLimitsAuthenticationSnapshotsAfterWriteLock(t *testing.T) {
	srv := New(&config.Config{}, zap.NewNop(), &Deps{})
	publishTestMediaLimits(t, srv, netproto.MediaLimits{VideoMaxBitrate: 1_000_000})
	sender, receiver := net.Pipe()
	defer func() { _ = sender.Close(); _ = receiver.Close() }()
	client := &Client{Conn: sender}
	srv.configMu.Lock()
	var unlock sync.Once
	defer unlock.Do(srv.configMu.Unlock)
	done := make(chan error, 1)
	go func() { done <- srv.writeAuthenticationResponse(t.Context(), client, netproto.AuthResponse{OK: true}) }()
	// While the snapshot is blocked, auth must already own the socket lock.
	// Reading limits first would permit a notification to overtake that reply.
	awaitMediaWriterLock(t, client)
	srv.cfg.VideoMaxBitrate, srv.mediaLimitsRevision = 2_000_000, 2
	unlock.Do(srv.configMu.Unlock)
	_ = receiver.SetReadDeadline(time.Now().Add(3 * time.Second))
	frame, err := netproto.ReadFrame(receiver)
	if err != nil {
		t.Fatal(err)
	}
	var response netproto.AuthResponse
	if err := netproto.Decode(frame, &response); err != nil || response.MediaLimitsRevision != 2 || response.MediaLimits == nil || response.MediaLimits.VideoMaxBitrate != 2_000_000 {
		t.Fatalf("stale baseline after waiting for writer: %+v, %v", response, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestMediaLimitsPublicationAndAuthenticationOverTLS(t *testing.T) {
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) {
		authority, err := authorization.NewAuthority(t.Context(), serverRoleFixture(), func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
		if err != nil {
			t.Fatal(err)
		}
		d.Authority = authority
	})
	defer env.stop()
	conn, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = conn.Close() }()
	want := netproto.MediaLimits{VideoMaxBitrate: 1_000_000, VideoMaxWidth: 640, VideoMaxHeight: 360}
	publishTestMediaLimits(t, env.srv, want)
	var update netproto.MediaLimitsChanged
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgMediaLimitsChanged), &update); err != nil || update.Revision != 1 || update.MediaLimits != want {
		t.Fatalf("update = %+v, %v", update, err)
	}
	second := dialRetry(t, env.addr)
	defer func() { _ = second.Close() }()
	send(t, second, netproto.MsgAuthenticate, netproto.Authenticate{Username: "admin-uid", Password: "pw", AuthorizationModels: []string{netproto.AuthorizationModelRolesV1}})
	var response netproto.AuthResponse
	if err := netproto.Decode(readOfType(t, second, netproto.MsgAuthResponse), &response); err != nil || !response.OK || response.MediaLimitsRevision != 1 || response.MediaLimits == nil || *response.MediaLimits != want {
		t.Fatalf("auth baseline = %+v, %v", response, err)
	}
	// Server-originated notifications are not a client mutation API.
	send(t, conn, netproto.MsgMediaLimitsChanged, netproto.MediaLimitsChanged{Revision: 99})
	_ = readOfType(t, conn, netproto.MsgError)
	if env.srv.mediaLimitsSnapshot() != update {
		t.Fatal("client forged media settings")
	}
}

func TestMediaLimitsPublicationQueueFailuresAndUnchangedUpdates(t *testing.T) {
	bc := broadcast.New(zap.NewNop(), nil)
	defer bc.Close()
	srv := New(&config.Config{}, zap.NewNop(), &Deps{Broadcast: bc})
	makeClient := func(id string, authed bool) (*Client, *blockingTCPConn) {
		conn := newBlockingTCPConn()
		client := &Client{ID: id, Conn: conn}
		if authed {
			client.setIdentity(id, id, 1, false)
		}
		client.mediaLimitsReady = authed
		t.Cleanup(client.stopMediaLimitsDelivery)
		srv.register(client)
		return client, conn
	}
	full, fullConn := makeClient("full", true)
	_, err := bc.Register(full.ID)
	if err != nil {
		t.Fatal(err)
	}
	for {
		err := bc.BroadcastToClient(full.ID, []byte(`{}`))
		if errors.Is(err, broadcast.ErrChannelFull) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	_, missingConn := makeClient("missing", true)
	_, pendingConn := makeClient("pending", false)
	joining, joiningConn := makeClient("joining", true)
	joining.mediaLimitsReady = false
	revoked, revokedConn := makeClient("revoked", true)
	revoked.revokeSession()
	healthy, healthyConn := makeClient("healthy", true)
	out, err := bc.Register(healthy.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := netproto.MediaLimits{VideoMaxBitrate: 1_000_000, VideoMaxWidth: 640, VideoMaxHeight: 360}
	publishTestMediaLimits(t, srv, want)
	for _, conn := range []*blockingTCPConn{fullConn, missingConn} {
		select {
		case <-conn.closed:
		default:
			t.Error("failed queue retained a stale connection")
		}
	}
	for _, conn := range []*blockingTCPConn{pendingConn, joiningConn, revokedConn, healthyConn} {
		select {
		case <-conn.closed:
			t.Error("publication closed an unaffected connection")
		default:
		}
	}
	select {
	case payload := <-out:
		if string(payload) != mediaLimitsNotification {
			t.Fatal("queue stored something other than the refresh marker")
		}
	default:
		t.Fatal("publication omitted the refresh marker")
	}
	before := srv.mediaLimitsSnapshot()
	publishTestMediaLimits(t, srv, want)
	if srv.mediaLimitsSnapshot() != before || len(out) != 0 {
		t.Fatal("identical limits created another revision or notification")
	}
	srv.configSaveMu.Lock()
	err = srv.publishMediaLimitsLocked(netproto.MediaLimits{VideoMaxWidth: 640})
	srv.configSaveMu.Unlock()
	if !errors.Is(err, authorization.ErrRoleInvalid) || srv.mediaLimitsSnapshot() != before || len(out) != 0 {
		t.Fatal("invalid publication changed state")
	}
	srv.configMu.Lock()
	srv.mediaLimitsRevision = ^uint64(0)
	srv.configMu.Unlock()
	srv.configSaveMu.Lock()
	err = srv.publishMediaLimitsLocked(netproto.MediaLimits{})
	srv.configSaveMu.Unlock()
	if err == nil || srv.mediaLimitsSnapshot().MediaLimits != want || srv.mediaLimitsSnapshot().Revision != ^uint64(0) || len(out) != 0 {
		t.Fatal("exhausted revision wrapped or published changed values")
	}
}

func TestMediaLimitsNotificationUsesLatestSnapshotAndBoundedWrite(t *testing.T) {
	srv := New(&config.Config{VideoMaxBitrate: 1_000_000}, zap.NewNop(), &Deps{})
	srv.mediaLimitsRevision = 1
	sender, receiver := net.Pipe()
	defer func() { _ = sender.Close(); _ = receiver.Close() }()
	client := &Client{ID: "user", Conn: sender}
	client.setIdentity("uid", "name", 1, false)
	srv.configMu.Lock()
	var unlock sync.Once
	defer unlock.Do(srv.configMu.Unlock)
	done := make(chan error, 1)
	go func() { done <- srv.writeCurrentMediaLimits(t.Context(), client) }()
	// As with authentication, prove that the writer owns the socket before
	// allowing its snapshot to complete.
	awaitMediaWriterLock(t, client)
	srv.cfg.VideoMaxBitrate, srv.mediaLimitsRevision = 2_000_000, 2
	unlock.Do(srv.configMu.Unlock)
	_ = receiver.SetReadDeadline(time.Now().Add(time.Second))
	frame, err := netproto.ReadFrame(receiver)
	if err != nil {
		t.Fatal(err)
	}
	var update netproto.MediaLimitsChanged
	if err := netproto.Decode(frame, &update); err != nil || update.Revision != 2 || update.VideoMaxBitrate != 2_000_000 {
		t.Fatal("queued notification used stale limits")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	go func() { done <- srv.writeCurrentMediaLimits(ctx, client) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("unread write succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("notification ignored its deadline")
	}
}

func TestMediaLimitsDeadlineCoversBlockedEarlierBroadcast(t *testing.T) {
	srv := New(&config.Config{VideoMaxBitrate: 1_000_000}, zap.NewNop(), &Deps{})
	srv.mediaLimitsRevision = 1
	sender, receiver := net.Pipe()
	defer func() { _ = sender.Close(); _ = receiver.Close() }()
	observed := &pausedMediaWrite{Conn: sender, entered: make(chan struct{}), release: make(chan struct{})}
	close(observed.release)
	client := &Client{ID: "slow", Conn: observed, mediaLimitsReady: true}
	client.setIdentity("uid", "name", 1, false)
	t.Cleanup(client.stopMediaLimitsDelivery)
	srv.register(client)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.writeMessage(client, netproto.MsgEvent, map[string]any{"type": "old_event"})
	}()
	select {
	case <-observed.entered:
	case <-time.After(time.Second):
		t.Fatal("earlier broadcast did not start")
	}
	if !client.beginMediaLimitsDelivery(1, 30*time.Millisecond) {
		t.Fatal("delivery deadline did not start")
	}
	// A second publication must retain the first deadline, not extend it.
	srv.notifyMediaLimitsChanged()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("earlier broadcast hid pending limits beyond deadline")
	}
}

func TestMediaLimitsDeliveryCompletionAndObsoleteTimer(t *testing.T) {
	conn := newBlockingTCPConn()
	client := &Client{Conn: conn, mediaLimitsReady: true}
	client.setIdentity("uid", "name", 1, false)
	t.Cleanup(client.stopMediaLimitsDelivery)
	if !client.beginMediaLimitsDelivery(1, time.Hour) {
		t.Fatal("first delivery did not start")
	}
	firstGeneration := client.mediaLimitsTimerGeneration
	client.completeMediaLimitsDelivery(1)
	if !client.beginMediaLimitsDelivery(2, time.Hour) {
		t.Fatal("second delivery did not start")
	}
	client.expireMediaLimitsDelivery(firstGeneration)
	client.completeMediaLimitsDelivery(1)
	if client.mediaLimitsPending != 2 {
		t.Fatal("obsolete timer or completion cleared newer delivery")
	}
	select {
	case <-conn.closed:
		t.Fatal("obsolete timer closed healthy connection")
	default:
	}
	client.completeMediaLimitsDelivery(2)
	client.expireMediaLimitsDelivery(client.mediaLimitsTimerGeneration)
	select {
	case <-conn.closed:
		t.Fatal("completed delivery still closed connection")
	default:
	}
	if !client.beginMediaLimitsDelivery(3, time.Hour) {
		t.Fatal("third delivery did not start")
	}
	client.expireMediaLimitsDelivery(client.mediaLimitsTimerGeneration)
	select {
	case <-conn.closed:
	default:
		t.Fatal("expired delivery retained stale connection")
	}
	if client.beginMediaLimitsDelivery(4, time.Hour) {
		t.Fatal("expired connection accepted another delivery")
	}
}
