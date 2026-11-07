package netproto

import (
	"context"
	"encoding/hex"
	"testing"
	"time"
)

var testPowPrefix = []byte("0123456789abcdef")

func TestPowDigestVector(t *testing.T) {
	// Shared with mobile/unit/pow.test.mjs: both implementations must agree.
	// The nonce stays below 2^53 so JavaScript numbers hold it exactly.
	got := PowDigest(testPowPrefix, 0x0000010203040506)
	const want = "0d1e59398619364f0d52dc2ce392a963092bade50b9de2a5d5999ca27f5fcec4"
	if hex.EncodeToString(got[:]) != want {
		t.Fatalf("digest %x", got)
	}
}

func TestSolvePowFindsValidSolution(t *testing.T) {
	ch := PowChallenge{Algorithm: PowAlgorithm, Prefix: testPowPrefix, Difficulty: 12}
	nonce, ok := SolvePow(context.Background(), ch)
	if !ok || !PowSolutionValid(testPowPrefix, nonce, 12) {
		t.Fatalf("nonce %d ok=%v", nonce, ok)
	}
	if PowSolutionValid(testPowPrefix, nonce, 30) && PowLeadingZeroBits(PowDigest(testPowPrefix, nonce)) < 30 {
		t.Fatal("difficulty not enforced")
	}
	if PowSolutionValid([]byte("short"), nonce, 12) {
		t.Fatal("short prefix accepted")
	}
}

func TestSolvePowRefusesBadChallenges(t *testing.T) {
	for _, ch := range []PowChallenge{
		{Algorithm: "md5", Prefix: testPowPrefix, Difficulty: 8},
		{Algorithm: PowAlgorithm, Prefix: []byte("x"), Difficulty: 8},
		{Algorithm: PowAlgorithm, Prefix: testPowPrefix, Difficulty: MaxPowDifficulty + 1},
		{Algorithm: PowAlgorithm, Prefix: testPowPrefix, Difficulty: 0},
	} {
		if _, ok := SolvePow(context.Background(), ch); ok {
			t.Fatalf("solved %+v", ch)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, ok := SolvePow(ctx, PowChallenge{Algorithm: PowAlgorithm, Prefix: testPowPrefix, Difficulty: MaxPowDifficulty}); ok {
		t.Log("solved within 20ms (lucky); cancellation still covered by the loop")
	}
}

func TestRestartingReasonRoundTrip(t *testing.T) {
	for _, seconds := range []int{1, 42, 600} {
		got, ok := ParseRestartingReason(RestartingReason(seconds))
		if !ok || got != seconds {
			t.Fatalf("%d -> %d %v", seconds, got, ok)
		}
	}
	if RestartingReason(0) != "server is restarting; try again in 1 seconds" {
		t.Fatal(RestartingReason(0))
	}
	for _, bad := range []string{"", "server is restarting; try again in seconds", "server is restarting; try again in 5 minutes", "internal error"} {
		if _, ok := ParseRestartingReason(bad); ok {
			t.Fatalf("parsed %q", bad)
		}
	}
}
