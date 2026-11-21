package server

import (
	"context"
	"encoding/base64"
	"fmt"
	"math"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/speechnames"
	"noxa/internal/state"
)

type countingSynth struct{ calls atomic.Int32 }

func (c *countingSynth) Synthesize(context.Context, string, string) ([]float64, error) {
	c.calls.Add(1)
	samples := make([]float64, speechnames.SampleRate)
	for i := range samples[:speechnames.SampleRate/2] {
		samples[i] = 0.3 * math.Sin(float64(i)/7)
	}
	return samples, nil
}

func startSpeechEnv(t *testing.T, svc *speechnames.Service) *testEnv {
	t.Helper()
	backend := serverRoleFixture()
	backend.policy.Roles[0].Permissions = []authorization.Capability{authorization.ViewChannel, authorization.Connect}
	// Channel 1 is hidden from @everyone (only the owner, user-uid, sees it).
	backend.policy.Channels[0].Overrides = []authorization.RoleOverride{{RoleID: 10, Capability: authorization.ViewChannel, Effect: authorization.Deny}}
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	env := startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority; d.SpeechNames = svc })
	env.state.AddChannel(testChannel(1))
	return env
}

func TestSpokenNamesOnlyForPresentSessions(t *testing.T) {
	synth := &countingSynth{}
	env := startSpeechEnv(t, speechnames.New(t.TempDir(), synth, zap.NewNop()))
	defer env.stop()
	var pub [32]byte
	asker, resp := dialAuthedX25519(t, env.addr, "admin-uid", pub)
	defer func() { _ = asker.Close() }()
	if !slices.Contains(resp.Capabilities, netproto.CapabilitySpokenNames) {
		t.Fatal("capability not advertised")
	}
	other, otherID := dialAuthed(t, env.addr, "user-uid")
	defer func() { _ = other.Close() }()

	fetch := func() netproto.SpokenNameData {
		t.Helper()
		send(t, asker, netproto.MsgSpokenNameGet, netproto.SpokenNameGet{ClientID: otherID, Language: "de"})
		var data netproto.SpokenNameData
		if err := netproto.Decode(readOfType(t, asker, netproto.MsgSpokenNameData), &data); err != nil {
			t.Fatal(err)
		}
		if data.ClientID != otherID || data.Language != "de" || data.Nickname == "" || len(data.Key) != 64 {
			t.Fatalf("reply %+v", data)
		}
		return data
	}
	// The first request is answered at once as pending: it never waits for synthesis.
	if first := fetch(); !first.Pending || first.DataBase64 != "" {
		t.Fatalf("first reply %+v", first)
	}
	var data netproto.SpokenNameData
	// 200 ms keeps the polling well inside the request budget (40, then 1/s).
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		if data = fetch(); !data.Pending || time.Now().After(deadline) {
			break
		}
	}
	wav, _ := base64.StdEncoding.DecodeString(data.DataBase64)
	if !strings.HasPrefix(string(wav), "RIFF") || fetch().DataBase64 != data.DataBase64 {
		t.Fatalf("clip %+v", data)
	}
	if synth.calls.Load() != 1 {
		t.Fatalf("synthesized %d times", synth.calls.Load())
	}
	// Free text and absent sessions are never voiced.
	for _, request := range []netproto.SpokenNameGet{{ClientID: "no-such-session", Language: "de"}, {ClientID: otherID, Language: "fr"}} {
		send(t, asker, netproto.MsgSpokenNameGet, request)
		if got := readError(t, asker); got.Code != errCodeNotFound && got.Code != errCodeMalformed {
			t.Fatalf("%+v: %+v", request, got)
		}
	}
	// A member the asker cannot see (hidden channel, invisible) is not voiced.
	if err := env.state.MoveClient(otherID, 1); err != nil {
		t.Fatal(err)
	}
	send(t, asker, netproto.MsgSpokenNameGet, netproto.SpokenNameGet{ClientID: otherID, Language: "de"})
	if got := readError(t, asker); got.Code != errCodeNotFound {
		t.Fatalf("hidden channel: %+v", got)
	}
	if err := env.state.LeaveChannel(otherID); err != nil {
		t.Fatal(err)
	}
	env.state.SetStatus(otherID, "invisible", "")
	send(t, asker, netproto.MsgSpokenNameGet, netproto.SpokenNameGet{ClientID: otherID, Language: "de"})
	if got := readError(t, asker); got.Code != errCodeNotFound {
		t.Fatalf("invisible: %+v", got)
	}
	env.state.SetStatus(otherID, "", "")
	// Each session has its own request budget.
	limited := false
	for range 45 {
		send(t, asker, netproto.MsgSpokenNameGet, netproto.SpokenNameGet{ClientID: otherID, Language: "de"})
	}
	for replies := 0; replies < 45; {
		f := readFrame(t, asker)
		var e netproto.Error
		switch netproto.MessageType(f.Type) {
		case netproto.MsgSpokenNameData:
			replies++
		case netproto.MsgError:
			replies++
			limited = limited || (netproto.Decode(f, &e) == nil && e.Message == netproto.RequestRateLimitMessage)
		}
	}
	if !limited || synth.calls.Load() != 1 {
		t.Fatalf("rate limited %v, synthesized %d times", limited, synth.calls.Load())
	}
}

func TestSpokenNamesDisabledWithoutEngine(t *testing.T) {
	env := startSpeechEnv(t, nil)
	defer env.stop()
	var pub [32]byte
	asker, resp := dialAuthedX25519(t, env.addr, "user-uid", pub)
	defer func() { _ = asker.Close() }()
	if slices.Contains(resp.Capabilities, netproto.CapabilitySpokenNames) {
		t.Fatal("capability advertised without an engine")
	}
	send(t, asker, netproto.MsgSpokenNameGet, netproto.SpokenNameGet{ClientID: resp.ClientID, Language: "en"})
	if got := readError(t, asker); got.Code != errCodeUnavailable {
		t.Fatalf("%+v", got)
	}
}

// The budget for new clips belongs to the account, so a new connection of the
// same account does not start with a full one.
func TestSpokenNameSynthesisBudgetSurvivesReconnect(t *testing.T) {
	synth := &countingSynth{}
	dir := t.TempDir()
	env := startSpeechEnv(t, speechnames.New(dir, synth, zap.NewNop()))
	defer env.stop()
	const names = speechSynthesisBurst + 4
	for i := range names {
		env.state.AddClient(&state.Client{ClientID: fmt.Sprintf("member-%d", i), UniqueID: fmt.Sprintf("member-uid-%d", i), Nickname: fmt.Sprintf("Name %c%c", 'a'+rune(i), 'k'+rune(i)), ConnectedAt: time.Now()})
	}
	// accepted counts the names in [from, names) one session of uid gets voiced.
	accepted := func(uid string, from int) int {
		t.Helper()
		conn, id := dialAuthed(t, env.addr, uid)
		n := 0
		for i := from; i < names; i++ {
			send(t, conn, netproto.MsgSpokenNameGet, netproto.SpokenNameGet{ClientID: fmt.Sprintf("member-%d", i), Language: "en"})
			for answered := false; !answered; {
				switch netproto.MessageType(readFrame(t, conn).Type) {
				case netproto.MsgSpokenNameData:
					n, answered = n+1, true
				case netproto.MsgError:
					answered = true
				}
			}
		}
		_ = conn.Close()
		waitFor(t, "session gone", func() bool { _, ok := env.state.GetClient(id); return !ok })
		return n
	}
	if got := accepted("user-uid", 0); got != speechSynthesisBurst {
		t.Fatalf("first connection: %d new clips, want %d", got, speechSynthesisBurst)
	}
	if got := accepted("user-uid", speechSynthesisBurst); got != 0 {
		t.Fatalf("reconnect refilled the budget: %d new clips", got)
	}
	if got := accepted("admin-uid", speechSynthesisBurst); got != names-speechSynthesisBurst {
		t.Fatalf("another account: %d new clips, want %d", got, names-speechSynthesisBurst)
	}
	// The background syntheses finish before the clip directory is removed.
	waitFor(t, "clips stored", func() bool {
		clips, _ := filepath.Glob(filepath.Join(dir, "en", "*.wav"))
		return len(clips) == names
	})
}
