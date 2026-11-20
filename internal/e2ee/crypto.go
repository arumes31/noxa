// Package e2ee contains the cryptographic state machines used by private chat.
package e2ee

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	stdhkdf "crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// FileChunkCiphertextOverhead is the per-chunk overhead emitted by
// EncryptFileChunk: the random AES-GCM nonce followed by the authentication
// tag. Derive it from the AEAD used by sealAES so callers that bound framed
// file transfers cannot drift from the ciphertext format.
var fileChunkCiphertextOverhead = func() int {
	block, err := aes.NewCipher(make([]byte, 32))
	if err != nil {
		panic(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		panic(err)
	}
	return aead.NonceSize() + aead.Overhead()
}()

func FileChunkCiphertextOverhead() int { return fileChunkCiphertextOverhead }

type KeyPair struct {
	Private []byte
	Public  []byte
}

func GenerateX25519() (KeyPair, error) {
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return KeyPair{}, err
	}
	return KeyPair{Private: key.Bytes(), Public: key.PublicKey().Bytes()}, nil
}

type PreKeyBundle struct {
	IdentityDH      []byte
	SigningPublic   []byte
	SignedPreKeyID  uint32
	SignedPreKey    []byte
	Signature       []byte
	OneTimePreKeyID uint32
	OneTimePreKey   []byte
}

func preKeySignedBytes(id uint32, public []byte) []byte {
	prefix := []byte("voicx:signed-prekey:")
	out := make([]byte, len(prefix)+4+len(public))
	copy(out, prefix)
	binary.BigEndian.PutUint32(out[len(prefix):], id)
	copy(out[len(prefix)+4:], public)
	return out
}

func NewPreKeyBundle(identityDH KeyPair, signingPrivate ed25519.PrivateKey, signedID uint32, signedPreKey KeyPair, oneTimeID uint32, oneTimePreKey KeyPair) PreKeyBundle {
	signingPublic := signingPrivate.Public().(ed25519.PublicKey)
	return PreKeyBundle{
		IdentityDH: identityDH.Public, SigningPublic: append([]byte(nil), signingPublic...),
		SignedPreKeyID: signedID, SignedPreKey: signedPreKey.Public,
		Signature:       ed25519.Sign(signingPrivate, preKeySignedBytes(signedID, signedPreKey.Public)),
		OneTimePreKeyID: oneTimeID, OneTimePreKey: oneTimePreKey.Public,
	}
}

// VerifyPreKeyBundle validates the binding between a device signing identity
// and its medium-lived X25519 signed prekey without initiating a session.
func VerifyPreKeyBundle(bundle PreKeyBundle) bool {
	return len(bundle.IdentityDH) == 32 && len(bundle.SignedPreKey) == 32 &&
		len(bundle.SigningPublic) == ed25519.PublicKeySize && len(bundle.Signature) == ed25519.SignatureSize &&
		ed25519.Verify(bundle.SigningPublic, preKeySignedBytes(bundle.SignedPreKeyID, bundle.SignedPreKey), bundle.Signature)
}

func hkdf(secret, salt, info []byte, size int) []byte {
	// crypto/hkdf.Key implements the same RFC 5869 extract-and-expand
	// construction formerly implemented locally. It keeps a nil salt and
	// arbitrary binary info semantically intact.
	out, err := stdhkdf.Key(sha256.New, secret, salt, string(info), size)
	if err != nil {
		// Every caller requests a fixed 32-byte key, well within HKDF's
		// maximum. Keeping this private helper's long-standing no-error
		// contract avoids public API churn.
		panic(fmt.Sprintf("deriving HKDF key: %v", err))
	}
	return out
}

func sealAES(key, plain, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return append(nonce, aead.Seal(nil, nonce, plain, aad)...), nil
}

func openAES(key, blob, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(blob) < aead.NonceSize()+aead.Overhead() {
		return nil, errors.New("invalid ciphertext")
	}
	return aead.Open(nil, blob[:aead.NonceSize()], blob[aead.NonceSize():], aad)
}

// DeriveMessageKey exposes the common HKDF construction for protocol layers
// that need a separately labelled key without reusing a chain key directly.
func DeriveMessageKey(root []byte, conversationID string, number uint64) []byte {
	info := make([]byte, len(conversationID)+8)
	copy(info, conversationID)
	binary.BigEndian.PutUint64(info[len(conversationID):], number)
	return hkdf(root, nil, append([]byte("voicx:message:"), info...), 32)
}

func EncryptFileChunk(master []byte, fileID string, index uint64, plain []byte) ([]byte, error) {
	key := DeriveMessageKey(master, "file:"+fileID, index)
	var aad [8]byte
	binary.BigEndian.PutUint64(aad[:], index)
	return sealAES(key, plain, append([]byte(fileID), aad[:]...))
}

func DecryptFileChunk(master []byte, fileID string, index uint64, blob []byte) ([]byte, error) {
	key := DeriveMessageKey(master, "file:"+fileID, index)
	var aad [8]byte
	binary.BigEndian.PutUint64(aad[:], index)
	return openAES(key, blob, append([]byte(fileID), aad[:]...))
}

// EqualFingerprint avoids data-dependent early exits in identity comparisons.
func EqualFingerprint(a, b []byte) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare(a, b) == 1
}
