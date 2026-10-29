package server

import (
	"context"
	"sync"
	"testing"
	"time"

	"noxa/internal/config"
	"noxa/internal/e2ee/admission"
	"noxa/internal/netproto"
	"noxa/internal/store"
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
		if srv.chatRate == nil || srv.olmControlRate == nil {
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
		admit := func(d *device, action string, control bool) bool {
			if !d.budget.allow(netproto.MsgOlmAdmissionRequest, time.Now()) {
				return false
			}
			r := netproto.OlmAdmissionRequest{Action: action}
			if action == "begin" {
				purpose := "chat"
				if control {
					purpose = "control"
				}
				r.Manifest = &admission.Manifest{Purpose: purpose}
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
				var steps [][2]string
				for range members - 1 {
					steps = append(steps, [2]string{"snapshot", ""}, [2]string{"begin", "control"}, [2]string{"put_part", ""}, [2]string{"finalize", ""}, [2]string{"retire_receipt", ""})
				}
				// ... and the doorbell-driven drain of the controls it receives.
				for i := range members - 1 {
					if i%2 == 0 {
						steps = append(steps, [2]string{"deliveries", ""})
					}
					steps = append(steps, [2]string{"receive_ack", ""})
				}
				for _, s := range steps {
					for !admit(d, s[0], s[1] == "control") {
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

// The doorbell reaches every live session of each non-local target device
// of a finalized attempt, and nobody else; it carries no content.
func TestOlmDoorbellRingsOnlyTargetDevices(t *testing.T) {
	env := startTestEnv(t, nil)
	defer env.stop()
	target, targetID := dialAuthed(t, env.addr, "user-uid")
	defer target.Close()
	other, otherID := dialAuthed(t, env.addr, "admin-uid")
	defer other.Close()
	tc, _ := env.srv.clientByID(targetID)
	tc.setDeviceID("dev-target")
	oc, _ := env.srv.clientByID(otherID)
	oc.setDeviceID("dev-other")
	env.srv.olmAttempts.note("admin-uid", "dev-other", admission.Manifest{AttemptID: "a1", Purpose: "control",
		Targets: []admission.Target{{Principal: "admin-uid", Device: "dev-other", Local: true}, {Principal: "user-uid", Device: "dev-target"}}})
	env.srv.ringOlmDoorbells("admin-uid", "dev-other", "a1")
	f := readOfType(t, target, netproto.MsgEvent)
	if string(f.Payload) != `{"type":"olm_pending","data":{}}` {
		t.Fatalf("doorbell payload %s", f.Payload)
	}
	_ = other.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	for {
		g, err := netproto.ReadFrame(other)
		if err != nil {
			break
		}
		if g.Type == uint16(netproto.MsgEvent) && string(g.Payload) == `{"type":"olm_pending","data":{}}` {
			t.Fatal("the sender's own (local) device was rung")
		}
	}
	// An unknown attempt rings nobody.
	env.srv.ringOlmDoorbells("admin-uid", "dev-other", "nope")
}
