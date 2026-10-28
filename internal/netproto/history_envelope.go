package netproto

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// History transfer chunk envelope (noxa-history-v1). The sealed plaintext of
// a history chunk is
//
//	<header JSON> "\n" <records JSON array>
//
// The header binds purpose, source and target device, transfer ID, sequence
// and total, and the SHA-256 of the records bytes, and carries an Ed25519
// signature by the source device's noXa key over HistoryChunkTranscript. An
// own-device DM copy uses the same box keys but is never signed, so it can
// never be replayed as a history chunk; DM receivers refuse text that starts
// like a history envelope, so a chunk is never shown as a message.
const (
	HistoryEnvelopeVersion = "noxa-history-v1"
	historyChunkDomain     = "noxa-history-chunk-v1"
	// HistoryEnvelopeReserve is the plaintext reserved for the header.
	HistoryEnvelopeReserve = 1024
)

// HistoryEnvelopePrefix is how every envelope's plaintext starts.
var HistoryEnvelopePrefix = `{"v":"` + HistoryEnvelopeVersion + `"`

// HistoryHeader is the signed chunk header.
type HistoryHeader struct {
	V         string `json:"v"`
	Source    string `json:"source"`
	Target    string `json:"target"`
	Transfer  string `json:"transfer"`
	Seq       int    `json:"seq"`
	Total     int    `json:"total"`
	Digest    string `json:"digest"`
	Signature []byte `json:"sig"`
}

var errHistoryEnvelope = errors.New("invalid history chunk envelope")

func historyField(s string) bool {
	return s != "" && len(s) <= 128 && !strings.ContainsAny(s, "\r\n")
}

// HistoryChunkTranscript is what the source device signs.
func HistoryChunkTranscript(h HistoryHeader) ([]byte, error) {
	if h.V != HistoryEnvelopeVersion || !historyField(h.Source) || !historyField(h.Target) || !historyField(h.Transfer) ||
		h.Seq < 0 || h.Total < 1 || h.Total > MaxHistoryChunks || h.Seq >= h.Total || len(h.Digest) != 64 {
		return nil, errHistoryEnvelope
	}
	return []byte(strings.Join([]string{historyChunkDomain, h.Source, h.Target, h.Transfer, strconv.Itoa(h.Seq), strconv.Itoa(h.Total), h.Digest}, "\n")), nil
}

// EncodeHistoryChunk builds the plaintext; sign signs the transcript.
func EncodeHistoryChunk(h HistoryHeader, records []byte, sign func([]byte) ([]byte, error)) ([]byte, error) {
	sum := sha256.Sum256(records)
	h.V, h.Digest = HistoryEnvelopeVersion, hex.EncodeToString(sum[:])
	t, err := HistoryChunkTranscript(h)
	if err != nil {
		return nil, err
	}
	if h.Signature, err = sign(t); err != nil {
		return nil, err
	}
	head, err := json.Marshal(h)
	if err != nil {
		return nil, err
	}
	return append(append(head, '\n'), records...), nil
}

// DecodeHistoryChunk splits and checks an envelope (not the signature: the
// caller verifies it under the source device's trusted key).
func DecodeHistoryChunk(plain []byte) (HistoryHeader, []byte, error) {
	var h HistoryHeader
	i := bytes.IndexByte(plain, '\n')
	if i < 0 || !bytes.HasPrefix(plain, []byte(HistoryEnvelopePrefix)) {
		return h, nil, errHistoryEnvelope
	}
	if err := json.Unmarshal(plain[:i], &h); err != nil {
		return h, nil, errHistoryEnvelope
	}
	records := plain[i+1:]
	sum := sha256.Sum256(records)
	if _, err := HistoryChunkTranscript(h); err != nil || h.Digest != hex.EncodeToString(sum[:]) {
		return h, nil, errHistoryEnvelope
	}
	return h, records, nil
}

// IsHistoryEnvelope reports text that must never be shown as a DM.
func IsHistoryEnvelope(text string) bool { return strings.HasPrefix(text, HistoryEnvelopePrefix) }
