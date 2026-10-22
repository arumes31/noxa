package authorization

import "testing"

func TestHomeChannelIsVisibleButNeverMediaOrChat(t *testing.T) {
	p := roleFixture()
	p.Channels[0].SystemKind = "home"
	e, err := NewRoleEvaluator(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range []int64{0, 1, 3, 4} {
		if !e.Evaluate(user, 100, ViewChannel).Allowed {
			t.Fatalf("home hidden for %d", user)
		}
		for _, cap := range []Capability{Connect, Speak, ShareCamera, ShareScreen, ReadHistory, SendMessages, UploadFiles, DownloadFiles, RecordChannel} {
			if e.Evaluate(user, 100, cap).Allowed {
				t.Fatalf("home grants %s to %d", cap, user)
			}
		}
	}
	for _, kind := range []ChannelChangeKind{ChannelDelete, ChannelMove, ChannelEdit} {
		if _, err = ApplyChannelTreeChange(p, 1, ChannelTreeChange{Kind: kind, ChannelID: 100, ExpectedRevision: p.Revision}); err == nil {
			t.Fatalf("owner changed home: %s", kind)
		}
	}
	// Home's own disabled media is not inherited by its voice children.
	if !e.Evaluate(1, 101, Connect).Allowed {
		t.Fatal("child lost voice")
	}
}

func TestChannelAccessEditsCannotChangeLifecycleMarkers(t *testing.T) {
	for _, kind := range []string{"", "home", "public"} {
		for _, supplied := range []string{"", "home", "public"} {
			p := roleFixture()
			p.Channels[0].SystemKind = kind
			draft := p.Channels[0]
			draft.SystemKind = supplied
			next, err := ApplyRoleChange(p, p.OwnerID, RoleChange{Kind: ChannelAccessSet, ExpectedRevision: p.Revision, Channel: draft})
			if err != nil {
				t.Fatal(err)
			}
			e, err := NewRoleEvaluator(next)
			if err != nil {
				t.Fatal(err)
			}
			if e.channels[draft.ChannelID].SystemKind != kind {
				t.Fatalf("ACL draft changed %q to %q", kind, supplied)
			}
			if kind == "home" && e.Evaluate(p.OwnerID, draft.ChannelID, SendMessages).Allowed {
				t.Fatal("ACL edit enabled home chat")
			}
		}
	}
	p := roleFixture()
	for _, kind := range []string{"home", "public"} {
		_, err := ApplyChannelTreeChange(p, p.OwnerID, ChannelTreeChange{Kind: ChannelCreate, ExpectedRevision: p.Revision, ChannelID: 999, Access: ChannelPolicy{ChannelID: 999, SystemKind: kind}})
		if err != ErrRoleInvalid {
			t.Fatalf("client created %s lifecycle: %v", kind, err)
		}
	}
}
