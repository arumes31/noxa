package main

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"regexp"

	"rsc.io/qr"
)

// safetyQRPrefix and the pairwise scheme tag match
// frontend/src/safety-qr.js (205). The payload names who shows the code,
// whom it is for and the full safety-number digest; it holds no secret.
const (
	safetyQRPrefix         = "noxa-sn:v1"
	safetyQRSchemePairwise = "o"
)

var safetyQRUniqueID = regexp.MustCompile(`^[A-Za-z0-9+/=_-]{1,128}$`)

// safetyQRPayload builds the QR text for the pairwise safety number.
func safetyQRPayload(me, peer string, digest []byte) (string, error) {
	if !safetyQRUniqueID.MatchString(me) || !safetyQRUniqueID.MatchString(peer) || len(digest) != 32 {
		return "", errors.New("invalid safety QR payload")
	}
	return safetyQRPrefix + ":" + safetyQRSchemePairwise + ":" + me + ":" + peer + ":" + hex.EncodeToString(digest), nil
}

// safetyQRImage renders a payload as a PNG data URL. Medium error correction
// keeps the code readable from a phone held at arm's length.
func safetyQRImage(payload string) (string, error) {
	code, err := qr.Encode(payload, qr.M)
	if err != nil {
		return "", err
	}
	code.Scale = 6
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(code.PNG()), nil
}
