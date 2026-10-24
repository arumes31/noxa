// Package webpush sends Web Push messages: RFC 8291 message encryption
// (aes128gcm, RFC 8188) with VAPID sender authentication (RFC 8292).
package webpush

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// recordSize is the RFC 8188 record size; one record carries the payload.
const recordSize = 4096

// p256PublicLen is the length of an uncompressed P-256 public key (keyid).
const p256PublicLen = 65

// MaxPayload is the largest plaintext that fits one record after padding
// delimiter and AEAD tag (push services accept 4096-byte bodies).
const MaxPayload = recordSize - 86 - 16 - 1

// ErrGone reports a subscription the push service no longer knows.
var ErrGone = errors.New("webpush: subscription expired or unsubscribed")

// Subscription is a browser PushSubscription.
type Subscription struct {
	Endpoint string
	P256dh   []byte // the user agent's uncompressed P-256 public key
	Auth     []byte // 16-byte authentication secret
}

// Validate checks the endpoint and key shapes before storing a subscription.
func (s Subscription) Validate() error {
	u, err := url.Parse(s.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || len(s.Endpoint) > 1024 {
		return errors.New("webpush: endpoint must be an https URL")
	}
	if _, err := ecdh.P256().NewPublicKey(s.P256dh); err != nil {
		return errors.New("webpush: invalid p256dh key")
	}
	if len(s.Auth) != 16 {
		return errors.New("webpush: auth secret must be 16 bytes")
	}
	return nil
}

// Encrypt encrypts plaintext for the subscription (RFC 8291) and returns
// the aes128gcm body.
func Encrypt(sub Subscription, plaintext []byte) ([]byte, error) {
	return encrypt(sub, plaintext, rand.Reader)
}

func encrypt(sub Subscription, plaintext []byte, random io.Reader) ([]byte, error) {
	if len(plaintext) > MaxPayload {
		return nil, fmt.Errorf("webpush: payload of %d bytes exceeds %d", len(plaintext), MaxPayload)
	}
	uaPublic, err := ecdh.P256().NewPublicKey(sub.P256dh)
	if err != nil {
		return nil, errors.New("webpush: invalid p256dh key")
	}
	if len(sub.Auth) != 16 {
		return nil, errors.New("webpush: auth secret must be 16 bytes")
	}
	asPrivate, err := ecdh.P256().GenerateKey(random)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err := io.ReadFull(random, salt); err != nil {
		return nil, err
	}
	shared, err := asPrivate.ECDH(uaPublic)
	if err != nil {
		return nil, err
	}
	asPublicBytes := asPrivate.PublicKey().Bytes()
	if len(asPublicBytes) != p256PublicLen {
		return nil, errors.New("webpush: unexpected P-256 public key length")
	}
	keyInfo := append(append([]byte("WebPush: info\x00"), sub.P256dh...), asPublicBytes...)
	ikm, err := hkdf.Key(sha256.New, shared, sub.Auth, string(keyInfo), 32)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	// A single, final record: plaintext followed by the 0x02 delimiter.
	record := append(append([]byte{}, plaintext...), 0x02)
	header := make([]byte, 0, 86)
	header = append(header, salt...)
	header = binary.BigEndian.AppendUint32(header, recordSize)
	header = append(header, p256PublicLen)
	header = append(header, asPublicBytes...)
	return gcm.Seal(header, nonce, record, nil), nil
}

// VAPID is the application server's signing key (RFC 8292).
type VAPID struct {
	key     *ecdsa.PrivateKey
	Subject string // mailto: or https: contact for push services
}

// PublicKey returns the uncompressed public key, base64url without
// padding, as PushManager.subscribe expects for applicationServerKey.
func (v *VAPID) PublicKey() string {
	pub, err := v.key.PublicKey.ECDH()
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(pub.Bytes())
}

// LoadOrCreateVAPID reads a PEM EC private key or creates one (0600).
func LoadOrCreateVAPID(path, subject string) (*VAPID, error) {
	if strings.TrimSpace(subject) == "" {
		return nil, errors.New("webpush: a VAPID subject (mailto: or https:) is required")
	}
	// #nosec G304 -- path is the administrator-configured web.push_vapid_key_file.
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		key, genErr := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if genErr != nil {
			return nil, genErr
		}
		der, marshalErr := x509.MarshalECPrivateKey(key)
		if marshalErr != nil {
			return nil, marshalErr
		}
		if mkErr := os.MkdirAll(filepath.Dir(path), 0o700); mkErr != nil {
			return nil, mkErr
		}
		out := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
		if writeErr := os.WriteFile(path, out, 0o600); writeErr != nil {
			return nil, writeErr
		}
		return &VAPID{key: key, Subject: subject}, nil
	}
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("webpush: %s is not PEM", path)
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("webpush: %s: %w", path, err)
	}
	if key.Curve != elliptic.P256() {
		return nil, errors.New("webpush: VAPID key must be P-256")
	}
	return &VAPID{key: key, Subject: subject}, nil
}

// token returns the ES256 JWT for the endpoint's origin.
func (v *VAPID) token(endpoint string, now time.Time) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, err := json.Marshal(map[string]any{
		"aud": u.Scheme + "://" + u.Host,
		"exp": now.Add(12 * time.Hour).Unix(),
		"sub": v.Subject,
	})
	if err != nil {
		return "", err
	}
	signingInput := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, v.key, digest[:])
	if err != nil {
		return "", err
	}
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

// Urgency is the RFC 8030 Urgency header.
type Urgency string

const (
	UrgencyNormal Urgency = "normal"
	UrgencyHigh   Urgency = "high"
)

// Message is one push delivery.
type Message struct {
	Payload []byte
	TTL     time.Duration
	Urgency Urgency
	// Topic lets a newer message replace a pending one (base64url, ≤32 chars).
	Topic string
}

// Sender delivers messages to push services.
type Sender struct {
	VAPID  *VAPID
	Client *http.Client
	Now    func() time.Time
}

// PublicKey returns the VAPID public key subscriptions must use.
func (s *Sender) PublicKey() string { return s.VAPID.PublicKey() }

// Send encrypts and posts one message. It returns ErrGone when the push
// service reports the subscription as expired (404 or 410).
func (s *Sender) Send(ctx context.Context, sub Subscription, msg Message) error {
	body, err := Encrypt(sub, msg.Payload)
	if err != nil {
		return err
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	jwt, err := s.VAPID.token(sub.Endpoint, now())
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	ttl := int(msg.TTL / time.Second)
	if ttl < 0 {
		ttl = 0
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("TTL", strconv.Itoa(ttl))
	req.Header.Set("Authorization", "vapid t="+jwt+", k="+s.VAPID.PublicKey())
	if msg.Urgency != "" {
		req.Header.Set("Urgency", string(msg.Urgency))
	}
	if msg.Topic != "" {
		req.Header.Set("Topic", msg.Topic)
	}
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return ErrGone
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	default:
		return fmt.Errorf("webpush: push service answered %d", resp.StatusCode)
	}
}
