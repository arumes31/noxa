package authorization

import (
	"errors"
	"reflect"
	"testing"
)

func TestChannelMoveOrderDoesNotChangeAccessSemantics(t *testing.T) {
	p := roleFixture()
	change := ChannelTreeChange{Kind: ChannelMove, ExpectedRevision: p.Revision, ChannelID: 101, ParentID: 102}
	want, err := ApplyChannelTreeChange(p, 1, change)
	if err != nil {
		t.Fatal(err)
	}
	for _, order := range []int32{-2147483648, 0, 2147483647} {
		change.OrderIndex = &order
		got, err := ApplyChannelTreeChange(p, 1, change)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("order %d changed policy: %+v %v", order, got, err)
		}
	}
	for _, kind := range []ChannelChangeKind{ChannelCreate, ChannelEdit, ChannelDelete} {
		change.Kind = kind
		if _, err := ApplyChannelTreeChange(p, 1, change); !errors.Is(err, ErrRoleInvalid) {
			t.Fatalf("accepted move-only order for %s: %v", kind, err)
		}
	}
}
