//go:build integration

package store

import (
	"encoding/json"
	"strings"
	"testing"

	"noxa/internal/authorization"
)

func TestRoleAuditPersistsTrustedScopesAndActualState(t *testing.T) {
	s, owner, member := roleTestStore(t)
	p, err := s.PrepareRolePolicy(t.Context(), owner)
	if err != nil {
		t.Fatal(err)
	}
	id := p.Channels[0].ChannelID
	_, err = s.EditRoleChannel(t.Context(), owner, id, p.Revision, RoleChannelSettings{Name: "Actual new name", OpusBitrate: 64000})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := s.AuditList(t.Context(), 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	latest := entries[0]
	if len(latest.ChannelIDs) != 1 || latest.ChannelIDs[0] != id {
		t.Fatalf("scope was not persisted: %+v", latest)
	}
	var detail struct {
		Before, After *channelAuditState
		Revision      int64
	}
	if err := json.Unmarshal([]byte(latest.Detail), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Before == nil || detail.After == nil || detail.Before.Settings.Name == detail.After.Settings.Name || detail.After.Settings.Name != "Actual new name" || detail.Revision != 2 {
		t.Fatalf("bad before/after: %+v", detail)
	}
	if strings.Contains(latest.Detail, "Password") {
		t.Fatal("secret field in audit")
	}
	// JSON-looking historical text never becomes trusted scope metadata.
	s.Audit(t.Context(), "legacy", "channel_create", "private-channel", `{"version":1,"channel_ids":[]}`)
	s.AuditScoped(t.Context(), "server", "server_config_set", "server", "", []int64{})
	entries, err = s.AuditList(t.Context(), 0, 2)
	if err != nil || len(entries) != 2 || entries[0].ChannelIDs == nil || entries[1].ChannelIDs != nil {
		t.Fatalf("NULL/empty scope distinction lost: %+v, %v", entries, err)
	}
	// Irrelevant request fields must never be serialized as authoritative data.
	_, err = s.ChangeRolePolicy(t.Context(), owner, authorization.RoleChange{Kind: authorization.OwnerTransfer, ExpectedRevision: 2, UserID: member,
		Role: authorization.Role{Name: "unused-request-canary"}})
	if err != nil {
		t.Fatal(err)
	}
	entries, err = s.AuditList(t.Context(), 0, 1)
	if err != nil || len(entries) != 1 || strings.Contains(entries[0].Detail, "unused-request-canary") || !strings.Contains(entries[0].Detail, "owner_id") {
		t.Fatalf("request rather than state audited: %+v %v", entries, err)
	}
}
