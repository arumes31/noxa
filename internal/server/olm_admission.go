package server

import (
	"context"
	"database/sql"
	"errors"
	"noxa/internal/authorization"
	"noxa/internal/e2ee/admission"
	"noxa/internal/netproto"
	"noxa/internal/store"
	"time"
)

func (s *TCPServer) handleOlmAdmission(ctx context.Context, client *Client, f *netproto.Frame) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if !s.olmAdmissionEnabled {
		return s.writeMessageInContext(ctx, client, netproto.MsgError, netproto.Error{Code: errCodeMalformed, Message: "Olm admission unavailable", OriginType: uint16(netproto.MsgOlmAdmissionRequest)})
	}
	r, e := netproto.DecodeOlmAdmissionRequest(f)
	if e != nil {
		return s.writeMessageInContext(ctx, client, netproto.MsgError, netproto.Error{Code: errCodeMalformed, Message: "invalid Olm admission request", OriginType: uint16(netproto.MsgOlmAdmissionRequest)})
	}
	response := netproto.OlmAdmissionResult{RequestID: r.RequestID, Action: r.Action}
	reply := func(code string) error {
		response.Error = code
		return s.writeMessageInContext(ctx, client, netproto.MsgOlmAdmissionResult, response)
	}
	e = s.rolePolicyRead(ctx, client, func(ctx context.Context) error {
		if !client.isAuthed() || client.sessionRevoked() || client.rulesBlocked() {
			return reply("authority-required")
		}
		directory, binding, ok := s.olmDirectory()
		if !ok {
			return reply("unavailable")
		}
		db, ok := directory.(*store.Store)
		if !ok {
			return reply("unavailable")
		}
		client.mu.Lock()
		signing := client.authSigningKey
		client.mu.Unlock()
		a := store.OlmSource{Server: binding, Principal: client.uniqueID(), Device: client.currentDeviceID(), Signing: signing, AccountEpoch: r.AccountEpoch, AllowGroups: client.userID() > 0}
		if a.Device == "" || a.Principal == "" {
			return reply("authority-required")
		}
		if s.chatRate != nil {
			if r.Action == "begin" || r.Action == "finalize" {
				if !s.chatRate.allowLimit(a.Principal, time.Now(), s.chatActionLimit(ctx, client)) {
					return reply("rate-limit")
				}
			} else if !s.chatRate.allow(a.Principal+":olm-admission", time.Now()) {
				return reply("rate-limit")
			}
		}
		opCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		var data any
		switch r.Action {
		case "snapshot":
			data, e = db.OlmSnapshot(opCtx, a, r.Scope, r.Peer, r.Conversation)
		case "begin":
			data, e = db.BeginOlmAttempt(opCtx, a, *r.Manifest)
		case "put_part":
			e = db.PutOlmPart(opCtx, a, r.AttemptID, r.Part, r.Data)
		case "finalize":
			data, e = db.FinalizeOlmAttempt(opCtx, a, r.AttemptID)
		case "status":
			data, e = db.OlmAttemptStatus(opCtx, a, r.AttemptID)
		case "abort":
			data, e = db.AbortOlmAttempt(opCtx, a, *r.Manifest)
		case "retire_receipt":
			data, e = db.RetireOlmReceipt(opCtx, a, r.AttemptID, r.Digest)
		case "receive_ack":
			e = db.AckOlmDelivery(opCtx, a, r.DeliveryID, r.Digest)
		case "close_keys":
			data, e = db.CloseOlmKeys(opCtx, a, r.Keys)
		case "reset":
			data, e = db.ResetOlmAccount(opCtx, a, *r.Reset, *r.Bundle)
		case "reset_chain":
			data, e = db.OlmResetChain(opCtx, a, r.TargetDevice, r.AfterEpoch)
		case "deliveries":
			sent := false
			e = db.WithOlmDeliveries(opCtx, a, r.After, func(ds store.OlmDeliveryPage) error { sent = true; response.Data = ds; return reply("") })
			if sent {
				return e
			}
		}
		response.Data = data
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
		case errors.Is(e, store.ErrOlmLimit):
			return reply("limit")
		case errors.Is(e, store.ErrOlmIncomplete):
			return reply("incomplete")
		case errors.Is(e, store.ErrOlmRetired):
			return reply("retired")
		case errors.Is(e, admission.ErrInvalid):
			return reply("invalid")
		case errors.Is(e, sql.ErrNoRows):
			return reply("absent")
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
