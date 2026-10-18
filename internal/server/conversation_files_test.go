package server

import (
	"context"
	"errors"
	"net"
	"slices"
	"testing"
	"time"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/filetransfer"
	"noxa/internal/netproto"
	"noxa/internal/store"
)

const testGroupID = "ce7189ed-d3d5-442b-b147-241ea9cb3fb9"
const testGroupFolder = conversationFileRoot + "/" + testGroupID
const testGroupBlob = "0123456789abcdef0123456789abcdef.vcx"

// conversationFilesTestEnv grants the server-wide (scope 0) file
// capabilities that DM attachments already require, so every refusal below
// comes from private-group membership rather than from roles.
func conversationFilesTestEnv(t *testing.T, members ...netproto.ConversationMember) (*testEnv, *privateCallTestStore, *fakeFileTransfer) {
	t.Helper()
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.UploadFiles, authorization.DownloadFiles}
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	chat := &privateCallTestStore{fakeChat: newFakeChat(), calls: map[string]netproto.CallSession{}, group: netproto.Conversation{
		ID: testGroupID, Name: "Private group", Owner: "user-uid", Revision: 1, Epoch: 1, Members: members,
	}}
	ft := &fakeFileTransfer{}
	env := startTestEnvLogger(t, nil, nil, func(d *Deps) { d.Authority = authority; d.Chat = chat; d.FileTransfer = ft }, nil)
	return env, chat, ft
}

func expectDenied(t *testing.T, conn net.Conn, label string) {
	t.Helper()
	if got := readError(t, conn); got.Code != errCodePermissionDenied {
		t.Fatalf("%s: got %+v, want permission denied", label, got)
	}
}

func TestConversationAttachmentTransfersRequireAcceptedMembership(t *testing.T) {
	env, _, ft := conversationFilesTestEnv(t,
		netproto.ConversationMember{UniqueID: "user-uid", JoinedEpoch: 1},
		netproto.ConversationMember{UniqueID: "admin-uid", Pending: true, JoinedEpoch: 1},
	)
	defer env.stop()
	member, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = member.Close() }()
	invited, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = invited.Close() }()

	send(t, member, netproto.MsgFileTransferInit, netproto.FileTransferInit{Direction: "upload", Folder: testGroupFolder, Name: testGroupBlob, Size: 64})
	readOfType(t, member, netproto.MsgFileTransferInitResponse)
	send(t, member, netproto.MsgFileTransferInit, netproto.FileTransferInit{Direction: "download", Folder: testGroupFolder, Name: testGroupBlob})
	readOfType(t, member, netproto.MsgFileTransferInitResponse)

	for _, direction := range []string{"upload", "download"} {
		send(t, invited, netproto.MsgFileTransferInit, netproto.FileTransferInit{Direction: direction, Folder: testGroupFolder, Name: testGroupBlob, Size: 64})
		expectDenied(t, invited, "pending member "+direction)
	}

	ft.mu.Lock()
	defer ft.mu.Unlock()
	if len(ft.uploads) != 1 || ft.uploads[0].folder != testGroupFolder || ft.uploads[0].channelID != 0 || ft.uploads[0].uploader != "user-uid" {
		t.Fatalf("uploads=%+v", ft.uploads)
	}
	if len(ft.downloads) != 1 || ft.downloads[0].folder != testGroupFolder {
		t.Fatalf("downloads=%+v", ft.downloads)
	}
}

func TestConversationAttachmentRejectsNonMembersAndMalformedFolders(t *testing.T) {
	env, _, ft := conversationFilesTestEnv(t, netproto.ConversationMember{UniqueID: "user-uid", JoinedEpoch: 1})
	defer env.stop()
	outsider, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = outsider.Close() }()
	member, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = member.Close() }()

	for _, direction := range []string{"upload", "download"} {
		send(t, outsider, netproto.MsgFileTransferInit, netproto.FileTransferInit{Direction: direction, Folder: testGroupFolder, Name: testGroupBlob, Size: 64})
		expectDenied(t, outsider, "outsider "+direction)
	}
	for _, request := range []netproto.FileTransferInit{
		// Only sealed, content-addressed attachments may be written.
		{Direction: "upload", Folder: testGroupFolder, Name: "plain.txt", Size: 5},
		// Non-canonical aliases of the namespace never authorize.
		{Direction: "download", Folder: "Private-Groups/" + testGroupID, Name: testGroupBlob},
		{Direction: "download", Folder: conversationFileRoot + "/" + "CE7189ED-D3D5-442B-B147-241EA9CB3FB9", Name: testGroupBlob},
		{Direction: "download", Folder: testGroupFolder + "/nested", Name: testGroupBlob},
		{Direction: "download", Folder: conversationFileRoot, Name: testGroupBlob},
		{Direction: "upload", Folder: conversationFileRoot + "/not-a-group", Name: testGroupBlob, Size: 5},
	} {
		send(t, member, netproto.MsgFileTransferInit, request)
		expectDenied(t, member, request.Folder+"/"+request.Name)
	}
	ft.mu.Lock()
	defer ft.mu.Unlock()
	if len(ft.uploads) != 0 || len(ft.downloads) != 0 {
		t.Fatalf("refused requests reached storage: up=%+v down=%+v", ft.uploads, ft.downloads)
	}
}

func TestConversationAttachmentAccessEndsWhenMemberIsRemoved(t *testing.T) {
	env, chat, _ := conversationFilesTestEnv(t,
		netproto.ConversationMember{UniqueID: "user-uid", JoinedEpoch: 1},
		netproto.ConversationMember{UniqueID: "admin-uid", JoinedEpoch: 1},
	)
	defer env.stop()
	owner, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = owner.Close() }()
	removed, sessionID := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = removed.Close() }()

	send(t, removed, netproto.MsgFileTransferInit, netproto.FileTransferInit{Direction: "download", Folder: testGroupFolder, Name: testGroupBlob})
	readOfType(t, removed, netproto.MsgFileTransferInitResponse)

	client, ok := env.srv.clientByID(sessionID)
	if !ok {
		t.Fatal("removed member session missing")
	}
	principal := filetransfer.Principal{UserID: client.userID(), SessionID: sessionID}
	guardCtx := filetransfer.WithTransferFolder(t.Context(), testGroupFolder)
	effect := func(context.Context) error { return nil }
	if err := env.srv.guardRoleFileTransfer(guardCtx, principal, 0, "download", effect); err != nil {
		t.Fatalf("member transfer guard: %v", err)
	}

	chat.groupMu.Lock()
	revision := chat.group.Revision
	chat.groupMu.Unlock()
	send(t, owner, netproto.MsgConversationRequest, netproto.ConversationRequest{Action: "remove", ID: testGroupID, Revision: revision, Target: "admin-uid"})
	readOfType(t, owner, netproto.MsgConversationResult)

	// An in-flight transfer loses access at its next guarded chunk, without
	// waiting for the positive membership cache to expire.
	if err := env.srv.guardRoleFileTransfer(guardCtx, principal, 0, "download", effect); !errors.Is(err, filetransfer.ErrAccessRevoked) {
		t.Fatalf("removed member kept transfer access: %v", err)
	}
	send(t, removed, netproto.MsgFileTransferInit, netproto.FileTransferInit{Direction: "download", Folder: testGroupFolder, Name: testGroupBlob})
	expectDenied(t, removed, "removed member download")
	// Unreserved scope-0 transfers (DM attachments) are unaffected.
	if err := env.srv.guardRoleFileTransfer(filetransfer.WithTransferFolder(t.Context(), ""), principal, 0, "download", effect); err != nil {
		t.Fatalf("DM attachment guard changed: %v", err)
	}
}

func TestConversationFoldersStayOutOfFileManagement(t *testing.T) {
	env, _, ft := conversationFilesTestEnv(t, netproto.ConversationMember{UniqueID: "user-uid", JoinedEpoch: 1})
	defer env.stop()
	ft.mu.Lock()
	ft.files = []store.FileRecord{
		{Name: testGroupBlob, Folder: testGroupFolder, Uploader: "user-uid"},
		{Name: "shared.txt", Folder: "docs", Uploader: "user-uid"},
	}
	ft.mu.Unlock()
	member, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = member.Close() }()

	send(t, member, netproto.MsgFileList, netproto.FileList{})
	var listing netproto.FileListResponse
	if err := netproto.Decode(readOfType(t, member, netproto.MsgFileListResponse), &listing); err != nil {
		t.Fatal(err)
	}
	if len(listing.Folders) != 1 || listing.Folders[0] != "docs" {
		t.Fatalf("group folder enumerated: %+v", listing.Folders)
	}
	for _, request := range []struct {
		kind netproto.MessageType
		body any
	}{
		{netproto.MsgFileList, netproto.FileList{Folder: testGroupFolder}},
		{netproto.MsgFileVersions, netproto.FileVersions{Folder: testGroupFolder, Name: testGroupBlob}},
		{netproto.MsgFileRename, netproto.FileRename{Folder: testGroupFolder, Name: testGroupBlob, NewFolder: "docs", NewName: "leak.vcx"}},
		{netproto.MsgFileRename, netproto.FileRename{Folder: "docs", Name: "shared.txt", NewFolder: testGroupFolder, NewName: testGroupBlob}},
		{netproto.MsgFileLink, netproto.FileLink{Folder: testGroupFolder, Name: testGroupBlob}},
	} {
		send(t, member, request.kind, request.body)
		expectDenied(t, member, request.kind.String())
	}
	ft.mu.Lock()
	defer ft.mu.Unlock()
	if len(ft.moved) != 0 || len(ft.renamed) != 0 {
		t.Fatalf("group file mutated: moved=%v renamed=%v", ft.moved, ft.renamed)
	}
}

func TestConversationFolderNamespaceIsScopeZeroOnly(t *testing.T) {
	if conversationFolderReserved(1, testGroupFolder) {
		t.Fatal("channel folders must not be reinterpreted as group storage")
	}
	if !conversationFolderReserved(0, "PRIVATE-GROUPS") || conversationFolderReserved(0, "private-groups-x/a") || conversationFolderReserved(0, "") {
		t.Fatal("reserved namespace detection")
	}
	if id, ok := conversationFolderID(testGroupFolder); !ok || id != testGroupID {
		t.Fatalf("folder id=%q ok=%v", id, ok)
	}
	if got := filterConversationFolders(5, []string{testGroupFolder}); len(got) != 1 {
		t.Fatalf("channel listing filtered: %v", got)
	}
}

// TestConversationFileGrantIgnoresChecksOverlappingRevocation pins the cache
// race: a positive membership answer read before a leave/remove committed is
// not cached after the revocation.
func TestConversationFileGrantIgnoresChecksOverlappingRevocation(t *testing.T) {
	var access conversationFileAccess
	now := time.Now()
	epoch := access.epoch()
	access.revoke("user-uid", testGroupID) // the removal lands mid-check
	access.grant("user-uid", testGroupID, now, epoch)
	if access.cached("user-uid", testGroupID, now) {
		t.Fatal("stale membership answer cached after revocation")
	}
	access.grant("user-uid", testGroupID, now, access.epoch())
	if !access.cached("user-uid", testGroupID, now) || access.cached("user-uid", testGroupID, now.Add(conversationFileAccessTTL)) {
		t.Fatal("fresh grant not cached for exactly its TTL")
	}
}

func deleteGroupFile(t *testing.T, conn net.Conn, folder, name string) {
	t.Helper()
	send(t, conn, netproto.MsgFileDelete, netproto.FileDelete{Folder: folder, Name: name, AckRequested: true})
}

// TestConversationFileDeleteAllowsUploaderMembersAndModerators pins who may
// remove a group attachment: its uploader while still an accepted member, or
// a holder of ManageFiles (moderation). Everyone else is refused, so an
// uploader is never stuck with a file counting against their quota while the
// group lives, and abusive content can still be taken down.
func TestConversationFileDeleteAllowsUploaderMembersAndModerators(t *testing.T) {
	// user-uid is the policy owner (ManageFiles through the owner bypass) and
	// deliberately NOT a group member; admin-uid is a plain accepted member.
	env, _, ft := conversationFilesTestEnv(t,
		netproto.ConversationMember{UniqueID: "admin-uid", JoinedEpoch: 1},
		netproto.ConversationMember{UniqueID: "third-uid", Pending: true, JoinedEpoch: 1},
	)
	defer env.stop()
	env.auth.passwords["third-uid"] = "pw"
	env.auth.users["third-uid"] = &auth.User{ID: 3, UniqueID: "third-uid", Nickname: "third"}
	const (
		memberBlob  = "11111111111111111111111111111111.vcx"
		otherBlob   = "22222222222222222222222222222222.vcx"
		pendingBlob = "33333333333333333333333333333333.vcx"
	)
	ft.mu.Lock()
	ft.files = []store.FileRecord{
		{Name: memberBlob, Folder: testGroupFolder, Uploader: "admin-uid"},
		{Name: otherBlob, Folder: testGroupFolder, Uploader: "user-uid"},
		{Name: pendingBlob, Folder: testGroupFolder, Uploader: "third-uid"},
	}
	ft.mu.Unlock()
	member, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = member.Close() }()
	moderator, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = moderator.Close() }()
	pending, _ := dialAuthed(t, env.addr, "third-uid")
	defer func() { _ = pending.Close() }()

	// A member cannot delete someone else's attachment.
	deleteGroupFile(t, member, testGroupFolder, otherBlob)
	expectDenied(t, member, "member deleting another uploader's file")
	// An uploader who is not an accepted member (pending, left, removed)
	// cannot either: current membership is required, as for transfers.
	deleteGroupFile(t, pending, testGroupFolder, pendingBlob)
	expectDenied(t, pending, "non-member uploader")
	// Non-canonical aliases never authorize, not even for moderators.
	deleteGroupFile(t, moderator, "Private-Groups/"+testGroupID, otherBlob)
	expectDenied(t, moderator, "non-canonical folder")

	deleteGroupFile(t, member, testGroupFolder, memberBlob)
	readOfType(t, member, netproto.MsgFileMutationSaved)
	deleteGroupFile(t, moderator, testGroupFolder, pendingBlob)
	readOfType(t, moderator, netproto.MsgFileMutationSaved)

	ft.mu.Lock()
	defer ft.mu.Unlock()
	want := []string{testGroupFolder + "/" + memberBlob, testGroupFolder + "/" + pendingBlob}
	if !slices.Equal(ft.deleted, want) {
		t.Fatalf("deleted=%v want %v", ft.deleted, want)
	}
}

// TestConversationGroupDeletionRemovesItsFiles pins reclamation: the change
// that deletes a group (its last member leaving) removes the group's whole
// attachment folder, so those files stop counting against uploader quotas.
func TestConversationGroupDeletionRemovesItsFiles(t *testing.T) {
	env, chat, ft := conversationFilesTestEnv(t,
		netproto.ConversationMember{UniqueID: "user-uid", JoinedEpoch: 1},
		netproto.ConversationMember{UniqueID: "admin-uid", JoinedEpoch: 1},
	)
	defer env.stop()
	ft.mu.Lock()
	ft.files = []store.FileRecord{{Name: testGroupBlob, Folder: testGroupFolder, Uploader: "admin-uid"}}
	ft.mu.Unlock()
	owner, _ := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = owner.Close() }()
	member, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = member.Close() }()

	revision := func() int64 {
		chat.groupMu.Lock()
		defer chat.groupMu.Unlock()
		return chat.group.Revision
	}
	send(t, member, netproto.MsgConversationRequest, netproto.ConversationRequest{Action: "leave", ID: testGroupID, Revision: revision()})
	readOfType(t, member, netproto.MsgConversationResult)
	ft.mu.Lock()
	kept := len(ft.foldersDeleted) == 0 && len(ft.files) == 1
	ft.mu.Unlock()
	if !kept {
		t.Fatal("a leave that keeps the group removed its files")
	}

	send(t, owner, netproto.MsgConversationRequest, netproto.ConversationRequest{Action: "leave", ID: testGroupID, Revision: revision()})
	readOfType(t, owner, netproto.MsgConversationResult)
	ft.mu.Lock()
	defer ft.mu.Unlock()
	if !slices.Equal(ft.foldersDeleted, []string{testGroupFolder}) || len(ft.files) != 0 {
		t.Fatalf("group deletion left files: folders=%v files=%+v", ft.foldersDeleted, ft.files)
	}
}

// TestConversationFileDenialDoesNotInvalidateMembersGrants pins that a
// non-member probing group folders cannot bump the global revocation epoch
// and so cannot defeat every member's grant cache (database load).
func TestConversationFileDenialDoesNotInvalidateMembersGrants(t *testing.T) {
	env, _, _ := conversationFilesTestEnv(t, netproto.ConversationMember{UniqueID: "user-uid", JoinedEpoch: 1})
	defer env.stop()
	outsider, _ := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = outsider.Close() }()

	before := env.srv.conversationFiles.epoch()
	for range 3 {
		send(t, outsider, netproto.MsgFileTransferInit, netproto.FileTransferInit{Direction: "download", Folder: testGroupFolder, Name: testGroupBlob})
		expectDenied(t, outsider, "outsider download")
	}
	if after := env.srv.conversationFiles.epoch(); after != before {
		t.Fatalf("plain denials bumped the revocation epoch %d -> %d", before, after)
	}

	var access conversationFileAccess
	now := time.Now()
	epoch := access.epoch()
	access.forget("outsider", testGroupID) // not cached: no epoch change
	access.grant("member", testGroupID, now, epoch)
	if !access.cached("member", testGroupID, now) {
		t.Fatal("an unrelated denial discarded a member's grant")
	}
	// A denial that contradicts a cached grant still revokes it and fences
	// checks that overlapped it.
	epoch = access.epoch()
	access.forget("member", testGroupID)
	access.grant("member", testGroupID, now, epoch)
	if access.cached("member", testGroupID, now) {
		t.Fatal("contradicted grant survived")
	}
}

// TestConversationGuardFailsClosedWithoutFolder pins that a scope-0 guard
// call that does not name its folder cannot reach group storage by default.
func TestConversationGuardFailsClosedWithoutFolder(t *testing.T) {
	env, _, _ := conversationFilesTestEnv(t, netproto.ConversationMember{UniqueID: "user-uid", JoinedEpoch: 1})
	defer env.stop()
	conn, sessionID := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = conn.Close() }()
	client, ok := env.srv.clientByID(sessionID)
	if !ok {
		t.Fatal("session missing")
	}
	principal := filetransfer.Principal{UserID: client.userID(), SessionID: sessionID}
	effect := func(context.Context) error { return nil }
	if err := env.srv.guardRoleFileTransfer(t.Context(), principal, 0, "download", effect); !errors.Is(err, filetransfer.ErrAccessRevoked) {
		t.Fatalf("folderless scope-0 guard call allowed: %v", err)
	}
	if err := env.srv.guardRoleFileTransfer(filetransfer.WithTransferFolder(t.Context(), testGroupFolder), principal, 0, "download", effect); err != nil {
		t.Fatalf("member guard with folder: %v", err)
	}
}
