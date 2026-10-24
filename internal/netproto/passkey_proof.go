package netproto

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
)

// PasskeyCertificate delegates future device approval to a WebAuthn credential.
// Its issuer is an already trusted device; Chain preserves its signed path to
// the account identity even after the registering browser signs out.
type PasskeyCertificate struct {
	Principal    string     `json:"principal"`
	Issuer       string     `json:"issuer"`
	CredentialID string     `json:"credential_id"`
	Algorithm    int64      `json:"algorithm"`
	PublicKey    []byte     `json:"public_key"` // DER SubjectPublicKeyInfo
	RPID         string     `json:"rp_id"`
	Origin       string     `json:"origin"`
	Signature    []byte     `json:"signature"`
	Chain        []DMDevice `json:"chain"`
}

const MaxPasskeyProof = 32 << 10

func (c PasskeyCertificate) Transcript() []byte {
	return []byte(strings.Join([]string{"noxa-passkey-delegation-v1", c.Principal, c.Issuer, c.CredentialID, base64.StdEncoding.EncodeToString(c.PublicKey), c.RPID, c.Origin, strconv.FormatInt(c.Algorithm, 10)}, "\n"))
}

// PasskeyLoginProof binds a user-verified assertion to both new device keys.
// It is evidence for a trusted desktop to issue the normal Ed25519 endorsement;
// it never makes a server-provided confirmed flag sufficient for E2EE trust.
type PasskeyLoginProof struct {
	Certificate PasskeyCertificate `json:"certificate"`
	Nonce       []byte             `json:"nonce"`
	Assertion   json.RawMessage    `json:"assertion"`
}

func PasskeyDeviceChallenge(nonce []byte, edPublic, xPublic string) []byte {
	transcript := strings.Join([]string{"noxa-passkey-device-v1", base64.StdEncoding.EncodeToString(nonce), edPublic, xPublic}, "\n")
	sum := sha256.Sum256([]byte(transcript))
	return sum[:]
}
