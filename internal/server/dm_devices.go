package server

import (
	"context"
	"crypto/ed25519"
	"errors"
	"time"

	"go.uber.org/zap"

	"noxa/internal/auth"
	"noxa/internal/netproto"
	"noxa/internal/store"
)

// DeviceMessageStore holds per-device message state: endorsements and
// history copies. The device store implements it.
type DeviceMessageStore interface {
	EndorseDevice(ctx context.Context, principal, deviceID, approverID string, signature []byte) (store.Device, error)
	PutHistoryChunk(ctx context.Context, principal string, c store.HistoryChunk, maxBytes int64) error
	HistoryChunks(ctx context.Context, principal, deviceID string, from, limit int) ([]store.HistoryChunk, error)
	AckHistoryChunks(ctx context.Context, principal, deviceID string, through int) error
	ExpireHistoryChunks(ctx context.Context, before time.Time) (int64, error)
}

// historyTTL bounds how long uploaded history waits for its device.
const historyTTL = 24 * time.Hour

// historyPage bounds the chunks one history_get returns.
const historyPage = 2

const errDMUpdateRequired = "this person uses noXa on several devices; update noXa to send them direct messages"

func (s *TCPServer) deviceMessageStore() (DeviceMessageStore, bool) {
	if s.deps == nil || s.deps.Devices == nil {
		return nil, false
	}
	d, ok := s.deps.Devices.(DeviceMessageStore)
	return d, ok
}

// dmDevices lists the principal's active devices that can receive direct
// messages: the first device and devices with an approval signature.
func (s *TCPServer) dmDevices(ctx context.Context, principal string) ([]netproto.DMDevice, error) {
	if s.deps == nil || s.deps.Devices == nil || principal == "" {
		return nil, nil
	}
	devices, err := s.deps.Devices.ListDevices(ctx, principal, false)
	if err != nil {
		return nil, err
	}
	out := make([]netproto.DMDevice, 0, len(devices))
	for _, d := range devices {
		if !d.FirstDevice && len(d.ApprovalSignature) == 0 {
			continue // linked with a passkey, not yet endorsed
		}
		if d.X25519Public == "" {
			continue
		}
		out = append(out, netproto.DMDevice{
			DeviceID: d.DeviceID, Kind: d.Kind, Ed25519Public: d.Ed25519Public, X25519Public: d.X25519Public,
			FirstDevice: d.FirstDevice, ApprovedBy: d.ApprovedBy, ApprovalSignature: d.ApprovalSignature,
		})
	}
	return out, nil
}

// requiresDeviceCopies reports whether direct messages to the principal must
// be sent per device: it has a trusted web device that the account-wide key
// cannot reach.
func (s *TCPServer) requiresDeviceCopies(ctx context.Context, principal string) (bool, error) {
	devices, err := s.dmDevices(ctx, principal)
	if err != nil {
		return false, err
	}
	for _, d := range devices {
		if d.Kind == store.DeviceKindWeb {
			return true, nil
		}
	}
	return false, nil
}

func (s *TCPServer) rejectStaleDevices(ctx context.Context, client *Client, message string) error {
	return s.writeMessage(client, netproto.MsgError, netproto.Error{
		Code: errCodeConflict, OriginType: uint16(requestOrigin(ctx)), RecipientDevicesStale: true, Message: message,
	})
}

// sendDirectCopies delivers a direct message sealed once per device. Each
// copy goes to its device's live sessions or is spooled for that device.
func (s *TCPServer) sendDirectCopies(ctx context.Context, client *Client, msg netproto.ChatSend) error {
	origin := requestOrigin(ctx)
	if msg.ToUniqueID == "" || msg.ToClientID != "" || msg.Text != "" || msg.RecipientPublicKey != "" || len(msg.Copies) > netproto.MaxDMCopies {
		return s.sendErrorFor(client, origin, errCodeMalformed, "invalid direct-message copies")
	}
	senderDevice := client.currentDeviceID()
	principal := client.uniqueID()
	if senderDevice == "" {
		return s.sendErrorFor(client, origin, errCodePermissionDenied, "this device is not registered for direct messages")
	}
	recipientDevices, err := s.dmDevices(ctx, msg.ToUniqueID)
	if err != nil {
		return s.sendErrorFor(client, origin, errCodeUnavailable, "device directory unavailable")
	}
	if len(recipientDevices) == 0 {
		return s.rejectStaleDevices(ctx, client, "the recipient has no devices; send one copy to the account key")
	}
	ownDevices := recipientDevices
	if msg.ToUniqueID != principal {
		if ownDevices, err = s.dmDevices(ctx, principal); err != nil {
			return s.sendErrorFor(client, origin, errCodeUnavailable, "device directory unavailable")
		}
	}
	type target struct {
		device    netproto.DMDevice
		principal string
	}
	targets := make(map[string]target)
	for _, d := range ownDevices {
		targets[d.DeviceID] = target{d, principal}
	}
	for _, d := range recipientDevices {
		targets[d.DeviceID] = target{d, msg.ToUniqueID}
	}
	sender, trusted := targets[senderDevice]
	if !trusted || sender.principal != principal {
		return s.sendErrorFor(client, origin, errCodePermissionDenied, "confirm this device on your desktop before sending direct messages")
	}
	seen := make(map[string]bool, len(msg.Copies))
	for _, c := range msg.Copies {
		if _, known := targets[c.DeviceID]; !known || seen[c.DeviceID] {
			return s.rejectStaleDevices(ctx, client, "direct-message devices changed; refresh them before retrying")
		}
		if c.Text == "" || len(c.Text) > maxChatBytes {
			return s.sendErrorFor(client, origin, errCodeMalformed, "chat message too large")
		}
		seen[c.DeviceID] = true
	}
	for _, d := range recipientDevices {
		if !seen[d.DeviceID] {
			return s.rejectStaleDevices(ctx, client, "direct-message devices changed; refresh them before retrying")
		}
	}

	userIDs := map[string]int64{principal: client.userID()}
	if s.deps.Auth != nil && msg.ToUniqueID != principal {
		if user, err := s.deps.Auth.LookupUser(ctx, msg.ToUniqueID); err == nil && user != nil {
			userIDs[msg.ToUniqueID] = user.ID
		} else if err != nil && !errors.Is(err, auth.ErrUserNotFound) {
			return s.sendErrorFor(client, origin, errCodeUnavailable, "user lookup failed")
		}
	}
	for _, c := range msg.Copies {
		t := targets[c.DeviceID]
		payload, err := eventEnvelope(eventChat, netproto.ChatBroadcast{
			Direct: true, ToUniqueID: msg.ToUniqueID, FromClientID: client.ID, FromUniqueID: principal, From: client.nickname(),
			Text: c.Text, Enc: true, E2E: true, ClientMsgID: msg.ClientMsgID,
			SenderDeviceID: senderDevice, SenderDeviceKey: sender.device.X25519Public, RecipientDeviceID: c.DeviceID,
		})
		if err != nil {
			return err
		}
		if c.DeviceID == senderDevice {
			if err := s.echoAcceptedDirect(client, msg, payload); err != nil {
				return err
			}
			continue
		}
		delivered := false
		for _, session := range s.sessionsOf(t.principal) {
			if session.currentDeviceID() == c.DeviceID && s.deps.Broadcast.BroadcastToClient(session.ID, payload) == nil {
				delivered = true
			}
		}
		if delivered {
			continue
		}
		toUserID := userIDs[t.principal]
		if s.deps.Spool == nil || toUserID <= 0 {
			continue // guests and spool-less servers receive live copies only
		}
		if err := s.deps.Spool.SpoolMessage(ctx, client.userID(), toUserID, principal, c.Text, store.DMKeyBinding{
			SenderPublicKey: sender.device.X25519Public, RecipientPublicKey: t.device.X25519Public, ClientMsgID: msg.ClientMsgID,
			SenderDeviceID: senderDevice, RecipientDeviceID: c.DeviceID,
		}); err != nil {
			s.logger.Warn("spooling device copy failed", zap.String("client_id", client.ID), zap.Error(err))
			return s.sendErrorFor(client, origin, errCodeUnavailable, "spooling message failed")
		}
	}
	s.metricsSink().IncChatMessage("direct")
	s.pushDirect(client, msg.ToUniqueID)
	return s.acknowledgeChat(client, msg, netproto.ChatQueued, 0)
}

// handleEndorse records a trusted device's signature for a passkey-linked
// device of the same account.
func (s *TCPServer) handleEndorse(ctx context.Context, client *Client, r netproto.DeviceRequest) error {
	messages, ok := s.deviceMessageStore()
	if !ok {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "device management is unavailable")
	}
	principal := client.uniqueID()
	approver := client.currentDeviceID()
	trusted, err := s.dmDevices(ctx, principal)
	if err != nil {
		return s.deviceFailure(ctx, client, err)
	}
	var approverKey string
	for _, d := range trusted {
		if d.DeviceID == approver {
			approverKey = d.Ed25519Public
		}
	}
	if approverKey == "" {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "only a confirmed device can confirm another device")
	}
	target, err := s.deps.Devices.DeviceByID(ctx, principal, r.DeviceID)
	if err != nil {
		return s.deviceFailure(ctx, client, err)
	}
	signer, err := auth.LoadPublicKey(approverKey)
	if err != nil || !ed25519.Verify(ed25519.PublicKey(signer), netproto.DeviceApprovalTranscript(principal, approver, target.Ed25519Public, target.X25519Public), r.Signature) {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "invalid confirmation signature")
	}
	d, err := messages.EndorseDevice(ctx, principal, r.DeviceID, approver, r.Signature)
	if err != nil {
		return s.deviceFailure(ctx, client, err)
	}
	info := deviceInfo(d, "")
	s.notifyDevices(principal, netproto.DeviceEvent{Event: netproto.DeviceEventEndorsed, Device: &info})
	return s.writeMessage(client, netproto.MsgDeviceResult, netproto.DeviceResult{Action: r.Action, Device: &info})
}

// handleHistory serves the history copy actions.
func (s *TCPServer) handleHistory(ctx context.Context, client *Client, r netproto.DeviceRequest) error {
	messages, ok := s.deviceMessageStore()
	if !ok {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "device management is unavailable")
	}
	principal := client.uniqueID()
	device := client.currentDeviceID()
	if device == "" {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "this device is not registered")
	}
	switch r.Action {
	case netproto.DeviceActionHistoryPut:
		if r.DeviceID == device {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "a device cannot copy history to itself")
		}
		trusted, err := s.dmDevices(ctx, principal)
		if err != nil {
			return s.deviceFailure(ctx, client, err)
		}
		sender := false
		for _, d := range trusted {
			sender = sender || d.DeviceID == device
		}
		if !sender {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "only a confirmed device can copy history")
		}
		chunk := store.HistoryChunk{DeviceID: r.DeviceID, Seq: r.History.Seq, Total: r.History.Total, FromDeviceID: device, Data: r.History.Data}
		if err := messages.PutHistoryChunk(ctx, principal, chunk, netproto.MaxHistoryBytes); err != nil {
			if errors.Is(err, store.ErrHistoryLimit) {
				return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "history copy exceeds 50 MB")
			}
			return s.deviceFailure(ctx, client, err)
		}
		if r.History.Seq == r.History.Total-1 {
			for _, session := range s.sessionsOf(principal) {
				if session.currentDeviceID() == r.DeviceID {
					_ = s.writeMessage(session, netproto.MsgDeviceEvent, netproto.DeviceEvent{Event: netproto.DeviceEventHistoryReady, HistoryTotal: r.History.Total})
				}
			}
		}
		return s.writeMessage(client, netproto.MsgDeviceResult, netproto.DeviceResult{Action: r.Action})
	case netproto.DeviceActionHistoryGet:
		chunks, err := messages.HistoryChunks(ctx, principal, device, r.Seq, historyPage)
		if err != nil {
			return s.deviceFailure(ctx, client, err)
		}
		out := netproto.DeviceResult{Action: r.Action, History: []netproto.HistoryChunk{}}
		for _, c := range chunks {
			out.History = append(out.History, netproto.HistoryChunk{Seq: c.Seq, Total: c.Total, FromDeviceID: c.FromDeviceID, Data: c.Data})
		}
		return s.writeMessage(client, netproto.MsgDeviceResult, out)
	default: // history_ack
		if err := messages.AckHistoryChunks(ctx, principal, device, r.Seq); err != nil {
			return s.deviceFailure(ctx, client, err)
		}
		return s.writeMessage(client, netproto.MsgDeviceResult, netproto.DeviceResult{Action: r.Action})
	}
}

// expireHistoryCopies drops history copies nobody fetched in time.
func (s *TCPServer) expireHistoryCopies(ctx context.Context, now time.Time) {
	messages, ok := s.deviceMessageStore()
	if !ok {
		return
	}
	if n, err := messages.ExpireHistoryChunks(ctx, now.Add(-historyTTL)); err != nil {
		s.logger.Warn("expiring history copies failed", zap.Error(err))
	} else if n > 0 {
		s.logger.Info("expired history copies", zap.Int64("chunks", n))
	}
}
