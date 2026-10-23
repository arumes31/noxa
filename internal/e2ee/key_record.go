package e2ee

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"golang.org/x/crypto/curve25519"
	"strings"
	"unicode/utf8"
)

const keyRecordPrefix = "noxa-dm-key-v1:"

var ErrKeyRecord = errors.New("a valid signed encryption identity is required; update the peer client")

// KeyRecord binds an account UID and its encryption key to an Ed25519 identity.
// Account aliases are authenticated by the server on publication; peers retain
// the signing identity on first contact and require confirmation for changes.
type KeyRecord struct {
	UniqueID      string `json:"uid"`
	SigningKey    []byte `json:"signing_key"`
	EncryptionKey []byte `json:"encryption_key"`
	Signature     []byte `json:"signature"`
}

func keyRecordPayload(r KeyRecord) []byte {
	// JSON has unambiguous string and byte-string boundaries. The domain is
	// distinct from authentication challenges and signed prekey bundles.
	b, _ := json.Marshal(struct {
		UID                       string
		SigningKey, EncryptionKey []byte
	}{r.UniqueID, r.SigningKey, r.EncryptionKey})
	return append([]byte("noxa/dm-identity/v1\x00"), b...)
}
func SignKeyRecord(uid string, encryption [32]byte, private ed25519.PrivateKey) (string, error) {
	if len(private) != ed25519.PrivateKeySize {
		return "", ErrKeyRecord
	}
	r := KeyRecord{UniqueID: uid, SigningKey: append([]byte(nil), private.Public().(ed25519.PublicKey)...), EncryptionKey: encryption[:]}
	r.Signature = ed25519.Sign(private, keyRecordPayload(r))
	b, _ := json.Marshal(r)
	encoded := keyRecordPrefix + base64.StdEncoding.EncodeToString(b)
	if _, err := ParseKeyRecord(encoded, uid); err != nil {
		return "", err
	}
	return encoded, nil
}
func ParseKeyRecord(encoded, uid string) (KeyRecord, error) {
	r, err := DecodeKeyRecord(encoded)
	if err != nil || r.UniqueID != uid {
		return KeyRecord{}, ErrKeyRecord
	}
	return r, nil
}
func DecodeKeyRecord(encoded string) (KeyRecord, error) {
	var r KeyRecord
	if len(encoded) > 2048 || !strings.HasPrefix(encoded, keyRecordPrefix) {
		return r, ErrKeyRecord
	}
	b, err := base64.StdEncoding.Strict().DecodeString(strings.TrimPrefix(encoded, keyRecordPrefix))
	if err != nil || json.Unmarshal(b, &r) != nil {
		return KeyRecord{}, ErrKeyRecord
	}
	canonical, _ := json.Marshal(r)
	if !bytes.Equal(canonical, b) || keyRecordPrefix+base64.StdEncoding.EncodeToString(b) != encoded || r.UniqueID == "" || len(r.UniqueID) > 256 || !utf8.ValidString(r.UniqueID) || strings.ContainsRune(r.UniqueID, 0) || len(r.SigningKey) != 32 || len(r.EncryptionKey) != 32 || len(r.Signature) != 64 || !ed25519.Verify(r.SigningKey, keyRecordPayload(r), r.Signature) {
		return KeyRecord{}, ErrKeyRecord
	}
	// Reject low-order X25519 keys at the common signature boundary.
	var key [32]byte
	copy(key[:], r.EncryptionKey)
	if _, err := curve25519.X25519(make([]byte, 32), key[:]); err != nil {
		return KeyRecord{}, ErrKeyRecord
	}
	return r, nil
}
func (r KeyRecord) PublicKey() (key [32]byte) { copy(key[:], r.EncryptionKey); return }
func (r KeyRecord) Fingerprint() string {
	h := sha256.Sum256(r.SigningKey)
	return hex.EncodeToString(h[:])
}
