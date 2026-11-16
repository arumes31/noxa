// Package framecrypt encrypts H.264 frames for encrypted channel media
// ("noxa-media-e2ee-v1") byte-identically to the pinned LiveKit 2.23.0
// FrameCryptor used by the webview (client/frontend/src/media-e2ee-core.js):
// AES-128-GCM keyed by HKDF-SHA256(salt "LKFrameEncryptionKey", 128 zero info
// bytes), clear header through the first slice header byte (start codes
// normalized to 4 bytes, authenticated as AAD), IV = SSRC | RTP timestamp |
// timestamp - send count, trailer IV(12) | 12 | key index, and an RBSP-escaped
// encrypted tail.
package framecrypt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math"
	"math/big"
	"sync"
)

const (
	ivLength    = 12
	keyring     = 16
	maxEpochs   = 4
	ratchetSalt = "LKFrameEncryptionKey"
)

// ErrNoKey reports that no key is active: the frame must be dropped, never sent.
var ErrNoKey = errors.New("framecrypt: no active media key")

type epochKey struct {
	kid  uint8
	aead cipher.AEAD
	fp   [32]byte
}

// Encryptor holds one publication's key epochs (pid "self|screen").
type Encryptor struct {
	mu         sync.Mutex
	epochs     map[uint64]epochKey
	active     uint64
	retired    uint64
	sendCounts map[uint32]uint64
	seed       func() uint64
}

// NewEncryptor returns an Encryptor without keys.
func NewEncryptor() *Encryptor {
	return &Encryptor{epochs: map[uint64]epochKey{}, sendCounts: map[uint32]uint64{}, seed: randomSeed}
}

// randomSeed mirrors LiveKit's Math.floor(Math.random() * 0xffff).
func randomSeed() uint64 {
	n, err := rand.Int(rand.Reader, big.NewInt(0xffff))
	if err != nil {
		panic(err)
	}
	return n.Uint64()
}

func deriveAEAD(material []byte) (cipher.AEAD, error) {
	key, err := hkdf.Key(sha256.New, material, []byte(ratchetSalt), string(make([]byte, 128)), 16)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// SetKey installs one epoch's 32-byte key material in key slot kid.
func (e *Encryptor) SetKey(epoch uint64, kid uint8, material []byte) error {
	if len(material) != 32 || kid >= keyring || epoch == 0 || epoch > math.MaxInt64 {
		return errors.New("framecrypt: invalid media key")
	}
	fp := sha256.Sum256(material)
	e.mu.Lock()
	defer e.mu.Unlock()
	if epoch <= e.retired {
		return errors.New("framecrypt: retired media epoch")
	}
	if old, ok := e.epochs[epoch]; ok {
		if old.fp != fp || old.kid != kid {
			return errors.New("framecrypt: conflicting media key for epoch")
		}
		return nil
	}
	for other, entry := range e.epochs {
		if entry.kid == kid && other > epoch {
			return errors.New("framecrypt: a newer media epoch holds this key slot")
		}
	}
	aead, err := deriveAEAD(material)
	if err != nil {
		return err
	}
	for other, entry := range e.epochs {
		if entry.kid == kid {
			e.retireLocked(other)
		}
	}
	for len(e.epochs) >= maxEpochs {
		oldest := uint64(math.MaxUint64)
		for other := range e.epochs {
			oldest = min(oldest, other)
		}
		e.retireLocked(oldest)
	}
	e.epochs[epoch] = epochKey{kid: kid, aead: aead, fp: fp}
	return nil
}

func (e *Encryptor) retireLocked(epoch uint64) {
	if _, ok := e.epochs[epoch]; !ok {
		return
	}
	delete(e.epochs, epoch)
	e.retired = max(e.retired, epoch)
	if e.active == epoch {
		e.active = 0
	}
}

// Activate makes an installed epoch the one frames are encrypted with.
func (e *Encryptor) Activate(epoch uint64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.epochs[epoch]; !ok {
		return errors.New("framecrypt: unknown media epoch")
	}
	if e.active != 0 && epoch < e.active {
		return errors.New("framecrypt: media epoch is older than the active one")
	}
	e.active = epoch
	return nil
}

// Retire removes every epoch up to and including through.
func (e *Encryptor) Retire(through uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for epoch := range e.epochs {
		if epoch <= through {
			e.retireLocked(epoch)
		}
	}
	e.retired = max(e.retired, through)
}

// RetireOne removes exactly one epoch.
func (e *Encryptor) RetireOne(epoch uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.retireLocked(epoch)
}

// Forget removes all epochs; retired epochs can still never return.
func (e *Encryptor) Forget() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for epoch := range e.epochs {
		e.retireLocked(epoch)
	}
}

// Active reports the epoch frames are currently encrypted with.
func (e *Encryptor) Active() (uint64, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.active, e.active != 0
}

// EncryptH264 encrypts one Annex-B access unit sent with this RTP SSRC and
// timestamp. It fails closed with ErrNoKey when no epoch is active.
func (e *Encryptor) EncryptH264(frame []byte, ssrc, rtpTimestamp uint32) ([]byte, error) {
	clearBytes, err := unencryptedBytes(frame)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	entry, ok := e.epochs[e.active]
	if e.active == 0 || !ok {
		e.mu.Unlock()
		return nil, ErrNoKey
	}
	count, seen := e.sendCounts[ssrc]
	if !seen {
		count = e.seed()
	}
	e.sendCounts[ssrc] = count + 1
	e.mu.Unlock()

	iv := make([]byte, ivLength)
	binary.BigEndian.PutUint32(iv[0:], ssrc)
	binary.BigEndian.PutUint32(iv[4:], rtpTimestamp)
	binary.BigEndian.PutUint32(iv[8:], rtpTimestamp-uint32(count%0xffff))
	header := normalizeAnnexBStartCodes(frame[:clearBytes])
	sealed := entry.aead.Seal(nil, iv, frame[clearBytes:], header)
	sealed = append(append(sealed, iv...), ivLength, entry.kid)
	return append(append([]byte(nil), header...), writeRbsp(sealed)...), nil
}

// unencryptedBytes is LiveKit's processNALUsForEncryption for H.264: the
// offset of the first slice NAL header plus two bytes.
func unencryptedBytes(frame []byte) (int, error) {
	indices, err := findNALUIndices(frame)
	if err != nil {
		return 0, err
	}
	for _, index := range indices {
		if t := frame[index] & 0x1f; t == 1 || t == 5 {
			if index+2 > len(frame) {
				break
			}
			return index + 2, nil
		}
	}
	return 0, errors.New("framecrypt: no H.264 slice NAL")
}

// findNALUIndices ports LiveKit naluUtils.findNALUIndices.
func findNALUIndices(stream []byte) ([]int, error) {
	var result []int
	start, pos, searchLength := 0, 0, len(stream)-3
	for pos < searchLength {
		for pos < searchLength {
			if pos < searchLength-1 && stream[pos] == 0 && stream[pos+1] == 0 && stream[pos+2] == 0 && stream[pos+3] == 1 {
				break
			}
			if stream[pos] == 0 && stream[pos+1] == 0 && stream[pos+2] == 1 {
				break
			}
			pos++
		}
		if pos >= searchLength {
			pos = len(stream)
		}
		end := pos
		for end > start && stream[end-1] == 0 {
			end--
		}
		if start == 0 {
			if end != start {
				return nil, errors.New("framecrypt: byte stream contains leading data")
			}
		} else {
			result = append(result, start)
		}
		codeLength := 3
		if pos < len(stream)-3 && stream[pos] == 0 && stream[pos+1] == 0 && stream[pos+2] == 0 && stream[pos+3] == 1 {
			codeLength = 4
		}
		start = pos + codeLength
		pos = start
	}
	return result, nil
}

// normalizeAnnexBStartCodes ports LiveKit's 3-byte to 4-byte start code rewrite.
func normalizeAnnexBStartCodes(data []byte) []byte {
	out := make([]byte, 0, len(data)+4)
	from := 0
	for i := 0; i+2 < len(data); {
		if data[i+2] == 0 {
			i++
			continue
		}
		if data[i+2] == 1 && data[i] == 0 && data[i+1] == 0 && (i == 0 || data[i-1] != 0) {
			out = append(append(out, data[from:i]...), 0)
			from = i
		}
		i += 3
	}
	return append(out, data[from:]...)
}

// writeRbsp ports LiveKit's emulation prevention of the encrypted tail.
func writeRbsp(in []byte) []byte {
	out := make([]byte, 0, len(in)+len(in)/64+4)
	zeros := 0
	for _, b := range in {
		if b <= 3 && zeros >= 2 {
			out = append(out, 3)
			zeros = 0
		}
		out = append(out, b)
		if b == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out
}
