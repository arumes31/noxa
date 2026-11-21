package server

import (
	"context"
	"encoding/base64"
	"errors"
	"time"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/speechnames"
)

// New name clips are bounded per account, so a session cannot use the server
// as a TTS service and a reconnect does not refill the budget.
const (
	speechSynthesisBurst  = 10
	speechSynthesisWindow = 10 * time.Minute
)

// allowSpeechSynthesis charges one new clip to the requester's account. A
// guest's unique ID can be new on every login, so guests are charged by
// source address (the keyLookupRate pattern).
func (s *TCPServer) allowSpeechSynthesis(client *Client) bool {
	key := "uid:" + client.uniqueID()
	if client.userID() == 0 {
		key = "ip:" + auth.SourceAddressKey(remoteIP(client.Conn))
	}
	return s.speechSynthRate.allow(key, time.Now())
}

// handleSpokenNameGet answers with the spoken name of a session the requester
// can see.
func (s *TCPServer) handleSpokenNameGet(ctx context.Context, client *Client, f *netproto.Frame) error {
	var msg netproto.SpokenNameGet
	if netproto.Decode(f, &msg) != nil || (msg.Language != "en" && msg.Language != "de") || len(msg.ClientID) > 128 {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid spoken name request")
	}
	if s.deps == nil || !s.deps.SpeechNames.Enabled() || s.deps.State == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "spoken names unavailable")
	}
	nickname := ""
	if err := s.rolePolicyRead(ctx, client, func(ctx context.Context) error {
		e := ctx.Value(roleLeaseKey{}).(roleLease).evaluator
		member, ok := s.deps.State.GetClient(msg.ClientID)
		// The same visibility as avatars: present, in a visible channel, and
		// not hidden unless the requester may see connection info.
		if ok && e.Evaluate(client.userID(), member.ChannelID, authorization.ViewChannel).Allowed &&
			(!s.deps.State.PresenceHidden(member) || e.Evaluate(client.userID(), 0, authorization.ViewConnectionInfo).Allowed) {
			nickname = member.Nickname
		}
		return nil
	}); err != nil {
		return err
	}
	if nickname == "" {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeNotFound, "no spoken name for this session")
	}
	// A stored clip is a file read. A new one is synthesized in the background
	// and answered as pending at once, so no request waits for the engine.
	key, wav, err := s.deps.SpeechNames.Clip(msg.Language, nickname,
		func() bool { return s.allowSpeechSynthesis(client) })
	reply := netproto.SpokenNameData{ClientID: msg.ClientID, Language: msg.Language, Nickname: nickname, Key: key}
	switch {
	case errors.Is(err, speechnames.ErrPending):
		reply.Pending = true
	case errors.Is(err, speechnames.ErrRateLimited):
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "spoken names busy")
	case errors.Is(err, speechnames.ErrUnspeakable):
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeNotFound, "nothing speakable in this name")
	case err != nil:
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "spoken name unavailable")
	default:
		reply.DataBase64 = base64.StdEncoding.EncodeToString(wav)
	}
	return s.writeMessage(client, netproto.MsgSpokenNameData, reply)
}
