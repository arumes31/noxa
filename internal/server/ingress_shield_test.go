package server

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"noxa/internal/config"
	"noxa/internal/netproto"
)

func shieldOn(c *config.Config) {
	c.IngressShield = config.IngressShieldOn
	c.IngressShieldDifficulty = 12
}

func authResponse(t *testing.T, conn net.Conn) netproto.AuthResponse {
	t.Helper()
	var response netproto.AuthResponse
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgAuthResponse), &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func TestIngressShieldRejectsLegacyLogin(t *testing.T) {
	env := startTestEnvDeps(t, nil, shieldOn, nil)
	defer env.stop()
	conn := dialRetry(t, env.addr)
	defer func() { _ = conn.Close() }()
	send(t, conn, netproto.MsgAuthenticate, netproto.Authenticate{Username: "user-uid", Password: "pw"})
	if r := authResponse(t, conn); r.OK || r.Reason != netproto.ReasonIngressShieldUpdate {
		t.Fatalf("response=%+v", r)
	}
}

func TestIngressShieldChallengeThenLogin(t *testing.T) {
	env := startTestEnvDeps(t, nil, shieldOn, nil)
	defer env.stop()
	conn := dialRetry(t, env.addr)
	defer func() { _ = conn.Close() }()
	login := netproto.Authenticate{Username: "user-uid", Password: "pw", ClientFeatures: []string{netproto.ClientFeaturePow}}
	send(t, conn, netproto.MsgAuthenticate, login)
	var ch netproto.PowChallenge
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgPowChallenge), &ch); err != nil {
		t.Fatal(err)
	}
	if ch.Algorithm != netproto.PowAlgorithm || ch.Difficulty != 12 || len(ch.Prefix) != netproto.PowPrefixBytes || ch.ExpiresIn <= 0 {
		t.Fatalf("challenge=%+v", ch)
	}
	nonce, ok := netproto.SolvePow(context.Background(), ch)
	if !ok {
		t.Fatal("unsolvable challenge")
	}
	login.PowPrefix, login.PowNonce = ch.Prefix, nonce
	send(t, conn, netproto.MsgAuthenticate, login)
	if r := authResponse(t, conn); !r.OK {
		t.Fatalf("solved login rejected: %+v", r)
	}
}

func TestIngressShieldRejectsWrongOrReusedSolution(t *testing.T) {
	env := startTestEnvDeps(t, nil, shieldOn, nil)
	defer env.stop()
	conn := dialRetry(t, env.addr)
	defer func() { _ = conn.Close() }()
	login := netproto.Authenticate{Username: "user-uid", Password: "pw", ClientFeatures: []string{netproto.ClientFeaturePow}}
	send(t, conn, netproto.MsgAuthenticate, login)
	var ch netproto.PowChallenge
	if err := netproto.Decode(readOfType(t, conn, netproto.MsgPowChallenge), &ch); err != nil {
		t.Fatal(err)
	}
	// Find a nonce that does NOT solve the challenge.
	bad := uint64(0)
	for netproto.PowSolutionValid(ch.Prefix, bad, ch.Difficulty) {
		bad++
	}
	login.PowPrefix, login.PowNonce = ch.Prefix, bad
	send(t, conn, netproto.MsgAuthenticate, login)
	if r := authResponse(t, conn); r.OK || r.Reason != "proof of work rejected" {
		t.Fatalf("response=%+v", r)
	}

	// A solution for another connection's (or an older) challenge is refused.
	other := dialRetry(t, env.addr)
	defer func() { _ = other.Close() }()
	login.PowPrefix, login.PowNonce = nil, 0
	send(t, other, netproto.MsgAuthenticate, login)
	var first netproto.PowChallenge
	if err := netproto.Decode(readOfType(t, other, netproto.MsgPowChallenge), &first); err != nil {
		t.Fatal(err)
	}
	send(t, other, netproto.MsgAuthenticate, login) // asks again: the first challenge is replaced
	var second netproto.PowChallenge
	if err := netproto.Decode(readOfType(t, other, netproto.MsgPowChallenge), &second); err != nil {
		t.Fatal(err)
	}
	nonce, _ := netproto.SolvePow(context.Background(), first)
	login.PowPrefix, login.PowNonce = first.Prefix, nonce
	send(t, other, netproto.MsgAuthenticate, login)
	if r := authResponse(t, other); r.OK {
		t.Fatal("stale challenge accepted")
	}
}

func TestShutdownGraceRefusesNewLogins(t *testing.T) {
	env := startTestEnvDeps(t, nil, nil, nil)
	defer env.stop()
	env.srv.AnnounceShutdown(30 * time.Second)

	hinted := dialRetry(t, env.addr)
	defer func() { _ = hinted.Close() }()
	send(t, hinted, netproto.MsgAuthenticate, netproto.Authenticate{Username: "user-uid", Password: "pw", ClientFeatures: []string{netproto.ClientFeatureRetryHint}})
	r := authResponse(t, hinted)
	seconds, ok := netproto.ParseRestartingReason(r.Reason)
	if r.OK || !ok || seconds < 29 || seconds > 30+60 {
		t.Fatalf("response=%+v seconds=%d", r, seconds)
	}

	legacy := dialRetry(t, env.addr)
	defer func() { _ = legacy.Close() }()
	send(t, legacy, netproto.MsgAuthenticate, netproto.Authenticate{Username: "user-uid", Password: "pw"})
	_ = legacy.SetReadDeadline(time.Now().Add(5 * time.Second))
	if f, err := netproto.ReadFrame(legacy); err == nil {
		t.Fatalf("legacy client got frame %v instead of a closed connection", netproto.MessageType(f.Type))
	}
}

func TestIngressShieldAutoSurgeAndOverride(t *testing.T) {
	env := startTestEnvDeps(t, nil, func(c *config.Config) {
		c.IngressShield = config.IngressShieldAuto
		c.IngressShieldArrivals = 10
		c.IngressShieldDistinctIPs = 5
	}, nil)
	defer env.stop()
	// A second of its own, away from the environment's setup connections.
	now := time.Unix(time.Now().Unix()+100, 0)
	for i := range 9 {
		env.srv.noteIngressArrival(fmt.Sprintf("198.51.100.%d", i%5), now)
	}
	if env.srv.IngressShieldStatus(now).Active {
		t.Fatal("raised before the surge threshold")
	}
	env.srv.noteIngressArrival("198.51.100.9", now)
	state := env.srv.IngressShieldStatus(now)
	if !state.Active || state.Until.IsZero() {
		t.Fatalf("surge did not raise the shield: %+v", state)
	}
	if env.srv.IngressShieldStatus(now.Add(6 * time.Minute)).Active {
		t.Fatal("shield did not expire after its hold")
	}
	if _, err := env.srv.SetIngressShieldOverride(context.Background(), "tester", shieldOverrideOff); err != nil {
		t.Fatal(err)
	}
	if env.srv.ingressShieldActive(now) {
		t.Fatal("override off ignored")
	}
	if _, err := env.srv.SetIngressShieldOverride(context.Background(), "tester", shieldOverrideOn); err != nil {
		t.Fatal(err)
	}
	if !env.srv.ingressShieldActive(now.Add(time.Hour)) {
		t.Fatal("override on ignored")
	}
	if _, err := env.srv.SetIngressShieldOverride(context.Background(), "tester", "maybe"); err == nil {
		t.Fatal("invalid override accepted")
	}
}

func TestIngressShieldRollingWindowAndReraise(t *testing.T) {
	env := startTestEnvDeps(t, nil, func(c *config.Config) {
		c.IngressShield = config.IngressShieldAuto
		c.IngressShieldArrivals = 10
		c.IngressShieldDistinctIPs = 5
	}, nil)
	defer env.stop()
	// Ten arrivals within 400 ms that straddle a whole-second boundary.
	base := time.Unix(time.Now().Unix()+200, 0).Add(800 * time.Millisecond)
	for i := range 10 {
		env.srv.noteIngressArrival(fmt.Sprintf("203.0.113.%d", i%5), base.Add(time.Duration(i)*40*time.Millisecond))
	}
	if !env.srv.IngressShieldStatus(base.Add(time.Second)).Active {
		t.Fatal("a surge split across a second boundary did not raise the shield")
	}
	// Arrivals spread over more than a second never form a surge.
	env2 := startTestEnvDeps(t, nil, func(c *config.Config) {
		c.IngressShield = config.IngressShieldAuto
		c.IngressShieldArrivals = 10
		c.IngressShieldDistinctIPs = 5
	}, nil)
	defer env2.stop()
	slow := time.Unix(time.Now().Unix()+300, 0)
	for i := range 20 {
		env2.srv.noteIngressArrival(fmt.Sprintf("203.0.113.%d", i%5), slow.Add(time.Duration(i)*150*time.Millisecond))
	}
	if env2.srv.IngressShieldStatus(slow.Add(3 * time.Second)).Active {
		t.Fatal("slow arrivals raised the shield")
	}
	// After the hold expires, a status read records the lowering, so a new
	// surge is a new raise.
	expired := base.Add(6 * time.Minute)
	if env.srv.IngressShieldStatus(expired).Active {
		t.Fatal("hold did not expire")
	}
	env.srv.shield.mu.Lock()
	reported := env.srv.shield.reported
	env.srv.shield.mu.Unlock()
	if reported {
		t.Fatal("expiry was not recorded")
	}
}

func TestPowChallengeGrantsOneLoginWindow(t *testing.T) {
	client := &Client{}
	now := time.Now()
	if _, err := client.issuePowChallenge(12, now); err != nil {
		t.Fatal(err)
	}
	first := client.powLoginDeadline()
	if !first.Equal(now.Add(powChallengeTTL)) {
		t.Fatalf("window=%v", first)
	}
	if _, err := client.issuePowChallenge(12, now.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	if !client.powLoginDeadline().Equal(first) {
		t.Fatal("a second challenge extended the login window")
	}
}
