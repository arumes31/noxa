package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/store"
)

type webhookStore interface {
	ManageWebhook(context.Context, netproto.WebhookRequest, int64, []byte) (netproto.WebhookResult, error)
	DeliverWebhook(context.Context, int64, []byte, func(store.WebhookIdentity) (string, uint32, error)) (store.WebhookIdentity, int64, string, uint32, error)
}

func (s *TCPServer) handleWebhook(ctx context.Context, client *Client, frame *netproto.Frame) error {
	var r netproto.WebhookRequest
	if netproto.Decode(frame, &r) != nil || !r.Valid() {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid webhook request")
	}
	return s.roleAction(ctx, client, r.ChannelID, authorization.ManageChannels, func(ctx context.Context) error {
		if client.userID() <= 0 || !s.roleAllowed(ctx, client, r.ChannelID, authorization.ViewChannel) {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "channel unavailable")
		}
		if r.Action == "create" && (!s.roleAllowed(ctx, client, r.ChannelID, authorization.SendMessages) || !s.roleAllowed(ctx, client, r.ChannelID, authorization.ReadHistory)) {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "sending messages is required")
		}
		backend, ok := s.deps.Chat.(webhookStore)
		if !ok {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "webhooks unavailable")
		}
		var token string
		var hash []byte
		if r.Action == "create" {
			secret := make([]byte, 32)
			if _, err := rand.Read(secret); err != nil {
				return err
			}
			token = base64.RawURLEncoding.EncodeToString(secret)
			sum := sha256.Sum256([]byte(token))
			hash = sum[:]
		}
		result, err := backend.ManageWebhook(ctx, r, client.userID(), hash)
		if err != nil {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "webhook operation failed (maximum 20 per channel)")
		}
		result.Token = token
		if s.cfg != nil {
			result.HealthPort, _ = portFromAddress(s.cfg.HealthAddr)
		}
		return s.writeCommittedReply(client, netproto.MsgWebhookResult, result)
	})
}

// IncomingWebhookHandler accepts text only. Bearer secrets never appear in a
// URL, log, persisted message, or list response. Operators terminate HTTPS in
// front of the existing HTTP listener for remote integrations.
func (s *TCPServer) IncomingWebhookHandler() http.Handler {
	active := make(chan struct{}, 16)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		fail := func(status int) { http.Error(w, http.StatusText(status), status) }
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			fail(405)
			return
		}
		id, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/hooks/"), 10, 64)
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		secret, decodeErr := base64.RawURLEncoding.DecodeString(token)
		if err != nil || id <= 0 || r.Header.Get("Authorization") == token || len(token) != 43 || decodeErr != nil || len(secret) != 32 {
			fail(401)
			return
		}
		media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || media != "application/json" || r.Header.Get("Origin") != "" {
			fail(400)
			return
		}
		var payload struct {
			Content string `json:"content"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&payload) != nil || decoder.Decode(new(any)) != io.EOF || strings.TrimSpace(payload.Content) == "" || len(payload.Content) > 12000 || !utf8.ValidString(payload.Content) {
			fail(400)
			return
		}
		if s.deps == nil || s.deps.Auth == nil || s.deps.State == nil || s.chatKeys == nil {
			fail(503)
			return
		}
		backend, ok := s.deps.Chat.(webhookStore)
		if !ok {
			fail(503)
			return
		}
		select {
		case active <- struct{}{}:
			defer func() { <-active }()
		default:
			w.Header().Set("Retry-After", "3")
			fail(429)
			return
		}
		hash := sha256.Sum256([]byte(token))
		var messageID int64
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		err = s.withRolePolicy(ctx, func(ctx context.Context) error {
			s.roleMetadataMu.Lock()
			defer s.roleMetadataMu.Unlock()
			evaluator := ctx.Value(roleLeaseKey{}).(roleLease).evaluator
			hook, id, body, key, err := backend.DeliverWebhook(ctx, id, hash[:], func(hook store.WebhookIdentity) (string, uint32, error) {
				for _, capability := range []authorization.Capability{authorization.ViewChannel, authorization.ReadHistory, authorization.ManageChannels, authorization.SendMessages} {
					if !evaluator.Evaluate(hook.CreatorID, hook.ChannelID, capability).Allowed {
						return "", 0, store.ErrWebhookDenied
					}
				}
				remote, _, _ := net.SplitHostPort(r.RemoteAddr)
				ban, err := s.deps.Auth.LookupActiveBan(ctx, hook.CreatorUID, remote)
				if err != nil || ban != nil {
					return "", 0, store.ErrWebhookDenied
				}
				if s.cfg != nil && s.cfg.ChatMaxLength > 0 && len(payload.Content) > s.cfg.ChatMaxLength {
					return "", 0, authorization.ErrRoleInvalid
				}
				if err = s.moderateBody(ctx, payload.Content); err != nil {
					return "", 0, authorization.ErrRoleInvalid
				}
				if ch, ok := s.deps.State.GetChannel(hook.ChannelID); !ok {
					return "", 0, store.ErrWebhookDenied
				} else if ch.SlowModeSeconds > 0 && !evaluator.Evaluate(hook.CreatorID, hook.ChannelID, authorization.BypassSlowmode).Allowed && s.chatSlow.check("webhook:"+strconv.FormatInt(hook.ID, 10), hook.ChannelID, ch.SlowModeSeconds, time.Now()) > 0 {
					return "", 0, store.ErrWebhookRate
				}
				if _, _, err = s.chatKeys.EnsureScope(ctx, hook.ChannelID); err != nil {
					return "", 0, err
				}
				key, body, err := s.chatKeys.seal(ctx, hook.ChannelID, payload.Content)
				return body, key, err
			})
			if err != nil {
				return err
			}
			messageID = id
			event, err := eventEnvelope(eventChat, netproto.ChatBroadcast{ChannelID: strconv.FormatInt(hook.ChannelID, 10), FromUniqueID: "webhook:" + strconv.FormatInt(hook.ID, 10), From: hook.Name + " [Webhook]", Text: body, Enc: true, KeyID: key, ID: id, Version: 1})
			if err != nil {
				return err
			}
			s.broadcastChannelScoped(ctx, hook.ChannelID, event)
			return nil
		})
		if err != nil {
			switch {
			case errors.Is(err, store.ErrWebhookDenied):
				fail(401)
			case errors.Is(err, store.ErrWebhookRate):
				w.Header().Set("Retry-After", "3")
				fail(429)
			case errors.Is(err, authorization.ErrRoleInvalid):
				fail(400)
			default:
				fail(503)
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]int64{"message_id": messageID})
	})
}
