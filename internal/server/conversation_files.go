package server

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"noxa/internal/filetransfer"
	"noxa/internal/netproto"
)

// Private-group attachments reuse the direct-message attachment pipeline:
// the client seals every file with a fresh key, uploads only ciphertext to the
// server-wide file scope (channel 0) under a content-derived ".vcx" name, and
// the key travels inside the per-recipient encrypted group message. Unlike a
// DM, a group has a server-known membership, so its blobs live in a reserved
// folder bound to the conversation and every transfer of that folder is
// authorized against CURRENT, accepted membership: invited (pending) users
// and removed members cannot fetch or add group files, even when they still
// hold a key from an older message.
const conversationFileRoot = "private-groups"

// conversationFileAccessTTL bounds how long a positive membership check may be
// reused by the per-chunk file-transfer guard. leave/remove invalidate the
// cached grant immediately, so this only limits database load.
const conversationFileAccessTTL = 5 * time.Second

var conversationAttachmentName = regexp.MustCompile(`^[0-9a-f]{32}\.vcx$`)

// conversationFolderReserved reports whether a scope-0 folder lies in the
// private-group namespace. The first segment is compared case-insensitively
// so a case-folding filesystem cannot alias a group folder.
func conversationFolderReserved(scope int64, folder string) bool {
	if scope != 0 || folder == "" {
		return false
	}
	first, _, _ := strings.Cut(folder, "/")
	return strings.EqualFold(first, conversationFileRoot)
}

// conversationFolderID returns the group a reserved folder belongs to. Only
// the exact canonical form "private-groups/<lowercase uuid>" is accepted.
func conversationFolderID(folder string) (string, bool) {
	rest, ok := strings.CutPrefix(folder, conversationFileRoot+"/")
	if !ok {
		return "", false
	}
	id, err := uuid.Parse(rest)
	if err != nil || id.String() != rest {
		return "", false
	}
	return rest, true
}

type conversationFileAccess struct {
	mu      sync.Mutex
	granted map[string]time.Time // uid + "\x00" + group -> expiry
	// revocations counts leave/remove events. A membership check that began
	// before a revocation must not cache its (now stale) positive answer.
	revocations uint64
}

func conversationAccessKey(uid, group string) string { return uid + "\x00" + group }

func (c *conversationFileAccess) cached(uid, group string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	expiry, ok := c.granted[conversationAccessKey(uid, group)]
	return ok && now.Before(expiry)
}

// epoch is captured before a membership query and handed back to grant.
func (c *conversationFileAccess) epoch() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.revocations
}

func (c *conversationFileAccess) grant(uid, group string, now time.Time, epoch uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if epoch != c.revocations {
		return
	}
	if c.granted == nil {
		c.granted = map[string]time.Time{}
	}
	for key, expiry := range c.granted {
		if !now.Before(expiry) {
			delete(c.granted, key)
		}
	}
	c.granted[conversationAccessKey(uid, group)] = now.Add(conversationFileAccessTTL)
}

func (c *conversationFileAccess) revoke(uid, group string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.revocations++
	delete(c.granted, conversationAccessKey(uid, group))
}

// conversationMember reports whether uid is an accepted member of group,
// reading the authoritative store (never a client claim).
func (s *TCPServer) conversationMember(ctx context.Context, uid, group string) bool {
	if uid == "" || s.deps == nil {
		return false
	}
	backend, ok := s.deps.Chat.(conversationStore)
	if !ok {
		return false
	}
	member := false
	err := backend.WithConversations(ctx, uid, netproto.ConversationRequest{Action: "get", ID: group}, func(result netproto.ConversationResult) error {
		for _, c := range result.Conversations {
			if c.ID != group {
				continue
			}
			if m, found := c.Member(uid); found && !m.Pending {
				member = true
			}
		}
		return nil
	})
	return err == nil && member
}

// authorizeConversationFile checks a transfer into or out of a reserved group
// folder. Callers have already applied the ordinary scope-0 file capability,
// exactly as for DM attachments.
func (s *TCPServer) authorizeConversationFile(ctx context.Context, uid, folder, direction, name string) error {
	group, ok := conversationFolderID(folder)
	if !ok {
		return errConversationFileDenied
	}
	if direction == "upload" && !conversationAttachmentName.MatchString(name) {
		return errConversationFileDenied
	}
	epoch := s.conversationFiles.epoch()
	if !s.conversationMember(ctx, uid, group) {
		s.conversationFiles.revoke(uid, group)
		return errConversationFileDenied
	}
	s.conversationFiles.grant(uid, group, time.Now(), epoch)
	return nil
}

// guardConversationTransfer re-validates membership while bytes move. It uses
// the short positive cache so a large download costs at most one membership
// query every few seconds, while leave/remove revoke at once.
func (s *TCPServer) guardConversationTransfer(ctx context.Context, client *Client, scope int64) error {
	folder, ok := filetransfer.TransferFolder(ctx)
	if !ok || !conversationFolderReserved(scope, folder) {
		return nil
	}
	group, valid := conversationFolderID(folder)
	if !valid {
		return filetransfer.ErrAccessRevoked
	}
	uid := client.uniqueID()
	if s.conversationFiles.cached(uid, group, time.Now()) {
		return nil
	}
	epoch := s.conversationFiles.epoch()
	if !s.conversationMember(ctx, uid, group) {
		return filetransfer.ErrAccessRevoked
	}
	s.conversationFiles.grant(uid, group, time.Now(), epoch)
	return nil
}

var errConversationFileDenied = errors.New("private group file unavailable or access denied")

// filterConversationFolders hides group folders from scope-0 listings. Group
// files are reachable only through the encrypted group messages that name
// them; the file browser never enumerates which groups exist.
func filterConversationFolders(scope int64, folders []string) []string {
	if scope != 0 {
		return folders
	}
	out := folders[:0:0]
	for _, folder := range folders {
		if !conversationFolderReserved(scope, folder) {
			out = append(out, folder)
		}
	}
	return out
}
