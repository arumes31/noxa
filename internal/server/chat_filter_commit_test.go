package server

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"noxa/internal/authorization"
	"noxa/internal/config"
	"noxa/internal/netproto"
)

type failingFilterReadStore struct {
	*fakeChat
	readErr   error
	writes    int
	afterSave func()
}

func (b *failingFilterReadStore) GetServerSetting(ctx context.Context, key string) (string, uint32, error) {
	if key == chatFiltersKey && b.readErr != nil {
		return "", 0, b.readErr
	}
	return b.fakeChat.GetServerSetting(ctx, key)
}

func (b *failingFilterReadStore) SetServerSetting(ctx context.Context, key, value string, id uint32) error {
	b.writes++
	err := b.fakeChat.SetServerSetting(ctx, key, value, id)
	if err == nil && b.afterSave != nil {
		b.afterSave()
	}
	return err
}

func TestChatFilterCommitSurvivesCancellationAndRejectsCorruption(t *testing.T) {
	b := &failingFilterReadStore{fakeChat: newFakeChat()}
	authority, err := authorization.NewAuthority(t.Context(), serverRoleFixture(), func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	audit := &disconnectAuditContext{}
	srv := New(&config.Config{ChatLinkBlacklist: "baseline.example"}, zap.NewNop(), &Deps{Chat: b, Groups: audit, Authority: authority})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	b.afterSave = cancel
	words := "  First, , Second  "
	got, err := srv.saveChatFilters(ctx, "actor", netproto.ChatFilterSet{WordFilter: &words})
	if err != nil || got.WordFilter != "First,Second" || got.LinkBlacklist != "baseline.example" || got.FromConfig || ctx.Err() == nil || audit.calls != 1 || audit.err != nil {
		t.Fatalf("committed filters: %+v %v audit=%+v", got, err, audit)
	}
	b.readErr = errors.New("unavailable after commit")
	if err := srv.moderateBody(t.Context(), "a second message"); err == nil {
		t.Fatal("committed filter was not published to runtime cache")
	}
	b.readErr, b.afterSave = nil, nil
	for _, raw := range []string{"{", "null"} {
		if err := b.fakeChat.SetServerSetting(t.Context(), chatFiltersKey, raw, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := srv.saveChatFilters(t.Context(), "actor", netproto.ChatFilterSet{WordFilter: &words}); err == nil {
			t.Fatalf("corrupt stored document %q was overwritten", raw)
		}
	}
	oversized := strings.Repeat("é", 2049)
	if _, err := srv.saveChatFilters(t.Context(), "actor", netproto.ChatFilterSet{WordFilter: &oversized}); !errors.Is(err, authorization.ErrRoleInvalid) {
		t.Fatal(err)
	}
	srv.chatFilters.writeMu.Lock()
	waitCtx, stop := context.WithTimeout(t.Context(), 20*time.Millisecond)
	_, err = srv.saveChatFilters(waitCtx, "actor", netproto.ChatFilterSet{WordFilter: &words})
	stop()
	srv.chatFilters.writeMu.Unlock()
	if !errors.Is(err, context.DeadlineExceeded) || b.writes != 1 {
		t.Fatalf("invalid/corrupt/canceled patch reached persistence: %v writes=%d", err, b.writes)
	}
}

func TestChatFilterConcurrentPartialSavesRetainBothChanges(t *testing.T) {
	srv := New(&config.Config{ChatLinkWhitelist: "baseline.example"}, zap.NewNop(), &Deps{Chat: newFakeChat()})
	words, blacklist := "words", "blocked.example"
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, patch := range []netproto.ChatFilterSet{{WordFilter: &words}, {LinkBlacklist: &blacklist}} {
		wg.Go(func() { <-start; _, err := srv.saveChatFilters(t.Context(), "actor", patch); errs <- err })
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := srv.readManagedChatFilters(t.Context())
	if err != nil || got.WordFilter != words || got.LinkBlacklist != blacklist || got.LinkWhitelist != "baseline.example" || got.FromConfig {
		t.Fatalf("lost concurrent partial edit: %+v %v", got, err)
	}
}

type blockedFilterStore struct {
	*fakeChat
	firstSaved, release chan struct{}
}

func (b *blockedFilterStore) SetServerSetting(ctx context.Context, key, value string, id uint32) error {
	if err := b.fakeChat.SetServerSetting(ctx, key, value, id); err != nil {
		return err
	}
	if strings.Contains(value, `"word_filter":"first"`) {
		close(b.firstSaved)
		<-b.release
	}
	return nil
}

func TestLegacyFilterWriterCannotRaceCommittedCachePublication(t *testing.T) {
	b := &blockedFilterStore{fakeChat: newFakeChat(), firstSaved: make(chan struct{}), release: make(chan struct{})}
	srv := New(&config.Config{}, zap.NewNop(), &Deps{Chat: b})
	var once sync.Once
	defer once.Do(func() { close(b.release) })
	first, second := make(chan error, 1), make(chan error, 1)
	word := "first"
	go func() {
		_, err := srv.saveChatFilters(t.Context(), "actor", netproto.ChatFilterSet{WordFilter: &word})
		first <- err
	}()
	select {
	case <-b.firstSaved:
	case <-time.After(3 * time.Second):
		t.Fatal("first save missing")
	}
	go func() {
		second <- srv.setServerSettingAndAnnounce(t.Context(), chatFiltersKey, `{"word_filter":"second"}`, "")
	}()
	overtook := false
	select {
	case err := <-second:
		overtook = true
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Millisecond):
	}
	once.Do(func() { close(b.release) })
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if !overtook {
		if err := <-second; err != nil {
			t.Fatal(err)
		}
	}
	filters, _ := srv.effectiveFilters(t.Context())
	if overtook || filters.WordFilter != "second" {
		t.Fatalf("legacy write raced cache publication: overtook=%v filters=%+v", overtook, filters)
	}
}

func TestChatFilterPartialSaveDoesNotOverwriteAfterReadFailure(t *testing.T) {
	b := &failingFilterReadStore{fakeChat: newFakeChat(), readErr: errors.New("database read unavailable")}
	const stored = `{"word_filter":"old","link_blacklist":"protected.example","link_whitelist":"trusted.example"}`
	if err := b.fakeChat.SetServerSetting(t.Context(), chatFiltersKey, stored, 0); err != nil {
		t.Fatal(err)
	}
	authority, err := authorization.NewAuthority(t.Context(), serverRoleFixture(), func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	srv := New(&config.Config{}, zap.NewNop(), &Deps{Chat: b, Authority: authority})
	client := &Client{Conn: newBlockingTCPConn()}
	client.setIdentity("owner", "Owner", 2, false)
	words := "new"
	frame, err := netproto.Encode(netproto.MsgChatFilterSet, netproto.ChatFilterSet{WordFilter: &words})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.handleChatFilterSet(t.Context(), client, frame); err != nil {
		t.Fatal(err)
	}
	value, _, err := b.fakeChat.GetServerSetting(t.Context(), chatFiltersKey)
	if err != nil || value != stored || b.writes != 0 {
		t.Fatalf("read failure erased untouched lists: writes=%d value=%q err=%v", b.writes, value, err)
	}
}
