package server

import (
	"context"
	"encoding/base64"
	"errors"
	"time"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/speechnames"
)

// handleSpokenNameGet answers with the spoken name of a session the requester
// can see. A first synthesis takes seconds, so the clip is produced off the
// session's read loop and the reply follows asynchronously.
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
	origin := requestOrigin(ctx)
	go func() {
		key, wav, err := s.deps.SpeechNames.Clip(context.Background(), msg.Language, nickname,
			func() bool { return client.requestBudget.allowSpeechSynthesis(time.Now()) })
		if !client.isAuthed() {
			return
		}
		switch {
		case errors.Is(err, speechnames.ErrRateLimited):
			_ = s.sendErrorFor(client, origin, errCodeUnavailable, "spoken names busy")
		case errors.Is(err, speechnames.ErrUnspeakable):
			_ = s.sendErrorFor(client, origin, errCodeNotFound, "nothing speakable in this name")
		case err != nil:
			_ = s.sendErrorFor(client, origin, errCodeUnavailable, "spoken name unavailable")
		default:
			_ = s.writeMessage(client, netproto.MsgSpokenNameData, netproto.SpokenNameData{
				ClientID: msg.ClientID, Language: msg.Language, Key: key, DataBase64: base64.StdEncoding.EncodeToString(wav)})
		}
	}()
	return nil
}
