package webpush

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func verifyToken(token string, pub *ecdsa.PublicKey) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		return false
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	return ecdsa.Verify(pub, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:]))
}

// decrypt is the user agent side of RFC 8291, written from the RFC.
func decrypt(t *testing.T, uaPrivate *ecdh.PrivateKey, auth, body []byte) []byte {
	t.Helper()
	salt := body[:16]
	rs := binary.BigEndian.Uint32(body[16:20])
	if rs != recordSize {
		t.Fatalf("record size %d", rs)
	}
	idlen := int(body[20])
	asPublicBytes := body[21 : 21+idlen]
	ciphertext := body[21+idlen:]
	asPublic, err := ecdh.P256().NewPublicKey(asPublicBytes)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := uaPrivate.ECDH(asPublic)
	if err != nil {
		t.Fatal(err)
	}
	info := append(append([]byte("WebPush: info\x00"), uaPrivate.PublicKey().Bytes()...), asPublicBytes...)
	ikm, _ := hkdf.Key(sha256.New, shared, auth, string(info), 32)
	cek, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	end := bytes.LastIndexByte(plain, 0x02)
	if end < 0 || len(bytes.Trim(plain[end+1:], "\x00")) != 0 {
		t.Fatalf("missing final-record delimiter")
	}
	return plain[:end]
}

func testSubscription(t *testing.T, endpoint string) (Subscription, *ecdh.PrivateKey) {
	t.Helper()
	uaPrivate, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	return Subscription{Endpoint: endpoint, P256dh: uaPrivate.PublicKey().Bytes(), Auth: auth}, uaPrivate
}

func TestEncryptRoundTrip(t *testing.T) {
	sub, uaPrivate := testSubscription(t, "https://push.example/x")
	payload := []byte(`{"kind":"dm","title":"New message"}`)
	body, err := Encrypt(sub, payload)
	if err != nil {
		t.Fatal(err)
	}
	if got := decrypt(t, uaPrivate, sub.Auth, body); !bytes.Equal(got, payload) {
		t.Fatalf("got %q", got)
	}
	// A fresh key and salt per message.
	again, _ := Encrypt(sub, payload)
	if bytes.Equal(body[:16], again[:16]) || bytes.Equal(body, again) {
		t.Fatal("salt or ciphertext reused")
	}
}

func TestEncryptRejectsOversizeAndBadKeys(t *testing.T) {
	sub, _ := testSubscription(t, "https://push.example/x")
	if _, err := Encrypt(sub, make([]byte, MaxPayload+1)); err == nil {
		t.Fatal("oversize payload accepted")
	}
	if _, err := Encrypt(sub, make([]byte, MaxPayload)); err != nil {
		t.Fatalf("max payload rejected: %v", err)
	}
	bad := sub
	bad.P256dh = []byte{4, 1, 2}
	if _, err := Encrypt(bad, []byte("x")); err == nil {
		t.Fatal("bad key accepted")
	}
	if err := (Subscription{Endpoint: "http://push.example/x", P256dh: sub.P256dh, Auth: sub.Auth}).Validate(); err == nil {
		t.Fatal("plain http endpoint accepted")
	}
	if err := sub.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestVAPIDKeyPersistsAndSends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys", "vapid.pem")
	v1, err := LoadOrCreateVAPID(path, "mailto:ops@example.com")
	if err != nil {
		t.Fatal(err)
	}
	v2, err := LoadOrCreateVAPID(path, "mailto:ops@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if v1.PublicKey() != v2.PublicKey() || len(v1.PublicKey()) != 87 {
		t.Fatalf("key not persisted: %q %q", v1.PublicKey(), v2.PublicKey())
	}

	var gotHeaders http.Header
	var gotBody []byte
	status := http.StatusCreated
	push := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(status)
	}))
	defer push.Close()

	sub, uaPrivate := testSubscription(t, push.URL+"/send/abc")
	sender := &Sender{VAPID: v1, Client: push.Client()}
	msg := Message{Payload: []byte("hello"), TTL: time.Minute, Urgency: UrgencyHigh, Topic: "dm-1"}
	if err := sender.Send(context.Background(), sub, msg); err != nil {
		t.Fatal(err)
	}
	if gotHeaders.Get("Content-Encoding") != "aes128gcm" || gotHeaders.Get("TTL") != "60" ||
		gotHeaders.Get("Urgency") != "high" || gotHeaders.Get("Topic") != "dm-1" {
		t.Fatalf("headers %v", gotHeaders)
	}
	if got := decrypt(t, uaPrivate, sub.Auth, gotBody); string(got) != "hello" {
		t.Fatalf("body %q", got)
	}
	auth := gotHeaders.Get("Authorization")
	if !strings.HasPrefix(auth, "vapid t=") || !strings.HasSuffix(auth, ", k="+v1.PublicKey()) {
		t.Fatalf("authorization %q", auth)
	}
	token := strings.TrimSuffix(strings.TrimPrefix(auth, "vapid t="), ", k="+v1.PublicKey())
	if !verifyToken(token, &v1.key.PublicKey) {
		t.Fatal("VAPID signature does not verify")
	}
	claimsJSON, _ := base64.RawURLEncoding.DecodeString(strings.Split(token, ".")[1])
	var claims struct {
		Aud string `json:"aud"`
		Sub string `json:"sub"`
		Exp int64  `json:"exp"`
	}
	_ = json.Unmarshal(claimsJSON, &claims)
	if claims.Aud != push.URL || claims.Sub != "mailto:ops@example.com" || claims.Exp <= time.Now().Unix() {
		t.Fatalf("claims %+v", claims)
	}

	status = http.StatusGone
	if err := sender.Send(context.Background(), sub, msg); !errors.Is(err, ErrGone) {
		t.Fatalf("410 gave %v", err)
	}
	status = http.StatusTooManyRequests
	if err := sender.Send(context.Background(), sub, msg); err == nil || errors.Is(err, ErrGone) {
		t.Fatalf("429 gave %v", err)
	}
}

func TestVAPIDRequiresSubject(t *testing.T) {
	if _, err := LoadOrCreateVAPID(filepath.Join(t.TempDir(), "v.pem"), " "); err == nil {
		t.Fatal("empty subject accepted")
	}
}
