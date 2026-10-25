package main

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestUploadStageAttribution(t *testing.T) {
	ok := func(string, []byte) error { return nil }
	if _, err := uploadChatAttachmentUsing("a.txt", "%%%not-base64%%%", ok); uploadErrorStage(err) != uploadStageRead || !strings.HasPrefix(err.Error(), "[upload:read] ") {
		t.Fatalf("read stage: %v", err)
	}
	data := base64.StdEncoding.EncodeToString([]byte("hello"))
	failing := errors.New("server refused")
	_, err := uploadChatAttachmentUsing("a.txt", data, func(string, []byte) error { return failing })
	if uploadErrorStage(err) != uploadStageTransfer || !errors.Is(err, failing) || err.Error() != "[upload:transfer] server refused" {
		t.Fatalf("transfer stage: %v", err)
	}
	// Cancellation keeps its sentinel so callers still match errTransferCanceled.
	_, err = uploadChatAttachmentUsing("a.txt", data, func(string, []byte) error { return errTransferCanceled })
	if !errors.Is(err, errTransferCanceled) || uploadErrorStage(err) != "" {
		t.Fatalf("canceled: %v", err)
	}
	// An already attributed error is never relabelled.
	wrapped := stageUploadError(uploadStageTransfer, stageUploadError(uploadStageEncrypt, errors.New("seal")))
	if uploadErrorStage(wrapped) != uploadStageEncrypt {
		t.Fatalf("relabelled: %v", wrapped)
	}
	if token, err := uploadChatAttachmentUsing("a.txt", data, ok); err != nil || !strings.HasPrefix(token, "[file:") {
		t.Fatalf("success: %q %v", token, err)
	}
}

func orphanTestApp(t *testing.T) (*App, *connManager) {
	t.Helper()
	a, cm := dmContextTestApp(t)
	cm.mu.Lock()
	cm.addr = "example.com:12333"
	cm.fingerprint = "cert-a"
	cm.mu.Unlock()
	return a, cm
}

func orphanToken(storage, name string) string {
	return formatFileRef(storage, base64.StdEncoding.EncodeToString(make([]byte, 32)), attachmentMeta{Size: 5}, name)
}

func TestUploadOrphanTrackingFollowsPublishOutcome(t *testing.T) {
	a, cm := orphanTestApp(t)
	a.recordUploadedAttachment(cm, 7, orphanToken("aaaa.vcx", "a.png"), "a.png")
	entries, err := loadUploadOrphans(a.uploadOrphanPath())
	if err != nil || len(entries) != 1 || entries[0].State != uploadOrphanUploaded || entries[0].ChannelID != 7 || entries[0].Storage != "aaaa.vcx" {
		t.Fatalf("recorded: %+v %v", entries, err)
	}
	if !strings.HasPrefix(entries[0].Server, "example.com") || !strings.HasSuffix(entries[0].Server, "|cert-a") {
		t.Fatalf("server namespace: %q", entries[0].Server)
	}
	// A published message owns its file: the entry disappears.
	a.resolvePublishedAttachments(cm, "look "+orphanToken("aaaa.vcx", "a.png"), "")
	if entries, _ = loadUploadOrphans(a.uploadOrphanPath()); len(entries) != 0 {
		t.Fatalf("published entry kept: %+v", entries)
	}
	a.recordUploadedAttachment(cm, 7, orphanToken("bbbb.vcx", "b.png"), "b.png")
	a.recordUploadedAttachment(cm, 0, orphanToken("cccc.vcx", "c.png"), "c.png")
	a.resolvePublishedAttachments(cm, orphanToken("bbbb.vcx", "b.png"), "permission denied")
	a.resolvePublishedAttachments(cm, orphanToken("cccc.vcx", "c.png"), "connection closed before ack")
	entries, _ = loadUploadOrphans(a.uploadOrphanPath())
	states := map[string]string{}
	for _, entry := range entries {
		states[entry.Storage] = entry.State
	}
	if states["bbbb.vcx"] != uploadOrphanPublishFailed || states["cccc.vcx"] != uploadOrphanUncertain {
		t.Fatalf("states: %+v", states)
	}
	// Text without tokens and tokens of another server never touch the store.
	a.resolvePublishedAttachments(cm, "plain text", "")
	other := []uploadOrphan{{Server: "other|x", Storage: "bbbb.vcx", State: uploadOrphanUploaded}}
	if got := settleUploadOrphans(other, entries[0].Server, []string{"bbbb.vcx"}, ""); len(got) != 1 {
		t.Fatalf("foreign server settled: %+v", got)
	}
}

func TestUploadOrphanSweepSelection(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-30 * time.Second).UnixMilli()
	old := now.Add(-10 * time.Minute).UnixMilli()
	entries := []uploadOrphan{
		{Server: "s", Storage: "fresh", State: uploadOrphanUploaded, CreatedAt: fresh},
		{Server: "s", Storage: "old", State: uploadOrphanUploaded, CreatedAt: old},
		{Server: "s", Storage: "failed", State: uploadOrphanPublishFailed, CreatedAt: fresh},
		{Server: "s", Storage: "uncertain", State: uploadOrphanUncertain, CreatedAt: old},
		{Server: "elsewhere", Storage: "old", State: uploadOrphanUploaded, CreatedAt: old},
	}
	var names []string
	for _, entry := range sweepableUploadOrphans(entries, "s", now) {
		names = append(names, entry.Storage)
	}
	if strings.Join(names, ",") != "old,failed" {
		t.Fatalf("sweep selection: %v", names)
	}
	stale := append(entries, uploadOrphan{Server: "s", Storage: "ancient", State: uploadOrphanUncertain, CreatedAt: now.Add(-8 * 24 * time.Hour).UnixMilli()})
	if kept := expireUploadOrphans(stale, now); len(kept) != len(entries) {
		t.Fatalf("retention: %d", len(kept))
	}
	if !uncertainPublishFailure("write: connection reset by peer") || uncertainPublishFailure("permission denied") {
		t.Fatal("uncertain classification")
	}
}

func TestUploadOrphanCleanupDeletesOnlyUnreferencedFiles(t *testing.T) {
	a, cm := orphanTestApp(t)
	now := time.Now()
	server := uploadOrphanServer(cm)
	path := a.uploadOrphanPath()
	seed := []uploadOrphan{
		{Server: server, ChannelID: 3, Storage: "gone.vcx", State: uploadOrphanUploaded, CreatedAt: now.Add(-time.Hour).UnixMilli()},
		{Server: server, ChannelID: 3, Storage: "rejected.vcx", State: uploadOrphanPublishFailed, CreatedAt: now.UnixMilli()},
		{Server: server, ChannelID: 3, Storage: "locked.vcx", State: uploadOrphanPublishFailed, CreatedAt: now.UnixMilli()},
		{Server: server, ChannelID: 3, Storage: "maybe.vcx", State: uploadOrphanUncertain, CreatedAt: now.UnixMilli()},
		{Server: server, ChannelID: 3, Storage: "inflight.vcx", State: uploadOrphanUploaded, CreatedAt: now.UnixMilli()},
		{Server: "other|cert", ChannelID: 3, Storage: "foreign.vcx", State: uploadOrphanUploaded, CreatedAt: now.Add(-time.Hour).UnixMilli()},
	}
	if err := saveUploadOrphans(path, seed); err != nil {
		t.Fatal(err)
	}
	var removed []string
	result := a.cleanupOrphanUploads(cm, func(entry uploadOrphan) string {
		removed = append(removed, entry.Storage)
		switch entry.Storage {
		case "rejected.vcx":
			return "file not found"
		case "locked.vcx":
			return "forbidden"
		}
		return ""
	}, now)
	if strings.Join(removed, ",") != "gone.vcx,rejected.vcx,locked.vcx" {
		t.Fatalf("deleted set: %v", removed)
	}
	if result.Removed != 2 || result.Failed != 1 || result.Kept != 1 {
		t.Fatalf("summary: %+v", result)
	}
	entries, _ := loadUploadOrphans(path)
	var left []string
	for _, entry := range entries {
		left = append(left, entry.Storage)
		if entry.Storage == "locked.vcx" && entry.Error != "forbidden" {
			t.Fatalf("failure not recorded: %+v", entry)
		}
	}
	if strings.Join(left, ",") != "locked.vcx,maybe.vcx,inflight.vcx,foreign.vcx" {
		t.Fatalf("remaining: %v", left)
	}
}
