package devicetrust

import (
	"crypto/ed25519"
	"encoding/pem"
	"errors"
	"fmt"

	"filippo.io/edwards25519"
)

// LoadPublicKey parses a PEM-encoded Ed25519 public key and returns the raw
// ed25519.PublicKey.
func LoadPublicKey(publicKeyPEM string) (ed25519.PublicKey, error) {
	block, _ := pem.Decode([]byte(publicKeyPEM))
	if block == nil {
		return nil, errors.New("failed to decode public key PEM block")
	}
	if block.Type != "PUBLIC KEY" {
		return nil, fmt.Errorf("unexpected PEM block type %q, want %q", block.Type, "PUBLIC KEY")
	}
	if len(block.Bytes) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid ed25519 public key length: got %d, want %d", len(block.Bytes), ed25519.PublicKeySize)
	}
	if smallOrderPublicKey(block.Bytes) {
		return nil, errors.New("invalid ed25519 public key: small-order point")
	}
	return ed25519.PublicKey(block.Bytes), nil
}

// smallOrderPublicKey reports a key in the small torsion subgroup (such as the
// identity encoding 01 00..00). crypto/ed25519 verification is cofactorless,
// so for such a key anyone can forge signatures ([S]B == R): it is never an
// identity credential. Encodings that are not points at all are left to
// ed25519.Verify, which rejects them.
func smallOrderPublicKey(raw []byte) bool {
	point, err := new(edwards25519.Point).SetBytes(raw)
	if err != nil {
		return false
	}
	return new(edwards25519.Point).MultByCofactor(point).Equal(edwards25519.NewIdentityPoint()) == 1
}
