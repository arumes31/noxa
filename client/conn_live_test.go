// conn_live_test.go is a headless integration test for the client backend
// against a LIVE noxa server. It is skipped unless NOXA_LIVE_ADDR is set:
//
//	NOXA_LIVE_ADDR=127.0.0.1:12333 go test -run Live -v ./... -count=1
//
// Required when enabled: NOXA_LIVE_{ALICE,BOB,ADMIN}_{UID,PASS} and
// NOXA_LIVE_TLS_FINGERPRINT from the disposable server's local certificate.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"noxa/internal/authorization"
	"noxa/internal/broadcast"
	"noxa/internal/netproto"
)

var (
	liveAliceUID  = os.Getenv("NOXA_LIVE_ALICE_UID")
	liveAlicePass = os.Getenv("NOXA_LIVE_ALICE_PASS")
	liveBobUID    = os.Getenv("NOXA_LIVE_BOB_UID")
	liveBobPass   = os.Getenv("NOXA_LIVE_BOB_PASS")
	liveAdminUID  = os.Getenv("NOXA_LIVE_ADMIN_UID")
	liveAdminPass = os.Getenv("NOXA_LIVE_ADMIN_PASS")
)

// liveAddr returns the control address or skips the test.
func liveAddr(t *testing.T) string {
	t.Helper()
	addr := os.Getenv("NOXA_LIVE_ADDR")
	if addr == "" {
		t.Skip("NOXA_LIVE_ADDR not set; skipping live integration test")
	}
	for _, name := range []string{"ALICE_UID", "ALICE_PASS", "BOB_UID", "BOB_PASS", "ADMIN_UID", "ADMIN_PASS", "TLS_FINGERPRINT"} {
		if os.Getenv("NOXA_LIVE_"+name) == "" {
			t.Fatalf("NOXA_LIVE_%s is required for the configured live server", name)
		}
	}
	return addr
}

// eventRecorder is an eventSink that records everything for assertions.
type eventRecorder struct {
	mu     sync.Mutex
	events []recordedEvent
}

type recordedEvent struct {
	name    string
	payload string
}

// Emit implements eventSink.
func (r *eventRecorder) Emit(name string, payload any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, recordedEvent{name: name, payload: fmt.Sprint(payload)})
}

// waitFor polls until an event matching pred arrives or the timeout passes.
func (r *eventRecorder) waitFor(t *testing.T, name string, pred func(string) bool, timeout time.Duration, what string) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		for _, e := range r.events {
			if e.name == name && (pred == nil || pred(e.payload)) {
				r.mu.Unlock()
				return e.payload
			}
		}
		r.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
	return ""
}

// newTestBackend returns a connManager wired to a recording sink and a
// throwaway identity (so tests never touch the real identity.json or bind
// it to live accounts).
func newTestBackend(t *testing.T) (*connManager, *eventRecorder) {
	t.Helper()
	rec := &eventRecorder{}
	cm := newConnManager(context.Background())
	cm.sink = rec
	cm.id = mustTempIdentity(t)
	return cm, rec
}

// newLiveTestBackend adds the externally configured live server pin without
// coupling local fake-server unit tests to live-server environment variables.
func newLiveTestBackend(t *testing.T) (*connManager, *eventRecorder) {
	t.Helper()
	cm, rec := newTestBackend(t)
	cm.knownServers = loadKnownServersAt(filepath.Join(t.TempDir(), "known_servers.json"))
	addr, err := normalizeServerAddr(os.Getenv("NOXA_LIVE_ADDR"))
	if err != nil {
		t.Fatalf("live server address: %v", err)
	}
	if err := cm.knownServers.trust(addr, os.Getenv("NOXA_LIVE_TLS_FINGERPRINT")); err != nil {
		t.Fatalf("pinning live server certificate: %v", err)
	}
	return cm, rec
}

func TestLiveBackendTrustIsIsolatedAndPinned(t *testing.T) {
	t.Setenv("NOXA_LIVE_ADDR", "127.0.0.1:12483")
	const fingerprint = "fa:17:3d:a2:81:17:6a:2d:4e:d6:5b:c6:78:e0:b2:df:9b:aa:b4:d8:ca:43:ad:a5:f9:9b:21:6e:5d:8c:a7:66"
	t.Setenv("NOXA_LIVE_TLS_FINGERPRINT", fingerprint)
	cm, _ := newLiveTestBackend(t)
	status, err := cm.knownServers.verify("127.0.0.1:12483", fingerprint)
	if err != nil || status != trustOK {
		t.Fatalf("preconfigured certificate pin: status %v, error %v", status, err)
	}
	status, err = cm.knownServers.verify("127.0.0.1:12483", strings.Repeat("00:", 31)+"00")
	if err != nil || status != trustMismatch {
		t.Fatalf("changed certificate pin: status %v, error %v", status, err)
	}
	if cm.allowPlaintext {
		t.Fatal("live helper enables plaintext fallback")
	}
}

// ensureLiveChannel creates a room through the current client role API.
func ensureLiveChannel(t *testing.T) int64 {
	t.Helper()
	admin, _ := newLiveTestBackend(t)
	if err := admin.connect(liveAddr(t), liveAdminUID, liveAdminPass, ""); err != "" {
		t.Fatalf("fixture admin connect: %s", err)
	}
	defer admin.disconnect()
	app := appWithCM(admin)
	state, err := app.RoleChannelState(netproto.RoleChannelQuery{Kind: authorization.ChannelCreate})
	if err != nil {
		t.Fatalf("fixture channel preflight: %v", err)
	}
	var overrides []authorization.RoleOverride
	for _, capability := range []authorization.Capability{authorization.ViewChannel, authorization.ReadHistory, authorization.SendMessages, authorization.Connect, authorization.Speak, authorization.UploadFiles, authorization.DownloadFiles} {
		overrides = append(overrides, authorization.RoleOverride{RoleID: state.EveryoneID, Capability: capability, Effect: authorization.Allow})
	}
	result, err := app.ChangeRoleChannel(netproto.RoleChannelChange{
		Kind: authorization.ChannelCreate, ExpectedRevision: state.Revision, ChannelType: 2,
		Settings: &netproto.RoleChannelSettings{Name: "live-e2e-" + strconv.FormatInt(time.Now().UnixNano(), 10), OpusBitrate: 64000, OpusFEC: true},
		Access:   &netproto.RoleChannelAccess{Synced: false, Overrides: overrides},
	})
	if err != nil || result.ChannelID <= 0 {
		t.Fatalf("fixture channel creation: %+v, %v", result, err)
	}
	return result.ChannelID
}

// --- tests -------------------------------------------------------------------

// TestLiveAuth exercises connect + password auth (alice, bob), wrong
// password rejection, and anonymous guest auth against the live server.
func TestLiveAuth(t *testing.T) {
	addr := liveAddr(t)

	// Alice.
	alice, _ := newLiveTestBackend(t)
	if err := alice.connect(addr, liveAliceUID, liveAlicePass, ""); err != "" {
		t.Fatalf("alice connect: %s", err)
	}
	defer alice.disconnect()
	if alice.uniqueID != liveAliceUID {
		t.Errorf("alice uniqueID = %q, want %q", alice.uniqueID, liveAliceUID)
	}
	if alice.nickname == "" {
		t.Error("alice nickname empty")
	}

	// Bob.
	bob, _ := newLiveTestBackend(t)
	if err := bob.connect(addr, liveBobUID, liveBobPass, ""); err != "" {
		t.Fatalf("bob connect: %s", err)
	}
	defer bob.disconnect()
	if bob.uniqueID != liveBobUID {
		t.Errorf("bob uniqueID = %q, want %q", bob.uniqueID, liveBobUID)
	}

	// Wrong password must be rejected.
	bad, _ := newLiveTestBackend(t)
	if err := bad.connect(addr, liveAliceUID, "definitely-wrong", ""); err == "" {
		bad.disconnect()
		t.Fatal("wrong password accepted")
	}

	// Anonymous guest with the client's own identity (key-derived UID).
	guest, _ := newLiveTestBackend(t)
	wantUID, err := guest.id.uniqueID()
	if err != nil {
		t.Fatalf("uniqueID: %v", err)
	}
	if err := guest.connect(addr, "live-guest", "", ""); err != "" {
		t.Fatalf("guest connect: %s", err)
	}
	defer guest.disconnect()
	if guest.uniqueID != wantUID {
		t.Errorf("guest uniqueID = %q, want key-derived %q", guest.uniqueID, wantUID)
	}
	if guest.nickname != "live-guest" {
		t.Errorf("guest nickname = %q, want live-guest", guest.nickname)
	}
}

// mustTempIdentity creates a throwaway identity in a temp dir so tests never
// touch the real identity.json.
func mustTempIdentity(t *testing.T) *identity {
	t.Helper()
	id, err := loadOrCreateIdentityAt(t.TempDir() + "/identity.json")
	if err != nil {
		t.Fatalf("loadOrCreateIdentityAt: %v", err)
	}
	return id
}

// TestLiveChannelFlow exercises the snapshot, channel join (with membership snapshot
// event), and channel chat between two backend instances.
func TestLiveChannelFlow(t *testing.T) {
	addr := liveAddr(t)
	channelID := ensureLiveChannel(t)

	alice, aliceEvents := newLiveTestBackend(t)
	if err := alice.connect(addr, liveAliceUID, liveAlicePass, ""); err != "" {
		t.Fatalf("alice connect: %s", err)
	}
	defer alice.disconnect()

	bob, bobEvents := newLiveTestBackend(t)
	if err := bob.connect(addr, liveBobUID, liveBobPass, ""); err != "" {
		t.Fatalf("bob connect: %s", err)
	}
	defer bob.disconnect()

	// Both backends must receive a snapshot containing the new channel.
	cidStr := strconv.FormatInt(channelID, 10)
	aliceEvents.waitFor(t, "snapshot", func(p string) bool {
		return strings.Contains(p, `"ChannelID":`+cidStr)
	}, 5*time.Second, "alice snapshot with channel")
	bobEvents.waitFor(t, "snapshot", func(p string) bool {
		return strings.Contains(p, `"ChannelID":`+cidStr)
	}, 5*time.Second, "bob snapshot with channel")

	// Alice joins; then bob joins. Bob must observe a membership snapshot event for
	// alice's join.
	if err := appWithCM(alice).JoinChannel(channelID); err != "" {
		t.Fatalf("alice join: %v", err)
	}
	bobEvents.waitFor(t, "snapshot", func(p string) bool {
		return liveMemberSnapshot(p, alice.clientID, channelID, "")
	}, 5*time.Second, "bob observing alice membership snapshot")

	if err := appWithCM(bob).JoinChannel(channelID); err != "" {
		t.Fatalf("bob join: %v", err)
	}
	// Alice should observe bob's move too.
	aliceEvents.waitFor(t, "snapshot", func(p string) bool {
		return liveMemberSnapshot(p, bob.clientID, channelID, "")
	}, 5*time.Second, "alice observing bob membership snapshot")

	// Alice sends channel chat; bob receives it. Chat is encrypted (4b): the
	// backend seals with the channel key delivered after the join, and bob's
	// backend decrypts before emitting.
	text := "live-chat-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	keyDeadline := time.Now().Add(5 * time.Second)
	for {
		if _, _, ok := alice.scopeKeys.current(channelID); ok {
			break
		}
		if time.Now().After(keyDeadline) {
			t.Fatal("alice never received the channel chat key")
		}
		time.Sleep(20 * time.Millisecond)
	}
	msg, err := alice.encryptChat("channel", cidStr, text)
	if err != nil {
		t.Fatalf("alice encrypt chat: %v", err)
	}
	if err := alice.write(netproto.MsgChatSend, msg); err != nil {
		t.Fatalf("alice chat: %v", err)
	}
	bobEvents.waitFor(t, "event", func(p string) bool {
		return strings.Contains(p, `"chat"`) && strings.Contains(p, text)
	}, 5*time.Second, "bob receiving alice's channel chat")
}

func TestLiveClientInfo(t *testing.T) {
	addr := liveAddr(t)

	alice, _ := newLiveTestBackend(t)
	if err := alice.connect(addr, liveAliceUID, liveAlicePass, ""); err != "" {
		t.Fatalf("alice connect: %s", err)
	}
	defer alice.disconnect()

	bob, _ := newLiveTestBackend(t)
	if err := bob.connect(addr, liveBobUID, liveBobPass, ""); err != "" {
		t.Fatalf("bob connect: %s", err)
	}
	defer bob.disconnect()

	aliceApp := appWithCM(alice)
	bobApp := appWithCM(bob)
	channelID := ensureLiveChannel(t)
	if err := aliceApp.JoinChannel(channelID); err != "" {
		t.Fatalf("alice join: %s", err)
	}
	if err := bobApp.JoinChannel(channelID); err != "" {
		t.Fatalf("bob join: %s", err)
	}

	// Self query: full data incl. IP.
	self, err := bobApp.GetClientInfo(bob.clientID)
	if err != nil {
		t.Fatalf("GetClientInfo(self): %v", err)
	}
	if self.UniqueID != liveBobUID {
		t.Fatalf("self unique id = %q, want %q", self.UniqueID, liveBobUID)
	}
	if self.IP == "" || self.Port == 0 {
		t.Fatalf("self query missing ip/port: %+v", self)
	}
	if self.ConnectedAt <= 0 {
		t.Fatalf("connected_at = %d", self.ConnectedAt)
	}

	// Alice queries bob: IP must be hidden (deny-on-unset).
	other, err := aliceApp.GetClientInfo(bob.clientID)
	if err != nil {
		t.Fatalf("GetClientInfo(bob): %v", err)
	}
	if other.UniqueID != liveBobUID {
		t.Fatalf("other unique id = %q, want %q", other.UniqueID, liveBobUID)
	}
	if other.IP != "" || other.Port != 0 {
		t.Fatalf("alice sees bob's ip/port without permission: %+v", other)
	}
}

// --- wave-6b: permission/group management bindings -----------------------------

// TestLiveGroupManagement exercises the wave-6b bindings against the live
func livePNG(t *testing.T) []byte {
	t.Helper()
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatalf("encode icon fixture: %v", err)
	}
	return encoded.Bytes()
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestLiveFileManagement(t *testing.T) {
	addr := liveAddr(t)
	channelID := ensureLiveChannel(t)

	admin, _ := newLiveTestBackend(t)
	if err := admin.connect(addr, liveAdminUID, liveAdminPass, ""); err != "" {
		t.Fatalf("admin connect: %s", err)
	}
	defer admin.disconnect()
	app := appWithCM(admin)

	name := "w7-live-" + strconv.FormatInt(time.Now().UnixNano(), 36) + ".txt"
	defer app.FileDelete(channelID, "", name)
	defer app.FileDelete(channelID, "", name+".v1")

	// Upload v1, then overwrite with v2.
	if err := app.UploadFile(channelID, name, base64.StdEncoding.EncodeToString([]byte("version-one"))); err != "" {
		t.Fatalf("upload v1: %s", err)
	}
	if err := app.UploadFile(channelID, name, base64.StdEncoding.EncodeToString([]byte("version-two"))); err != "" {
		t.Fatalf("upload v2: %s", err)
	}

	// The current file is v2; v1 was rotated into a version (264).
	list, err := app.FileList(channelID, "")
	if err != nil {
		t.Fatalf("FileList: %v", err)
	}
	var cur *netproto.FileEntry
	for i := range list.Entries {
		if list.Entries[i].Name == name {
			cur = &list.Entries[i]
		}
	}
	if cur == nil {
		t.Fatalf("uploaded file missing: %+v", list.Entries)
	}
	if cur.Size != int64(len("version-two")) {
		t.Fatalf("current size = %d, want v2", cur.Size)
	}
	versions, err := app.FileVersions(channelID, "", name)
	if err != nil {
		t.Fatalf("FileVersions: %v", err)
	}
	if len(versions.Entries) != 1 || versions.Entries[0].Name != name+".v1" {
		t.Fatalf("versions = %+v", versions.Entries)
	}

	// Folder upload via the low-level helpers (261).
	folderFile := "foldered.txt"
	f, err := admin.request(netproto.MsgFileTransferInit, netproto.MsgFileTransferInitResponse,
		netproto.FileTransferInit{ChannelID: channelID, Direction: "upload", Folder: "docs", Name: folderFile, Size: 4},
		10*time.Second)
	if err != nil {
		t.Fatalf("folder init: %v", err)
	}
	var init netproto.FileTransferInitResponse
	if err := json.Unmarshal(f.Payload, &init); err != nil {
		t.Fatalf("decode init: %v", err)
	}
	ep, err := app.ftTarget(init)
	if err != nil {
		t.Fatalf("ftTarget: %v", err)
	}
	if err := ftUpload(ep, init.Token, init.TransferID, []byte("docs")); err != nil {
		t.Fatalf("folder upload: %v", err)
	}
	defer app.FileDelete(channelID, "docs", folderFile)

	docs, err := app.FileList(channelID, "docs")
	if err != nil || len(docs.Entries) != 1 || docs.Entries[0].Folder != "docs" {
		t.Fatalf("docs list = %+v, err=%v", docs.Entries, err)
	}
	found := false
	for _, f := range docs.Folders {
		if f == "docs" {
			found = true
		}
	}
	if !found {
		t.Fatalf("folders = %v, want docs", docs.Folders)
	}

	// Rename/move into the root folder (262).
	if err := app.FileRename(channelID, "docs", folderFile, "", "moved.txt", 0); err != "" {
		t.Fatalf("FileRename: %s", err)
	}
	defer app.FileDelete(channelID, "", "moved.txt")
	list, err = app.FileList(channelID, "")
	if err != nil {
		t.Fatalf("FileList after rename: %v", err)
	}
	found = false
	for _, e := range list.Entries {
		if e.Name == "moved.txt" {
			found = true
		}
	}
	if !found {
		t.Fatalf("moved.txt missing after rename: %+v", list.Entries)
	}

	// Download link (267): the /dl/ URL serves the bytes. The URL host comes
	// from our own control address (the server cannot know its published
	// address behind Docker/NAT).
	link, err := app.FileLink(channelID, "", "moved.txt")
	if err != nil {
		t.Fatalf("FileLink: %v", err)
	}
	if !strings.Contains(link.Path, "/dl/") {
		t.Fatalf("link path = %q", link.Path)
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	linkURL := fmt.Sprintf("http://%s:%d%s", host, link.HealthPort, link.Path)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, linkURL, nil)
	if err != nil {
		t.Fatalf("build GET link request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET link: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close link response: %v", err)
	}
	if string(body) != "docs" {
		t.Fatalf("link body = %q, want %q", body, "docs")
	}

	// Checksum verify (280).
	ok, err := app.VerifyFile(channelID, "", "moved.txt", sha256Hex([]byte("docs")))
	if err != nil || !ok {
		t.Fatalf("VerifyFile match = %v, %v", ok, err)
	}
	ok, err = app.VerifyFile(channelID, "", "moved.txt", sha256Hex([]byte("different")))
	if err != nil || ok {
		t.Fatalf("VerifyFile mismatch = %v, %v, want false", ok, err)
	}

	// Delete (263): the admin (not the uploader here — same account) deletes.
	if err := app.FileDelete(channelID, "", "moved.txt"); err != "" {
		t.Fatalf("FileDelete: %s", err)
	}
	list, _ = app.FileList(channelID, "")
	for _, e := range list.Entries {
		if e.Name == "moved.txt" {
			t.Fatal("moved.txt still listed after delete")
		}
	}

	// Server icon round trip (270).
	iconBytes := livePNG(t)
	if err := app.ServerIconSet(base64.StdEncoding.EncodeToString(iconBytes)); err != "" {
		t.Fatalf("ServerIconSet: %s", err)
	}
	icon, err := app.ServerIconGet()
	if err != nil {
		t.Fatalf("ServerIconGet: %v", err)
	}
	gotIcon, err := base64.StdEncoding.DecodeString(icon.DataBase64)
	if err != nil || !bytes.Equal(gotIcon, iconBytes) {
		t.Fatalf("server icon round trip mismatch: bytes=%d, decode error=%v", len(gotIcon), err)
	}
}

// --- wave-8b: presence and social bindings ------------------------------------

// TestLivePresence exercises SetStatus, ServerInfo, and the poke gate
// against the live server.
func TestLivePresence(t *testing.T) {
	addr := liveAddr(t)

	alice, _ := newLiveTestBackend(t)
	if err := alice.connect(addr, liveAliceUID, liveAlicePass, ""); err != "" {
		t.Fatalf("alice connect: %s", err)
	}
	defer alice.disconnect()
	bob, bobEvents := newLiveTestBackend(t)
	if err := bob.connect(addr, liveBobUID, liveBobPass, ""); err != "" {
		t.Fatalf("bob connect: %s", err)
	}
	defer bob.disconnect()

	aliceApp := appWithCM(alice)
	bobApp := appWithCM(bob)
	channelID := ensureLiveChannel(t)
	if err := aliceApp.JoinChannel(channelID); err != "" {
		t.Fatalf("alice join: %s", err)
	}
	if err := bobApp.JoinChannel(channelID); err != "" {
		t.Fatalf("bob join: %s", err)
	}

	// Server info (313): version and counts present.
	info, err := aliceApp.ServerInfo()
	if err != nil {
		t.Fatalf("ServerInfo: %v", err)
	}
	if info.Version == "" || info.ClientsOnline < 2 {
		t.Fatalf("server info = %+v", info)
	}

	// Status (307): alice goes away; bob receives the broadcast.
	if err := aliceApp.SetStatus("away", "brb"); err != "" {
		t.Fatalf("SetStatus: %s", err)
	}
	bobEvents.waitFor(t, "snapshot", func(p string) bool {
		return liveMemberSnapshot(p, alice.clientID, channelID, "away")
	}, 5*time.Second, "status snapshot")
	if err := aliceApp.SetStatus("online", ""); err != "" {
		t.Fatalf("SetStatus online: %s", err)
	}

	// Role-mode mutations return their acknowledged errors synchronously.
	if err := bobApp.Poke(alice.clientID, "hi"); err == "" {
		t.Fatal("unauthorized poke accepted")
	}
	if err := aliceApp.SetStatus("sleeping", ""); !strings.Contains(err, "invalid status") {
		t.Fatalf("invalid status result: %s", err)
	}
}

func liveMemberSnapshot(payload, id string, channel int64, status string) bool {
	var snapshot broadcast.TreeSnapshot
	if json.Unmarshal([]byte(payload), &snapshot) != nil {
		return false
	}
	matches := func(client *broadcast.ClientInfo) bool {
		return client.ClientID == id && client.ChannelID == channel && client.Status == status
	}
	for _, client := range snapshot.UnassignedClients {
		if matches(client) {
			return true
		}
	}
	var visit func([]*broadcast.ChannelNode) bool
	visit = func(channels []*broadcast.ChannelNode) bool {
		for _, item := range channels {
			for _, client := range item.Clients {
				if matches(client) {
					return true
				}
			}
			if visit(item.Children) {
				return true
			}
		}
		return false
	}
	return visit(snapshot.RootChannels)
}
