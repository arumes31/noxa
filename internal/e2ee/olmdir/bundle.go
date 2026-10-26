package olmdir

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"filippo.io/edwards25519"
)

const Version = "noxa-olm-directory-v1"
const Protocol = "noxa-olm-v1"
const MaxKeys = 100
const MaxBytes = 24 << 10
const MaxLifetime = 7 * 24 * time.Hour
const DeliveryGrace = 24 * time.Hour

var ErrInvalid = errors.New("invalid Olm directory bundle")
var fingerprint = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ServerBinding accepts a verified control-channel certificate fingerprint only.
// An AuthResponse over an unauthenticated transport does not establish trust.
func ServerBinding(verifiedFingerprint string) (string, error) {
	f := strings.ToLower(verifiedFingerprint)
	if matched, _ := regexp.MatchString(`^(?:[0-9a-f]{2}:){31}[0-9a-f]{2}$`, f); matched {
		f = strings.ReplaceAll(f, ":", "")
	}
	if !fingerprint.MatchString(f) {
		return "", ErrInvalid
	}
	return "tls-sha256:" + f, nil
}
func ValidText(s string) bool {
	if len(s) == 0 || len(s) > 128 || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func raw(s string, n int) ([]byte, error) {
	if len(s) != (n*8+5)/6 {
		return nil, ErrInvalid
	}
	b, e := base64.RawStdEncoding.Strict().DecodeString(s)
	if e != nil || len(b) != n || base64.RawStdEncoding.EncodeToString(b) != s {
		return nil, ErrInvalid
	}
	return b, nil
}

// KeyNumber decodes vodozemac's big-endian uint64 identifier, bounded to SQL int64.
func KeyNumber(id string) (int64, error) {
	b, e := raw(id, 8)
	if e != nil {
		return 0, e
	}
	n := binary.BigEndian.Uint64(b)
	if n > math.MaxInt64 {
		return 0, ErrInvalid
	}
	return int64(n), nil
}
func ValidCurve(s string) bool {
	b, e := raw(s, 32)
	if e != nil {
		return false
	}
	// RFC7748 permits noncanonical encodings; directory v1 deliberately does not.
	if b[31]&128 != 0 {
		return false
	}
	p := []byte{0xed, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x7f}
	for i := 31; i >= 0; i-- {
		if b[i] < p[i] {
			break
		}
		if b[i] > p[i] || i == 0 {
			return false
		}
	}
	pub, e := ecdh.X25519().NewPublicKey(b)
	if e != nil {
		return false
	}
	seed := make([]byte, 32)
	seed[0] = 42
	priv, _ := ecdh.X25519().NewPrivateKey(seed)
	_, e = priv.ECDH(pub)
	return e == nil
}
func ValidSigning(s string) bool {
	b, e := raw(s, 32)
	if e != nil {
		return false
	}
	p, e := new(edwards25519.Point).SetBytes(b)
	return e == nil && string(p.Bytes()) == string(b) && new(edwards25519.Point).MultByCofactor(p).Equal(edwards25519.NewIdentityPoint()) != 1
}

type Key struct {
	ID  string `json:"id"`
	Key string `json:"key"`
}
type Bundle struct {
	Version      string `json:"version"`
	Protocol     string `json:"protocol"`
	Server       string `json:"server"`
	Principal    string `json:"principal"`
	DeviceID     string `json:"device_id"`
	AccountEpoch int64  `json:"account_epoch"`
	Generation   int64  `json:"generation"`
	IssuedAt     int64  `json:"issued_at"`
	ExpiresAt    int64  `json:"expires_at"`
	Identity     string `json:"identity"`
	Signing      string `json:"signing"`
	OneTimeKeys  []Key  `json:"one_time_keys"`
	Signature    []byte `json:"signature"`
}

// Transcript is a UTF-8 byte-length-prefixed sequence, independent of JSON layout.
// Key order is signed; callers must persist and retry the original ordered bundle.
func (b Bundle) Transcript() ([]byte, error) {
	if b.Version != Version || b.Protocol != Protocol || !strings.HasPrefix(b.Server, "tls-sha256:") || !fingerprint.MatchString(strings.TrimPrefix(b.Server, "tls-sha256:")) || !ValidText(b.Principal) || !ValidText(b.DeviceID) || b.AccountEpoch < 1 || b.AccountEpoch > 9007199254740991 || b.Generation < 1 || b.Generation > 9007199254740991 || b.IssuedAt < 1 || b.ExpiresAt <= b.IssuedAt || b.ExpiresAt > 9007199254740991 || b.ExpiresAt-b.IssuedAt > int64(MaxLifetime/time.Second) || !ValidCurve(b.Identity) || !ValidSigning(b.Signing) || len(b.OneTimeKeys) < 1 || len(b.OneTimeKeys) > MaxKeys {
		return nil, ErrInvalid
	}
	fields := []string{b.Version, b.Protocol, b.Server, b.Principal, b.DeviceID, strconv.FormatInt(b.AccountEpoch, 10), strconv.FormatInt(b.Generation, 10), strconv.FormatInt(b.IssuedAt, 10), strconv.FormatInt(b.ExpiresAt, 10), b.Identity, b.Signing, strconv.Itoa(len(b.OneTimeKeys))}
	seen := map[string]bool{}
	pubs := map[string]bool{}
	for _, k := range b.OneTimeKeys {
		if _, e := KeyNumber(k.ID); e != nil || seen[k.ID] || pubs[k.Key] || k.Key == b.Identity || !ValidCurve(k.Key) {
			return nil, ErrInvalid
		}
		seen[k.ID] = true
		pubs[k.Key] = true
		fields = append(fields, k.ID, k.Key)
	}
	var out strings.Builder
	for _, f := range fields {
		fmt.Fprintf(&out, "%d:%s", len(f), f)
	}
	return []byte(out.String()), nil
}
func (b Bundle) Verify(key ed25519.PublicKey, server, principal, device string, now time.Time) error {
	t, e := b.Transcript()
	if e != nil || b.Server != server || b.Principal != principal || b.DeviceID != device || len(key) != 32 || len(b.Signature) != 64 || b.IssuedAt > now.Unix()+300 || b.ExpiresAt <= now.Unix() || !ed25519.Verify(key, t, b.Signature) {
		return ErrInvalid
	}
	return nil
}

// VerifyClaim requires a signing key obtained from a currently trusted device
// chain and an independently pinned Olm identity (empty only on first approval).
func (b Bundle) VerifyClaim(key ed25519.PublicKey, server, principal, device, pinnedIdentity, claimedID string, now time.Time) (Key, error) {
	if e := b.Verify(key, server, principal, device, now); e != nil {
		return Key{}, e
	}
	if pinnedIdentity != "" && pinnedIdentity != b.Identity {
		return Key{}, errors.New("Olm account identity changed; explicit approval required")
	}
	for _, k := range b.OneTimeKeys {
		if k.ID == claimedID {
			return k, nil
		}
	}
	return Key{}, ErrInvalid
}

// Sign creates the publication using the existing authenticated noXa device
// private key. The caller must atomically persist its account snapshot and this
// exact signed publication before sending either to the directory.
func (b Bundle) Sign(noxaDeviceKey ed25519.PrivateKey) (Bundle, error) {
	t, e := b.Transcript()
	if e != nil {
		return Bundle{}, e
	}
	if len(noxaDeviceKey) != ed25519.PrivateKeySize {
		return Bundle{}, ErrInvalid
	}
	b.Signature = ed25519.Sign(noxaDeviceKey, t)
	return b, nil
}

// VerifyEvidence validates immutable signed identity evidence for an admitted
// delayed initial packet. Publication expiry forbids new claims, not receipt.
// Caller independently verifies the admitted claim/session and current trust.
func (b Bundle) VerifyEvidence(key ed25519.PublicKey, server, principal, device string) error {
	t, e := b.Transcript()
	if e != nil || b.Server != server || b.Principal != principal || b.DeviceID != device || len(key) != 32 || len(b.Signature) != 64 || !ed25519.Verify(key, t, b.Signature) {
		return ErrInvalid
	}
	return nil
}
func (b Bundle) VerifyReservedClaim(key ed25519.PublicKey, server, principal, device, pinnedIdentity, claimedID string, claimUntil int64, now time.Time) (Key, error) {
	if claimUntil <= now.Unix() || claimUntil > b.ExpiresAt+3600 || b.IssuedAt > now.Unix()+300 || b.VerifyEvidence(key, server, principal, device) != nil || (pinnedIdentity != "" && pinnedIdentity != b.Identity) {
		return Key{}, ErrInvalid
	}
	for _, k := range b.OneTimeKeys {
		if k.ID == claimedID {
			return k, nil
		}
	}
	return Key{}, ErrInvalid
}
