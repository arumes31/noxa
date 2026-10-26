// Package ratchet wraps the pinned vodozemac Olm implementation shared with the
// browser. It performs no network IO or persistent writes. Callers must verify
// device-key bindings and commit returned snapshots atomically with their outbox
// or authenticated receive record before using them. See README.md.
package ratchet

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

const Version = "noxa-olm-v1"
const MaxInput = 1 << 20
const MaxOutput = 2 << 20
const MaxPlaintext = 65536
const MaxPreKeys = 100
const operationTimeout = 5 * time.Second

const (
	NewAccount   = "new_account"
	PreKeys      = "prekeys"
	RetirePreKey = "retire_prekey"
	Outbound     = "outbound"
	Inbound      = "inbound"
	Encrypt      = "encrypt"
	Decrypt      = "decrypt"
)

//go:embed core.wasm
var core []byte

//go:embed core.sha256
var coreHash string

// Binding is authenticated inside each encrypted payload as well as carried in
// its envelope. Receive callers supply independently expected values; copying
// untrusted envelope fields into Binding does not authenticate routing context.
type Binding struct {
	Server          string `json:"server"`
	Conversation    string `json:"conversation"`
	Sender          string `json:"sender"`
	SenderDevice    string `json:"sender_device"`
	Recipient       string `json:"recipient"`
	RecipientDevice string `json:"recipient_device"`
	MessageID       string `json:"message_id"`
	Purpose         string `json:"purpose"`
}

type Envelope struct {
	Version   string          `json:"version"`
	SessionID string          `json:"session_id"`
	Binding   Binding         `json:"binding"`
	Message   json.RawMessage `json:"message"`
}

type PublicPreKey struct {
	ID  string `json:"id"`
	Key string `json:"key"`
}

// Bundle is unsigned cryptographic material. It is NOT a trusted device record.
// Authenticate every field under the existing noXa device signing identity
// before publishing/using it; never replace a pinned contact identity with it.
type Bundle struct {
	Identity    string         `json:"identity"`
	Signing     string         `json:"signing"`
	OneTimeKeys []PublicPreKey `json:"one_time_keys"`
}

type Request struct {
	Op           string    `json:"op"`
	Key          []byte    `json:"key"` // 32-byte local storage key; never send to peers.
	Account      string    `json:"account,omitempty"`
	Session      string    `json:"session,omitempty"`
	PeerIdentity string    `json:"peer_identity,omitempty"`
	OneTimeKey   string    `json:"one_time_key,omitempty"`
	Count        int       `json:"count,omitempty"`
	Binding      *Binding  `json:"binding,omitempty"`
	Plaintext    string    `json:"plaintext,omitempty"`
	Envelope     *Envelope `json:"envelope,omitempty"`
}

type Response struct {
	Account   string    `json:"account,omitempty"`
	Session   string    `json:"session,omitempty"`
	SessionID string    `json:"session_id,omitempty"`
	Bundle    *Bundle   `json:"bundle,omitempty"`
	Envelope  *Envelope `json:"envelope,omitempty"`
	Plaintext string    `json:"plaintext,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// Engine caches compiled code, never accounts or sessions. Apply calls use
// separate bounded instances, so concurrent operations cannot share ratchets.
// Persistent state concurrency belongs to the caller's transaction/CAS layer.
type Engine struct {
	runtime  wazero.Runtime
	compiled wazero.CompiledModule
	slots    chan struct{}
	entropy  func([]byte) (int, error)
}

func New(ctx context.Context) (*Engine, error) {
	digest := sha256.Sum256(core)
	if hex.EncodeToString(digest[:]) != strings.TrimSpace(coreHash) {
		return nil, errors.New("ratchet core hash mismatch")
	}
	r := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().WithMemoryLimitPages(1024).WithCloseOnContextDone(true))
	e := &Engine{runtime: r, entropy: rand.Read, slots: make(chan struct{}, 2)}
	_, err := r.NewHostModuleBuilder("noxa").NewFunctionBuilder().WithFunc(func(_ context.Context, m api.Module, ptr, n uint32) uint32 {
		if n > 65536 {
			return 1
		}
		b, ok := m.Memory().Read(ptr, n)
		if !ok {
			return 1
		}
		count, err := e.entropy(b)
		if err != nil || count != len(b) {
			clear(b)
			return 1
		}
		return 0
	}).Export("random_fill").Instantiate(ctx)
	if err == nil {
		e.compiled, err = r.CompileModule(ctx, core)
	}
	if err != nil {
		_ = r.Close(ctx)
		return nil, errors.New("ratchet core initialization failed")
	}
	return e, nil
}
func (e *Engine) Close(ctx context.Context) error { return e.runtime.Close(ctx) }

// Apply returns a candidate next state only after the operation succeeds. It
// never persists that state. An error returns an empty Response. A successful
// send must commit Session + Envelope atomically, then retry the same Envelope.
func (e *Engine) Apply(ctx context.Context, r Request) (Response, error) {
	select {
	case e.slots <- struct{}{}:
		defer func() { <-e.slots }()
	default:
		return Response{}, errors.New("ratchet core busy")
	}
	if len(r.Key) != 32 || len(r.Plaintext) > MaxPlaintext {
		return Response{}, errors.New("invalid ratchet request")
	}
	encoded, err := json.Marshal(r)
	if err != nil {
		return Response{}, errors.New("invalid ratchet request")
	}
	defer clear(encoded)
	if len(encoded) > MaxInput {
		return Response{}, errors.New("ratchet request too large")
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	m, err := e.runtime.InstantiateModule(ctx, e.compiled, wazero.NewModuleConfig().WithName(""))
	if err != nil {
		return Response{}, errors.New("ratchet instance failed")
	}
	defer func() {
		if b, ok := m.Memory().Read(0, m.Memory().Size()); ok {
			clear(b)
		}
		_ = m.Close(context.Background())
	}()
	p, err := m.ExportedFunction("alloc").Call(ctx, uint64(len(encoded)))
	if err != nil || len(p) != 1 || p[0] == 0 {
		return Response{}, errors.New("ratchet allocation failed")
	}
	if !m.Memory().Write(uint32(p[0]), encoded) {
		return Response{}, errors.New("ratchet memory bounds")
	}
	result, err := m.ExportedFunction("run").Call(ctx, p[0], uint64(len(encoded)))
	if err != nil || len(result) != 1 || result[0] == 0 {
		return Response{}, errors.New("ratchet operation failed")
	}
	ptr, n := uint32(result[0]), uint32(result[0]>>32)
	if n > MaxOutput {
		return Response{}, errors.New("ratchet response too large")
	}
	data, ok := m.Memory().Read(ptr, n)
	if !ok {
		return Response{}, errors.New("ratchet memory bounds")
	}
	var out Response
	if json.Unmarshal(data, &out) != nil {
		return Response{}, errors.New("invalid ratchet response")
	}
	if out.Error != "" {
		return Response{}, fmt.Errorf("ratchet: %s", out.Error)
	}
	return out, nil
}
