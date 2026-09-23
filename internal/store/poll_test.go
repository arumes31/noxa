package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"noxa/internal/netproto"
)

func pollTestStore(t *testing.T) *Store {
	t.Helper()
	s := testScratchStore(t)
	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	return s
}

func pollTestDefinition() netproto.PollDefinition {
	return netproto.PollDefinition{Question: "Which map next?", Options: []string{"Forest", "Desert", "Harbor"}, ClosesAt: time.Now().Add(time.Hour).Unix()}
}

func createTestPoll(t *testing.T, s *Store, definition netproto.PollDefinition, reference string) (int64, string) {
	t.Helper()
	body, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext := sealTest(t, testScopeKey(31), string(body))
	id, inserted, err := s.StoreChatPoll(t.Context(), 0, "author", "Author", ciphertext, 7, reference, definition)
	if err != nil || !inserted || id <= 0 {
		t.Fatalf("create poll: id=%d inserted=%t error=%v", id, inserted, err)
	}
	return id, ciphertext
}

func assertPollBallot(t *testing.T, got netproto.PollState, id int64, counts, choices []int, voters int) {
	t.Helper()
	if got.MessageID != id || !slices.Equal(got.Counts, counts) || !slices.Equal(got.Choices, choices) || got.TotalVoters != voters || got.Version == 0 {
		t.Fatalf("poll=%+v; want id=%d counts=%v choices=%v voters=%d and a nonzero version", got, id, counts, choices, voters)
	}
}

func TestPollCreatePersistsCiphertextAndDeduplicates(t *testing.T) {
	s := pollTestStore(t)
	id, ciphertext := createTestPoll(t, s, pollTestDefinition(), "poll-request-1")
	initial, err := s.ReadPoll(t.Context(), id, "reader")
	if err != nil {
		t.Fatal(err)
	}
	assertPollBallot(t, initial, id, []int{0, 0, 0}, nil, 0)
	if initial.Closed || initial.ClosesAt <= time.Now().Unix() {
		t.Fatalf("new poll unexpectedly closed: %+v", initial)
	}
	if _, err := s.ChangePoll(t.Context(), id, "reader", []int{1}, false); err != nil {
		t.Fatal(err)
	}
	changedDefinition := netproto.PollDefinition{Question: "Retry must not replace", Options: []string{"A", "B", "C", "D"}, Multiple: true, ClosesAt: time.Now().Add(time.Hour).Unix()}
	duplicateID, inserted, err := s.StoreChatPoll(t.Context(), 0, "author", "Changed nickname", sealTest(t, testScopeKey(31), "changed ciphertext"), 8, "poll-request-1", changedDefinition)
	if err != nil || inserted || duplicateID != id {
		t.Fatalf("duplicate poll: id=%d inserted=%t error=%v", duplicateID, inserted, err)
	}
	// A separate pool proves data is persisted, not held in a store cache.
	other := testAdditionalStore(t, s)
	stored, err := other.ReadPoll(t.Context(), id, "reader")
	if err != nil {
		t.Fatal(err)
	}
	assertPollBallot(t, stored, id, []int{0, 1, 0}, []int{1}, 1)
	var body, bodyEnc, nickname string
	var keyID, rows int
	if err := other.DB().QueryRowContext(t.Context(), `SELECT body, body_enc, key_id, from_nickname FROM chat_messages WHERE id=$1`, id).Scan(&body, &bodyEnc, &keyID, &nickname); err != nil {
		t.Fatal(err)
	}
	if body != "" || bodyEnc != ciphertext || keyID != 7 || nickname != "Author" {
		t.Fatal("poll retry changed encrypted message, metadata, or wrote plaintext")
	}
	if err := other.DB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM chat_messages WHERE client_msg_id='poll-request-1'`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("duplicate message rows=%d error=%v", rows, err)
	}
}

func TestPollInvalidCreationDoesNotLeaveMessage(t *testing.T) {
	s := pollTestStore(t)
	definition := pollTestDefinition()
	definition.Options = []string{"Only one"}
	_, _, err := s.StoreChatPoll(t.Context(), 0, "author", "Author", sealTest(t, testScopeKey(31), "invalid poll"), 7, "invalid-poll", definition)
	if !errors.Is(err, netproto.ErrPollInvalid) {
		t.Fatalf("invalid definition error=%v", err)
	}
	var rows int
	if err := s.DB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM chat_messages WHERE client_msg_id='invalid-poll'`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("invalid poll left message rows=%d error=%v", rows, err)
	}
}

func TestPollReplaceClearAndPrivateBallots(t *testing.T) {
	s := pollTestStore(t)
	definition := pollTestDefinition()
	definition.Multiple = true
	id, _ := createTestPoll(t, s, definition, "")
	first, err := s.ChangePoll(t.Context(), id, "alice", []int{0, 2}, false)
	if err != nil {
		t.Fatal(err)
	}
	assertPollBallot(t, first, id, []int{1, 0, 1}, []int{0, 2}, 1)
	second, err := s.ChangePoll(t.Context(), id, "bob", []int{1}, false)
	if err != nil {
		t.Fatal(err)
	}
	assertPollBallot(t, second, id, []int{1, 1, 1}, []int{1}, 2)
	if second.Version <= first.Version {
		t.Fatal("successful vote did not advance version")
	}
	replaced, err := s.ChangePoll(t.Context(), id, "alice", []int{1}, false)
	if err != nil {
		t.Fatal(err)
	}
	assertPollBallot(t, replaced, id, []int{0, 2, 0}, []int{1}, 2)
	cleared, err := s.ChangePoll(t.Context(), id, "alice", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	assertPollBallot(t, cleared, id, []int{0, 1, 0}, nil, 1)
	for _, uid := range []string{"alice", "observer", ""} {
		got, err := s.ReadPoll(t.Context(), id, uid)
		if err != nil {
			t.Fatal(err)
		}
		assertPollBallot(t, got, id, []int{0, 1, 0}, nil, 1)
	}
	bob, err := s.ReadPoll(t.Context(), id, "bob")
	if err != nil {
		t.Fatal(err)
	}
	assertPollBallot(t, bob, id, []int{0, 1, 0}, []int{1}, 1)
}

func TestPollInvalidVotesDoNotMutate(t *testing.T) {
	s := pollTestStore(t)
	id, _ := createTestPoll(t, s, pollTestDefinition(), "")
	before, err := s.ChangePoll(t.Context(), id, "alice", []int{1}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, choices := range [][]int{{-1}, {3}, {0, 0}, {0, 2}, {1, 99}} {
		if _, err := s.ChangePoll(t.Context(), id, "alice", choices, false); !errors.Is(err, netproto.ErrPollInvalid) {
			t.Fatalf("invalid choices=%v error=%v", choices, err)
		}
		after, err := s.ReadPoll(t.Context(), id, "alice")
		if err != nil {
			t.Fatal(err)
		}
		assertPollBallot(t, after, id, []int{0, 1, 0}, []int{1}, 1)
		if after.Version != before.Version {
			t.Fatal("invalid vote changed poll version")
		}
	}
}

func TestPollConcurrentVotesKeepOneBallotPerIdentity(t *testing.T) {
	s := pollTestStore(t)
	other := testAdditionalStore(t, s)
	id, _ := createTestPoll(t, s, pollTestDefinition(), "")
	const distinctVoters = 18
	start := make(chan struct{})
	errs := make(chan error, distinctVoters+12)
	var workers sync.WaitGroup
	for n := range distinctVoters + 12 {
		workers.Go(func() {
			<-start
			writer := s
			if n%2 != 0 {
				writer = other
			}
			uid := fmt.Sprintf("voter-%d", n)
			if n >= distinctVoters {
				uid = "same-voter"
			}
			_, err := writer.ChangePoll(t.Context(), id, uid, []int{n % 3}, false)
			errs <- err
		})
	}
	close(start)
	workers.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := other.ReadPoll(t.Context(), id, "same-voter")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Choices) != 1 || got.Choices[0] < 0 || got.Choices[0] >= 3 {
		t.Fatalf("concurrent identity has invalid ballot: %+v", got)
	}
	want := []int{6, 6, 6}
	want[got.Choices[0]]++
	assertPollBallot(t, got, id, want, got.Choices, distinctVoters+1)
}

func TestPollCloseSerializesWithVotesAndIsIdempotent(t *testing.T) {
	s := pollTestStore(t)
	other := testAdditionalStore(t, s)
	id, _ := createTestPoll(t, s, pollTestDefinition(), "")
	start := make(chan struct{})
	voteDone := make(chan error, 1)
	go func() {
		<-start
		_, err := other.ChangePoll(t.Context(), id, "alice", []int{2}, false)
		voteDone <- err
	}()
	close(start)
	closed, err := s.ChangePoll(t.Context(), id, "author", nil, true)
	if err != nil || !closed.Closed {
		t.Fatalf("close poll=%+v error=%v", closed, err)
	}
	voteErr := <-voteDone
	if voteErr != nil && !errors.Is(voteErr, netproto.ErrPollClosed) {
		t.Fatal(voteErr)
	}
	got, err := other.ReadPoll(t.Context(), id, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Closed {
		t.Fatal("concurrent vote reopened poll")
	}
	if voteErr == nil {
		assertPollBallot(t, got, id, []int{0, 0, 1}, []int{2}, 1)
	} else {
		assertPollBallot(t, got, id, []int{0, 0, 0}, nil, 0)
	}
	again, err := other.ChangePoll(t.Context(), id, "author", nil, true)
	if err != nil || !again.Closed || again.Version != got.Version {
		t.Fatalf("idempotent close=%+v error=%v previous=%+v", again, err, got)
	}
	for _, choices := range [][]int{{0}, nil} {
		if _, err := s.ChangePoll(t.Context(), id, "alice", choices, false); !errors.Is(err, netproto.ErrPollClosed) {
			t.Fatalf("closed vote choices=%v error=%v", choices, err)
		}
	}
}

func TestPollExpiredRejectsVoting(t *testing.T) {
	s := pollTestStore(t)
	definition := pollTestDefinition()
	definition.ClosesAt = time.Now().Add(2 * time.Second).Unix()
	id, _ := createTestPoll(t, s, definition, "")
	initial, err := s.ReadPoll(t.Context(), id, "alice")
	if err != nil || initial.ClosesAt != definition.ClosesAt {
		t.Fatalf("expiry was not persisted: %+v %v", initial, err)
	}
	timer := time.NewTimer(time.Until(time.Unix(definition.ClosesAt, 0)) + 100*time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	if _, err := s.ChangePoll(t.Context(), id, "alice", []int{0}, false); !errors.Is(err, netproto.ErrPollClosed) {
		t.Fatalf("expired poll vote error=%v", err)
	}
	got, err := s.ReadPoll(t.Context(), id, "alice")
	if err != nil || !got.Closed {
		t.Fatalf("expired poll state=%+v error=%v", got, err)
	}
	assertPollBallot(t, got, id, []int{0, 0, 0}, nil, 0)
}

func TestPollDeletedAndNonPollMessagesAreUnavailable(t *testing.T) {
	s := pollTestStore(t)
	id, _ := createTestPoll(t, s, pollTestDefinition(), "")
	if _, err := s.ChangePoll(t.Context(), id, "alice", []int{0}, false); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteChatMessage(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	plainID, _, err := s.StoreChatMessage(t.Context(), 0, "author", "Author", sealTest(t, testScopeKey(31), "not a poll"), 7, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, messageID := range []int64{id, plainID, -1} {
		if _, err := s.ReadPoll(t.Context(), messageID, "alice"); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("unavailable poll id=%d read error=%v", messageID, err)
		}
		for _, closing := range []bool{false, true} {
			if _, err := s.ChangePoll(t.Context(), messageID, "alice", []int{1}, closing); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("unavailable poll id=%d closing=%t error=%v", messageID, closing, err)
			}
		}
	}
}
