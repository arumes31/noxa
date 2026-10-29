package server

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"noxa/internal/config"
	"noxa/internal/e2ee/admission"
	"noxa/internal/netproto"
	"noxa/internal/state"
	"noxa/internal/store"

	"go.uber.org/zap"
)

// seatForTest seats a session in a channel regardless of the cap.
func seatForTest(t *testing.T, srv *TCPServer, ch int64, client, principal, device string) {
	t.Helper()
	if _, _, ok := srv.mediaSeats.take(ch, client, mediaSeatID{principal: principal, device: device}, 1<<30, nil); !ok {
		t.Fatalf("seat %s in %d", client, ch)
	}
}

// fillEncryptedChannel adds MediaE2EEMaxVoice seated members to ch.
func fillEncryptedChannel(t *testing.T, env *testEnv, ch int64) {
	t.Helper()
	for i := 0; i < MediaE2EEMaxVoice; i++ {
		id := fmt.Sprintf("member-%d-%d", ch, i)
		env.state.AddClient(&state.Client{ClientID: id, UniqueID: id, MediaDevice: "dev-" + id})
		if err := env.state.MoveClient(id, ch); err != nil {
			t.Fatal(err)
		}
		seatForTest(t, env.srv, ch, id, id, "dev-"+id)
	}
}

func voiceJoined(env *testEnv, client string, ch int64) bool {
	env.voice.mu.Lock()
	defer env.voice.mu.Unlock()
	for _, j := range env.voice.joins {
		if j[0] == client && j[1] == ch {
			return true
		}
	}
	return false
}

// seatedVoiceMember dials a session that holds an encrypted voice seat in
// channel 1.
func seatedVoiceMember(t *testing.T, env *testEnv) (net.Conn, string) {
	t.Helper()
	env.voice.answerSDP = "server-answer"
	conn, userID := dialAuthed(t, env.addr, "user-uid")
	t.Cleanup(func() { _ = conn.Close() })
	placeVoiceTestClients(t, env, 1, userID)
	client, _ := env.srv.clientByID(userID)
	client.setDeviceID("dev-1")
	send(t, conn, netproto.MsgWebRTCOffer, netproto.WebRTCOffer{SDP: "secure-offer", MediaE2EE: netproto.MediaE2EEVersion})
	readOfType(t, conn, netproto.MsgWebRTCAnswer)
	if got := env.srv.mediaSeats.seat(userID); got != 1 {
		t.Fatalf("seat = %d, want 1", got)
	}
	return conn, userID
}

// Fix3 review P2-1 (repro R1): a channel move keeps the voice peer, so it is
// a join of the destination's encrypted voice. A move into a full channel is
// refused with the localized reason, the member stays where it was and keeps
// its seat; a move into a channel with room carries the seat, and the member
// renegotiates there.
func TestMediaE2EEMoveIntoAFullEncryptedChannelIsRefused(t *testing.T) {
	env := startMediaE2EEEnv(t, true)
	conn, userID := seatedVoiceMember(t, env)
	env.state.AddChannel(testChannel(2))
	env.state.AddChannel(testChannel(3))
	fillEncryptedChannel(t, env, 2)

	send(t, conn, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 2})
	if e := protocolError(t, conn); !strings.HasPrefix(e.Message, netproto.MediaE2EEReasonChannelFull+": ") {
		t.Fatalf("move into a full channel: %+v", e)
	}
	if c, _ := env.state.GetClient(userID); c.ChannelID != 1 {
		t.Fatalf("refused move changed the channel to %d", c.ChannelID)
	}
	if voiceJoined(env, userID, 2) {
		t.Fatal("refused move routed the peer into the full channel")
	}
	if got := env.srv.mediaSeats.others(2, ""); got != MediaE2EEMaxVoice {
		t.Fatalf("full channel has %d seats", got)
	}
	if got := env.srv.mediaSeats.seat(userID); got != 1 {
		t.Fatalf("refused move moved the seat to %d", got)
	}

	// Moderator moves go through the same path.
	if err := env.srv.moveClientWithCause(context.Background(), userID, 2, "moderator", true); err != errMediaE2EEChannelFull {
		t.Fatalf("forced move into a full channel: %v", err)
	}

	send(t, conn, netproto.MsgJoinChannel, netproto.JoinChannel{ChannelID: 3})
	waitFor(t, "moved to 3", func() bool { c, _ := env.state.GetClient(userID); return c.ChannelID == 3 })
	if got := env.srv.mediaSeats.seat(userID); got != 3 {
		t.Fatalf("seat after the move = %d, want 3", got)
	}
	fillEncryptedChannel(t, env, 3)
	send(t, conn, netproto.MsgWebRTCOffer, netproto.WebRTCOffer{SDP: "renegotiate", MediaE2EE: netproto.MediaE2EEVersion})
	var answer netproto.WebRTCAnswer
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgWebRTCAnswer), &answer); err != nil || answer.SDP != "server-answer" {
		t.Fatalf("renegotiation after a move refused: %+v %v", answer, err)
	}
}

// Leaving the channel parks the seat (the peer lives on), so the next join
// still meets the cap; a disconnect releases it.
func TestMediaE2EEParkedSeatMeetsTheCapAndDisconnectReleases(t *testing.T) {
	env := startMediaE2EEEnv(t, true)
	conn, userID := seatedVoiceMember(t, env)
	env.state.AddChannel(testChannel(2))
	fillEncryptedChannel(t, env, 2)
	client, _ := env.srv.clientByID(userID)
	if err := env.srv.leaveOwnChannelInContext(context.Background(), client); err != nil {
		t.Fatal(err)
	}
	if got := env.srv.mediaSeats.others(1, ""); got != 0 {
		t.Fatalf("left channel still counts %d seats", got)
	}
	if err := env.srv.moveClientWithCause(context.Background(), userID, 2, userID, false); err != errMediaE2EEChannelFull {
		t.Fatalf("parked seat joined a full channel: %v", err)
	}
	_ = conn.Close()
	waitFor(t, "seat released", func() bool {
		env.srv.mediaSeats.mu.Lock()
		defer env.srv.mediaSeats.mu.Unlock()
		_, ok := env.srv.mediaSeats.byClient[userID]
		return !ok
	})
}

// Fix3 review P3-5: a reconnect is not refused by its own stale session (same
// principal and device, not reaped yet); the stale seat is replaced.
func TestMediaE2EEReconnectReplacesItsStaleSeat(t *testing.T) {
	env := startMediaE2EEEnv(t, true)
	env.voice.answerSDP = "server-answer"
	conn, userID := dialAuthed(t, env.addr, "user-uid")
	t.Cleanup(func() { _ = conn.Close() })
	placeVoiceTestClients(t, env, 1, userID)
	client, _ := env.srv.clientByID(userID)
	client.setDeviceID("dev-1")
	for i := 0; i < MediaE2EEMaxVoice-1; i++ {
		id := fmt.Sprintf("member-%d", i)
		env.state.AddClient(&state.Client{ClientID: id, UniqueID: id, MediaDevice: "dev-" + id})
		if err := env.state.MoveClient(id, 1); err != nil {
			t.Fatal(err)
		}
		seatForTest(t, env.srv, 1, id, id, "dev-"+id)
	}
	env.state.AddClient(&state.Client{ClientID: "ghost", UniqueID: client.uniqueID(), MediaDevice: "dev-1"})
	if err := env.state.MoveClient("ghost", 1); err != nil {
		t.Fatal(err)
	}
	seatForTest(t, env.srv, 1, "ghost", client.uniqueID(), "dev-1")
	send(t, conn, netproto.MsgWebRTCOffer, netproto.WebRTCOffer{SDP: "secure-offer", MediaE2EE: netproto.MediaE2EEVersion})
	var answer netproto.WebRTCAnswer
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgWebRTCAnswer), &answer); err != nil || answer.SDP != "server-answer" {
		t.Fatalf("reconnect refused by its stale session: %+v %v", answer, err)
	}
	if env.srv.mediaSeats.seat("ghost") != 0 || env.srv.mediaSeats.others(1, "") != MediaE2EEMaxVoice {
		t.Fatalf("stale seat not replaced: ghost=%d seats=%d", env.srv.mediaSeats.seat("ghost"), env.srv.mediaSeats.others(1, ""))
	}
	// The stale session's later cleanup does not release the new seat.
	env.srv.mediaSeats.release("ghost")
	if env.srv.mediaSeats.seat(userID) != 1 {
		t.Fatal("stale cleanup released the reconnect's seat")
	}
}

// A different device of the same account is a separate seat and is counted.
func TestMediaSeatsCountOtherDevicesOfTheSameAccount(t *testing.T) {
	var m mediaSeats
	if _, _, ok := m.take(1, "a", mediaSeatID{"u", "d1"}, 1, nil); !ok {
		t.Fatal("first seat")
	}
	if _, _, ok := m.take(1, "b", mediaSeatID{"u", "d2"}, 1, nil); ok {
		t.Fatal("second device of the account passed a cap of 1")
	}
	undo, _, ok := m.take(2, "a", mediaSeatID{"u", "d1"}, 1, nil)
	if !ok || m.seat("a") != 2 {
		t.Fatal("move")
	}
	undo()
	if m.seat("a") != 1 || m.others(2, "") != 0 {
		t.Fatalf("undo: seat %d, channel 2 seats %d", m.seat("a"), m.others(2, ""))
	}
}

// Fix3 review P3-1 (repro R2): Sybil accounts that fill the per-target map
// with made-up targets never refuse a legitimate first control: at capacity
// the per-target cap is skipped (account and device caps still apply).
func TestOlmPerTargetCapAtCapacityNeverRefusesLegitimateControls(t *testing.T) {
	srv := New(&config.Config{ChatRateMsgs: 5, ChatRateWindowSeconds: 3}, nil, &Deps{})
	begin := func(principal, device, tp, td string) bool {
		m := &admission.Manifest{Purpose: "control", Targets: []admission.Target{{Local: true}, {Principal: tp, Device: td}}}
		return srv.allowOlmAdmission(context.Background(), &Client{}, store.OlmSource{Principal: principal, Device: device}, netproto.OlmAdmissionRequest{Action: "begin", Manifest: m})
	}
	accounts := 0
	for a := 0; ; a++ {
		srv.olmBudget.perTarget.mu.Lock()
		n := len(srv.olmBudget.perTarget.buckets)
		srv.olmBudget.perTarget.mu.Unlock()
		if n >= chatLimiterMaxKeys {
			accounts = a
			break
		}
		for i := 0; i < olmAccountControlBegins; i++ {
			begin(fmt.Sprint("sybil", a), fmt.Sprint("d", a%10), fmt.Sprint("fake", a, "-", i), "x")
		}
	}
	if !begin("alice", "a1", "bob", "b1") {
		t.Fatalf("%d sybil accounts filled perTarget and refused alice->bob", accounts)
	}
	// The account and device caps still bound a single account.
	refused := false
	for i := 0; i < 2*olmAccountControlBegins; i++ {
		if !begin("mallory", "m1", fmt.Sprint("t", i), "x") {
			refused = true
			break
		}
	}
	if !refused {
		t.Fatal("device/account caps did not apply at per-target capacity")
	}
	srv.olmBudget.perTarget.mu.Lock()
	n := len(srv.olmBudget.perTarget.buckets)
	srv.olmBudget.perTarget.mu.Unlock()
	if n > chatLimiterMaxKeys {
		t.Fatalf("per-target map grew past its cap: %d", n)
	}
}

// Fix3 review P3-2 (repro R3): 64 members joining within one window rekey
// without refusals: an early member's buckets grow with the channel.
func TestOlmGrowingChannelRekeyIsNeverRefused(t *testing.T) {
	sm := state.New(zap.NewNop())
	sm.AddChannel(&state.Channel{ChannelID: 9, Name: "big"})
	srv := New(&config.Config{ChatRateMsgs: 5, ChatRateWindowSeconds: 3}, nil, &Deps{State: sm})
	const members = MediaE2EEMaxVoice
	clients := []*Client{}
	refusedBy := map[int]int{}
	for j := 0; j < members; j++ {
		id := fmt.Sprintf("c%d", j)
		sm.AddClient(&state.Client{ClientID: id, UniqueID: fmt.Sprintf("u%d", j), MediaDevice: fmt.Sprintf("d%d", j)})
		if err := sm.MoveClient(id, 9); err != nil {
			t.Fatal(err)
		}
		seatForTest(t, srv, 9, id, fmt.Sprintf("u%d", j), fmt.Sprintf("d%d", j))
		clients = append(clients, &Client{ID: id})
		for i := 0; i < j; i++ {
			for _, pair := range [][2]int{{i, j}, {j, i}} {
				s, r := pair[0], pair[1]
				src := store.OlmSource{Principal: fmt.Sprintf("u%d", s), Device: fmt.Sprintf("d%d", s)}
				for _, req := range []netproto.OlmAdmissionRequest{{Action: "snapshot"},
					{Action: "begin", Manifest: &admission.Manifest{Purpose: "control", Targets: []admission.Target{{Local: true}, {Principal: fmt.Sprintf("u%d", r), Device: fmt.Sprintf("d%d", r)}}}},
					{Action: "put_part"}, {Action: "finalize"}, {Action: "retire_receipt"}} {
					if !srv.allowOlmAdmission(context.Background(), clients[s], src, req) {
						refusedBy[s]++
					}
				}
			}
		}
	}
	total := 0
	for _, v := range refusedBy {
		total += v
	}
	if total > 0 {
		t.Fatalf("64 members joining within one window: %d refused frames (member0 %d, member10 %d, member40 %d)", total, refusedBy[0], refusedBy[10], refusedBy[40])
	}
}

// A limit that grows within the window raises the remaining tokens; a
// smaller limit later in the window does not shrink them.
func TestChatRateLimiterLimitGrowsWithinTheWindow(t *testing.T) {
	l := newChatRateLimiter(2, olmBudgetWindow)
	now := time.Now()
	for i := 0; i < 2; i++ {
		if !l.allowLimit("k", now, 2) {
			t.Fatal("initial tokens")
		}
	}
	if l.allowLimit("k", now, 2) {
		t.Fatal("cap of 2")
	}
	first, second, third := l.allowLimit("k", now, 4), l.allowLimit("k", now, 4), l.allowLimit("k", now, 4)
	if !first || !second || third {
		t.Fatal("grown limit must add exactly 2 tokens")
	}
	if l.allowLimit("k", now, 1) {
		t.Fatal("a smaller limit must not add tokens")
	}
}

// A move racing a release never recreates the released seat (CodeRabbit).
func TestMediaSeatsCarryNeverRecreatesAReleasedSeat(t *testing.T) {
	var m mediaSeats
	m.take(1, "a", mediaSeatID{"u", "d"}, 64, nil)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			m.carry(int64(2+i%2), "a", nil, 64, nil)
		}
		close(done)
	}()
	m.release("a")
	<-done
	if got := m.seat("a"); got != 0 {
		t.Fatalf("released seat recreated in channel %d", got)
	}
}
