package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

func TestSafetyQRPayloadMatchesTheFrontendFormat(t *testing.T) {
	digest := sha256.Sum256([]byte("pair"))
	got, err := safetyQRPayload("me+/=", "peer_-", digest[:])
	if err != nil {
		t.Fatal(err)
	}
	// frontend/src/safety-qr.js parses exactly this layout.
	if want := "noxa-sn:v1:o:me+/=:peer_-:" + hex.EncodeToString(digest[:]); got != want {
		t.Fatalf("payload = %q, want %q", got, want)
	}
}

func TestSafetyQRPayloadRejectsMalformedInput(t *testing.T) {
	digest := sha256.Sum256(nil)
	for _, tc := range []struct {
		me, peer string
		digest   []byte
	}{
		{"", "peer", digest[:]},
		{"me", "", digest[:]},
		{"me:x", "peer", digest[:]},
		{"me", strings.Repeat("a", 129), digest[:]},
		{"me", "peer", digest[:16]},
	} {
		if _, err := safetyQRPayload(tc.me, tc.peer, tc.digest); err == nil {
			t.Fatalf("accepted %q/%q/%d", tc.me, tc.peer, len(tc.digest))
		}
	}
}

func TestSafetyQRImageIsAPNG(t *testing.T) {
	url, err := safetyQRImage("noxa-sn:v1:o:me:peer:" + strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(url, "data:image/png;base64,"))
	if err != nil || !bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatalf("not a PNG data URL: %v", err)
	}
}
