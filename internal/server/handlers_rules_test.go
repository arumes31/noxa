package server

import (
	"context"
	"errors"
	"testing"

	"noxa/internal/netproto"
)

type recordedRulesAcceptance struct {
	userID int64
	hash   string
}

type handlerRulesBackend struct {
	acceptErr error
	accepted  chan recordedRulesAcceptance
}

func (*handlerRulesBackend) Pending(context.Context, int64) (string, string, bool, error) {
	return "Current rules", "current-hash", true, nil
}

func (*handlerRulesBackend) Text(context.Context) (string, string, error) {
	return "Current rules", "current-hash", nil
}

func (r *handlerRulesBackend) Accept(_ context.Context, userID int64, hash string) error {
	if r.acceptErr != nil {
		return r.acceptErr
	}
	r.accepted <- recordedRulesAcceptance{userID: userID, hash: hash}
	return nil
}

func TestServerRulesAcceptancePreservesSessionGate(t *testing.T) {
	for _, tt := range []struct {
		name      string
		guest     bool
		acceptErr error
	}{
		{name: "registered member"},
		{name: "guest", guest: true},
		{name: "persistence failure", acceptErr: errors.New("rules storage unavailable")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rules := &handlerRulesBackend{acceptErr: tt.acceptErr, accepted: make(chan recordedRulesAcceptance, 1)}
			env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Rules = rules })
			defer env.stop()
			conn := dialRetry(t, env.addr)
			defer func() { _ = conn.Close() }()
			request := netproto.Authenticate{Username: "user-uid", Password: "pw", AuthorizationModels: []string{netproto.AuthorizationModelRolesV1}}
			if tt.guest {
				request.Username, request.Password, request.Anonymous = "", "", true
			}
			send(t, conn, netproto.MsgAuthenticate, request)
			var response netproto.AuthResponse
			if err := netproto.Decode(readOfType(t, conn, netproto.MsgAuthResponse), &response); err != nil || !response.OK {
				t.Fatalf("authenticate: response=%+v error=%v", response, err)
			}
			readOfType(t, conn, netproto.MsgSnapshot)
			var pending netproto.ServerRules
			if err := netproto.Decode(readOfType(t, conn, netproto.MsgServerRules), &pending); err != nil {
				t.Fatal(err)
			}
			if pending != (netproto.ServerRules{Text: "Current rules", Hash: "current-hash"}) {
				t.Fatalf("pending rules = %+v", pending)
			}
			client, ok := env.srv.clientByID(response.ClientID)
			if !ok || !client.rulesBlocked() {
				t.Fatal("pending rules did not block the session")
			}

			// A stale acceptance must resend current wording and retain the gate.
			send(t, conn, netproto.MsgServerRulesAccept, netproto.ServerRulesAccept{Hash: "old-hash"})
			var rejection netproto.Error
			if err := netproto.Decode(readOfType(t, conn, netproto.MsgError), &rejection); err != nil {
				t.Fatal(err)
			}
			if rejection.Code != errCodeMalformed {
				t.Fatalf("stale acceptance error = %+v", rejection)
			}
			var refreshed netproto.ServerRules
			if err := netproto.Decode(readOfType(t, conn, netproto.MsgServerRules), &refreshed); err != nil {
				t.Fatal(err)
			}
			if refreshed != pending || !client.rulesBlocked() || len(rules.accepted) != 0 {
				t.Fatal("stale acceptance changed the gate or persisted an acceptance")
			}

			send(t, conn, netproto.MsgServerRulesAccept, netproto.ServerRulesAccept{Hash: pending.Hash})
			if tt.acceptErr != nil {
				if err := netproto.Decode(readOfType(t, conn, netproto.MsgError), &rejection); err != nil {
					t.Fatal(err)
				}
				if rejection.Code != errCodeUnavailable || !client.rulesBlocked() {
					t.Fatal("failed persistence released the rules gate")
				}
				return
			}
			var accepted netproto.ServerRules
			if err := netproto.Decode(readOfType(t, conn, netproto.MsgServerRules), &accepted); err != nil {
				t.Fatal(err)
			}
			if accepted != (netproto.ServerRules{}) || client.rulesBlocked() {
				t.Fatal("acceptance did not acknowledge and release the session gate")
			}
			if tt.guest {
				if len(rules.accepted) != 0 {
					t.Fatal("guest acceptance was persisted against a users row")
				}
			} else {
				select {
				case got := <-rules.accepted:
					if got != (recordedRulesAcceptance{userID: 2, hash: pending.Hash}) {
						t.Fatalf("persisted acceptance = %+v", got)
					}
				default:
					t.Fatal("member acceptance was not persisted")
				}
			}
		})
	}
}
