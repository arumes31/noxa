package server

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

type statusFence struct {
	saved  []netproto.StatusSaved
	errors []netproto.Error
	err    error
}

func readStatusFence(conn net.Conn, accepted chan<- struct{}) statusFence {
	var got statusFence
	for {
		f, err := netproto.ReadFrame(conn)
		if err != nil {
			got.err = err
			return got
		}
		switch netproto.MessageType(f.Type) {
		case netproto.MsgStatusSaved:
			var saved netproto.StatusSaved
			got.err = netproto.Decode(f, &saved)
			got.saved = append(got.saved, saved)
			accepted <- struct{}{}
		case netproto.MsgError:
			var e netproto.Error
			got.err = netproto.Decode(f, &e)
			got.errors = append(got.errors, e)
		case netproto.MsgPong:
			return got
		}
		if got.err != nil {
			return got
		}
	}
}

func TestStatusConfirmationFollowsStateAndAuthority(t *testing.T) {
	for _, outcome := range []string{"saved", "online", "owner invisible", "role admin invisible", "unrequested", "invalid", "long message", "missing session"} {
		t.Run(outcome, func(t *testing.T) {
			backend := serverRoleFixture()
			if outcome == "role admin invisible" {
				backend.policy.Roles = append(backend.policy.Roles, authorization.Role{ID: 20, Name: "Admin", Position: 1, Permissions: []authorization.Capability{authorization.Administrator}})
				backend.policy.Members = []authorization.RoleMember{{UserID: 1, RoleIDs: []int64{20}}}
			}
			authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority })
			defer env.stop()
			uid := "admin-uid"
			if outcome == "owner invisible" {
				uid = "user-uid"
			}
			conn, id := dialAuthed(t, env.addr, uid)
			defer func() { _ = conn.Close() }()
			env.state.SetStatus(id, "busy", "initial")
			msg := netproto.SetStatus{Status: "away", Message: "back soon", AckRequested: outcome != "unrequested"}
			switch outcome {
			case "online":
				msg.Status, msg.Message = " ONLINE ", ""
			case "owner invisible", "role admin invisible":
				msg.Status = "invisible"
			case "invalid":
				msg.Status = "bogus"
			case "long message":
				msg.Message = strings.Repeat("x", maxStatusMessage+1)
			case "missing session":
				env.state.RemoveClient(id)
			}
			denied := outcome == "invalid" || outcome == "long message" || outcome == "missing session"
			held := !denied
			var once sync.Once
			unblock := func() {
				if held {
					once.Do(env.srv.roleMetadataMu.Unlock)
				}
			}
			if held {
				env.srv.roleMetadataMu.Lock()
			}
			defer unblock()
			send(t, conn, netproto.MsgSetStatus, msg)
			send(t, conn, netproto.MsgPing, netproto.Ping{})
			if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			accepted := make(chan struct{}, 2)
			done := make(chan statusFence, 1)
			go func() { done <- readStatusFence(conn, accepted) }()
			if held {
				select {
				case <-accepted:
					t.Fatal("confirmed before state update")
				case result := <-done:
					t.Fatalf("returned before state update: %+v", result)
				case <-time.After(25 * time.Millisecond):
				}
				current, ok := env.state.GetClient(id)
				if !ok || current.Status != "busy" || current.StatusMessage != "initial" {
					t.Fatalf("changed while held: %+v", current)
				}
				unblock()
			}
			got := <-done
			if got.err != nil {
				t.Fatal(got.err)
			}
			if denied {
				if len(got.saved) != 0 || len(got.errors) != 1 || got.errors[0].OriginType != uint16(netproto.MsgSetStatus) {
					t.Fatalf("denial: %+v", got)
				}
				if current, ok := env.state.GetClient(id); ok && (current.Status != "busy" || current.StatusMessage != "initial") {
					t.Fatalf("denied mutation: %+v", current)
				}
				return
			}
			wantStatus := msg.Status
			if outcome == "online" {
				wantStatus = ""
			}
			if len(got.errors) != 0 {
				t.Fatalf("errors: %+v", got)
			}
			if msg.AckRequested {
				want := netproto.StatusSaved{ClientID: id, Status: wantStatus, Message: msg.Message}
				if len(got.saved) != 1 || got.saved[0] != want {
					t.Fatalf("saved: %+v, want %+v", got, want)
				}
			} else if len(got.saved) != 0 {
				t.Fatalf("unrequested reply: %+v", got)
			}
			current, ok := env.state.GetClient(id)
			if !ok || current.Status != wantStatus || current.StatusMessage != msg.Message {
				t.Fatalf("confirmed wrong state: %+v", current)
			}
		})
	}
}

func TestRoleSnapshotInvisibleEligibility(t *testing.T) {
	backend := serverRoleFixture()
	backend.policy.Roles = append(backend.policy.Roles, authorization.Role{ID: 20, Name: "Admin", Position: 1, Permissions: []authorization.Capability{authorization.Administrator}})
	for _, granted := range []bool{true, false} {
		backend.policy.Members = nil
		if granted {
			backend.policy.Members = []authorization.RoleMember{{UserID: 1, RoleIDs: []int64{20}}}
		}
		e, err := authorization.NewRoleEvaluator(backend.policy)
		if err != nil {
			t.Fatal(err)
		}
		env := startTestEnv(t, nil)
		if got := buildRoleSnapshot(env.state, e, 1, "admin-uid").CanSetInvisible; got != granted {
			t.Errorf("member invisible eligibility = %v, want %v", got, granted)
		}
		if !buildRoleSnapshot(env.state, e, 2, "user-uid").CanSetInvisible {
			t.Error("owner lost invisible eligibility")
		}
		env.stop()
	}
}
