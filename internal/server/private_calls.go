package server

import (
	"context"
	"encoding/base64"
	"errors"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

const eventPrivateCall = "private_call"
const eventPrivateCallSignal = "private_call_signal"

type privateCallStore interface {
	SavePrivateCall(context.Context, netproto.CallSession) error
	PrivateCallHistory(context.Context, string) ([]netproto.CallSession, error)
	StartPrivateGroupCall(context.Context, string, string, func(netproto.Conversation) (netproto.CallSession, error)) (netproto.CallSession, error)
}

func (s *TCPServer) handlePrivateCall(ctx context.Context, client *Client, frame *netproto.Frame) error {
	var request netproto.CallRequest
	if err := netproto.Decode(frame, &request); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid call request")
	}
	switch request.Action {
	case "start", "get", "history", "accept", "decline", "cancel", "leave", "signal":
	default:
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid call action")
	}
	return s.rolePolicyRead(ctx, client, func(ctx context.Context) error {
		if !client.isAuthed() || client.userID() <= 0 || client.rulesBlocked() {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "sign in and accept the rules before calling")
		}
		backend, ok := s.deps.Chat.(privateCallStore)
		if !ok {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "private calls unavailable")
		}
		if request.Action == "history" {
			s.privateCallsMu.Lock()
			defer s.privateCallsMu.Unlock()
			history, err := backend.PrivateCallHistory(ctx, client.UniqueID)
			if err != nil {
				return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "call history unavailable")
			}
			for i := range history {
				stored := history[i]
				if live, found := s.privateCalls[stored.ID]; found {
					history[i] = live
				} else if stored.EndedAt == 0 {
					history[i].Revision++
					history[i].EndedAt = time.Now().Unix()
					for j := range history[i].Participants {
						if history[i].Participants[j].State == "ringing" {
							history[i].Participants[j].State = "missed"
						} else if history[i].Participants[j].State == "accepted" {
							history[i].Participants[j].State = "left"
						}
					}
				}
				if history[i].Revision > stored.Revision {
					if err := backend.SavePrivateCall(ctx, history[i]); err != nil {
						return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "call history recovery unavailable")
					}
				}
			}
			return s.writeCommittedReply(client, netproto.MsgCallResult, netproto.CallResult{Action: "history", History: history})
		}
		if request.Action == "start" {
			if s.chatRate != nil && !s.chatRate.allowLimit("call:"+client.UniqueID, time.Now(), 4) {
				return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "call rate limit exceeded")
			}
			return s.roleAction(ctx, client, 0, authorization.Connect, func(ctx context.Context) error { return s.startPrivateCall(ctx, client, request, backend) })
		}
		s.privateCallsMu.Lock()
		defer s.privateCallsMu.Unlock()
		call, found := s.privateCalls[request.ID]
		participant, member := call.Participant(client.UniqueID)
		if !found || !member || participant.ClientID != client.ID {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, netproto.ErrCallDenied.Error())
		}
		if request.Action == "signal" {
			if s.callSignalRate != nil && !s.callSignalRate.allow(client.UniqueID, time.Now()) {
				return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "call signal rate limit exceeded")
			}
			if call.EndedAt != 0 || participant.State != "accepted" {
				return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, netproto.ErrCallDenied.Error())
			}
			target, found := call.Participant(request.Target)
			if !found || target.State != "accepted" || target.UniqueID == client.UniqueID || len(request.Signal) > 96000 {
				return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid call signal")
			}
			raw, err := base64.StdEncoding.Strict().DecodeString(request.Signal)
			if err != nil || len(raw) < 40 || base64.StdEncoding.EncodeToString(raw) != request.Signal {
				return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid encrypted call signal")
			}
			payload, err := eventEnvelope(eventPrivateCallSignal, netproto.CallSignal{CallID: call.ID, From: client.UniqueID, To: target.UniqueID, Body: request.Signal})
			if err != nil {
				return err
			}
			if s.deps.Broadcast == nil {
				return errors.New("call broadcaster unavailable")
			}
			if err := s.deps.Broadcast.BroadcastToClient(target.ClientID, payload); err != nil {
				return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "call peer unavailable")
			}
		} else if request.Action != "get" {
			if request.Action == "accept" && !s.roleAllowed(ctx, client, 0, authorization.Connect) {
				return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "calling is not permitted")
			}
			next, err := call.Change(client.UniqueID, request.Action, time.Now().Unix())
			if err != nil {
				return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, err.Error())
			}
			if err := backend.SavePrivateCall(ctx, next); err != nil {
				return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "call change could not be saved")
			}
			s.publishPrivateCallLocked(next)
			call = next
		}
		return s.writeCommittedReply(client, netproto.MsgCallResult, netproto.CallResult{Action: request.Action, Call: &call})
	})
}

func (s *TCPServer) startPrivateCall(ctx context.Context, client *Client, request netproto.CallRequest, backend privateCallStore) error {
	if (request.Target == "") == (request.ConversationID == "") {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "choose a user or private group")
	}
	s.roleMetadataMu.Lock()
	defer s.roleMetadataMu.Unlock()
	s.privateCallsMu.Lock()
	defer s.privateCallsMu.Unlock()
	if s.privateCalls == nil {
		s.privateCalls = map[string]netproto.CallSession{}
	}
	if s.privateCallByClient == nil {
		s.privateCallByClient = map[string]string{}
	}
	if s.privateCallByClient[client.ID] != "" || len(s.privateCalls) >= 1024 {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "already in a call or call capacity reached")
	}
	build := func(uids []string) (netproto.CallSession, error) {
		now := time.Now().Unix()
		call := netproto.CallSession{ID: uuid.NewString(), ConversationID: request.ConversationID, Caller: client.UniqueID, CreatedAt: now, RingUntil: now + 30, Revision: 1, Participants: []netproto.CallParticipant{{UniqueID: client.UniqueID, ClientID: client.ID, State: "accepted"}}}
		e := ctx.Value(roleLeaseKey{}).(roleLease).evaluator
		for _, uid := range uids {
			if uid == client.UniqueID {
				continue
			}
			target, found := s.clientByUniqueID(uid)
			if !found || !target.isAuthed() || target.userID() <= 0 || target.rulesBlocked() || s.privateCallByClient[target.ID] != "" || !s.rolePokeTargetVisible(e, client, target.ID) || !e.Evaluate(target.userID(), 0, authorization.Connect).Allowed {
				continue
			}
			call.Participants = append(call.Participants, netproto.CallParticipant{UniqueID: uid, ClientID: target.ID, State: "ringing"})
		}
		if len(call.Participants) < 2 {
			return call, netproto.ErrCallDenied
		}
		return call, nil
	}
	var call netproto.CallSession
	var err error
	if request.ConversationID == "" {
		call, err = build([]string{request.Target})
		if err == nil {
			err = backend.SavePrivateCall(ctx, call)
		}
	} else {
		call, err = backend.StartPrivateGroupCall(ctx, client.UniqueID, request.ConversationID, func(group netproto.Conversation) (netproto.CallSession, error) {
			uids := []string{}
			for _, member := range group.Members {
				if !member.Pending {
					uids = append(uids, member.UniqueID)
				}
			}
			return build(uids)
		})
	}
	if err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "call unavailable: no available participants or storage failure")
	}
	s.publishPrivateCallLocked(call)
	time.AfterFunc(30*time.Second, func() { s.expirePrivateCall(call.ID) })
	return s.writeCommittedReply(client, netproto.MsgCallResult, netproto.CallResult{Action: "start", Call: &call})
}

// Caller holds privateCallsMu. Events carry only an invalidation; clients fetch
// the current state, so a late ringing event cannot revive a cancelled call.
func (s *TCPServer) publishPrivateCallLocked(call netproto.CallSession) {
	previous := s.privateCalls[call.ID]
	s.privateCalls[call.ID] = call
	for _, participant := range call.Participants {
		if call.EndedAt == 0 && (participant.State == "accepted" || participant.State == "ringing") {
			s.privateCallByClient[participant.ClientID] = call.ID
		} else if s.privateCallByClient[participant.ClientID] == call.ID {
			delete(s.privateCallByClient, participant.ClientID)
		}
	}
	if s.deps.Broadcast != nil {
		payload, err := eventEnvelope(eventPrivateCall, map[string]string{"id": call.ID})
		if err == nil {
			for _, participant := range call.Participants {
				_ = s.deps.Broadcast.BroadcastToClient(participant.ClientID, payload)
			}
		}
	}
	if call.EndedAt != 0 && previous.EndedAt == 0 {
		time.AfterFunc(time.Minute, func() { s.retirePrivateCall(call.ID) })
	}
}

func (s *TCPServer) expirePrivateCall(id string) {
	s.privateCallsMu.Lock()
	defer s.privateCallsMu.Unlock()
	call, found := s.privateCalls[id]
	if !found {
		return
	}
	next, changed := call.Expire(time.Now().Unix())
	if !changed {
		return
	}
	s.saveCallCleanup(next)
	s.publishPrivateCallLocked(next)
}

func (s *TCPServer) saveCallCleanup(call netproto.CallSession) {
	if backend, ok := s.deps.Chat.(privateCallStore); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := backend.SavePrivateCall(ctx, call); err != nil {
			s.logger.Warn("private call cleanup persistence failed", zap.Error(err))
			if s.privateCallDirty == nil {
				s.privateCallDirty = map[string]bool{}
			}
			if !s.privateCallDirty[call.ID] {
				s.privateCallDirty[call.ID] = true
				time.AfterFunc(10*time.Second, func() {
					s.privateCallsMu.Lock()
					defer s.privateCallsMu.Unlock()
					delete(s.privateCallDirty, call.ID)
					if current, found := s.privateCalls[call.ID]; found {
						s.saveCallCleanup(current)
					}
				})
			}
		} else {
			delete(s.privateCallDirty, call.ID)
		}
	}
}

func (s *TCPServer) retirePrivateCall(id string) {
	s.privateCallsMu.Lock()
	defer s.privateCallsMu.Unlock()
	if s.privateCallDirty[id] {
		time.AfterFunc(10*time.Second, func() { s.retirePrivateCall(id) })
		return
	}
	delete(s.privateCalls, id)
}

func (s *TCPServer) disconnectPrivateCall(clientID string) {
	s.privateCallsMu.Lock()
	defer s.privateCallsMu.Unlock()
	id := s.privateCallByClient[clientID]
	call, found := s.privateCalls[id]
	if !found {
		return
	}
	for _, participant := range call.Participants {
		if participant.ClientID != clientID {
			continue
		}
		action := "leave"
		if participant.State == "ringing" {
			action = "decline"
		}
		if next, err := call.Change(participant.UniqueID, action, time.Now().Unix()); err == nil {
			s.saveCallCleanup(next)
			s.publishPrivateCallLocked(next)
		}
		return
	}
}

// Membership changes revoke the corresponding private media membership too.
// Call peers close their authenticated peer connection when this state changes.
func (s *TCPServer) removeConversationCaller(groupID, uid string) {
	s.privateCallsMu.Lock()
	defer s.privateCallsMu.Unlock()
	for _, call := range s.privateCalls {
		if call.ConversationID != groupID || call.EndedAt != 0 {
			continue
		}
		participant, found := call.Participant(uid)
		if !found {
			continue
		}
		action := "leave"
		if participant.State == "ringing" {
			action = "decline"
		}
		if next, err := call.Change(uid, action, time.Now().Unix()); err == nil {
			s.saveCallCleanup(next)
			s.publishPrivateCallLocked(next)
		}
	}
}
