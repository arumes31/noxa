// handlers_chat.go routes live chat and delivers spooled direct messages.
package server

import (
	"context"
	"errors"
	"strconv"
	"time"

	"go.uber.org/zap"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/store"
)

// maxChatBytes caps the chat body size (for encrypted messages this is the
// base64 ciphertext length).
const maxChatBytes = 16 * 1024

// handleChatSend routes a chat message: channel chat to the channel's
// members, a direct message to a user by unique ID (spooled when offline), a
// direct message to an online connection by client ID (echoed back to the
// sender), or a global message to all clients.
//
// Encryption (4b): plaintext is always rejected and no setting admits it;
// every send is a scope (client→server→client) or E2E ciphertext.
// For scope messages the server validates the scope key id and opens the body
// for moderation. A direct message must be E2EE (unverifiable by design): a
// direct message carrying a scope key id would be server-readable and is
// rejected, so the rule is plaintext never, DMs end-to-end, channels and
// global chat client→server→client.
func (s *TCPServer) handleChatSend(ctx context.Context, client *Client, f *netproto.Frame) error {
	return s.rolePolicyRead(ctx, client, func(ctx context.Context) error {
		return s.sendSessionChat(ctx, client, f)
	})
}

func (s *TCPServer) sendSessionChat(ctx context.Context, client *Client, f *netproto.Frame) error {
	var msg netproto.ChatSend
	if err := netproto.Decode(f, &msg); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed chat_send: "+err.Error())
	}
	if msg.ToUniqueID != "" && msg.ToClientID != "" {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "choose a single direct-message destination")
	}
	if msg.AckRequested && !validAcknowledgedChat(msg) {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid acknowledged chat destination or reference")
	}
	if s.deps == nil || s.deps.Broadcast == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "broadcast backend unavailable")
	}
	// (215) same gate as the join: rules the user has not answered would
	// otherwise be advisory, and DMs would route around a channel-only check.
	if client.rulesBlocked() {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "accept the server rules before sending messages")
	}

	if len(msg.Text) > maxChatBytes {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "chat message too large")
	}
	if !msg.Enc {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, errPlaintextChat)
	}
	if msg.Enc && msg.KeyID == 0 && msg.ChannelID != "" {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "channel chat requires a scope key id")
	}

	isDM := msg.ToUniqueID != "" || msg.ToClientID != ""
	if s.chatRate != nil && !s.chatRate.allowLimit(client.UniqueID, time.Now(), s.chatActionLimit(ctx, client)) {
		s.metricsSink().IncChatMessage("rejected")
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "chat rate limit exceeded — slow down")
	}

	// Channel/global scopes run the moderation pipeline (wave 5a): rate
	// limit, slow mode, decrypt, filters, spam, mentions, store, relay.
	if !isDM {
		var channelID int64
		if msg.ChannelID != "" {
			id, err := strconv.ParseInt(msg.ChannelID, 10, 64)
			if err != nil {
				return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid channel_id: "+msg.ChannelID)
			}
			channelID = id
		}
		return s.roleAction(ctx, client, channelID, authorization.SendMessages, func(ctx context.Context) error {
			// Read entitlement FIRST, before anything touches chatKeys: members and
			// entitled subscribers may write the channel tab they can read, while
			// routeScopedChat may ensure a scope's first generation. Reaching it with an
			// attacker-supplied channel id is a disk-exhaustion DoS (91).
			if channelID != 0 {
				if s.deps.State == nil {
					return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "state backend unavailable")
				}
				if !s.scopeReadable(ctx, client, channelID) {
					return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "not a member or subscriber of this channel")
				}
			}
			if msg.Enc {
				if s.chatKeys == nil {
					return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "chat key manager unavailable")
				}
				// Non-minting lookup: an unknown scope is "rejoin", never a mint.
				currentID, _, err := s.chatKeys.current(ctx, channelID)
				if errors.Is(err, ErrNoScopeKey) {
					return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "no chat key for this channel yet — rejoin the channel")
				}
				if err != nil {
					return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "chat key unavailable")
				}
				if msg.KeyID != currentID {
					scope := "channel"
					if channelID == 0 {
						scope = "global scope"
					}
					return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "stale chat key for "+scope+" (key rotated; wait for re-key)")
				}
			}
			return s.routeScopedChat(ctx, client, msg, channelID)
		})
	}

	// A key id would mean a body sealed with a scope key the server holds.
	// Direct messages are end-to-end only, so that is refused outright.
	if msg.KeyID != 0 {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, errServerSealedDM)
	}
	if len(msg.Copies) > 0 {
		return s.sendDirectCopies(ctx, client, msg)
	}
	if msg.DMAction != "" || msg.RefClientMsgID != "" {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "direct-message edits need per-device copies")
	}

	if msg.ToUniqueID == "" {
		s.roleMetadataMu.Lock()
		defer s.roleMetadataMu.Unlock()
		lease := ctx.Value(roleLeaseKey{}).(roleLease)
		target, exists := s.clientByID(msg.ToClientID)
		if !exists || !target.isAuthed() || !s.rolePokeTargetVisible(lease.evaluator, client, msg.ToClientID) {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeNotFound, "target client not reachable")
		}
		if target.isWebTransport() {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, errDMUpdateRequired)
		}
	}

	// Direct messages are relayed without moderation.
	chat := netproto.ChatBroadcast{
		Direct:       true,
		ToUniqueID:   msg.ToUniqueID,
		ChannelID:    msg.ChannelID,
		FromClientID: client.ID,
		FromUniqueID: client.UniqueID,
		From:         client.nickname(),
		Text:         msg.Text,
		Enc:          msg.Enc,
		KeyID:        msg.KeyID,
		E2E:          msg.Enc,
		ClientMsgID:  msg.ClientMsgID,
	}
	if chat.ToUniqueID == "" {
		if target, ok := s.clientByID(msg.ToClientID); ok {
			chat.ToUniqueID = target.uniqueID()
		}
	}
	var binding []store.DMKeyBinding
	if msg.RecipientPublicKey != "" {
		recipientKey, valid := canonicalDMKey(msg.RecipientPublicKey)
		if !msg.Enc || msg.KeyID != 0 || !valid {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid direct-message key binding")
		}
		senderKey, published := s.senderDMKey(client)
		if !published {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "publish an encryption key before sending a bound direct message")
		}
		msg.RecipientPublicKey = recipientKey
		chat.SenderPublicKey, chat.RecipientPublicKey = senderKey, recipientKey
		binding = []store.DMKeyBinding{{SenderPublicKey: senderKey, RecipientPublicKey: recipientKey, ClientMsgID: msg.ClientMsgID}}
		if msg.ToClientID != "" {
			target, _ := s.boundDMRecipient(chat.ToUniqueID, msg.ToClientID, recipientKey)
			if target == nil {
				return s.rejectStaleDMKey(client)
			}
		}
	}
	payload, err := eventEnvelope(eventChat, chat)
	if err != nil {
		return err
	}

	switch {
	case msg.ToUniqueID != "":
		return s.sendDirectByUniqueID(ctx, client, msg, payload, binding...)
	default: // msg.ToClientID != ""
		if err := s.deps.Broadcast.BroadcastToClient(msg.ToClientID, payload); err != nil {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeNotFound, "target client not reachable")
		}
		// Echo the direct message back to the sender.
		if err := s.echoAcceptedDirect(client, msg, payload); err != nil {
			return err
		}
		s.metricsSink().IncChatMessage("direct")
		s.pushDirect(client, chat.ToUniqueID)
	}
	return s.acknowledgeChat(client, msg, netproto.ChatRelayed, 0)
}

// sendDirectByUniqueID delivers a direct message to the user with the given
// unique ID. If the user is online the message is delivered immediately (and
// echoed to the sender); otherwise it is spooled into offline_messages for
// delivery at their next login.
func (s *TCPServer) sendDirectByUniqueID(ctx context.Context, client *Client, msg netproto.ChatSend, payload []byte, binding ...store.DMKeyBinding) error {
	// A single account-wide copy cannot reach the recipient's phones.
	if perDevice, err := s.requiresDeviceCopies(ctx, msg.ToUniqueID); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "device directory unavailable")
	} else if perDevice {
		return s.rejectStaleDevices(ctx, client, errDMUpdateRequired)
	}
	// Guests have authenticated live identities but no account row. Resolve
	// the online session before consulting account storage for offline spooling.
	tc, _ := s.clientByUniqueID(msg.ToUniqueID)
	if tc != nil && tc.isWebTransport() {
		// Phones only take per-device copies; spool for the account instead.
		tc = nil
	}
	if msg.RecipientPublicKey != "" {
		// Check the same durable record for online and offline accounts.
		// A matching live device is a delivery optimization, not a key oracle.
		if s.deps.Auth != nil {
			stored, err := s.deps.Auth.GetE2EPublicKey(ctx, msg.ToUniqueID)
			if err != nil && !errors.Is(err, auth.ErrUserNotFound) {
				return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "recipient encryption key unavailable")
			}
			if tc == nil || tc.userID() > 0 {
				key, valid := canonicalDMKey(stored)
				if !valid || key != msg.RecipientPublicKey {
					return s.rejectStaleDMKey(client)
				}
			}
		}
		var online bool
		tc, online = s.boundDMRecipient(msg.ToUniqueID, "", msg.RecipientPublicKey)
		if tc == nil && online {
			// Registered recipients can receive on their matching device later.
			// Guests have no durable mailbox.
			if s.deps.Auth == nil {
				return s.rejectStaleDMKey(client)
			}
			if user, err := s.deps.Auth.LookupUser(ctx, msg.ToUniqueID); err != nil || user == nil {
				return s.rejectStaleDMKey(client)
			}
		}
	}

	if tc != nil {
		if err := s.deps.Broadcast.BroadcastToClient(tc.ID, payload); err != nil {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeNotFound, "target client not reachable")
		}
		if err := s.echoAcceptedDirect(client, msg, payload); err != nil {
			return err
		}
		s.metricsSink().IncChatMessage("direct")
		s.pushDirect(client, msg.ToUniqueID)
		return s.acknowledgeChat(client, msg, netproto.ChatQueued, 0)
	}

	if s.deps.Auth == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "authentication backend unavailable")
	}
	target, err := s.deps.Auth.LookupUser(ctx, msg.ToUniqueID)
	if err != nil {
		if errors.Is(err, auth.ErrUserNotFound) {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeNotFound, "target user not found")
		}
		s.logger.Warn("user lookup failed",
			zap.String("client_id", client.ID),
			zap.Error(err),
		)
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "user lookup failed")
	}

	if s.deps.Spool == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeNotFound, "target user is offline")
	}
	if msg.RecipientPublicKey != "" {
		storedKey, err := s.deps.Auth.GetE2EPublicKey(ctx, msg.ToUniqueID)
		if err != nil {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "recipient encryption key unavailable")
		}
		key, valid := canonicalDMKey(storedKey)
		if !valid || key != msg.RecipientPublicKey {
			return s.rejectStaleDMKey(client)
		}
	}
	// Only end-to-end ciphertext is spooled (sendSessionChat already refused
	// plaintext and scope-sealed DMs; this guards the spool itself).
	if !msg.Enc {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, errPlaintextChat)
	}
	if msg.KeyID != 0 {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, errServerSealedDM)
	}
	// E2EE DMs are spooled as ciphertext the server cannot read; the sender's
	// unique ID travels along so the recipient can fetch the public key.
	if err := s.deps.Spool.SpoolMessage(ctx, client.userID(), target.ID, client.UniqueID, msg.Text, binding...); err != nil {
		s.logger.Warn("spooling message failed",
			zap.String("client_id", client.ID),
			zap.Error(err),
		)
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "spooling message failed")
	}
	s.logger.Info("message spooled for offline user",
		zap.String("client_id", client.ID),
		zap.String("to_unique_id", msg.ToUniqueID),
	)
	// Echo accepted legacy and acknowledged messages identically, irrespective
	// of recipient presence. This is acceptance, not a delivery receipt.
	if err := s.echoAcceptedDirect(client, msg, payload); err != nil {
		return err
	}
	s.pushDirect(client, msg.ToUniqueID)
	return s.acknowledgeChat(client, msg, netproto.ChatQueued, 0)
}

// spoolPeer is the account a spooled message was addressed to: the
// recipient, or the contact when the sender's own device sent it.
func spoolPeer(m store.SpooledMessage, recipient string) string {
	if m.PeerUniqueID != "" {
		return m.PeerUniqueID
	}
	return recipient
}

// deviceCopyKey is the sending device's key of a per-device copy.
func deviceCopyKey(m store.SpooledMessage) string {
	if m.RecipientDeviceID == "" {
		return ""
	}
	return m.SenderPublicKey
}

// pushDirect notifies the recipient's phones about a direct message. The
// payload names the sender only; the message stays end-to-end encrypted.
func (s *TCPServer) pushDirect(client *Client, toUniqueID string) {
	if toUniqueID == "" || toUniqueID == client.uniqueID() {
		return
	}
	s.pushNotify(toUniqueID, pushNotice{Kind: pushKindDM, Tag: netproto.ReadScopeDM + ":" + client.uniqueID(), From: client.nickname()})
}

// deliverSpooled sends any spooled offline messages for the user to the
// client as offline chat events and marks them delivered.
func (s *TCPServer) deliverSpooled(ctx context.Context, client *Client, userID int64) {
	if userID <= 0 || s.deps.Spool == nil || s.deps.Broadcast == nil {
		return
	}
	// Concurrent sessions must not both read and deliver the same pending row.
	// A fixed stripe set bounds memory while retaining single-recipient delivery.
	lock := &s.spoolDelivery[userID%int64(len(s.spoolDelivery))]
	lock.Lock()
	defer lock.Unlock()
	deviceKey, _ := s.senderDMKey(client)
	msgs, err := s.deps.Spool.PendingMessages(ctx, userID)
	if err != nil {
		s.logger.Warn("loading spooled messages failed",
			zap.String("client_id", client.ID),
			zap.Error(err),
		)
		return
	}
	if len(msgs) == 0 {
		return
	}

	device := client.currentDeviceID()
	ids := make([]int64, 0, len(msgs))
	for _, m := range msgs {
		if m.RecipientDeviceID != "" {
			// A per-device copy opens only on its device.
			if m.RecipientDeviceID != device {
				continue
			}
		} else if client.isWebTransport() {
			continue // account-wide copies are sealed to the desktop's key
		} else if m.SenderPublicKey != "" || m.RecipientPublicKey != "" {
			_, senderValid := canonicalDMKey(m.SenderPublicKey)
			recipient, recipientValid := canonicalDMKey(m.RecipientPublicKey)
			if !senderValid || !recipientValid || recipient != deviceKey {
				continue // Retain ciphertext for the device that can actually open it.
			}
		}
		// Every spooled row is E2EE ciphertext: 012 deleted the undelivered
		// pre-4b plaintext rows and offline_messages_sealed stops new ones,
		// so there is no plaintext replay branch left to take (91).
		payload, err := eventEnvelope(eventChat, netproto.ChatBroadcast{
			FromClientID:       strconv.FormatInt(m.FromUserID, 10),
			FromUniqueID:       m.FromUniqueID,
			From:               m.FromName,
			ToUniqueID:         spoolPeer(m, client.uniqueID()),
			Text:               m.Message,
			Direct:             true,
			Offline:            true,
			Enc:                true,
			E2E:                true,
			SenderPublicKey:    m.SenderPublicKey,
			RecipientPublicKey: m.RecipientPublicKey,
			ClientMsgID:        m.ClientMsgID,
			SenderDeviceID:     m.SenderDeviceID,
			SenderDeviceKey:    deviceCopyKey(m),
			RecipientDeviceID:  m.RecipientDeviceID,
			DMAction:           m.DMAction,
			RefClientMsgID:     m.RefClientMsgID,
		})
		if err != nil {
			continue
		}
		if err := s.deps.Broadcast.BroadcastToClient(client.ID, payload); err != nil {
			s.logger.Warn("delivering spooled message failed",
				zap.String("client_id", client.ID),
				zap.Int64("message_id", m.ID),
				zap.Error(err),
			)
			continue
		}
		ids = append(ids, m.ID)
	}

	if err := s.deps.Spool.MarkMessagesDelivered(ctx, ids); err != nil {
		s.logger.Warn("marking spooled messages delivered failed",
			zap.String("client_id", client.ID),
			zap.Error(err),
		)
	}
}
