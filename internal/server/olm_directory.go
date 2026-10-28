package server

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/e2ee/olmdir"
	"noxa/internal/netproto"
	"noxa/internal/store"
	"time"
)

type olmDirectoryStore interface {
	PublishOlmBundle(context.Context, string, string, string, olmdir.Bundle) error
	ClaimOlmKey(context.Context, string, string, string, string, string, int64, string, ...int64) (store.OlmClaim, error)
	ExpireOlmDirectory(context.Context) (int64, error)
	OwnOlmBundle(context.Context, string, string) (olmdir.Bundle, error)
}

func (s *TCPServer) olmDirectory() (olmDirectoryStore, string, bool) {
	if s.deps == nil || s.deps.Devices == nil {
		return nil, "", false
	}
	binding, e := olmdir.ServerBinding(s.TLSFingerprint())
	if e != nil {
		return nil, "", false
	}
	db, ok := s.deps.Devices.(olmDirectoryStore)
	return db, binding, ok
}
func (s *TCPServer) handleOlmDirectory(ctx context.Context, client *Client, f *netproto.Frame) error {
	r, e := netproto.DecodeOlmDirectoryRequest(f)
	if e != nil {
		return s.sendErrorFor(client, netproto.MsgOlmDirectoryRequest, errCodeMalformed, "invalid Olm directory request")
	}
	response := netproto.OlmDirectoryResult{RequestID: r.RequestID, Action: r.Action, ClaimUntil: r.ClaimUntil}
	reply := func(code string) error {
		response.Error = code
		return s.writeMessage(client, netproto.MsgOlmDirectoryResult, response)
	}
	e = s.withRoleSession(ctx, client, func(ctx context.Context) error {
		if !client.isAuthed() || client.sessionRevoked() || client.rulesBlocked() {
			return reply("authority-required")
		}
		db, binding, ok := s.olmDirectory()
		if !ok {
			return reply("unavailable")
		}
		principal, id := client.uniqueID(), client.currentDeviceID()
		if id == "" || principal == "" {
			return reply("authority-required")
		}
		if s.chatRate != nil && !s.chatRate.allow(principal+":olm-directory", time.Now()) {
			return reply("rate-limit")
		}
		opCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		d, e := s.deps.Devices.DeviceByID(opCtx, principal, id)
		if e != nil || !d.Active() {
			return reply("authority-required")
		}
		client.mu.Lock()
		sessionKey := client.authSigningKey
		client.mu.Unlock()
		signed, e := auth.LoadPublicKey(sessionKey)
		if e != nil {
			return reply("authority-required")
		}
		registered, e := auth.LoadPublicKey(d.Ed25519Public)
		if e != nil || !bytes.Equal(signed, registered) {
			return reply("authority-required")
		}
		if r.Action == "publish" {
			e = db.PublishOlmBundle(opCtx, principal, id, binding, *r.Bundle)
		} else if r.Action == "own_state" {
			// Read-only, only the authenticated principal/device's own row.
			var own olmdir.Bundle
			if own, e = db.OwnOlmBundle(opCtx, principal, id); e == nil {
				response.Bundle = &own
			} else if errors.Is(e, sql.ErrNoRows) {
				return reply("absent")
			}
		} else {
			// Unique-ID DMs have no additional contact/block ACL in this server.
			// rolePolicyRead, rules and active-device gates match direct-message entry;
			// recipient current trust must independently be checked by the caller.
			var claim store.OlmClaim
			claim, e = db.ClaimOlmKey(opCtx, principal, id, r.Principal, r.DeviceID, r.RequestID, r.ClaimUntil, binding, r.AccountEpoch)
			if e == nil {
				response.Bundle = &claim.Bundle
				response.KeyID = claim.KeyID
			}
		}
		switch {
		case e == nil:
			return reply("")
		case errors.Is(e, store.ErrOlmAuthority):
			return reply("authority-required")
		case errors.Is(e, store.ErrOlmConflict):
			return reply("conflict")
		case errors.Is(e, store.ErrOlmAccountChanged):
			return reply("account-change-required")
		case errors.Is(e, store.ErrOlmExhausted):
			return reply("exhausted")
		case errors.Is(e, olmdir.ErrInvalid):
			return reply("invalid")
		case errors.Is(e, store.ErrOlmLimit):
			return reply("limit")
		default:
			return reply("unavailable")
		}
	})
	if errors.Is(e, authorization.ErrRoleForbidden) {
		return reply("authority-required")
	}
	if errors.Is(e, authorization.ErrAuthorizationUnavailable) {
		return reply("unavailable")
	}
	return e
}
