package store

import (
	"encoding/json"
	"noxa/internal/authorization"
	"strings"
	"testing"
)

func TestRoleImageAuditRecordsFingerprintWithoutPayload(t *testing.T) {
	image := "data:image/png;base64,example"
	role := authorization.Role{ID: 20, Name: "Member", IconImage: &image}
	policy := authorization.RolePolicy{Roles: []authorization.Role{role}}
	for _, kind := range []authorization.RoleChangeKind{authorization.RoleUpdate, authorization.RolesReorder} {
		detail := roleAuditDetail(authorization.RolePolicy{}, policy, authorization.RoleChange{Kind: kind, Role: role})
		raw, err := json.Marshal(detail)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "data:image") || !strings.Contains(string(raw), "icon_image_hash") {
			t.Fatalf("unexpected image audit %s", raw)
		}
		if authorization.RoleIconImage(policy.Roles[0].IconImage) != image {
			t.Fatal("audit changed live policy")
		}
	}
}
