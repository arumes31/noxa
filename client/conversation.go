package main

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"noxa/internal/netproto"
)

// conversationAttachmentRoot must match the server's reserved scope-0 folder
// (internal/server/conversation_files.go). The server authorizes every
// transfer below it against current, accepted group membership.
const conversationAttachmentRoot = "private-groups"

// canonicalConversationID matches the server's canonical (lowercase) UUID form.
var canonicalConversationID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func conversationAttachmentLocation(groupID string) (attachmentLocation, error) {
	if !canonicalConversationID.MatchString(groupID) {
		return attachmentLocation{}, errors.New("invalid private group")
	}
	return attachmentLocation{folder: conversationAttachmentRoot + "/" + groupID}, nil
}

// UploadConversationAttachmentForTab seals a file exactly like a DM or channel
// attachment (fresh per-file key, content-addressed ciphertext) and stores it
// in the group's folder. The returned [file:...] token carries the key and is
// only ever sent inside the per-member encrypted group message.
func (a *App) UploadConversationAttachmentForTab(tabID, groupID, name, dataBase64 string) (string, error) {
	location, err := conversationAttachmentLocation(groupID)
	if err != nil {
		return "", err
	}
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return "", err
	}
	result, err := m.conversationRequest(netproto.ConversationRequest{Action: "get", ID: groupID})
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	uid := m.uniqueID
	m.mu.Unlock()
	if len(result.Conversations) != 1 {
		return "", netproto.ErrConversationDenied
	}
	if member, found := result.Conversations[0].Member(uid); !found || member.Pending {
		return "", netproto.ErrConversationDenied
	}
	return uploadChatAttachmentTo(m, location, name, dataBase64)
}

// DownloadConversationAttachmentForTab returns a bounded inline preview of a
// sealed group attachment.
func (a *App) DownloadConversationAttachmentForTab(tabID, groupID, storage, keyB64 string) (string, error) {
	location, err := conversationAttachmentLocation(groupID)
	if err != nil {
		return "", err
	}
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return "", err
	}
	plain, err := a.chatAttachmentBytesAt(m, location, storage, keyB64)
	return inlineChatAttachment(plain, err)
}

// SaveConversationAttachmentForTab saves a decrypted group attachment through
// the same native, no-overwrite path as channel and DM attachments.
func (a *App) SaveConversationAttachmentForTab(tabID, groupID, storage, keyB64, defaultName string) (string, error) {
	location, err := conversationAttachmentLocation(groupID)
	if err != nil {
		return "", err
	}
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return "", err
	}
	return a.saveChatAttachmentAt(m, location, storage, keyB64, defaultName)
}

// The authenticated inner envelope binds server-visible routing metadata to
// the ciphertext, preventing a relay from moving content between groups/epochs.
type conversationPlaintext struct {
	ConversationID string `json:"conversation_id"`
	Epoch          int64  `json:"epoch"`
	Reference      string `json:"reference"`
	From           string `json:"from"`
	To             string `json:"to"`
	Text           string `json:"text"`
}

func isConversationRead(kind netproto.MessageType, body any) bool {
	request, ok := body.(netproto.ConversationRequest)
	return ok && kind == netproto.MsgConversationRequest && (request.Action == "list" || request.Action == "get" || request.Action == "history")
}

func (m *connManager) conversationRequest(request netproto.ConversationRequest) (netproto.ConversationResult, error) {
	frame, err := m.request(netproto.MsgConversationRequest, netproto.MsgConversationResult, request, 10*time.Second)
	if err != nil {
		return netproto.ConversationResult{}, err
	}
	var result netproto.ConversationResult
	if err := netproto.Decode(frame, &result); err != nil {
		return result, err
	}
	if result.Action != request.Action || len(result.Conversations) > 64 || len(result.Messages) > 25 {
		return netproto.ConversationResult{}, errors.New("invalid private group response")
	}
	for _, c := range result.Conversations {
		if c.ID == "" || c.Epoch <= 0 || c.Revision <= 0 || len(c.Members) > netproto.MaxConversationMembers || !netproto.ValidConversationName(c.Name) {
			return netproto.ConversationResult{}, errors.New("invalid private group")
		}
		if request.Action != "list" && request.Action != "create" && c.ID != request.ID {
			return netproto.ConversationResult{}, errors.New("private group response mismatch")
		}
	}
	return result, nil
}

func (a *App) ConversationForTab(tabID string, request netproto.ConversationRequest) (netproto.ConversationResult, error) {
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return netproto.ConversationResult{}, err
	}
	if request.Action == "send" {
		return netproto.ConversationResult{}, errors.New("use encrypted group messaging")
	}
	request.ShareRead = request.Action == "mark_read" && a.shareConversationReads(m)
	result, err := m.conversationRequest(request)
	if err != nil || request.Action != "history" {
		return result, err
	}
	id, err := m.identity()
	if err != nil {
		return netproto.ConversationResult{}, err
	}
	pub, priv, err := id.x25519()
	if err != nil {
		return netproto.ConversationResult{}, err
	}
	m.mu.Lock()
	uid := m.uniqueID
	m.mu.Unlock()
	for i := range result.Messages {
		message := &result.Messages[i]
		// Verification is local evidence, never accepted from the server.
		message.EncVerified = false
		if message.ConversationID != request.ID {
			return netproto.ConversationResult{}, errors.New("private group history mismatch")
		}
		peer := pub
		if message.FromUniqueID != uid {
			var found bool
			peer, found = m.peerPubKey(message.FromUniqueID)
			if !found {
				message.Body = "[Encryption key unavailable]"
				continue
			}
		}
		body, err := openConversation(*message, uid, peer, priv)
		if err != nil {
			message.Body = "[Message authentication failed]"
			continue
		}
		message.Body, message.EncVerified = body, true
	}
	return result, nil
}

func openConversation(message netproto.ConversationMessage, uid string, pub, priv [32]byte) (string, error) {
	plain, err := openDM(message.Body, pub, priv)
	if err != nil {
		return "", err
	}
	var envelope conversationPlaintext
	if err := json.Unmarshal([]byte(plain), &envelope); err != nil {
		return "", err
	}
	if envelope.ConversationID != message.ConversationID || envelope.Epoch != message.Epoch || envelope.Reference != message.Reference || envelope.From != message.FromUniqueID || envelope.To != uid || len(envelope.Text) > 8192 {
		return "", errors.New("private group envelope mismatch")
	}
	return envelope.Text, nil
}

func (a *App) SendConversationForTab(tabID, groupID, text, reference string) (int64, error) {
	if strings.TrimSpace(text) == "" || len(text) > 8192 || reference == "" || len(reference) > 128 {
		return 0, errors.New("invalid group message")
	}
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return 0, err
	}
	result, err := m.conversationRequest(netproto.ConversationRequest{Action: "get", ID: groupID})
	if err != nil {
		return 0, err
	}
	if len(result.Conversations) != 1 {
		return 0, errors.New("private group unavailable")
	}
	c := result.Conversations[0]
	id, err := m.identity()
	if err != nil {
		return 0, err
	}
	pub, priv, err := id.x25519()
	if err != nil {
		return 0, err
	}
	m.mu.Lock()
	uid := m.uniqueID
	m.mu.Unlock()
	member, found := c.Member(uid)
	if !found || member.Pending {
		return 0, netproto.ErrConversationDenied
	}
	message := netproto.ConversationSend{Epoch: c.Epoch, Reference: reference, Envelopes: map[string]string{}}
	for _, recipient := range c.Members {
		if recipient.Pending {
			continue
		}
		peer := pub
		if recipient.UniqueID != uid {
			var found bool
			peer, found = m.peerPubKey(recipient.UniqueID)
			if !found {
				return 0, errors.New("a group member's encryption key is unavailable")
			}
		}
		plain, err := json.Marshal(conversationPlaintext{ConversationID: c.ID, Epoch: c.Epoch, Reference: reference, From: uid, To: recipient.UniqueID, Text: text})
		if err != nil {
			return 0, err
		}
		sealed, err := sealDM(string(plain), peer, priv)
		if err != nil {
			return 0, err
		}
		message.Envelopes[recipient.UniqueID] = sealed
	}
	result, err = m.conversationRequest(netproto.ConversationRequest{Action: "send", ID: c.ID, Revision: c.Revision, Message: &message})
	if err != nil {
		return 0, err
	}
	if result.MessageID <= 0 {
		return 0, errors.New("group message not acknowledged")
	}
	return result.MessageID, nil
}
