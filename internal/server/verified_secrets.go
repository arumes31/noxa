package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"sync"
)

// verifiedSecretsMax bounds the cache; it is cleared when full.
const verifiedSecretsMax = 1024

// verifiedSecrets remembers (password hash, secret) pairs that already passed
// the KDF, so a correct server or channel password presented again (reconnects,
// rejoins) takes no KDF slot. Only successes are stored, as keyed digests
// under a per-process random key; a changed password hash never matches. The
// zero value is ready to use.
type verifiedSecrets struct {
	mu   sync.Mutex
	key  []byte
	seen map[[32]byte]struct{}
}

func (v *verifiedSecrets) digestLocked(hash, secret string) [32]byte {
	if v.key == nil {
		v.key = make([]byte, 32)
		_, _ = rand.Read(v.key)
		v.seen = make(map[[32]byte]struct{})
	}
	mac := hmac.New(sha256.New, v.key)
	mac.Write([]byte(hash))
	mac.Write([]byte{0})
	mac.Write([]byte(secret))
	return [32]byte(mac.Sum(nil))
}

func (v *verifiedSecrets) known(hash, secret string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	_, ok := v.seen[v.digestLocked(hash, secret)]
	return ok
}

func (v *verifiedSecrets) remember(hash, secret string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	d := v.digestLocked(hash, secret)
	if len(v.seen) >= verifiedSecretsMax {
		clear(v.seen)
	}
	v.seen[d] = struct{}{}
}
