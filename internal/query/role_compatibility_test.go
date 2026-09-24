package query

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"go.uber.org/zap"
	"noxa/internal/authorization"
)

type incompatibleRoleBackend struct{ Backend }

func (incompatibleRoleBackend) RoleIntegrationsEnabled() bool { return false }

func (incompatibleRoleBackend) Authenticate(context.Context, string, string) (bool, bool, error) {
	return false, false, authorization.ErrIncompatibleAdministration
}

func TestLoginReportsIncompatibleAuthorizationModel(t *testing.T) {
	s := New("", zap.NewNop(), incompatibleRoleBackend{})
	var out bytes.Buffer
	sess := &session{w: &out, authed: true, username: "previous"}
	if !s.execute(t.Context(), sess, "login owner password") {
		t.Fatal("could not send compatibility error")
	}
	if sess.authed || sess.username != "" || !strings.Contains(out.String(), "roles-v1") {
		t.Fatalf("login retained authority or hid upgrade explanation: %+v %q", sess, out.String())
	}
}

// These legacy surfaces must stay closed until a named role-aware replacement
// exists. A nil legacy Backend makes accidental dispatch a test failure.
func TestRoleQueryLegacyOperationInventoryHasNoFallback(t *testing.T) {
	s := New("", zap.NewNop(), &roleQueryBackend{})
	for _, name := range []string{
		"clientmove", "clientkick", "sendtextmessage", "channelcreate", "channeldelete", "channeledit", "serverset", "banclient",
		"complaintlist", "complaintdel", "complaintdelall", "tokenadd", "tokenlist", "tokendelete", "auditlog", "serveredit", "serverstop", "serverrestart",
		"permoverview", "channelpermlist", "channeladdperm", "channeldelperm", "servergroupadd", "servergroupdel", "servergroupaddclient", "servergroupdelclient", "servergrouplist", "servergroupclientlist",
		"customset", "customdel", "custominfo", "logview", "serverrules",
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			sess := &session{w: &out, authed: true, username: "integration", authorizationModel: "roles-v1"}
			if !s.execute(t.Context(), sess, name+" cid=1 clid=target") {
				t.Fatal("denial could not be delivered")
			}
			if got := out.String(); got != errorLine(errInsufficientPermissions, "command unavailable with roles-v1 authorization") {
				t.Fatalf("legacy command escaped the role dispatch boundary: %q", got)
			}
		})
	}
}
