package olmdir

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

const ResetVersion = "noxa-olm-reset-v1"

type Reset struct {
	Version           string `json:"version"`
	Server            string `json:"server"`
	Principal         string `json:"principal"`
	Device            string `json:"device"`
	TransitionID      string `json:"transition_id"`
	OldEpoch          int64  `json:"old_epoch"`
	OldIdentity       string `json:"old_identity"`
	OldSigning        string `json:"old_signing"`
	NewEpoch          int64  `json:"new_epoch"`
	NewIdentity       string `json:"new_identity"`
	NewSigning        string `json:"new_signing"`
	Generation        int64  `json:"generation"`
	PublicationDigest string `json:"publication_digest"`
	Reason            string `json:"reason"`
	Signature         []byte `json:"signature"`
}

func PublicationDigest(b Bundle) (string, error) {
	t, e := b.Transcript()
	if e != nil || len(b.Signature) != 64 {
		return "", ErrInvalid
	}
	h := sha256.Sum256(append(t, b.Signature...))
	return hex.EncodeToString(h[:]), nil
}
func (r Reset) Transcript() ([]byte, error) {
	if r.Version != ResetVersion || len(r.Server) != 75 || !strings.HasPrefix(r.Server, "tls-sha256:") || !fingerprint.MatchString(r.Server[11:]) || !ValidText(r.Principal) || !ValidText(r.Device) || !ValidText(r.TransitionID) || r.OldEpoch < 1 || r.NewEpoch != r.OldEpoch+1 || r.NewEpoch > 9007199254740991 || r.Generation < 2 || r.Generation > 9007199254740991 || !ValidCurve(r.OldIdentity) || !ValidSigning(r.OldSigning) || !ValidCurve(r.NewIdentity) || !ValidSigning(r.NewSigning) || r.OldIdentity == r.NewIdentity || !fingerprint.MatchString(r.PublicationDigest) || (r.Reason != "lost" && r.Reason != "restored") {
		return nil, ErrInvalid
	}
	fields := []string{r.Version, r.Server, r.Principal, r.Device, r.TransitionID, strconv.FormatInt(r.OldEpoch, 10), r.OldIdentity, r.OldSigning, strconv.FormatInt(r.NewEpoch, 10), r.NewIdentity, r.NewSigning, strconv.FormatInt(r.Generation, 10), r.PublicationDigest, r.Reason}
	var b strings.Builder
	for _, f := range fields {
		fmt.Fprintf(&b, "%d:%s", len(f), f)
	}
	return []byte(b.String()), nil
}
func (r Reset) Sign(key ed25519.PrivateKey) (Reset, error) {
	t, e := r.Transcript()
	if e != nil || len(key) != 64 {
		return Reset{}, ErrInvalid
	}
	r.Signature = ed25519.Sign(key, t)
	return r, nil
}

// Verify requires independently trusted current noXa authority and the cached
// old epoch/pins. A server assertion cannot promote a peer account.
func (r Reset) Verify(key ed25519.PublicKey, server, principal, device string, epoch int64, identity, signing string) error {
	t, e := r.Transcript()
	if e != nil || r.Server != server || r.Principal != principal || r.Device != device || r.OldEpoch != epoch || r.OldIdentity != identity || r.OldSigning != signing || len(key) != 32 || len(r.Signature) != 64 || !ed25519.Verify(key, t, r.Signature) {
		return ErrInvalid
	}
	return nil
}

type ResetReceipt struct {
	Server            string `json:"server"`
	Device            string `json:"device"`
	TransitionID      string `json:"transition_id"`
	AccountEpoch      int64  `json:"account_epoch"`
	Digest            string `json:"digest"`
	PublicationDigest string `json:"publication_digest"`
}

// ResetPin is a cached peer Olm pin: the account epoch, identity and signing
// key a client adopted from a bundle its independently trusted noXa key signed.
type ResetPin struct {
	Epoch    int64  `json:"epoch"`
	Identity string `json:"identity"`
	Signing  string `json:"signing"`
}

// MaxResetChainPages bounds one promotion (32 transitions per reset_chain page).
const MaxResetChainPages = 64

// VerifyResetChain advances from a cached pin to the target epoch/identity
// (and signing, when given) only through noxa-olm-reset-v1 transitions, each
// verified with Verify against the pin reached so far under the trusted noXa
// key. fetch returns transitions with old_epoch >= after (one reset_chain
// page, any order). A gap, a foreign or altered transition, or a chain that
// ends elsewhere fails with ErrInvalid: a server can never promote a pin.
// It returns the new pin and the pins passed on the way (oldest first).
func VerifyResetChain(from ResetPin, to ResetPin, key ed25519.PublicKey, server, principal, device string, fetch func(after int64) ([]Reset, error)) (ResetPin, []ResetPin, error) {
	if to.Epoch <= from.Epoch {
		return from, nil, ErrInvalid
	}
	cur, passed := from, []ResetPin(nil)
	for pages := 0; cur.Epoch < to.Epoch; pages++ {
		if pages >= MaxResetChainPages {
			return from, nil, ErrInvalid
		}
		page, err := fetch(cur.Epoch)
		if err != nil {
			return from, nil, err
		}
		byOld := map[int64]Reset{}
		for _, r := range page {
			// Two different signed successors of one epoch are a fork (e.g. a
			// recovery signed over stale server state next to the genuine
			// reset): adopt neither; the user confirms explicitly.
			if prev, ok := byOld[r.OldEpoch]; ok && !bytes.Equal(prev.Signature, r.Signature) {
				return from, nil, ErrInvalid
			}
			byOld[r.OldEpoch] = r
		}
		start := cur.Epoch
		for cur.Epoch < to.Epoch {
			r, ok := byOld[cur.Epoch]
			if !ok {
				break
			}
			if r.Verify(key, server, principal, device, cur.Epoch, cur.Identity, cur.Signing) != nil {
				return from, nil, ErrInvalid
			}
			passed = append(passed, cur)
			cur = ResetPin{Epoch: r.NewEpoch, Identity: r.NewIdentity, Signing: r.NewSigning}
		}
		if cur.Epoch == start {
			return from, nil, ErrInvalid // gap
		}
	}
	if cur.Epoch != to.Epoch || cur.Identity != to.Identity || (to.Signing != "" && cur.Signing != to.Signing) {
		return from, nil, ErrInvalid
	}
	return cur, passed, nil
}
