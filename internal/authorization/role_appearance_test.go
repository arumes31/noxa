package authorization

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestMemberAppearanceIsPublicIsolatedAndOrdered(t *testing.T) {
	e, err := NewRoleEvaluator(roleFixture())
	if err != nil {
		t.Fatal(err)
	}
	before := e.Evaluate(2, 100, ViewChannel)
	roles := e.MemberAppearance(2)
	if len(roles) != 2 || roles[0].ID != 30 || roles[1].ID != 20 || len(e.MemberAppearance(0)) != 0 {
		t.Fatalf("unexpected appearances: %+v", roles)
	}
	raw, err := json.Marshal(roles)
	if err != nil || strings.Contains(string(raw), "permissions") || strings.Contains(string(raw), "manage_roles") {
		t.Fatalf("appearance exposes grants: %s, %v", raw, err)
	}
	roles[0].Name = "changed"
	roles[0].Position = 0
	if e.MemberAppearance(2)[0].Name != "Moderator" || !reflect.DeepEqual(before, e.Evaluate(2, 100, ViewChannel)) {
		t.Fatal("cosmetic output mutated authority")
	}
}
