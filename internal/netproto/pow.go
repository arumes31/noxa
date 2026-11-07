package netproto

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"math/bits"
	"strconv"
)

// Anti-raid proof of work (#7). While the server's ingress shield is raised,
// a client that announced ClientFeaturePow receives a PowChallenge in reply
// to Authenticate and repeats Authenticate with a solution on the same
// connection. The challenge prefix is server-random and bound to that
// connection, single use and short lived, so solutions cannot be stockpiled
// or shared. Verification costs one SHA-256.

// MsgPowChallenge is sent by the server in reply to Authenticate while the
// ingress shield is raised.
const MsgPowChallenge MessageType = 204

// Client features announced in Authenticate.ClientFeatures.
const (
	// ClientFeaturePow: the client solves PowChallenge.
	ClientFeaturePow = "pow-v1"
	// ClientFeatureRetryHint: the client understands RetryAfterReason and
	// keeps reconnecting after it (a shutting-down server tells it when).
	ClientFeatureRetryHint = "retry-hint-v1"
)

// PowAlgorithm identifies the hash construction below.
const PowAlgorithm = "sha256-noxa-pow-v1"

// powLabel separates this hash domain from every other use of SHA-256.
const powLabel = "noxa-pow-v1"

// PowPrefixBytes is the length of the server-random challenge prefix.
const PowPrefixBytes = 16

// MaxPowDifficulty bounds what a client agrees to solve (16.7M hashes).
const MaxPowDifficulty = 24

// PowChallenge asks the client to find Nonce with
// leadingZeroBits(SHA-256(label || prefix || nonce_be64)) >= Difficulty.
type PowChallenge struct {
	Algorithm  string `json:"algorithm"`
	Prefix     []byte `json:"prefix"` // base64 in JSON
	Difficulty int    `json:"difficulty"`
	// ExpiresIn is the number of seconds the challenge stays valid.
	ExpiresIn int `json:"expires_in"`
}

// PowDigest is the hash a solution is judged by.
func PowDigest(prefix []byte, nonce uint64) [32]byte {
	var input [len(powLabel) + PowPrefixBytes + 8]byte
	n := copy(input[:], powLabel)
	n += copy(input[n:], prefix)
	binary.BigEndian.PutUint64(input[n:], nonce)
	return sha256.Sum256(input[:n+8])
}

// PowLeadingZeroBits counts the leading zero bits of digest.
func PowLeadingZeroBits(digest [32]byte) int {
	total := 0
	for _, b := range digest {
		if b != 0 {
			return total + bits.LeadingZeros8(b)
		}
		total += 8
	}
	return total
}

// PowSolutionValid reports whether nonce solves prefix at difficulty.
func PowSolutionValid(prefix []byte, nonce uint64, difficulty int) bool {
	if len(prefix) != PowPrefixBytes || difficulty < 1 || difficulty > 256 {
		return false
	}
	return PowLeadingZeroBits(PowDigest(prefix, nonce)) >= difficulty
}

// SolvePow searches nonces from 0 until a solution is found or ctx ends.
// It refuses challenges that are malformed or harder than MaxPowDifficulty.
func SolvePow(ctx context.Context, ch PowChallenge) (uint64, bool) {
	if ch.Algorithm != PowAlgorithm || len(ch.Prefix) != PowPrefixBytes || ch.Difficulty < 1 || ch.Difficulty > MaxPowDifficulty {
		return 0, false
	}
	for nonce := uint64(0); ; nonce++ {
		if nonce&0xfff == 0 && ctx.Err() != nil {
			return 0, false
		}
		if PowLeadingZeroBits(PowDigest(ch.Prefix, nonce)) >= ch.Difficulty {
			return nonce, true
		}
	}
}

// ReasonIngressShieldUpdate rejects a legacy client while the shield is
// raised: it cannot solve the challenge.
const ReasonIngressShieldUpdate = "the server is under heavy load; update noXa to connect"

// ReasonIngressShieldBusy refuses device linking while the shield is raised.
const ReasonIngressShieldBusy = "the server is under heavy load; try again in a few minutes"

// restartingReasonPrefix starts the retry-hint reason sent to clients that
// announced ClientFeatureRetryHint while a shutdown is scheduled.
const restartingReasonPrefix = "server is restarting; try again in "

// RestartingReason tells a client to reconnect after seconds.
func RestartingReason(seconds int) string {
	return restartingReasonPrefix + strconv.Itoa(max(seconds, 1)) + " seconds"
}

// ParseRestartingReason extracts the delay from RestartingReason.
func ParseRestartingReason(reason string) (int, bool) {
	if len(reason) <= len(restartingReasonPrefix) || reason[:len(restartingReasonPrefix)] != restartingReasonPrefix {
		return 0, false
	}
	rest := reason[len(restartingReasonPrefix):]
	n, i := 0, 0
	for ; i < len(rest) && rest[i] >= '0' && rest[i] <= '9' && i < 6; i++ {
		n = n*10 + int(rest[i]-'0')
	}
	if i == 0 || rest[i:] != " seconds" {
		return 0, false
	}
	return n, true
}

// IngressShieldStatus reports the shield to operators (ServerQuery).
type IngressShieldStatus struct {
	Mode string `json:"mode"`
	// Override is "on" or "off" while an operator forces the shield, else "auto".
	Override   string `json:"override"`
	Active     bool   `json:"active"`
	Until      int64  `json:"until_unix,omitempty"`
	Difficulty int    `json:"difficulty"`
}

// IngressShieldSet forces the shield ("on", "off") or returns it to the
// configured mode ("auto").
type IngressShieldSet struct {
	Override string `json:"override"`
}

// Valid reports a known override.
func (r IngressShieldSet) Valid() bool {
	return r.Override == "on" || r.Override == "off" || r.Override == "auto"
}
