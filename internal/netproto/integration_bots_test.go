package netproto

import "testing"

func TestMessageBulkDeleteValid(t *testing.T) {
	ids := make([]int64, MaxBulkDeleteMessages+1)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	for _, tc := range []struct {
		name string
		req  MessageBulkDelete
		ok   bool
	}{
		{"one", MessageBulkDelete{ChannelID: 3, MessageIDs: []int64{9}}, true},
		{"server chat", MessageBulkDelete{ChannelID: 0, MessageIDs: []int64{9}}, true},
		{"limit", MessageBulkDelete{ChannelID: 3, MessageIDs: ids[:MaxBulkDeleteMessages]}, true},
		{"over limit", MessageBulkDelete{ChannelID: 3, MessageIDs: ids}, false},
		{"empty", MessageBulkDelete{ChannelID: 3}, false},
		{"duplicate", MessageBulkDelete{ChannelID: 3, MessageIDs: []int64{4, 4}}, false},
		{"zero id", MessageBulkDelete{ChannelID: 3, MessageIDs: []int64{0}}, false},
		{"negative channel", MessageBulkDelete{ChannelID: -1, MessageIDs: []int64{1}}, false},
	} {
		if got := tc.req.Valid(); got != tc.ok {
			t.Errorf("%s: Valid() = %v", tc.name, got)
		}
	}
}

func TestIntegrationScopesSetValid(t *testing.T) {
	if !(IntegrationScopesSet{UniqueID: "u", Scopes: []string{}}).Valid() {
		t.Error("an empty ceiling must be accepted")
	}
	if !(IntegrationScopesSet{UniqueID: "u", Full: true}).Valid() {
		t.Error("clearing must be accepted")
	}
	for _, bad := range []IntegrationScopesSet{
		{UniqueID: "u"},
		{UniqueID: "u", Full: true, Scopes: []string{"server:read"}},
		{Scopes: []string{}},
	} {
		if bad.Valid() {
			t.Errorf("%+v accepted", bad)
		}
	}
}
