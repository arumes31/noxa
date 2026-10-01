package query

import (
	"bytes"
	"strings"
	"testing"

	"go.uber.org/zap"
	"noxa/internal/netproto"
)

// Exercise decoded list limits independently of the default 4096-byte command
// line bound, which rejects this input earlier and closes the text session.
func TestRoleFilterOversizedPatchBeforeBackend(t *testing.T) {
	b := &metadataQueryBackend{roleQueryBackend: &roleQueryBackend{}}
	s := New("", zap.NewNop(), b)
	oversized := strings.Repeat("x", netproto.MaxChatFilterListBytes+1)
	for _, patch := range []netproto.ChatFilterSet{{WordFilter: &oversized}, {LinkBlacklist: &oversized}, {LinkWhitelist: &oversized}} {
		var out bytes.Buffer
		if !s.execute(t.Context(), &session{w: &out, authed: true, authorizationModel: "roles-v1"}, inspectionCommand(t, "chatfilterset", patch)) {
			t.Fatal("invalid request reply missing")
		}
		if !strings.HasPrefix(out.String(), "error id=512 ") {
			t.Fatal(out.String())
		}
	}
	if b.filterCalls != 0 {
		t.Fatal("invalid filter reached backend")
	}
}
