package server

import (
	"context"
	"testing"
	"time"

	"noxa/internal/authorization"
	"noxa/internal/config"
	"noxa/internal/filetransfer"
	"noxa/internal/netproto"
	"noxa/internal/store"
)

func TestRoleUploaderQuotaAppliesToMembersAndOwner(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.UploadFiles}
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, func(c *config.Config) { c.FileUserQuotaMB = 25 }, func(d *Deps) { d.Authority = authority })
	defer env.stop()
	env.state.AddChannel(testChannel(1))
	for _, identity := range []string{"admin-uid", "user-uid"} {
		conn, _ := dialAuthed(t, env.addr, identity)
		defer func() { _ = conn.Close() }()
		send(t, conn, netproto.MsgFileTransferInit, netproto.FileTransferInit{Direction: "upload", ChannelID: 1, Name: "quota.txt", Size: 1})
		readOfType(t, conn, netproto.MsgFileTransferInitResponse)
	}
	env.ft.mu.Lock()
	defer env.ft.mu.Unlock()
	if len(env.ft.uploads) != 2 {
		t.Fatalf("uploads: %+v", env.ft.uploads)
	}
	for _, upload := range env.ft.uploads {
		if upload.quotaMB != 25 {
			t.Fatalf("resource ceiling bypassed: %+v", upload)
		}
	}
}

type disconnectFileTransfer struct {
	fakeFileTransfer
	revoked chan func(filetransfer.Principal, int64, string) bool
}

func (f *disconnectFileTransfer) RevokeTransfers(allowed func(filetransfer.Principal, int64, string) bool) {
	f.revoked <- allowed
}

func TestRoleDisconnectRevokesOnlyItsFileSession(t *testing.T) {
	backend := serverRoleFixture()
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	ft := &disconnectFileTransfer{revoked: make(chan func(filetransfer.Principal, int64, string) bool, 1)}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority; d.FileTransfer = ft })
	defer env.stop()
	conn, id := dialAuthed(t, env.addr, "user-uid")
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case allowed := <-ft.revoked:
		if allowed(filetransfer.Principal{UserID: 2, SessionID: id}, 1, "upload") {
			t.Fatal("disconnected session retained transfers")
		}
		if !allowed(filetransfer.Principal{UserID: 2, SessionID: "other-session"}, 1, "download") {
			t.Fatal("unrelated session was revoked")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("disconnect did not revoke files")
	}
}

func TestRoleFilesUseExplicitScopeAndProtectOtherUploaders(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.DownloadFiles, authorization.UploadFiles}
	backend.policy.Channels = append(backend.policy.Channels, authorization.ChannelPolicy{ChannelID: 2, Overrides: []authorization.RoleOverride{{RoleID: 10, Capability: authorization.ViewChannel, Effect: authorization.Deny}}})
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
	defer env.stop()
	for _, id := range []int64{1, 2} {
		env.state.AddChannel(testChannel(id))
	}
	conn, id := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = conn.Close() }()
	for _, request := range []struct {
		kind netproto.MessageType
		body any
	}{
		{netproto.MsgFileList, netproto.FileList{ChannelID: 2}},
		{netproto.MsgFileVersions, netproto.FileVersions{ChannelID: 2, Name: "private.txt"}},
		{netproto.MsgFileTransferInit, netproto.FileTransferInit{ChannelID: 2, Direction: "download", Name: "private.txt"}},
		{netproto.MsgFileTransferInit, netproto.FileTransferInit{ChannelID: 2, Direction: "upload", Name: "private.txt", Size: 5}},
		{netproto.MsgFileDelete, netproto.FileDelete{ChannelID: 2, Name: "private.txt"}},
		{netproto.MsgFileLink, netproto.FileLink{ChannelID: 2, Name: "private.txt"}},
	} {
		send(t, conn, request.kind, request.body)
		if got := readError(t, conn); got.Code != errCodePermissionDenied {
			t.Fatalf("hidden file scope: %+v", got)
		}
	}
	ft := env.srv.deps.FileTransfer.(*fakeFileTransfer)
	ft.mu.Lock()
	ft.files = []store.FileRecord{{Name: "other.txt", Uploader: "someone-else"}, {Name: "own.txt", Uploader: "admin-uid"}}
	ft.mu.Unlock()
	send(t, conn, netproto.MsgFileDelete, netproto.FileDelete{ChannelID: 1, Name: "other.txt"})
	if got := readError(t, conn); got.Code != errCodePermissionDenied {
		t.Fatalf("legacy admin deleted another uploader's file: %+v", got)
	}
	send(t, conn, netproto.MsgFileRename, netproto.FileRename{ChannelID: 1, Name: "own.txt", NewChannelID: 2, NewName: "moved.txt"})
	if got := readError(t, conn); got.Code != errCodePermissionDenied {
		t.Fatalf("move ignored destination: %+v", got)
	}
	ft.mu.Lock()
	if len(ft.deleted) != 0 || len(ft.moved) != 0 || len(ft.uploads) != 0 || len(ft.downloads) != 0 {
		t.Error("denied operation reached file backend")
	}
	ft.mu.Unlock()
	principal := filetransfer.Principal{UserID: 1, SessionID: id}
	if err := env.srv.guardRoleFileTransfer(t.Context(), principal, 1, "download", func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	principal.UserID = 2
	if err := env.srv.guardRoleFileTransfer(t.Context(), principal, 1, "download", func(context.Context) error { t.Fatal("forged principal reached data"); return nil }); err == nil {
		t.Fatal("accepted mismatched session principal")
	}
}
