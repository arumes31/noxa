// Package deviceproof verifies passkey delegations independently of server trust.
package deviceproof

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	"noxa/internal/auth"
	"noxa/internal/netproto"
)

var ErrProof = errors.New("unverified passkey device authorization")

// CertificateIssuer checks the signed device chain against the locally pinned
// account identity. Historical certificates grant no ability to receive DMs.
func CertificateIssuer(c netproto.PasskeyCertificate, principal string, pinned ed25519.PublicKey) (ed25519.PublicKey, error) {
	if c.Principal != principal || len(pinned) != ed25519.PublicKeySize || len(c.Chain) == 0 || len(c.Chain) > 32 {
		return nil, ErrProof
	}
	for _, field := range []string{c.Principal, c.Issuer, c.CredentialID, c.RPID, c.Origin} {
		if field == "" || len(field) > 2048 || strings.ContainsAny(field, "\r\n") {
			return nil, ErrProof
		}
	}
	origin, err := url.Parse(c.Origin)
	if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return nil, ErrProof
	}
	host := origin.Hostname()
	if host != c.RPID && !strings.HasSuffix(host, "."+c.RPID) {
		return nil, ErrProof
	}
	if len(c.PublicKey) > 4096 || len(c.Signature) != ed25519.SignatureSize {
		return nil, ErrProof
	}
	byID := map[string]netproto.DMDevice{}
	trusted := map[string]ed25519.PublicKey{}
	roots := 0
	for _, d := range c.Chain {
		if d.DeviceID == "" {
			return nil, ErrProof
		}
		if _, exists := byID[d.DeviceID]; exists {
			return nil, ErrProof
		}
		byID[d.DeviceID] = d
		if d.FirstDevice {
			roots++
			key, err := auth.LoadPublicKey(d.Ed25519Public)
			if err != nil || !bytes.Equal(key, pinned) {
				return nil, ErrProof
			}
			trusted[d.DeviceID] = key
		}
	}
	if roots != 1 {
		return nil, ErrProof
	}
	for changed := true; changed; {
		changed = false
		for id, d := range byID {
			if trusted[id] != nil || trusted[d.ApprovedBy] == nil {
				continue
			}
			key, err := auth.LoadPublicKey(d.Ed25519Public)
			if err != nil || !ed25519.Verify(trusted[d.ApprovedBy], netproto.DeviceApprovalTranscript(principal, d.ApprovedBy, d.Ed25519Public, d.X25519Public), d.ApprovalSignature) {
				continue
			}
			trusted[id] = key
			changed = true
		}
	}
	issuer := trusted[c.Issuer]
	if issuer == nil || !ed25519.Verify(issuer, c.Transcript(), c.Signature) {
		return nil, ErrProof
	}
	return issuer, nil
}

// Verify checks certificate lineage, RP/origin, user verification, credential
// identity, and the assertion challenge committing both target device keys.
func Verify(principal string, pinned ed25519.PublicKey, edPublic, xPublic string, raw []byte) error {
	if len(raw) == 0 || len(raw) > netproto.MaxPasskeyProof {
		return ErrProof
	}
	var proof netproto.PasskeyLoginProof
	if json.Unmarshal(raw, &proof) != nil || len(proof.Nonce) != 32 {
		return ErrProof
	}
	c := proof.Certificate
	if _, err := CertificateIssuer(c, principal, pinned); err != nil {
		return err
	}
	if _, err := auth.LoadPublicKey(edPublic); err != nil {
		return ErrProof
	}
	x, err := base64.StdEncoding.DecodeString(xPublic)
	if err != nil || len(x) != 32 || bytes.Equal(x, make([]byte, 32)) {
		return ErrProof
	}
	var assertion struct {
		Type     string `json:"type"`
		RawID    string `json:"rawId"`
		Response struct {
			ClientData string `json:"clientDataJSON"`
			AuthData   string `json:"authenticatorData"`
			Signature  string `json:"signature"`
		} `json:"response"`
	}
	if len(proof.Assertion) > netproto.MaxWebAuthnPayload || json.Unmarshal(proof.Assertion, &assertion) != nil || assertion.Type != "public-key" || assertion.RawID != c.CredentialID {
		return ErrProof
	}
	decode := base64.RawURLEncoding.DecodeString
	clientData, err := decode(assertion.Response.ClientData)
	if err != nil {
		return ErrProof
	}
	authData, err := decode(assertion.Response.AuthData)
	if err != nil || len(authData) < 37 || len(authData) > 2048 {
		return ErrProof
	}
	signature, err := decode(assertion.Response.Signature)
	if err != nil {
		return ErrProof
	}
	var data struct {
		Type        string `json:"type"`
		Challenge   string `json:"challenge"`
		Origin      string `json:"origin"`
		CrossOrigin bool   `json:"crossOrigin"`
	}
	if json.Unmarshal(clientData, &data) != nil || data.Type != "webauthn.get" || data.Origin != c.Origin || data.CrossOrigin {
		return ErrProof
	}
	if data.Challenge != base64.RawURLEncoding.EncodeToString(netproto.PasskeyDeviceChallenge(proof.Nonce, edPublic, xPublic)) {
		return ErrProof
	}
	rpHash := sha256.Sum256([]byte(c.RPID))
	if !bytes.Equal(authData[:32], rpHash[:]) || authData[32]&5 != 5 {
		return ErrProof
	}
	key, err := x509.ParsePKIXPublicKey(c.PublicKey)
	if err != nil {
		return ErrProof
	}
	clientHash := sha256.Sum256(clientData)
	signed := append(append([]byte{}, authData...), clientHash[:]...)
	algorithms := map[int64]x509.SignatureAlgorithm{
		-7: x509.ECDSAWithSHA256, -35: x509.ECDSAWithSHA384, -36: x509.ECDSAWithSHA512,
		-257: x509.SHA256WithRSA, -258: x509.SHA384WithRSA, -259: x509.SHA512WithRSA,
		-37: x509.SHA256WithRSAPSS, -38: x509.SHA384WithRSAPSS, -39: x509.SHA512WithRSAPSS,
		-8: x509.PureEd25519,
	}
	algorithm, ok := algorithms[c.Algorithm]
	if !ok {
		return ErrProof
	}
	verifier := x509.Certificate{PublicKey: key}
	if verifier.CheckSignature(algorithm, signed, signature) != nil {
		return ErrProof
	}
	return nil
}
