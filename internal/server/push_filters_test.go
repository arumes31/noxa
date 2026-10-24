package server

import (
	"encoding/json"
	"testing"
	"time"

	"noxa/internal/netproto"
)

func TestPushFilteredRules(t *testing.T) {
	// 2026-03-02 22:30 UTC; the phone is UTC+1, so 23:30 local.
	now := time.Date(2026, 3, 2, 22, 30, 0, 0, time.UTC)
	raw := func(f netproto.PushFilters) []byte {
		b, _ := json.Marshal(f)
		return b
	}
	dm := pushNotice{Kind: pushKindDM, Tag: "dm:ivy"}
	group := pushNotice{Kind: pushKindConversation, Tag: "conversation:g1"}
	mention := pushNotice{Kind: pushKindMention, Tag: "channel:4"}
	poke := pushNotice{Kind: pushKindPoke, Tag: "poke:ivy"}
	call := pushNotice{Kind: pushKindCall, Tag: "call:9"}
	clear := pushNotice{Kind: pushKindClear, Tag: "dm:ivy"}
	cases := []struct {
		name    string
		filters []byte
		notice  pushNotice
		want    bool
	}{
		{"no filters", nil, dm, false},
		{"empty filters", []byte("{}"), mention, false},
		{"malformed filters push", []byte("{"), dm, false},
		{"dnd active", raw(netproto.PushFilters{DNDUntil: now.Unix() + 60}), call, true},
		{"dnd expired", raw(netproto.PushFilters{DNDUntil: now.Unix() - 60}), dm, false},
		{"dnd never hides a clear", raw(netproto.PushFilters{DNDUntil: now.Unix() + 60}), clear, false},
		{"quiet hours wrap midnight", raw(netproto.PushFilters{QuietFrom: "22:00", QuietTo: "07:00", UTCOffsetMinutes: 60}), dm, true},
		{"outside quiet hours", raw(netproto.PushFilters{QuietFrom: "08:00", QuietTo: "17:00", UTCOffsetMinutes: 60}), dm, false},
		{"quiet hours use the phone's offset", raw(netproto.PushFilters{QuietFrom: "23:00", QuietTo: "23:59", UTCOffsetMinutes: 60}), dm, true},
		{"quiet hours in UTC miss", raw(netproto.PushFilters{QuietFrom: "23:00", QuietTo: "23:59"}), dm, false},
		{"equal quiet times are off", raw(netproto.PushFilters{QuietFrom: "22:00", QuietTo: "22:00"}), dm, false},
		{"muted channel mention", raw(netproto.PushFilters{MutedChannels: []int64{4}}), mention, true},
		{"other channel mention", raw(netproto.PushFilters{MutedChannels: []int64{5}}), mention, false},
		{"muted tag (blocked user)", raw(netproto.PushFilters{MutedTags: []string{"dm:ivy", "poke:ivy"}}), poke, true},
		{"muted kind", raw(netproto.PushFilters{MutedKinds: []string{"poke"}}), poke, true},
		{"muted kind spares others", raw(netproto.PushFilters{MutedKinds: []string{"poke"}}), dm, false},
		{"mentions level drops groups", raw(netproto.PushFilters{ChatLevel: "mentions"}), group, true},
		{"mentions level keeps mentions", raw(netproto.PushFilters{ChatLevel: "mentions"}), mention, false},
		{"dm level drops mentions", raw(netproto.PushFilters{ChatLevel: "dm"}), mention, true},
		{"dm level keeps calls", raw(netproto.PushFilters{ChatLevel: "dm"}), call, false},
	}
	for _, c := range cases {
		if got := pushFiltered(c.filters, c.notice, now); got != c.want {
			t.Errorf("%s: pushFiltered = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestPushFiltersValidation(t *testing.T) {
	valid := []netproto.PushFilters{
		{}, {ChatLevel: "dm", QuietFrom: "22:00", QuietTo: "06:30", UTCOffsetMinutes: -300}, {MutedTags: []string{"dm:x"}},
	}
	for _, f := range valid {
		if !f.Valid() {
			t.Errorf("valid filters rejected: %+v", f)
		}
	}
	tooMany := make([]int64, netproto.MaxPushFilterEntries+1)
	invalid := []netproto.PushFilters{
		{ChatLevel: "loud"}, {QuietFrom: "25:00"}, {QuietTo: "7:00"}, {UTCOffsetMinutes: 15 * 60},
		{MutedChannels: tooMany}, {MutedTags: []string{""}}, {DNDUntil: -1},
	}
	for _, f := range invalid {
		if f.Valid() {
			t.Errorf("invalid filters accepted: %+v", f)
		}
	}
	request := netproto.DeviceRequest{Action: netproto.DeviceActionPushPreferences, Preferences: &netproto.PushPreferences{Filters: &netproto.PushFilters{ChatLevel: "loud"}}}
	if request.Valid() {
		t.Fatal("push_preferences with invalid filters accepted")
	}
}

func TestPushPreferencesCarryFilters(t *testing.T) {
	p := startPushTestEnv(t)
	phone, session := p.phoneSession(t, "Pixel")
	subscribed := deviceCall(t, phone, netproto.DeviceRequest{
		Action: netproto.DeviceActionPushSubscribe, Subscription: browserSubscription(t, "https://push.example/1"),
		Preferences: &netproto.PushPreferences{Filters: &netproto.PushFilters{MutedChannels: []int64{4}}},
	})
	if f := subscribed.Preferences.Filters; f == nil || len(f.MutedChannels) != 1 || f.MutedChannels[0] != 4 {
		t.Fatalf("subscribe filters = %+v", f)
	}
	// An older app toggles a flag without filters: they are kept.
	changed := deviceCall(t, phone, netproto.DeviceRequest{Action: netproto.DeviceActionPushPreferences, Preferences: &netproto.PushPreferences{HideDetails: true}})
	if f := changed.Preferences.Filters; !changed.Preferences.HideDetails || f == nil || len(f.MutedChannels) != 1 {
		t.Fatalf("preferences without filters = %+v", changed.Preferences)
	}
	session.mu.Lock()
	session.webHidden = true
	session.mu.Unlock()

	// Mentions in the muted channel never push; other events do.
	p.env.srv.pushNotify("user-uid", pushNotice{Kind: pushKindMention, Tag: "channel:4", From: "Ivy"})
	p.sender.none(t, 200*time.Millisecond)
	p.env.srv.pushNotify("user-uid", pushNotice{Kind: pushKindMention, Tag: "channel:5", From: "Ivy"})
	if got := p.sender.next(t); got.payload.Tag != "channel:5" {
		t.Fatalf("push = %+v", got.payload)
	}

	// Snoozing replaces the filters and silences everything but clears.
	until := time.Now().Add(time.Hour).Unix()
	snoozed := deviceCall(t, phone, netproto.DeviceRequest{Action: netproto.DeviceActionPushPreferences,
		Preferences: &netproto.PushPreferences{HideDetails: true, Filters: &netproto.PushFilters{DNDUntil: until}}})
	if f := snoozed.Preferences.Filters; f == nil || f.DNDUntil != until || len(f.MutedChannels) != 0 {
		t.Fatalf("snoozed filters = %+v", f)
	}
	p.env.srv.pushNotify("user-uid", pushNotice{Kind: pushKindCall, Tag: "call:1", From: "Ivy"})
	p.sender.none(t, 200*time.Millisecond)

	// Invalid filters are refused as malformed.
	if e := deviceCallError(t, phone, netproto.DeviceRequest{Action: netproto.DeviceActionPushPreferences,
		Preferences: &netproto.PushPreferences{Filters: &netproto.PushFilters{QuietFrom: "99:99"}}}); e.Code != errCodeMalformed {
		t.Fatalf("invalid filters error = %+v", e)
	}
}
