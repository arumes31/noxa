package server

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"noxa/internal/config"
	"noxa/internal/e2ee/admission"
	"noxa/internal/netproto"
	"noxa/internal/state"
	"noxa/internal/store"

	"go.uber.org/zap"
)

// Task 2a review A P1-1: with the REAL production limiters (chat 5 per 3 s,
// the per-session frame budget and the per-device admission budgets of a
// server built by New), a whole channel rekey — every member sending one
// control to every other device at once and draining what it receives — is
// admitted within a stated bound, while the same users' chat sends are never
// refused. One principal has two devices in the channel (desktop + phone).
func TestOlmBudgetsAdmitAChannelRekeyAndKeepChatSends(t *testing.T) {
	const bound = 3 * time.Second
	for _, members := range []int{8, 12} {
		srv := New(&config.Config{ChatRateMsgs: 5, ChatRateWindowSeconds: 3}, nil, &Deps{})
		if srv.chatRate == nil || srv.olmBudget == nil {
			t.Fatal("production limiters must be on")
		}
		type device struct {
			principal, id string
			budget        sessionRequestBudget
		}
		devices := make([]*device, members)
		for i := range devices {
			principal := "u" + string(rune('a'+i))
			if i == members-1 {
				principal = devices[0].principal // the same user's second device
			}
			devices[i] = &device{principal: principal, id: "d" + string(rune('a'+i))}
		}
		admit := func(d *device, action string, control bool, targets ...string) bool {
			target := ""
			if len(targets) > 0 {
				target = targets[0]
			}
			if !d.budget.allow(netproto.MsgOlmAdmissionRequest, time.Now()) {
				return false
			}
			r := netproto.OlmAdmissionRequest{Action: action}
			if action == "begin" {
				purpose := "chat"
				if control {
					purpose = "control"
				}
				r.Manifest = &admission.Manifest{Purpose: purpose, Targets: []admission.Target{{Principal: d.principal, Device: d.id, Local: true}, {Principal: "t", Device: target}}}
			}
			return srv.allowOlmAdmission(context.Background(), &Client{}, store.OlmSource{Principal: d.principal, Device: d.id}, r)
		}
		start := time.Now()
		var wg sync.WaitGroup
		var mu sync.Mutex
		refused := 0
		for _, d := range devices {
			wg.Add(1)
			go func(d *device) {
				defer wg.Done()
				// Sends: one control per other device (snapshot, begin,
				// put_part, finalize, retire_receipt) ...
				var steps [][3]string
				for i := range members - 1 {
					target := "to" + string(rune('a'+i))
					steps = append(steps, [3]string{"snapshot"}, [3]string{"begin", "control", target}, [3]string{"put_part"}, [3]string{"finalize"}, [3]string{"retire_receipt"})
				}
				// ... and the doorbell-driven drain of the controls it receives.
				for i := range members - 1 {
					if i%2 == 0 {
						steps = append(steps, [3]string{"deliveries"})
					}
					steps = append(steps, [3]string{"receive_ack"})
				}
				for _, s := range steps {
					for !admit(d, s[0], s[1] == "control", s[2]) {
						mu.Lock()
						refused++
						mu.Unlock()
						if time.Since(start) > 30*time.Second {
							return
						}
						time.Sleep(20 * time.Millisecond)
					}
				}
			}(d)
		}
		// Meanwhile two members keep chatting within the chat limit.
		chatRefused := 0
		for i := 0; i < 4; i++ {
			for _, d := range []*device{devices[0], devices[1]} {
				if !admit(d, "begin", false) || !admit(d, "finalize", false) {
					chatRefused++
				}
			}
			time.Sleep(150 * time.Millisecond)
		}
		wg.Wait()
		elapsed := time.Since(start)
		t.Logf("%d members (one user with two devices): rekey admitted in %v (%d retries), chat refusals %d", members, elapsed.Round(time.Millisecond), refused, chatRefused)
		if elapsed > bound {
			t.Fatalf("%d-member rekey took %v > %v", members, elapsed, bound)
		}
		if chatRefused != 0 {
			t.Fatalf("chat sends refused during a rekey: %d", chatRefused)
		}
	}
}

// Fix review P2-3: budgets are capped per account, so ten devices of one
// account do not get ten times the budget; and control begins are capped per
// (sender account, target device) and refused while invisible.
func TestOlmBudgetsArePerAccountAndPerTarget(t *testing.T) {
	srv := New(&config.Config{ChatRateMsgs: 5, ChatRateWindowSeconds: 3}, nil, &Deps{})
	controls := 0
	for d := 0; d < 10; d++ {
		for i := 0; i < olmControlBegins; i++ {
			m := &admission.Manifest{Purpose: "control", Targets: []admission.Target{{Local: true}, {Principal: "v", Device: "to" + string(rune('a'+i%26)) + string(rune('a'+d))}}}
			if srv.allowOlmAdmission(context.Background(), &Client{}, store.OlmSource{Principal: "mallory", Device: "d" + string(rune('a'+d))}, netproto.OlmAdmissionRequest{Action: "begin", Manifest: m}) {
				controls++
			}
		}
	}
	if controls > olmAccountControlBegins {
		t.Fatalf("one account admitted %d control begins in a window (cap %d)", controls, olmAccountControlBegins)
	}
	same := 0
	for i := 0; i < 40; i++ {
		m := &admission.Manifest{Purpose: "control", Targets: []admission.Target{{Local: true}, {Principal: "v", Device: "victim"}}}
		if srv.allowOlmAdmission(context.Background(), &Client{}, store.OlmSource{Principal: "eve", Device: "e1"}, netproto.OlmAdmissionRequest{Action: "begin", Manifest: m}) {
			same++
		}
	}
	if same != olmControlsPerTarget {
		t.Fatalf("controls to one target device: %d, want %d", same, olmControlsPerTarget)
	}
}

// Fix review P1-1: rings are coalesced per target device.
func TestOlmDoorbellsAreCoalescedPerDevice(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	victim, victimID := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = victim.Close() }()
	vc, _ := env.srv.clientByID(victimID)
	vc.setDeviceID("dev-victim")
	for i := 0; i < 200; i++ {
		env.srv.ringOlmDoorbells([]store.OlmDeliveredDevice{{Principal: "user-uid", Device: "dev-victim"}})
	}
	if n := countOlmDoorbells(victim, 600*time.Millisecond); n < 1 || n > 4 {
		t.Fatalf("200 rings in a burst delivered %d doorbells", n)
	}
}

func countOlmDoorbells(c net.Conn, d time.Duration) int {
	n := 0
	_ = c.SetReadDeadline(time.Now().Add(d))
	for {
		f, err := netproto.ReadFrame(c)
		if err != nil {
			return n
		}
		if f.Type == uint16(netproto.MsgEvent) && string(f.Payload) == `{"type":"olm_pending","data":{}}` {
			n++
		}
	}
}

// readReplyFrame reads a request's reply from a test connection, skipping
// the content-free olm_pending doorbell that may be interleaved (production
// readers dispatch frames by type; test bridges read the next frame).
//
//nolint:unused // used by the integration-tagged PostgreSQL/browser tests
func readReplyFrame(r io.Reader) (*netproto.Frame, error) {
	for {
		f, err := netproto.ReadFrame(r)
		if err != nil || f.Type != uint16(netproto.MsgEvent) || string(f.Payload) != `{"type":"olm_pending","data":{}}` {
			return f, err
		}
	}
}

// Review 2a-fix2 P3-4: the control and admission budgets scale with the
// sender's encrypted voice channel, so a 50-member and a maximum-size
// (MediaE2EEMaxVoice) channel rekey completes at once with the real limiters
// and the per-session frame budget: every member sends one control to every
// other device and drains its own.
func TestOlmBudgetsAdmitLargeChannelRekeys(t *testing.T) {
	for _, members := range []int{50, MediaE2EEMaxVoice} {
		sm := state.New(zap.NewNop())
		sm.AddChannel(&state.Channel{ChannelID: 9, Name: "big"})
		srv := New(&config.Config{ChatRateMsgs: 5, ChatRateWindowSeconds: 3}, nil, &Deps{State: sm})
		clients := make([]*Client, members)
		for i := range clients {
			id := fmt.Sprintf("c%d", i)
			clients[i] = &Client{ID: id}
			sm.AddClient(&state.Client{ClientID: id, UniqueID: fmt.Sprintf("u%d", i), MediaDevice: fmt.Sprintf("d%d", i)})
			if err := sm.MoveClient(id, 9); err != nil {
				t.Fatal(err)
			}
			seatForTest(t, srv, 9, id, fmt.Sprintf("u%d", i), fmt.Sprintf("d%d", i))
		}
		start := time.Now()
		refused := 0
		for i, c := range clients {
			var budget sessionRequestBudget
			src := store.OlmSource{Principal: fmt.Sprintf("u%d", i), Device: fmt.Sprintf("d%d", i)}
			admit := func(r netproto.OlmAdmissionRequest) {
				if !budget.allow(netproto.MsgOlmAdmissionRequest, time.Now()) || !srv.allowOlmAdmission(context.Background(), c, src, r) {
					refused++
				}
			}
			for j := 0; j < members; j++ {
				if j == i {
					continue
				}
				admit(netproto.OlmAdmissionRequest{Action: "snapshot"})
				admit(netproto.OlmAdmissionRequest{Action: "begin", Manifest: &admission.Manifest{Purpose: "control", Targets: []admission.Target{{Local: true}, {Principal: fmt.Sprintf("u%d", j), Device: fmt.Sprintf("d%d", j)}}}})
				admit(netproto.OlmAdmissionRequest{Action: "put_part"})
				admit(netproto.OlmAdmissionRequest{Action: "finalize"})
				admit(netproto.OlmAdmissionRequest{Action: "retire_receipt"})
				admit(netproto.OlmAdmissionRequest{Action: "receive_ack"})
			}
		}
		t.Logf("%d-member channel: whole rekey admitted in %v, %d refusals", members, time.Since(start).Round(time.Millisecond), refused)
		if refused != 0 {
			t.Fatalf("%d-member rekey: %d admission requests refused", members, refused)
		}
	}
}

// Review 2a-fix2 P3-5: a device refused by its own cap never consumes the
// account token its siblings share.
func TestOlmDeviceCapRefusalKeepsTheAccountToken(t *testing.T) {
	device, account := newChatRateLimiter(1, time.Hour), newChatRateLimiter(2, time.Hour)
	now := time.Now()
	if !both(device, account, "d1", "acct", now, 1, 2) {
		t.Fatal("first request")
	}
	for i := 0; i < 5; i++ {
		if both(device, account, "d1", "acct", now, 1, 2) {
			t.Fatal("device cap must refuse")
		}
	}
	if !both(device, account, "d2", "acct", now, 1, 2) {
		t.Fatal("a sibling device lost the account token to refused requests")
	}
}

// Encrypted voice channels are capped at MediaE2EEMaxVoice members.
func TestMediaE2EERefusesAFullEncryptedChannel(t *testing.T) {
	env := startMediaE2EEEnv(t, true)
	conn, userID := dialAuthed(t, env.addr, "user-uid")
	t.Cleanup(func() { _ = conn.Close() })
	placeVoiceTestClients(t, env, 1, userID)
	client, _ := env.srv.clientByID(userID)
	client.setDeviceID("dev-1")
	for i := 0; i < MediaE2EEMaxVoice; i++ {
		id := fmt.Sprintf("member-%d", i)
		env.state.AddClient(&state.Client{ClientID: id, UniqueID: id, MediaDevice: "dev-" + id})
		if err := env.state.MoveClient(id, 1); err != nil {
			t.Fatal(err)
		}
		seatForTest(t, env.srv, 1, id, id, "dev-"+id)
	}
	send(t, conn, netproto.MsgWebRTCOffer, netproto.WebRTCOffer{SDP: "secure-offer", MediaE2EE: netproto.MediaE2EEVersion})
	if e := protocolError(t, conn); !strings.HasPrefix(e.Message, netproto.MediaE2EEReasonChannelFull+": ") {
		t.Fatalf("full channel: %+v", e)
	}
	if env.srv.mediaSeats.seat(userID) != 0 {
		t.Fatal("a refused offer holds a seat")
	}
}

// A member that holds a seat keeps renegotiating (camera, screen, ICE restart)
// even when the channel is full past the cap: one extra seat can never lock
// every seated member out. The seat is taken before the answer is written, so
// the test is deterministic (fix3 review P3-4).
func TestMediaE2EESeatedMemberRenegotiatesInAFullChannel(t *testing.T) {
	env := startMediaE2EEEnv(t, true)
	env.voice.answerSDP = "server-answer"
	conn, userID := dialAuthed(t, env.addr, "user-uid")
	t.Cleanup(func() { _ = conn.Close() })
	placeVoiceTestClients(t, env, 1, userID)
	client, _ := env.srv.clientByID(userID)
	client.setDeviceID("dev-1")
	send(t, conn, netproto.MsgWebRTCOffer, netproto.WebRTCOffer{SDP: "secure-offer", MediaE2EE: netproto.MediaE2EEVersion})
	readOfType(t, conn, netproto.MsgWebRTCAnswer)
	if got := env.srv.mediaSeats.seat(userID); got != 1 {
		t.Fatalf("seat after the answer = %d, want 1", got)
	}
	for i := 0; i <= MediaE2EEMaxVoice; i++ {
		id := fmt.Sprintf("member-%d", i)
		env.state.AddClient(&state.Client{ClientID: id, UniqueID: id, MediaDevice: "dev-" + id})
		if err := env.state.MoveClient(id, 1); err != nil {
			t.Fatal(err)
		}
		seatForTest(t, env.srv, 1, id, id, "dev-"+id)
	}
	send(t, conn, netproto.MsgWebRTCOffer, netproto.WebRTCOffer{SDP: "renegotiate", MediaE2EE: netproto.MediaE2EEVersion})
	var answer netproto.WebRTCAnswer
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgWebRTCAnswer), &answer); err != nil || answer.SDP != "server-answer" {
		t.Fatalf("seated renegotiation refused: %+v %v", answer, err)
	}
	// A seat in another channel does not carry over.
	seatForTest(t, env.srv, 2, userID, "user-uid", "dev-1")
	send(t, conn, netproto.MsgWebRTCOffer, netproto.WebRTCOffer{SDP: "secure-offer", MediaE2EE: netproto.MediaE2EEVersion})
	if e := protocolError(t, conn); !strings.HasPrefix(e.Message, netproto.MediaE2EEReasonChannelFull+": ") {
		t.Fatalf("seat from another channel: %+v", e)
	}
	if got := env.srv.mediaSeats.seat(userID); got != 2 {
		t.Fatalf("a refused offer moved the seat to %d", got)
	}
}
