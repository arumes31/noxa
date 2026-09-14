package store

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestListServerAdminsDB(t *testing.T) {
	s := testDBStore(t)
	ctx := context.Background()
	prefix := fmt.Sprintf("admin-list-%d-", time.Now().UnixNano())
	fixtures := []struct {
		suffix   string
		nickname any
		isAdmin  bool
	}{
		{"zulu", prefix + "Zulu", true},
		{"unnamed-b", nil, true},
		{"member", prefix + "Member", false},
		{"alpha", prefix + "Alpha", true},
		{"unnamed-a", nil, true},
	}
	fixtureIDs := make(map[string]bool, len(fixtures))
	for _, fixture := range fixtures {
		id := seedTestUser(t, s, prefix+fixture.suffix)
		uniqueID := "w6atest_" + prefix + fixture.suffix
		fixtureIDs[uniqueID] = true
		if _, err := s.DB().ExecContext(ctx,
			`UPDATE users SET nickname = $2, is_admin = $3 WHERE id = $1`,
			id, fixture.nickname, fixture.isAdmin); err != nil {
			t.Fatalf("setting fixture admin state: %v", err)
		}
		groups, err := s.UserGroupIDs(ctx, id)
		if err != nil || len(groups) != 0 {
			t.Fatalf("fixture groups = %v, err = %v; want no memberships", groups, err)
		}
	}

	admins, err := s.ListServerAdmins(ctx)
	if err != nil {
		t.Fatalf("ListServerAdmins: %v", err)
	}
	var got []AdminIdentity
	for _, admin := range admins {
		if fixtureIDs[admin.UniqueID] {
			got = append(got, admin)
		}
	}
	want := []AdminIdentity{
		{UniqueID: "w6atest_" + prefix + "alpha", Nickname: prefix + "Alpha"},
		{UniqueID: "w6atest_" + prefix + "zulu", Nickname: prefix + "Zulu"},
		{UniqueID: "w6atest_" + prefix + "unnamed-a"},
		{UniqueID: "w6atest_" + prefix + "unnamed-b"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListServerAdmins fixtures = %+v, want %+v", got, want)
	}
}
