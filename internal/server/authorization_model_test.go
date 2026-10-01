package server

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

func TestRoleLoginRequiresCompatibleAuthorizationModel(t *testing.T) {
	for _, path := range []string{"password", "guest", "challenge"} {
		for _, models := range [][]string{nil, {"future-model"}, {"roles-v1"}, {"future-model", "roles-v1"}, {"roles-v1", "2", "3", "4", "5", "6", "7", "8", "9"}} {
			t.Run(path+"/"+strings.Join(models, ","), func(t *testing.T) {
				backend := serverRoleFixture()
				env := startTestEnvDeps(t, nil, nil, func(d *Deps) {
					var err error
					d.Authority, err = authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
					if err != nil {
						t.Fatal(err)
					}
				})
				defer env.stop()
				conn := dialRetry(t, env.addr)
				defer func() { _ = conn.Close() }()
				request := map[string]any{"authorization_models": models}
				switch path {
				case "password":
					request["username"], request["password"] = "admin-uid", "pw"
				case "guest":
					request["anonymous"], request["nickname"] = true, "Guest"
				case "challenge":
					request["username"] = "admin-uid"
				}
				send(t, conn, netproto.MsgAuthenticate, request)
				frame := readFrame(t, conn)
				compatible := len(models) <= 8 && slices.Contains(models, "roles-v1")
				if compatible && path == "challenge" {
					if frame.Type != uint16(netproto.MsgAuthChallenge) {
						t.Fatalf("compatible challenge got %d", frame.Type)
					}
					return
				}
				if frame.Type != uint16(netproto.MsgAuthResponse) {
					t.Fatalf("expected admission response, got %d", frame.Type)
				}
				var response map[string]any
				if err := json.Unmarshal(frame.Payload, &response); err != nil {
					t.Fatal(err)
				}
				if response["ok"] != compatible || response["authorization_model"] != "roles-v1" {
					t.Fatalf("negotiation response = %+v", response)
				}
				if !compatible {
					if !strings.Contains(response["reason"].(string), "upgrade") {
						t.Fatalf("missing upgrade reason: %+v", response)
					}
					for _, field := range []string{"client_id", "chat_keys", "motd", "ice_servers"} {
						if _, leaked := response[field]; leaked {
							t.Fatalf("rejection leaked %s", field)
						}
					}
					send(t, conn, netproto.MsgRoleQuery, netproto.RoleQuery{})
					if next := readFrame(t, conn); next.Type != uint16(netproto.MsgError) {
						t.Fatalf("incompatible peer admitted: frame %d", next.Type)
					}
				}
			})
		}
	}
}

func TestAuthorizationModelBindsChallengeAndRejectsDowngrade(t *testing.T) {
	for _, guest := range []bool{false, true} {
		for _, downgrade := range []bool{false, true} {
			t.Run(map[bool]string{false: "account", true: "guest"}[guest]+"/"+map[bool]string{false: "accepted", true: "downgrade"}[downgrade], func(t *testing.T) {
				env := startTestEnvDeps(t, nil, nil, func(d *Deps) {
					var err error
					d.Authority, err = authorization.NewAuthority(t.Context(), serverRoleFixture(), func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
					if err != nil {
						t.Fatal(err)
					}
				})
				defer env.stop()
				pub, private, err := auth.GenerateIdentityKeyPair()
				if err != nil {
					t.Fatal(err)
				}
				identity := "admin-uid"
				env.auth.pubkeys[identity] = pub
				if guest {
					identity, err = auth.UniqueIDFromPublicKey(pub)
					if err != nil {
						t.Fatal(err)
					}
				}
				conn := dialRetry(t, env.addr)
				defer func() { _ = conn.Close() }()
				send(t, conn, netproto.MsgAuthenticate, netproto.Authenticate{Username: identity, Anonymous: guest, Nickname: "Guest", AuthorizationModels: []string{netproto.AuthorizationModelRolesV1}})
				var challenge netproto.AuthChallenge
				if err := netproto.Decode(readOfType(t, conn, netproto.MsgAuthChallenge), &challenge); err != nil {
					t.Fatal(err)
				}
				signature, err := auth.SignChallenge(private, challenge.Challenge)
				if err != nil {
					t.Fatal(err)
				}
				if downgrade {
					send(t, conn, netproto.MsgAuthenticate, map[string]any{"username": identity})
					readOfType(t, conn, netproto.MsgAuthResponse)
				}
				reply := netproto.AuthSignature{UniqueID: identity, Signature: signature}
				if guest {
					reply.PublicKey = pub
				}
				send(t, conn, netproto.MsgAuthSignature, reply)
				var response netproto.AuthResponse
				if err := netproto.Decode(readOfType(t, conn, netproto.MsgAuthResponse), &response); err != nil {
					t.Fatal(err)
				}
				if response.OK == downgrade || response.AuthorizationModel != netproto.AuthorizationModelRolesV1 {
					t.Fatalf("challenge negotiation = %+v", response)
				}
				if downgrade && !strings.Contains(response.Reason, "upgrade") {
					t.Fatalf("downgrade reason = %q", response.Reason)
				}
			})
		}
	}
}
