//go:build integration

package main

import (
	"context"
	"fmt"
	"net"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"go.uber.org/zap"
	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/broadcast"
	"noxa/internal/channels"
	"noxa/internal/chatcrypto"
	"noxa/internal/config"
	"noxa/internal/filetransfer"
	"noxa/internal/health"
	"noxa/internal/metrics"
	"noxa/internal/query"
	"noxa/internal/server"
	"noxa/internal/state"
	"noxa/internal/store"
)

// All role operations use the concrete native server, Authority and
// transactional store.
type scenarioQueryBackend struct {
	query.RoleIntegrationBackend
	query.RoleManagementBackend
	query.RoleInspectionBackend
	query.RoleChannelBackend
}

func scenarioTestStore(t *testing.T) *store.Store {
	t.Helper()
	dsn := os.Getenv("NOXA_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set NOXA_TEST_DATABASE_URL for disposable PostgreSQL role scenarios")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := store.New(dsn, zap.NewNop(), 2, 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	name := fmt.Sprintf("noxa_e2e_roles_%d", time.Now().UnixNano())
	if _, err := admin.DB().ExecContext(t.Context(), "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.DB().ExecContext(ctx, "DROP DATABASE "+name); err != nil {
			t.Errorf("drop scratch database: %v", err)
		}
	})
	u.Path, u.RawPath = "/"+name, ""
	db, err := store.New(u.String(), zap.NewNop(), 5, 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestRoleE2EScenariosUseCommittedQueryAuthority(t *testing.T) {
	db := scenarioTestStore(t)
	logger := zap.NewNop()
	a := auth.New(db, logger)
	const password = "e2e-integration-password"
	create := func(name string) auth.IntegrationPrincipal {
		t.Helper()
		uid, err := a.RegisterUser(t.Context(), name, password)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=TRUE WHERE unique_id=$1", uid); err != nil {
			t.Fatal(err)
		}
		principal, err := a.AuthenticateIntegration(t.Context(), uid, password, "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		return principal
	}
	owner, alice, bob := create("e2e-owner"), create("e2e-alice"), create("e2e-bob")
	initial, err := db.PrepareRolePolicy(t.Context(), owner.UserID())
	if err != nil {
		t.Fatal(err)
	}
	// Install the test server's public baseline before its Authority exists.
	// Live scenarios never rewrite @everyone or existing role definitions.
	everyone := initial.Roles[0]
	for _, role := range initial.Roles {
		if role.ID == initial.EveryoneID {
			everyone = role
		}
	}
	everyone.Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect, authorization.ReadHistory, authorization.SendMessages}
	initial, err = db.ChangeRolePolicy(t.Context(), owner.UserID(), authorization.RoleChange{Kind: authorization.RoleUpdate, ExpectedRevision: initial.Revision, Role: everyone})
	if err != nil {
		t.Fatal(err)
	}
	initial, err = db.RolePolicy(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	sm := state.New(logger)
	healthServer := health.New("", logger, db.DB().PingContext)
	metricSink := metrics.New()
	metricSink.RegisterStateStats(func() (int, int) { return sm.ClientCount(), sm.ChannelCount() })
	healthServer.HandleLocalGET("/metrics", metricSink.Handler())
	healthHTTP := httptest.NewServer(healthServer.Handler())
	t.Cleanup(healthHTTP.Close)
	udpListener, err := (&net.ListenConfig{}).ListenPacket(t.Context(), "udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	udpAddress := udpListener.LocalAddr().String()
	_ = udpListener.Close()
	udp := server.NewUDP(&config.Config{UDPAddr: udpAddress}, logger)
	udpContext, cancelUDP := context.WithCancel(t.Context())
	udpDone := make(chan error, 1)
	go func() { udpDone <- udp.Start(udpContext) }()
	t.Cleanup(func() {
		cancelUDP()
		select {
		case err := <-udpDone:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("UDP listener did not stop")
		}
	})
	bc := broadcast.New(logger, sm)
	t.Cleanup(bc.Close)
	manager := channels.New(db, sm, logger)
	t.Cleanup(manager.Close)
	fileListener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fileAddress := fileListener.Addr().String()
	_ = fileListener.Close()
	fileRoot := t.TempDir()
	files := filetransfer.New(filetransfer.Config{Addr: fileAddress, RootDir: fileRoot}, db, logger)
	fileContext, cancelFiles := context.WithCancel(t.Context())
	fileDone := make(chan error, 1)
	go func() { fileDone <- files.Start(fileContext) }()
	t.Cleanup(func() {
		cancelFiles()
		if err := files.Close(); err != nil {
			t.Error(err)
		}
		select {
		case err := <-fileDone:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("file listener did not stop")
		}
	})
	authority, err := authorization.NewAuthority(t.Context(), db, func(ctx context.Context, _, after *authorization.RoleEvaluator) error {
		files.RevokeTransfers(func(principal filetransfer.Principal, scope int64, direction string) bool {
			capability := authorization.DownloadFiles
			if direction == "upload" {
				capability = authorization.UploadFiles
			}
			return after.Evaluate(principal.UserID, scope, capability).Allowed
		})
		return manager.ReconcileRoleChannels(ctx, after.Policy(), func(deleted channels.DeleteResult) error {
			for _, id := range deleted.ChannelIDs {
				if err := files.TombstoneChannelData(id); err != nil {
					return err
				}
			}
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	manager.EnableRoleMode(authority)
	controlListener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	controlAddress := controlListener.Addr().String()
	_ = controlListener.Close()
	kek, err := chatcrypto.LoadKEKRing(filepath.Join(t.TempDir(), "kek.ring"), "", true)
	if err != nil {
		t.Fatal(err)
	}
	native := server.New(&config.Config{TCPAddr: controlAddress, FileAddr: fileAddress, FileRoot: fileRoot, ChatRateMsgs: 100}, logger, &server.Deps{Auth: a, Authority: authority, Roles: db, Channels: manager, State: sm, Broadcast: bc, Chat: db, PreKeys: db, ScopeKeys: db, ChatKEK: kek, FileTransfer: files})
	if err := native.EnsureGlobalScopeKey(t.Context()); err != nil {
		t.Fatal(err)
	}
	controlContext, cancelControl := context.WithCancel(t.Context())
	controlDone := make(chan error, 1)
	go func() { controlDone <- native.Start(controlContext) }()
	t.Cleanup(func() {
		cancelControl()
		ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if err := native.Shutdown(ctx); err != nil {
			t.Error(err)
		}
		select {
		case err := <-controlDone:
			if err != nil {
				t.Error(err)
			}
		case <-ctx.Done():
			t.Error("native listener did not stop")
		}
	})
	controlDeadline := time.Now().Add(3 * time.Second)
	for {
		conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(t.Context(), "tcp", controlAddress)
		if err == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(controlDeadline) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	backend := &scenarioQueryBackend{RoleIntegrationBackend: native, RoleManagementBackend: native, RoleInspectionBackend: native, RoleChannelBackend: native}
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	srv := query.New(address, logger, backend)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- srv.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Query listener did not stop")
		}
		if err := srv.Close(); err != nil {
			t.Error(err)
		}
	})
	deadline := time.Now().Add(3 * time.Second)
	for {
		conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(t.Context(), "tcp", address)
		if err == nil {
			_ = conn.Close()
			break
		}
		select {
		case err := <-done:
			t.Fatalf("Query startup: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	profile := options{authorizationModel: "roles-v1", healthURL: healthHTTP.URL, udpAddr: udpAddress, queryAddr: address, adminUID: owner.UniqueID(), adminPass: password, addr: controlAddress, aliceUID: alice.UniqueID(), alicePass: password, aliceNickname: "e2e-alice", bobUID: bob.UniqueID(), bobPass: password, fileAddr: fileAddress, filePayload: 4096}
	badCredentials := profile
	badCredentials.bobPass = "wrong-password"
	if code := runChecks(badCredentials); code != 1 {
		t.Fatalf("bad native credentials returned %d", code)
	}
	afterFailure, err := db.RolePolicy(t.Context())
	if err != nil || !reflect.DeepEqual(initial, afterFailure) {
		t.Fatalf("failed authentication changed policy: %v", err)
	}
	if code := runChecks(profile); code != 0 {
		t.Fatalf("role CLI checklist returned %d", code)
	}
	final, err := db.RolePolicy(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if final.Revision <= initial.Revision {
		t.Fatal("scenarios performed no committed writes")
	}
	final.Revision = initial.Revision
	if !reflect.DeepEqual(initial, final) {
		t.Fatalf("scenario failed to restore initial policy\nbefore: %+v\nafter: %+v", initial, final)
	}
	if sm.ChannelCount() != 0 {
		t.Fatal("scenario left a channel in live state")
	}
	entries, err := os.ReadDir(fileRoot)
	if err != nil || len(entries) != 0 {
		t.Fatalf("file cleanup left channel data: %v %v", entries, err)
	}
	var count int
	if err := db.DB().QueryRowContext(t.Context(), "SELECT COUNT(*) FROM audit_log WHERE action IN ('roles.role_create','roles.role_delete','roles.channel_create','roles.channel_delete')").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 10 {
		t.Fatalf("expected ten audited lifecycle changes, got %d", count)
	}
}
