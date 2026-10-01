package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

func checkRoleNativeSessionTraffic(s *roleScenario, c *checkCtx) (retErr error) {
	policy, err := s.policy()
	if err != nil {
		return err
	}
	preflight, err := e2eQueryJSON[netproto.RoleChannelState](s.query, "channelquery", netproto.RoleChannelQuery{Kind: authorization.ChannelCreate})
	if err != nil {
		return err
	}
	if preflight.Revision != s.revision || !preflight.CanCreatePermanent || !preflight.CanManageAccess || preflight.EveryoneID != policy.EveryoneID {
		return errors.New("native channel preflight differs from owner policy")
	}
	settings := preflight.Settings
	settings.Name = "e2e-native-" + randHex(8)
	access := authorization.ChannelPolicy{}
	for _, cap := range []authorization.Capability{authorization.ViewChannel, authorization.Connect, authorization.ReadHistory, authorization.SendMessages} {
		access.Overrides = append(access.Overrides, authorization.RoleOverride{RoleID: policy.EveryoneID, Capability: cap, Effect: authorization.Allow})
	}
	created, err := s.changeChannel(netproto.RoleChannelChange{Kind: authorization.ChannelCreate, ChannelType: 2, Settings: &settings, Access: &netproto.RoleChannelAccess{Overrides: access.Overrides}})
	if err != nil {
		return err
	}
	s.halted = true
	c.channelID, access.ChannelID = created.ChannelID, created.ChannelID
	saved, err := e2eQueryJSON[netproto.RoleChannelState](s.query, "channelquery", netproto.RoleChannelQuery{Kind: authorization.ChannelEdit, ChannelID: c.channelID})
	if err != nil {
		return fmt.Errorf("inspect native test channel %d: %w", c.channelID, err)
	}
	if saved.Revision != s.revision || saved.ChannelID != c.channelID || saved.Settings.Name != settings.Name {
		return fmt.Errorf("native test channel %d identity not verified; cleanup not attempted", c.channelID)
	}
	s.halted = false
	defer func() {
		c.close()
		c.alice, c.bob, c.guest = nil, nil, nil
		if s.halted {
			retErr = errors.Join(retErr, fmt.Errorf("inspect native test channel %d; cleanup not attempted after an uncertain write", c.channelID))
			return
		}
		_, err := s.changeChannel(netproto.RoleChannelChange{Kind: authorization.ChannelDelete, ChannelID: c.channelID})
		retErr = errors.Join(retErr, err)
	}()
	for _, cl := range []*client{c.alice, c.bob, c.guest} {
		if err := writeMsg(cl.conn, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: c.channelID}); err != nil {
			return err
		}
		if err := waitForRoleMembership(cl.conn, cl.clientID, c.channelID); err != nil {
			return fmt.Errorf("native join: %w", err)
		}
	}
	// Channel and guest traffic must be decryptable by both sender and recipient.
	for _, pair := range [][2]*client{{c.alice, c.bob}, {c.guest, c.bob}, {c.bob, c.guest}} {
		if _, err := exchangeRoleChat(pair[0], pair[1], c.channelID, "channel-"+randHex(8)); err != nil {
			return err
		}
	}
	if err := checkRoleDirectChat(c); err != nil {
		return err
	}
	if _, err := exchangeRoleChat(c.alice, c.bob, 0, "global-"+randHex(8)); err != nil {
		return err
	}
	if err := checkChatHistory(c); err != nil {
		return err
	}
	if err := checkRoleEditDelete(c); err != nil {
		return err
	}
	for _, cl := range []*client{c.alice, c.guest} {
		if err := checkDeniedRoleChannelDelete(s, cl, c.channelID); err != nil {
			return err
		}
	}
	// Existing native sessions must immediately observe new SendMessages rules.
	for i := range access.Overrides {
		if access.Overrides[i].Capability == authorization.SendMessages {
			access.Overrides[i].Effect = authorization.Deny
		}
	}
	// Keep Alice as an authorized observer. Her confirmed marker bounds the
	// recipient queue checked for forbidden traffic from Bob or the guest.
	access.Overrides = append(access.Overrides, authorization.RoleOverride{UserID: s.alice.UserID, Capability: authorization.SendMessages, Effect: authorization.Allow})
	if _, err := s.changeRole(authorization.RoleChange{Kind: authorization.ChannelAccessSet, Channel: access}); err != nil {
		return err
	}
	for _, cl := range []*client{c.bob, c.guest} {
		if err := checkRoleRejectedChat(cl, c.alice, c.channelID, 4, ""); err != nil {
			return err
		}
	}
	access.Overrides = append(access.Overrides, authorization.RoleOverride{UserID: s.bob.UserID, Capability: authorization.SendMessages, Effect: authorization.Allow})
	if _, err := s.changeRole(authorization.RoleChange{Kind: authorization.ChannelAccessSet, Channel: access}); err != nil {
		return err
	}
	if _, err := exchangeRoleChat(c.bob, c.alice, c.channelID, "member-allow-"+randHex(8)); err != nil {
		return err
	}
	if err := checkRoleRejectedChat(c.guest, c.alice, c.channelID, 4, ""); err != nil {
		return err
	}
	// Test a fresh sender under slow mode, then confirm the same session recovers.
	for i := range access.Overrides {
		if access.Overrides[i].Capability == authorization.SendMessages {
			access.Overrides[i].Effect = authorization.Allow
		}
	}
	if _, err := s.changeRole(authorization.RoleChange{Kind: authorization.ChannelAccessSet, Channel: access}); err != nil {
		return err
	}
	settings = saved.Settings
	settings.SlowModeSeconds = 30
	if _, err := s.changeChannel(netproto.RoleChannelChange{Kind: authorization.ChannelEdit, ChannelID: c.channelID, Settings: &settings}); err != nil {
		return err
	}
	if _, err := exchangeRoleChat(c.guest, c.bob, c.channelID, "slow-first-"+randHex(8)); err != nil {
		return err
	}
	if err := checkRoleRejectedChat(c.guest, c.alice, c.channelID, 2, "slow mode"); err != nil {
		return err
	}
	settings.SlowModeSeconds = 0
	if _, err := s.changeChannel(netproto.RoleChannelChange{Kind: authorization.ChannelEdit, ChannelID: c.channelID, Settings: &settings}); err != nil {
		return err
	}
	if _, err := exchangeRoleChat(c.guest, c.bob, c.channelID, "slow-recovered-"+randHex(8)); err != nil {
		return err
	}
	return checkRoleFiles(s, c, access)
}

func checkDeniedRoleChannelDelete(s *roleScenario, cl *client, channelID int64) error {
	if s.halted {
		return errors.New("scenario already stopped after an uncertain write")
	}
	s.halted = true
	if err := writeMsg(cl.conn, netproto.MsgRoleChannelChange, netproto.RoleChannelChange{Kind: authorization.ChannelDelete, ChannelID: channelID, ExpectedRevision: s.revision}); err != nil {
		return err
	}
	if err := expectRoleNativeError(cl.conn, netproto.MsgRoleChannelChange, 4, ""); err != nil {
		return err
	}
	s.halted = false
	return nil
}

func checkRoleNativeLogin(c *checkCtx) error {
	o := c.opts
	var err error
	c.alice, err = dialAuth(o.addr, o.aliceNickname, o.alicePass, o.serverPass, netproto.AuthorizationModelRolesV1)
	if err != nil {
		return err
	}
	if c.alice.uid != o.aliceUID {
		return errors.New("role nickname authentication did not return the canonical account")
	}
	c.bob, err = dialAuth(o.addr, o.bobUID, o.bobPass, o.serverPass, netproto.AuthorizationModelRolesV1)
	if err != nil {
		return err
	}
	c.guest, err = dialGuest(o.addr, "e2e-role-guest", o.serverPass, netproto.AuthorizationModelRolesV1)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(c.guest.uid, "guest:") || c.guest.nickname != "e2e-role-guest" {
		return errors.New("role guest identity invalid")
	}
	bad, err := dialTCP(o.addr)
	if err != nil {
		return err
	}
	defer closeE2EResource(bad)
	if err := writeMsg(bad, netproto.MsgAuthenticate, netproto.Authenticate{Username: o.aliceUID, Password: "invalid-" + randHex(8), ServerPassword: o.serverPass, AuthorizationModels: []string{netproto.AuthorizationModelRolesV1}}); err != nil {
		return err
	}
	f, err := readOfType(bad, netproto.MsgAuthResponse, readTimeout)
	if err != nil {
		return err
	}
	var response netproto.AuthResponse
	if err := netproto.Decode(f, &response); err != nil {
		return err
	}
	if response.OK {
		return errors.New("invalid role account password accepted")
	}
	return nil
}

func sendRoleChat(cl *client, channelID int64, body string) (netproto.ChatSend, error) {
	if err := awaitScopeKey(cl.conn, cl, channelID); err != nil {
		return netproto.ChatSend{}, err
	}
	id := cl.scopeLatest[channelID]
	blob, err := e2eSealScope(body, cl.scopeKeys[channelID][id])
	if err != nil {
		return netproto.ChatSend{}, err
	}
	request := netproto.ChatSend{Text: blob, Enc: true, KeyID: id, ClientMsgID: "e2e-" + randHex(12)}
	if channelID != 0 {
		request.ChannelID = strconv.FormatInt(channelID, 10)
	}
	return request, writeMsg(cl.conn, netproto.MsgChatSend, request)
}

func exchangeRoleChat(sender, receiver *client, channelID int64, body string) (netproto.ChatBroadcast, error) {
	request, err := sendRoleChat(sender, channelID, body)
	if err != nil {
		return netproto.ChatBroadcast{}, err
	}
	got, err := readRoleChat(receiver, sender, channelID, request.ClientMsgID, body)
	if err != nil {
		return got, err
	}
	if _, err := readRoleChat(sender, sender, channelID, request.ClientMsgID, body); err != nil {
		return got, err
	}
	return got, nil
}

func readRoleChat(receiver, sender *client, channelID int64, ref, body string, forbiddenRefs ...string) (netproto.ChatBroadcast, error) {
	deadline := time.Now().Add(readTimeout)
	wantChannel := ""
	if channelID != 0 {
		wantChannel = strconv.FormatInt(channelID, 10)
	}
	for time.Now().Before(deadline) {
		env, err := readEvent(receiver.conn, "chat", time.Until(deadline))
		if err != nil {
			return netproto.ChatBroadcast{}, err
		}
		var got netproto.ChatBroadcast
		if err := json.Unmarshal(env.Data, &got); err != nil {
			return got, err
		}
		if slices.Contains(forbiddenRefs, got.ClientMsgID) {
			return got, errors.New("rejected native chat reached the observer")
		}
		if got.ChannelID != wantChannel || got.FromUniqueID != sender.uid || got.FromClientID != sender.clientID || got.ClientMsgID != ref || got.Direct {
			continue
		}
		if !got.Enc || got.Text == body || got.ID <= 0 {
			return got, errors.New("correlated chat is not stored encrypted traffic")
		}
		key, ok := receiver.scopeKeys[channelID][got.KeyID]
		if !ok {
			return got, errors.New("correlated chat uses an unavailable scope key")
		}
		plain, err := e2eOpenScope(got.Text, key)
		if err != nil || plain != body {
			return got, errors.New("correlated chat did not decrypt to the sent body")
		}
		return got, nil
	}
	return netproto.ChatBroadcast{}, errors.New("correlated encrypted chat not received")
}

func expectRoleNativeError(conn net.Conn, origin netproto.MessageType, code uint16, detail string) error {
	f, err := readOfType(conn, netproto.MsgError, readTimeout)
	if err != nil {
		return err
	}
	var result netproto.Error
	if err := netproto.Decode(f, &result); err != nil {
		return err
	}
	return validateRoleNativeError(result, origin, code, detail)
}

func validateRoleNativeError(result netproto.Error, origin netproto.MessageType, code uint16, detail string) error {
	if result.Code != code || result.OriginType != uint16(origin) || (detail != "" && !strings.Contains(result.Message, detail)) {
		return fmt.Errorf("unexpected native rejection code=%d origin=%d", result.Code, result.OriginType)
	}
	return nil
}

func checkRoleDirectChat(c *checkCtx) error {
	// Resolve both keys before sending: reading a key response also consumes
	// intervening frames, including a DM that arrived before that response.
	alicePub, err := fetchPub(c.bob.conn, c.alice.uid)
	if err != nil {
		return err
	}
	bobPub, err := fetchPub(c.alice.conn, c.bob.uid)
	if err != nil {
		return err
	}
	body, ref := "direct-"+randHex(8), "dm-"+randHex(12)
	blob, err := e2eSealDM(body, alicePub, c.bob.e2ePriv)
	if err != nil {
		return err
	}
	if err := writeMsg(c.bob.conn, netproto.MsgChatSend, netproto.ChatSend{ToUniqueID: c.alice.uid, Text: blob, Enc: true, ClientMsgID: ref}); err != nil {
		return err
	}
	deadline := time.Now().Add(readTimeout)
	for time.Now().Before(deadline) {
		env, err := readEvent(c.alice.conn, "chat", time.Until(deadline))
		if err != nil {
			return err
		}
		var got netproto.ChatBroadcast
		if err := json.Unmarshal(env.Data, &got); err != nil {
			return err
		}
		if got.ClientMsgID != ref || got.FromUniqueID != c.bob.uid || got.FromClientID != c.bob.clientID || got.ToUniqueID != c.alice.uid {
			continue
		}
		if !got.Direct || !got.Enc || !got.E2E || got.Text == body {
			return errors.New("correlated DM lacks encrypted direct routing")
		}
		plain, err := e2eOpenDM(got.Text, bobPub, c.alice.e2ePriv)
		if err != nil || plain != body {
			return errors.New("correlated DM did not decrypt")
		}
		return nil
	}
	return errors.New("correlated DM not received")
}

func checkRoleEditDelete(c *checkCtx) error {
	chat, err := exchangeRoleChat(c.alice, c.bob, c.channelID, "edit-"+randHex(8))
	if err != nil {
		return err
	}
	// A non-author without ManageMessages cannot delete this known message.
	if err := writeMsg(c.bob.conn, netproto.MsgChatDelete, netproto.ChatDelete{MessageID: chat.ID}); err != nil {
		return err
	}
	// Message-level permission failures intentionally conceal the target.
	if err := expectRoleNativeError(c.bob.conn, netproto.MsgChatDelete, 6, ""); err != nil {
		return err
	}
	body := "edited-" + randHex(8)
	id := c.alice.scopeLatest[c.channelID]
	blob, err := e2eSealScope(body, c.alice.scopeKeys[c.channelID][id])
	if err != nil {
		return err
	}
	if err := writeMsg(c.alice.conn, netproto.MsgChatEdit, netproto.ChatEdit{MessageID: chat.ID, NewText: blob, Enc: true, KeyID: id, ExpectedVersion: chat.Version}); err != nil {
		return err
	}
	env, err := readEvent(c.bob.conn, "chat_edited", readTimeout)
	if err != nil {
		return err
	}
	var edited struct {
		MessageID int64  `json:"message_id"`
		ChannelID int64  `json:"channel_id"`
		Body      string `json:"body"`
		Enc       bool   `json:"enc"`
		KeyID     uint32 `json:"key_id"`
	}
	if err := json.Unmarshal(env.Data, &edited); err != nil {
		return err
	}
	plain, err := e2eOpenScope(edited.Body, c.bob.scopeKeys[c.channelID][edited.KeyID])
	if err != nil || edited.MessageID != chat.ID || edited.ChannelID != c.channelID || !edited.Enc || plain != body {
		return errors.New("edited message did not match the encrypted operation")
	}
	if err := writeMsg(c.alice.conn, netproto.MsgChatDelete, netproto.ChatDelete{MessageID: chat.ID}); err != nil {
		return err
	}
	env, err = readEvent(c.bob.conn, "chat_deleted", readTimeout)
	if err != nil {
		return err
	}
	var deleted struct {
		MessageID int64 `json:"message_id"`
		ChannelID int64 `json:"channel_id"`
	}
	if err := json.Unmarshal(env.Data, &deleted); err != nil {
		return err
	}
	if deleted.MessageID != chat.ID || deleted.ChannelID != c.channelID {
		return errors.New("delete event names a different message")
	}
	if err := writeMsg(c.bob.conn, netproto.MsgChatHistory, netproto.ChatHistory{ChannelID: c.channelID, Limit: 100}); err != nil {
		return err
	}
	f, err := readOfType(c.bob.conn, netproto.MsgChatHistoryResponse, readTimeout)
	if err != nil {
		return err
	}
	var history netproto.ChatHistoryResponse
	if err := netproto.Decode(f, &history); err != nil {
		return err
	}
	if history.ChannelID != c.channelID {
		return errors.New("history names another channel")
	}
	for _, message := range history.Messages {
		if message.ID == chat.ID {
			if !message.Deleted || message.Body != "" || message.BodyEnc != "" || message.KeyID != 0 {
				return errors.New("deleted history retained message contents")
			}
			return nil
		}
	}
	return errors.New("deleted message missing from history")
}
