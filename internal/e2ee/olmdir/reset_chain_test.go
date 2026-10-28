package olmdir_test

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"

	"noxa/internal/e2ee/olmdir"
)

var chainServer = "tls-sha256:" + strings.Repeat("ab", 32)

func chainKeys(t *testing.T) (string, string) {
	t.Helper()
	c, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, _, _ := ed25519.GenerateKey(rand.Reader)
	return base64.RawStdEncoding.EncodeToString(c.PublicKey().Bytes()), base64.RawStdEncoding.EncodeToString(s)
}

// buildChain returns the pins of epochs 1..n+1 and the n signed transitions.
func buildChain(t *testing.T, noxa ed25519.PrivateKey, n int) ([]olmdir.ResetPin, []olmdir.Reset) {
	t.Helper()
	id, sg := chainKeys(t)
	pins := []olmdir.ResetPin{{Epoch: 1, Identity: id, Signing: sg}}
	var resets []olmdir.Reset
	for i := 0; i < n; i++ {
		ni, ns := chainKeys(t)
		cur := pins[len(pins)-1]
		r, err := olmdir.Reset{Version: olmdir.ResetVersion, Server: chainServer, Principal: "dave", Device: "dave-phone", TransitionID: "t" + string(rune('a'+i%26)),
			OldEpoch: cur.Epoch, OldIdentity: cur.Identity, OldSigning: cur.Signing, NewEpoch: cur.Epoch + 1, NewIdentity: ni, NewSigning: ns,
			Generation: 2, PublicationDigest: strings.Repeat("cd", 32), Reason: "lost"}.Sign(noxa)
		if err != nil {
			t.Fatal(err)
		}
		resets = append(resets, r)
		pins = append(pins, olmdir.ResetPin{Epoch: cur.Epoch + 1, Identity: ni, Signing: ns})
	}
	return pins, resets
}

// pager mimics reset_chain: old_epoch >= after, ordered, at most 32.
func pager(resets []olmdir.Reset) func(int64) ([]olmdir.Reset, error) {
	return func(after int64) ([]olmdir.Reset, error) {
		var out []olmdir.Reset
		for _, r := range resets {
			if r.OldEpoch >= after && len(out) < 32 {
				out = append(out, r)
			}
		}
		return out, nil
	}
}

// 1c-iii-a item 2: a cached pin advances only through transitions signed by
// the independently trusted noXa key and chained from that exact pin.
func TestVerifyResetChain(t *testing.T) {
	pub, noxa, _ := ed25519.GenerateKey(rand.Reader)
	pins, resets := buildChain(t, noxa, 40) // more than one page
	got, passed, err := olmdir.VerifyResetChain(pins[0], pins[40], pub, chainServer, "dave", "dave-phone", pager(resets))
	if err != nil || got != pins[40] || len(passed) != 40 || passed[0] != pins[0] {
		t.Fatalf("multi-page chain: %v %+v %d", err, got, len(passed))
	}
	if got, _, err := olmdir.VerifyResetChain(pins[3], pins[4], pub, chainServer, "dave", "dave-phone", pager(resets)); err != nil || got != pins[4] {
		t.Fatalf("single step from a later pin: %v", err)
	}
	fail := func(name string, from, to olmdir.ResetPin, key ed25519.PublicKey, rs []olmdir.Reset) {
		t.Helper()
		if _, _, err := olmdir.VerifyResetChain(from, to, key, chainServer, "dave", "dave-phone", pager(rs)); err == nil {
			t.Fatalf("%s must not promote", name)
		}
	}
	otherPub, otherKey, _ := ed25519.GenerateKey(rand.Reader)
	_ = otherKey
	fail("signed by another key", pins[0], pins[5], otherPub, resets)
	fail("gap", pins[0], pins[5], pub, append(append([]olmdir.Reset{}, resets[:2]...), resets[3:]...))
	fail("ends at a different identity", pins[0], olmdir.ResetPin{Epoch: 6, Identity: pins[1].Identity}, pub, resets)
	fail("ends at a different signing key", pins[0], olmdir.ResetPin{Epoch: 6, Identity: pins[6].Identity, Signing: pins[1].Signing}, pub, resets)
	fail("not newer", pins[5], pins[5], pub, resets)
	fail("older", pins[5], pins[2], pub, resets)
	// An independent pin (not the chain's old identity) is never replaced.
	independent := pins[0]
	independent.Identity, _ = chainKeys(t)
	fail("from an independent pin", independent, pins[3], pub, resets)
	// A forged transition with the right shape but another signer.
	forged := append([]olmdir.Reset{}, resets...)
	forged[0], _ = forged[0].Sign(otherKey)
	fail("forged first link", pins[0], pins[3], pub, forged)
	// A transition for another server/principal/device.
	moved := append([]olmdir.Reset{}, resets...)
	moved[1].Device = "eve-phone"
	moved[1], _ = moved[1].Sign(noxa)
	fail("another device's transition", pins[0], pins[3], pub, moved)
	// Review P3-1: a second, differently signed successor of one epoch (e.g.
	// a recovery signed over stale server state) is a fork: neither branch
	// is adopted, whichever endpoint is asked for.
	forkTo, _ := chainKeys(t)
	fork := resets[1]
	fork.TransitionID, fork.NewIdentity = "fork", forkTo
	fork, _ = fork.Sign(noxa)
	forked := append(append(append([]olmdir.Reset{}, resets[:2]...), fork), resets[2:]...)
	fail("fork, genuine endpoint", pins[0], pins[3], pub, forked)
	fail("fork, forked endpoint", pins[0], olmdir.ResetPin{Epoch: 3, Identity: forkTo, Signing: pins[3].Signing}, pub, forked)
	// The exact same reset served twice is not a fork.
	if _, _, err := olmdir.VerifyResetChain(pins[0], pins[3], pub, chainServer, "dave", "dave-phone", pager(append(append(append([]olmdir.Reset{}, resets[:2]...), resets[1]), resets[2:]...))); err != nil {
		t.Fatalf("duplicate reset: %v", err)
	}
}
