package server

import (
	"context"
	"encoding/base64"
	"math"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"go.uber.org/zap"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/speechnames"
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
	authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	return startTestEnvDeps(t, nil, nil, func(d *Deps) { d.Authority = authority; d.SpeechNames = svc })
}

func TestSpokenNamesOnlyForPresentSessions(t *testing.T) {
	synth := &countingSynth{}
	env := startSpeechEnv(t, speechnames.New(t.TempDir(), synth, zap.NewNop()))
	defer env.stop()
	var pub [32]byte
	asker, resp := dialAuthedX25519(t, env.addr, "user-uid", pub)
	defer func() { _ = asker.Close() }()
	if !slices.Contains(resp.Capabilities, netproto.CapabilitySpokenNames) {
		t.Fatal("capability not advertised")
	}
	other, otherID := dialAuthed(t, env.addr, "admin-uid")
	defer func() { _ = other.Close() }()

	for range 2 {
		send(t, asker, netproto.MsgSpokenNameGet, netproto.SpokenNameGet{ClientID: otherID, Language: "de"})
		var data netproto.SpokenNameData
		if err := netproto.Decode(readOfType(t, asker, netproto.MsgSpokenNameData), &data); err != nil {
			t.Fatal(err)
		}
		wav, _ := base64.StdEncoding.DecodeString(data.DataBase64)
		if data.ClientID != otherID || data.Language != "de" || len(data.Key) != 64 || !strings.HasPrefix(string(wav), "RIFF") {
			t.Fatalf("reply %+v", data)
		}
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
