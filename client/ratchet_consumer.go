// ratchet_consumer.go — session-owned noxa-olm-v1 consumer for the desktop.
//
// Native mirror of mobile/src/core/ratchet-consumer.js over the shared Go
// durable store (bbolt), WASM core, admission manifests and signed directory.
// One instance belongs to exactly one authenticated connection, account fence
// and protected store namespace. No capability, trust or storage failure ever
// selects legacy NaCl sending or decoding. The protected archive is
// authoritative; projectors are views, never receipt proof.
//
// Identities are kept distinct:
//
//	protocol/provenance   RatchetRecord.Protocol / Provenance
//	semantic reference    RatchetRecord.Key = ratchetSemanticKey(sender, logicalID)
//	transport identifiers RatchetRecord.Transport {message_id, attempt_id, delivery_id}
//
// Edit/delete references always name the ORIGINAL sender's logical ID.
package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"noxa/internal/e2ee/admission"
	"noxa/internal/e2ee/durable"
	"noxa/internal/e2ee/olmdir"
	"noxa/internal/e2ee/ratchet"
	"noxa/internal/netproto"
)

const (
	ratchetProtocol = "noxa-olm-v1"
	// ratchetDeadlineMargin: seconds before a deadline at which an unstarted
	// attempt is aborted (and later resealed), not begun.
	ratchetDeadlineMargin = 30
	// ratchetLocalRetry bounds restarts after a local CAS loss.
	ratchetLocalRetry = 3
	ratchetMaxDirect  = 20
	ratchetMaxGroup   = 160
	ratchetPreKeys    = 20
	ratchetRefill     = 10
	ratchetRetireWin  = 64
)

// RatchetTransport carries server transport identifiers. They are opaque
// strings (UUIDs) and never coerced into legacy numeric message IDs.
type RatchetTransport struct {
	Message  string `json:"message_id,omitempty"`
	Attempt  string `json:"attempt_id,omitempty"`
	Delivery string `json:"delivery_id,omitempty"`
}

// RatchetRecord is one authoritative archive entry (own send or receipt).
type RatchetRecord struct {
	Protocol     string           `json:"protocol"`
	Provenance   string           `json:"provenance"`
	Sender       string           `json:"sender"`
	SenderDevice string           `json:"sender_device"`
	LogicalID    string           `json:"logical_id"`
	Key          string           `json:"key"`
	Scope        string           `json:"scope"`
	Peer         string           `json:"peer,omitempty"`
	Conversation string           `json:"conversation,omitempty"`
	Purpose      string           `json:"purpose"`
	Reference    string           `json:"reference,omitempty"`
	ReferenceKey string           `json:"reference_key,omitempty"`
	Body         json.RawMessage  `json:"body"`
	SentAt       float64          `json:"sent_at"`
	Self         bool             `json:"self"`
	Copied       bool             `json:"copied"`
	Transport    RatchetTransport `json:"transport"`
	Status       string           `json:"status,omitempty"`
	Reason       string           `json:"reason,omitempty"`
}

// RatchetOutcome is the result of one durable operation. Reused means the
// logical ID already had a committed operation: Record is ITS original
// content, whatever the caller passed now.
type RatchetOutcome struct {
	Record  RatchetRecord      `json:"record"`
	Receipt *admission.Receipt `json:"receipt,omitempty"`
	Reused  bool               `json:"reused"`
}

// RatchetQuarantine is a truthful placeholder for a copy that could not be
// opened. It is never ACKed and never decoded as legacy.
type RatchetQuarantine struct {
	ID           string  `json:"id"`
	Sender       string  `json:"sender"`
	Code         string  `json:"code"`
	Scope        string  `json:"scope"`
	Peer         string  `json:"peer,omitempty"`
	Conversation string  `json:"conversation,omitempty"`
	SeenAt       float64 `json:"seen_at"`
}

// ratchetIntent is one logical operation. Purpose is chat, edit or delete;
// edits/deletes reference the original sender's logical ID.
type ratchetIntent struct {
	LogicalID    string
	Scope        string
	Peer         string
	Conversation string
	Purpose      string
	Reference    string
	Body         json.RawMessage
}

// ratchetError carries a stable string code. Committed is set once a durable
// operation exists, so callers show its ORIGINAL archive.
type ratchetError struct {
	Code        string
	Committed   *RatchetOutcome
	AfterCommit bool
	RetryAfter  time.Duration
	Err         error
}

func (e *ratchetError) Error() string { return "ratchet: " + e.Code }
func (e *ratchetError) Unwrap() error { return e.Err }

func rfail(code string) *ratchetError { return &ratchetError{Code: code} }

// ratchetCode maps consumer, store and transport errors to their string code;
// "" means uncoded (transport loss, unexpected local error).
func ratchetCode(err error) string {
	var r *ratchetError
	switch {
	case err == nil:
		return ""
	case errors.As(err, &r):
		return r.Code
	case errors.Is(err, durable.ErrConflict):
		return "conflict"
	case errors.Is(err, durable.ErrFenced):
		return "fenced"
	case errors.Is(err, durable.ErrRevoked):
		return "revoked"
	case errors.Is(err, durable.ErrInvalid):
		return "invalid"
	case errors.Is(err, durable.ErrExhausted):
		return "exhausted"
	case errors.Is(err, durable.ErrCorrupt):
		return "corrupt"
	case errors.Is(err, durable.ErrUnavailable):
		return "unavailable"
	case errors.Is(err, durable.ErrInUse):
		return "in_use"
	}
	return ""
}

func withCommitted(err error, out RatchetOutcome) error {
	var r *ratchetError
	if errors.As(err, &r) {
		if r.Committed == nil {
			r.Committed = &out
		}
		return r
	}
	return &ratchetError{Code: ratchetCode(err), Committed: &out, Err: err}
}

// ratchetDeterministicReceive lists receive failures a retry cannot fix; only
// these are quarantined. Everything else is retried on the next drain.
var ratchetDeterministicReceive = map[string]bool{"invalid": true, "unauthenticated": true, "invalid_evidence": true, "lost_key": true, "fenced_copy": true,
	"untrusted_device": true, "revoked": true, "account_changed": true, "identity_changed": true, "legacy_session": true, "session_mismatch": true, "digest_mismatch": true}
var ratchetTransientReceive = map[string]bool{"core_unavailable": true, "fenced": true, "rate-limit": true, "unavailable": true, "conflict": true, "aborted": true, "local_conflict": true}

// ratchetDeterministic runs one verification/crypto stage. Only known string
// codes from the allow-lists pass through; anything else (uncoded core or
// verification rejections, malformed data) becomes the stage's code.
func ratchetDeterministic[T any](code string, work func() (T, error)) (T, error) {
	v, err := work()
	if err == nil {
		return v, nil
	}
	if c := ratchetCode(err); ratchetDeterministicReceive[c] || ratchetTransientReceive[c] {
		return v, err
	}
	return v, &ratchetError{Code: code, Err: err}
}

// ratchetReceiveDisposition: "retry" next drain, "skip" (another consumer of
// this namespace committed the SAME delivery) or "quarantine".
func ratchetReceiveDisposition(code string, committedElsewhere bool) string {
	if !ratchetDeterministicReceive[code] {
		return "retry"
	}
	if committedElsewhere && (code == "lost_key" || code == "unauthenticated") {
		return "skip"
	}
	return "quarantine"
}

func ratchetValidDeliveryID(id string) bool {
	if id == "" || len(id) > 512 || !utf8.ValidString(id) {
		return false
	}
	return strings.IndexFunc(id, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) < 0
}

func ratchetJSON(v any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return bytes.TrimRight(b.Bytes(), "\n")
}

// ratchetSemanticKey is the collision-free UI/semantic key for one sender's
// logical message ID (same shape as the browser semanticKey).
func ratchetSemanticKey(sender, logicalID string) string {
	return string(ratchetJSON([]string{ratchetProtocol, sender, logicalID}))
}

// ratchetCheckFanout enforces direct <= 20 and group <= 160 complete targets.
func ratchetCheckFanout(scope string, targets []admission.Target) error {
	limit := ratchetMaxDirect
	if scope == "group" {
		limit = ratchetMaxGroup
	}
	if len(targets) == 0 || len(targets) > limit {
		return rfail("fanout_limit")
	}
	return nil
}

// ratchetLatestAttempts keeps the newest attempt per logical ID ordered by
// (epoch, sequence): a reseal after abort/reject supersedes it.
func ratchetLatestAttempts(ids []string, logical func(string) string) []string {
	order := func(id string) (int64, int64) {
		e, s, _ := strings.Cut(id, ":")
		ei, _ := strconv.ParseInt(e, 10, 64)
		si, _ := strconv.ParseInt(s, 10, 64)
		return ei, si
	}
	sorted := append([]string(nil), ids...)
	sort.SliceStable(sorted, func(i, j int) bool {
		ei, si := order(sorted[i])
		ej, sj := order(sorted[j])
		return ei < ej || ei == ej && si < sj
	})
	latest := map[string]string{}
	var keys []string
	for _, id := range sorted {
		l := logical(id)
		if _, ok := latest[l]; !ok {
			keys = append(keys, l)
		}
		latest[l] = id
	}
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, latest[k])
	}
	return out
}

// ratchetInitialOneTimeKey reads recipient one-time key from an Olm v1 prekey
// message (version 3, protobuf field 1). It only selects a local obligation;
// the core authenticates the message.
func ratchetInitialOneTimeKey(body string) (string, error) {
	if body == "" || len(body) > 8192 {
		return "", rfail("invalid")
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(body, "="))
	if err != nil || len(raw) < 35 || raw[0] != 3 || raw[1] != 0x0a || raw[2] != 32 {
		return "", rfail("invalid")
	}
	return base64.RawStdEncoding.EncodeToString(raw[3:35]), nil
}

// Test seams (nil in production): ratchetBeforeCommit fails a local attempt
// commit; ratchetCoreFor wraps the core of one namespace (operation counts).
var (
	ratchetBeforeCommit func(durable.Namespace) error
	ratchetCoreFor      func(durable.Namespace, ratchetCore) ratchetCore
)

// ratchetCore is the WASM core boundary (ratchet.Engine).
type ratchetCore interface {
	Apply(ctx context.Context, r ratchet.Request) (ratchet.Response, error)
}

type ratchetDeps struct {
	Store         *durable.Store
	Core          ratchetCore
	Namespace     durable.Namespace
	Admission     func(context.Context, netproto.OlmAdmissionRequest) (json.RawMessage, error)
	Directory     func(context.Context, netproto.OlmDirectoryRequest) (netproto.OlmDirectoryResult, error)
	DeviceKey     func(ctx context.Context, principal, device string) (ed25519.PublicKey, error)
	SignBundle    func(olmdir.Bundle) (olmdir.Bundle, error)
	AssertCurrent func() error
	AttemptTTL    int64
	RefreshEvery  time.Duration
	Now           func() time.Time
}

type ratchetReadiness struct {
	identity  string
	epoch     int64
	expiresAt int64
}

type ratchetPin struct {
	Epoch    int64  `json:"epoch"`
	Identity string `json:"identity"`
	Signing  string `json:"signing"`
}

// ratchetTrustValue is the protected trust record value. Noxa is the pinned
// noXa device key (base64) or nil after an explicitly confirmed root change.
type ratchetTrustValue struct {
	Principal string      `json:"principal"`
	Device    string      `json:"device"`
	Noxa      *string     `json:"noxa"`
	Pin       *ratchetPin `json:"pin,omitempty"`
}

type ratchetTrusted struct {
	id  string
	key ed25519.PublicKey
	pin *ratchetPin
}

type ratchetKeptClaim struct {
	id, key string
	until   int64
}

type ratchetLogical struct {
	AttemptID string
	State     string
}

type ratchetConsumer struct {
	d      ratchetDeps
	ctx    context.Context
	cancel context.CancelFunc
	closed atomic.Bool

	// serial runs one operation at a time; every field below is guarded by it.
	serial      sync.Mutex
	ready       *ratchetReadiness
	readyAt     time.Time
	sticky      error
	identity    string
	claims      map[string]ratchetKeptClaim
	skewed      map[string]bool
	projectors  []func(RatchetRecord, bool)
	quarantined map[string]RatchetQuarantine
	qorder      []string
	sessions    map[durable.SessionContext]string
	retired     map[string]bool
}

func newRatchetConsumer(d ratchetDeps) (*ratchetConsumer, error) {
	if d.AttemptTTL == 0 {
		d.AttemptTTL = 300
	}
	// attemptTTL must exceed the abort margin and leave a fresh 1 h claim able
	// to cover a resend (browser mirror: invalid_config).
	if d.AttemptTTL < ratchetDeadlineMargin+5 || d.AttemptTTL > 3000 {
		return nil, rfail("invalid_config")
	}
	if d.RefreshEvery <= 0 {
		d.RefreshEvery = time.Hour
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Store == nil || d.Core == nil || d.Admission == nil || d.Directory == nil || d.DeviceKey == nil || d.SignBundle == nil || d.AssertCurrent == nil {
		return nil, rfail("invalid_config")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &ratchetConsumer{d: d, ctx: ctx, cancel: cancel, claims: map[string]ratchetKeptClaim{}, skewed: map[string]bool{},
		quarantined: map[string]RatchetQuarantine{}, retired: map[string]bool{}}, nil
}

func (c *ratchetConsumer) now() int64 { return c.d.Now().Unix() }

// project registers fn, called after each durable commit (isNew from the
// durable receive outcome). Views are never receipt proof.
func (c *ratchetConsumer) project(fn func(RatchetRecord, bool)) {
	c.serial.Lock()
	defer c.serial.Unlock()
	c.projectors = append(c.projectors, fn)
}

// close fences the consumer, cancels its network waits and awaits the
// operation in flight before returning (store release happens afterwards).
func (c *ratchetConsumer) close() {
	c.closed.Store(true)
	c.cancel()
	c.serial.Lock()
	c.serial.Unlock() //nolint:staticcheck // awaiting the in-flight operation
}

func (c *ratchetConsumer) check() error {
	if c.closed.Load() {
		return rfail("fenced")
	}
	if err := c.d.AssertCurrent(); err != nil {
		return &ratchetError{Code: "fenced", Err: err}
	}
	return nil
}

func (c *ratchetConsumer) emit(rec RatchetRecord, isNew bool) error {
	if err := c.check(); err != nil {
		return err
	}
	for _, fn := range c.projectors {
		func() {
			defer func() { _ = recover() }() // views are not receipt proof
			fn(rec, isNew)
		}()
		if err := c.check(); err != nil {
			return err
		}
	}
	return nil
}

func (c *ratchetConsumer) sleep(d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-c.ctx.Done():
		return rfail("fenced")
	}
}

// net retries a server rate limit with bounded backoff (the server checks
// limits before any handler action); afterwards it is retryable with a hint.
func (c *ratchetConsumer) net(fn func() error) error {
	for i := 0; ; i++ {
		if err := c.check(); err != nil {
			return err
		}
		err := fn()
		if err == nil {
			return c.check()
		}
		if ratchetCode(err) != "rate-limit" {
			return err
		}
		if i >= 7 {
			var r *ratchetError
			if errors.As(err, &r) {
				r.RetryAfter = 3 * time.Second
			}
			return err
		}
		if e := c.sleep(time.Duration(400*(i+1)) * time.Millisecond); e != nil {
			return e
		}
	}
}

func (c *ratchetConsumer) call(r netproto.OlmAdmissionRequest, out any) error {
	return c.net(func() error {
		data, err := c.d.Admission(c.ctx, r)
		if err != nil {
			return err
		}
		if out == nil || len(data) == 0 || string(data) == "null" {
			if out != nil {
				return rfail("invalid")
			}
			return nil
		}
		if json.Unmarshal(data, out) != nil {
			return rfail("invalid")
		}
		return nil
	})
}

func (c *ratchetConsumer) directory(r netproto.OlmDirectoryRequest) (netproto.OlmDirectoryResult, error) {
	var out netproto.OlmDirectoryResult
	err := c.net(func() error {
		var e error
		out, e = c.d.Directory(c.ctx, r)
		return e
	})
	return out, err
}

func (c *ratchetConsumer) read(refs ...durable.Ref) (durable.Snapshot, error) {
	if err := c.check(); err != nil {
		return durable.Snapshot{}, err
	}
	s, err := c.d.Store.Read(refs...)
	if err != nil {
		return s, err
	}
	return s, c.check()
}

func (c *ratchetConsumer) authority() (durable.AccountAuthority, error) {
	s, err := c.read()
	if err != nil {
		return durable.AccountAuthority{}, err
	}
	a, err := c.d.Store.AccountAuthority(s.Fence)
	if err != nil {
		return a, err
	}
	if a.Blocked || a.AccountID == "" || a.Epoch < 1 {
		return a, rfail("recovery_required")
	}
	return a, c.check()
}

// handoff rechecks account authority immediately before a network handoff.
func (c *ratchetConsumer) handoff(a durable.AccountAuthority, r netproto.OlmAdmissionRequest, out any) error {
	if err := c.d.Store.CheckAccountAuthority(a); err != nil {
		return err
	}
	r.AccountEpoch = a.Epoch
	return c.call(r, out)
}

// coreApply waits for a core slot; environment failures are retryable
// core_unavailable, while errors the core itself reports stay uncoded so the
// receive stage relabels them deterministically.
func (c *ratchetConsumer) coreApply(r ratchet.Request) (ratchet.Response, error) {
	if err := c.check(); err != nil {
		return ratchet.Response{}, err
	}
	r.Key = c.d.Store.CoreKey()
	defer clear(r.Key)
	var out ratchet.Response
	var err error
	for i := 0; i < 50; i++ {
		out, err = c.d.Core.Apply(c.ctx, r)
		if err == nil || err.Error() != "ratchet core busy" {
			break
		}
		if e := c.sleep(20 * time.Millisecond); e != nil {
			return out, e
		}
	}
	if err != nil {
		if strings.HasPrefix(err.Error(), "ratchet: ") {
			return out, err
		}
		return out, &ratchetError{Code: "core_unavailable", Err: err}
	}
	return out, c.check()
}

func (c *ratchetConsumer) refs(kind string) ([]durable.Ref, error) {
	var out []durable.Ref
	after := ""
	for {
		if err := c.check(); err != nil {
			return nil, err
		}
		page, err := c.d.Store.PendingRefs(kind, after, 200)
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if len(page) < 200 {
			return out, nil
		}
		after = page[len(page)-1].ID
	}
}

func (c *ratchetConsumer) readMany(refs []durable.Ref) (map[durable.Ref]durable.Record, error) {
	out := map[durable.Ref]durable.Record{}
	for i := 0; i < len(refs); i += 200 {
		end := min(i+200, len(refs))
		s, err := c.read(refs[i:end]...)
		if err != nil {
			return nil, err
		}
		for k, v := range s.Records {
			out[k] = v
		}
	}
	return out, nil
}

// --- Readiness and own publication -------------------------------------------

// ensureReady publishes the own account. Only recovery_required is sticky; a
// failed periodic refresh keeps a still-valid readiness and retries later.
func (c *ratchetConsumer) ensureReady() error {
	if c.sticky != nil {
		return c.sticky
	}
	if c.ready != nil && c.d.Now().Sub(c.readyAt) <= c.d.RefreshEvery {
		return nil
	}
	v, err := c.readyOnce()
	if err == nil {
		c.ready, c.readyAt, c.identity = &v, c.d.Now(), v.identity
		return nil
	}
	switch code := ratchetCode(err); {
	case code == "recovery_required":
		c.sticky, c.ready = err, nil
	case code == "fenced":
	case c.ready != nil && c.ready.expiresAt > c.now()+300:
		c.readyAt = c.d.Now().Add(-c.d.RefreshEvery + time.Minute)
		return nil
	default:
		c.ready = nil
	}
	return err
}

func (c *ratchetConsumer) readyOnce() (ratchetReadiness, error) {
	a, err := c.authority()
	if err != nil {
		return ratchetReadiness{}, err
	}
	accountRef := durable.Ref{Kind: durable.Account, ID: a.AccountID}
	s, err := c.read(accountRef)
	if err != nil {
		return ratchetReadiness{}, err
	}
	if s.Records[accountRef].Revision == 0 {
		// Only a fresh epoch-1 namespace may create an account. The server
		// refuses epoch 1 over an existing device account; that blocks below
		// as recovery_required instead of silently replacing old keys.
		if a.Epoch != 1 {
			return ratchetReadiness{}, rfail("recovery_required")
		}
		created, err := c.coreApply(ratchet.Request{Op: ratchet.NewAccount, Count: ratchetPreKeys})
		if err != nil {
			return ratchetReadiness{}, err
		}
		if err = c.commitBundle(s, a, created, 1, nil); err != nil {
			return ratchetReadiness{}, err
		}
	}
	latest, err := c.latestPublication(a)
	if err != nil {
		return ratchetReadiness{}, err
	}
	if latest == nil {
		return ratchetReadiness{}, rfail("recovery_required")
	}
	if latest.ExpiresAt-c.now() < 86400 {
		// Refresh before expiry with new one-time keys and next generation.
		if s, err = c.read(accountRef); err != nil {
			return ratchetReadiness{}, err
		}
		refreshed, err := c.coreApply(ratchet.Request{Op: ratchet.PreKeys, Account: string(s.Records[accountRef].Data), Count: ratchetRefill})
		if err != nil {
			return ratchetReadiness{}, err
		}
		if err = c.commitBundle(s, a, refreshed, latest.Generation+1, latest); err != nil {
			return ratchetReadiness{}, err
		}
		if latest, err = c.latestPublication(a); err != nil || latest == nil {
			return ratchetReadiness{}, rfail("recovery_required")
		}
	}
	if err = c.d.Store.CheckAccountAuthority(a); err != nil {
		return ratchetReadiness{}, err
	}
	if _, err = c.directory(netproto.OlmDirectoryRequest{Action: "publish", RequestID: newClientMsgID(), Bundle: latest}); err != nil {
		if ratchetCode(err) == "account-change-required" {
			return ratchetReadiness{}, &ratchetError{Code: "recovery_required", Err: err}
		}
		return ratchetReadiness{}, err
	}
	if err = c.check(); err != nil {
		return ratchetReadiness{}, err
	}
	return ratchetReadiness{identity: latest.Identity, epoch: a.Epoch, expiresAt: latest.ExpiresAt}, nil
}

func (c *ratchetConsumer) commitBundle(s durable.Snapshot, a durable.AccountAuthority, created ratchet.Response, generation int64, previous *olmdir.Bundle) error {
	if created.Bundle == nil || created.Account == "" {
		return rfail("core_unavailable")
	}
	issued := c.now()
	b := olmdir.Bundle{Version: olmdir.Version, Protocol: olmdir.Protocol, Server: c.d.Namespace.Server, Principal: c.d.Namespace.Principal, DeviceID: c.d.Namespace.Device,
		AccountEpoch: a.Epoch, Generation: generation, IssuedAt: issued, ExpiresAt: issued + 6*86400, Identity: created.Bundle.Identity, Signing: created.Bundle.Signing}
	if previous != nil {
		b.Identity, b.Signing = previous.Identity, previous.Signing
	}
	for _, k := range created.Bundle.OneTimeKeys {
		b.OneTimeKeys = append(b.OneTimeKeys, olmdir.Key{ID: k.ID, Key: k.Key})
	}
	signed, err := c.d.SignBundle(b)
	if err != nil {
		return err
	}
	if err = c.check(); err != nil {
		return err
	}
	id := fmt.Sprintf("pub:%s:%010d", a.AccountID, generation)
	otks := map[string]durable.OTKObligation{}
	for _, k := range signed.OneTimeKeys {
		otks[a.AccountID+":"+k.ID] = durable.OTKObligation{AccountID: a.AccountID, PublicationID: id, ExpiresAt: signed.ExpiresAt, KeyID: k.ID, PublicKey: k.Key}
	}
	payload, _ := json.Marshal(signed)
	if _, err = c.d.Store.CommitPublicationForAccount(s, a.AccountID, id, []byte(created.Account), payload, otks); err != nil {
		return err
	}
	return c.check()
}

func (c *ratchetConsumer) latestPublication(a durable.AccountAuthority) (*olmdir.Bundle, error) {
	refs, err := c.refs(durable.Publication)
	if err != nil {
		return nil, err
	}
	prefix := "pub:" + a.AccountID + ":"
	var last *durable.Ref
	for i := range refs {
		if strings.HasPrefix(refs[i].ID, prefix) && (last == nil || refs[i].ID > last.ID) {
			last = &refs[i]
		}
	}
	if last == nil {
		return nil, nil
	}
	s, err := c.read(*last)
	if err != nil {
		return nil, err
	}
	var rec durable.PublicationRecord
	var b olmdir.Bundle
	if json.Unmarshal(s.Records[*last].Data, &rec) != nil || json.Unmarshal(rec.Payload, &b) != nil {
		return nil, rfail("corrupt")
	}
	return &b, nil
}

// --- Trust ---------------------------------------------------------------------

func ratchetTrustID(principal, device string) string {
	return string(ratchetJSON([]string{principal, device}))
}

func (c *ratchetConsumer) casRetry(fn func() error) error {
	for i := 0; ; i++ {
		err := fn()
		if ratchetCode(err) != "conflict" || i >= ratchetLocalRetry {
			return err
		}
		if e := c.check(); e != nil {
			return e
		}
	}
}

func readTrust(s durable.Snapshot, id string) (rev uint64, revoked bool, v ratchetTrustValue, err error) {
	r := s.Records[durable.Ref{Kind: durable.Trust, ID: id}]
	if r.Revision == 0 {
		return 0, false, v, nil
	}
	var tr struct {
		Revoked bool   `json:"revoked"`
		Value   []byte `json:"value"`
	}
	if json.Unmarshal(r.Data, &tr) != nil || json.Unmarshal(tr.Value, &v) != nil {
		return r.Revision, false, v, rfail("corrupt")
	}
	return r.Revision, tr.Revoked, v, nil
}

// trust returns the noXa key from the pinned root chain and any adopted Olm
// pin. A changed noXa key blocks with account_changed.
func (c *ratchetConsumer) trust(principal, device string) (ratchetTrusted, error) {
	var out ratchetTrusted
	err := c.casRetry(func() error {
		var e error
		out, e = c.trustOnce(principal, device)
		return e
	})
	return out, err
}

func (c *ratchetConsumer) trustOnce(principal, device string) (ratchetTrusted, error) {
	key, err := c.d.DeviceKey(c.ctx, principal, device)
	if err != nil {
		return ratchetTrusted{}, err
	}
	if err = c.check(); err != nil {
		return ratchetTrusted{}, err
	}
	if len(key) != ed25519.PublicKeySize {
		return ratchetTrusted{}, rfail("untrusted_device")
	}
	id := ratchetTrustID(principal, device)
	ref := durable.Ref{Kind: durable.Trust, ID: id}
	s, err := c.read(ref)
	if err != nil {
		return ratchetTrusted{}, err
	}
	rev, revoked, saved, err := readTrust(s, id)
	if err != nil {
		return ratchetTrusted{}, err
	}
	encoded := base64.StdEncoding.EncodeToString(key)
	if rev == 0 || (!revoked && saved.Noxa == nil) {
		// First use, or an explicitly confirmed changed root (resetTrust):
		// pin the newly trusted key; an Olm pin must be re-adopted from a
		// bundle it signs.
		if err = c.d.Store.SetTrust(s, id, ratchetJSON(ratchetTrustValue{Principal: principal, Device: device, Noxa: &encoded}), false); err != nil {
			return ratchetTrusted{}, err
		}
		return ratchetTrusted{id: id, key: key}, c.check()
	}
	if revoked {
		return ratchetTrusted{}, rfail("revoked")
	}
	if *saved.Noxa != encoded {
		return ratchetTrusted{}, rfail("account_changed")
	}
	return ratchetTrusted{id: id, key: key, pin: saved.Pin}, nil
}

// adopt pins an Olm account only from a bundle the caller verified under the
// trusted noXa key. A changed epoch/identity blocks (no reset promotion here).
func (c *ratchetConsumer) adopt(t ratchetTrusted, b olmdir.Bundle) (ratchetTrusted, error) {
	out := t
	err := c.casRetry(func() error {
		var e error
		out, e = c.adoptOnce(t, b)
		return e
	})
	return out, err
}

func (c *ratchetConsumer) adoptOnce(t ratchetTrusted, b olmdir.Bundle) (ratchetTrusted, error) {
	if t.pin != nil {
		if t.pin.Epoch != b.AccountEpoch || t.pin.Identity != b.Identity {
			return t, rfail("account_changed")
		}
		return t, nil
	}
	s, err := c.read(durable.Ref{Kind: durable.Trust, ID: t.id})
	if err != nil {
		return t, err
	}
	_, revoked, saved, err := readTrust(s, t.id)
	if err != nil {
		return t, err
	}
	if revoked {
		return t, rfail("revoked")
	}
	if saved.Noxa == nil || *saved.Noxa != base64.StdEncoding.EncodeToString(t.key) {
		return t, rfail("account_changed")
	}
	if p := saved.Pin; p != nil {
		// Another consumer adopted concurrently: an identical pin is fine.
		if p.Epoch != b.AccountEpoch || p.Identity != b.Identity {
			return t, rfail("account_changed")
		}
		t.pin = p
		return t, nil
	}
	pin := &ratchetPin{Epoch: b.AccountEpoch, Identity: b.Identity, Signing: b.Signing}
	saved.Pin = pin
	if err = c.d.Store.SetTrust(s, t.id, ratchetJSON(saved), false); err != nil {
		return t, err
	}
	t.pin = pin
	return t, c.check()
}

// --- Sessions ------------------------------------------------------------------

// sessionContext is the device-pair context (no conversation: bound per message).
func (c *ratchetConsumer) sessionContext(a durable.AccountAuthority, t admission.Target) durable.SessionContext {
	return durable.SessionContext{LocalAccountID: a.AccountID, LocalEpoch: a.Epoch, PeerEpoch: t.AccountEpoch, PeerPrincipal: t.Principal,
		PeerDevice: t.Device, PeerIdentity: t.Identity, Scope: durable.DevicePairScope, Protocol: ratchetProtocol}
}

func (c *ratchetConsumer) sessionIndex() (map[durable.SessionContext]string, error) {
	if c.sessions != nil {
		return c.sessions, nil
	}
	refs, err := c.refs(durable.Session)
	if err != nil {
		return nil, err
	}
	records, err := c.readMany(refs)
	if err != nil {
		return nil, err
	}
	index := map[durable.SessionContext]string{}
	for ref, r := range records {
		var st durable.SessionState
		// Legacy per-conversation records are never selected for sending.
		if json.Unmarshal(r.Data, &st) == nil && !st.Retired && st.Context.Scope == durable.DevicePairScope {
			index[st.Context] = ref.ID
		}
	}
	c.sessions = index
	return index, nil
}

func readSession(s durable.Snapshot, id string) (durable.SessionState, bool, error) {
	r := s.Records[durable.Ref{Kind: durable.Session, ID: id}]
	if r.Revision == 0 {
		return durable.SessionState{}, false, nil
	}
	var st durable.SessionState
	if json.Unmarshal(r.Data, &st) != nil {
		return st, true, rfail("corrupt")
	}
	return st, true, nil
}

// --- Sending -------------------------------------------------------------------

// send runs one logical operation. A logical ID that already has a committed
// operation ALWAYS yields its original archive, whatever body is passed now.
func (c *ratchetConsumer) send(intent ratchetIntent) (RatchetOutcome, error) {
	c.serial.Lock()
	defer c.serial.Unlock()
	for tries := 0; ; tries++ {
		if err := c.check(); err != nil {
			return RatchetOutcome{}, err
		}
		if err := c.ensureReady(); err != nil {
			return RatchetOutcome{}, err
		}
		out, err := c.sendOnce(intent)
		// A concurrent consumer of this namespace won the snapshot. Nothing
		// was committed or transported for this intent; start over.
		if ratchetCode(err) != "local_conflict" || tries >= ratchetLocalRetry {
			return out, err
		}
		c.sessions = nil
	}
}

func (c *ratchetConsumer) sendOnce(intent ratchetIntent) (RatchetOutcome, error) {
	a, err := c.authority()
	if err != nil {
		return RatchetOutcome{}, err
	}
	lref := durable.Ref{Kind: durable.Logical, ID: intent.LogicalID}
	s, err := c.read(lref)
	if err != nil {
		return RatchetOutcome{}, err
	}
	if r := s.Records[lref]; r.Revision > 0 {
		var logical ratchetLogical
		if json.Unmarshal(r.Data, &logical) != nil {
			return RatchetOutcome{}, rfail("corrupt")
		}
		if logical.State != "rejected" && logical.State != "aborted" {
			return c.resolve(logical.AttemptID, a, true)
		}
		// A terminal non-accepted intent may be resealed with NEW ratchet
		// state, but only with its original committed content.
		_, rec, err := c.outcome(logical.AttemptID)
		if err != nil {
			return RatchetOutcome{}, err
		}
		return c.seal(a, ratchetIntent{LogicalID: rec.LogicalID, Scope: rec.Scope, Peer: rec.Peer, Conversation: rec.Conversation, Purpose: rec.Purpose, Reference: rec.Reference, Body: rec.Body}, true)
	}
	return c.seal(a, intent, false)
}

type ratchetRoster struct {
	Revision int64              `json:"revision"`
	Epoch    int64              `json:"epoch"`
	Rosters  []admission.Roster `json:"rosters"`
	Targets  []admission.Target `json:"targets"`
}

type ratchetPlan struct {
	t            admission.Target
	trusted      ratchetTrusted
	binding      ratchet.Binding
	context      durable.SessionContext
	prior        string
	claim        *ratchetKeptClaim
	claimKey     string
	pendingClaim *durable.SessionClaim
	replaces     string
}

func ratchetBodySentAt(body json.RawMessage, fallback float64) float64 {
	var b struct {
		SentAt json.Number `json:"sentAt"`
	}
	if json.Unmarshal(body, &b) == nil {
		if f, err := b.SentAt.Float64(); err == nil && f > 0 {
			return f
		}
	}
	return fallback
}

func (c *ratchetConsumer) seal(a durable.AccountAuthority, in ratchetIntent, reused bool) (RatchetOutcome, error) {
	if in.Purpose == "" {
		in.Purpose = "chat"
	}
	if (in.Scope != "direct" && in.Scope != "group") || (in.Purpose != "chat" && in.Purpose != "edit" && in.Purpose != "delete") ||
		(in.Purpose == "chat") == (in.Reference != "") || !json.Valid(in.Body) || !olmdir.ValidText(in.LogicalID) {
		return RatchetOutcome{}, rfail("invalid")
	}
	req := netproto.OlmAdmissionRequest{Action: "snapshot", Scope: in.Scope}
	if in.Scope == "direct" {
		req.Peer = in.Peer
	} else {
		req.Conversation = in.Conversation
	}
	var roster ratchetRoster
	if err := c.handoff(a, req, &roster); err != nil {
		return RatchetOutcome{}, err
	}
	ns := c.d.Namespace
	m := admission.Manifest{Version: admission.Version, Server: ns.Server, Sender: ns.Principal, Device: ns.Device, AccountEpoch: a.Epoch,
		AttemptID: strconv.FormatInt(a.NextAttempt+1, 10), LogicalID: in.LogicalID, Purpose: in.Purpose, Reference: in.Reference, Scope: in.Scope,
		Deadline: c.now() + c.d.AttemptTTL, Rosters: roster.Rosters, Targets: roster.Targets}
	if in.Scope == "direct" {
		m.Peer = in.Peer
	} else {
		m.Conversation, m.Revision, m.Epoch = in.Conversation, roster.Revision, roster.Epoch
	}
	if err := ratchetCheckFanout(in.Scope, m.Targets); err != nil {
		return RatchetOutcome{}, err
	}
	locals := 0
	for _, t := range m.Targets {
		if t.Local {
			locals++
			if t.Principal != m.Sender || t.Device != m.Device || t.AccountEpoch != a.Epoch || t.Identity != c.identity {
				return RatchetOutcome{}, rfail("local_identity_mismatch")
			}
		}
	}
	if locals != 1 {
		return RatchetOutcome{}, rfail("local_identity_mismatch")
	}
	sessions, err := c.sessionIndex()
	if err != nil {
		return RatchetOutcome{}, err
	}
	covers := func(until int64) bool { return until > c.now()+c.d.AttemptTTL+60 }
	var plans []ratchetPlan
	// Every non-local target is required: an unverifiable device fails the
	// whole operation rather than being silently omitted.
	for _, t := range m.Targets {
		if t.Local {
			continue
		}
		trusted, err := c.trust(t.Principal, t.Device)
		if err != nil {
			return RatchetOutcome{}, err
		}
		if trusted.pin != nil && (trusted.pin.Epoch != t.AccountEpoch || trusted.pin.Identity != t.Identity) {
			return RatchetOutcome{}, rfail("account_changed")
		}
		p := ratchetPlan{t: t, trusted: trusted, binding: admission.ExpectedBinding(m, t), context: c.sessionContext(a, t)}
		// Without a verified Olm pin (first contact or a confirmed root change)
		// a fresh signed claim is required even if a session exists.
		if trusted.pin != nil {
			p.prior = sessions[p.context]
		}
		if p.prior != "" {
			// A first-contact session whose claim-carrying attempt was never
			// accepted has no server session row: resend with its ORIGINAL
			// reserved claim while that still covers the deadline; otherwise
			// replace it (retired in the same commit) with a fresh claim.
			ps, err := c.read(durable.Ref{Kind: durable.Session, ID: p.prior})
			if err != nil {
				return RatchetOutcome{}, err
			}
			st, ok, err := readSession(ps, p.prior)
			if err != nil {
				return RatchetOutcome{}, err
			}
			if !ok {
				return RatchetOutcome{}, rfail("local_conflict")
			}
			if st.Pending != nil && covers(st.Pending.ClaimUntil) {
				p.pendingClaim = st.Pending
			} else if st.Pending != nil {
				p.replaces, p.prior = p.prior, ""
			}
		}
		p.claimKey = string(ratchetJSON([]any{a.AccountID, a.Epoch, t.Principal, t.Device, t.AccountEpoch, t.Identity}))
		kept, haveKept := c.claims[p.claimKey]
		if p.prior == "" && haveKept && covers(kept.until) && trusted.pin != nil {
			// The reservation from a lost local_conflict race is still ours on
			// the server; reuse it instead of spending more claim budget.
			p.claim = &kept
		} else if p.prior == "" {
			claimID, until := newClientMsgID(), c.now()+3600
			request := netproto.OlmDirectoryRequest{RequestID: claimID, Action: "claim", Principal: t.Principal, DeviceID: t.Device, AccountEpoch: a.Epoch, ClaimUntil: until}
			cs, err := c.read(durable.Ref{Kind: durable.Trust, ID: trusted.id}, durable.Ref{Kind: durable.Claim, ID: claimID})
			if err != nil {
				return RatchetOutcome{}, err
			}
			raw, _ := json.Marshal(request)
			if _, err = c.d.Store.ReserveClaim(cs, claimID, trusted.id, raw); err != nil {
				return RatchetOutcome{}, err
			}
			if err = c.d.Store.CheckAccountAuthority(a); err != nil {
				return RatchetOutcome{}, err
			}
			result, err := c.directory(request)
			if err != nil {
				return RatchetOutcome{}, err
			}
			expected := t.Identity
			if trusted.pin != nil {
				expected = trusted.pin.Identity
			}
			selected, err := verifyReservedClaim(result.Bundle, olmTrustExpectation{SigningKey: trusted.key, Server: m.Server, Principal: t.Principal, Device: t.Device,
				AccountEpoch: t.AccountEpoch, Identity: expected}, result.KeyID, until, c.d.Now())
			if err != nil {
				return RatchetOutcome{}, &ratchetError{Code: "invalid_claim", Err: err}
			}
			if trusted, err = c.adopt(trusted, *result.Bundle); err != nil {
				return RatchetOutcome{}, err
			}
			p.trusted = trusted
			p.claim = &ratchetKeptClaim{id: claimID, key: selected.Key, until: until}
			c.claims[p.claimKey] = *p.claim
		}
		plans = append(plans, p)
	}
	accountRef := durable.Ref{Kind: durable.Account, ID: a.AccountID}
	refs := []durable.Ref{{Kind: durable.Logical, ID: in.LogicalID}, accountRef}
	seen := map[durable.Ref]bool{refs[0]: true, refs[1]: true}
	add := func(r durable.Ref) {
		if !seen[r] {
			seen[r] = true
			refs = append(refs, r)
		}
	}
	for _, p := range plans {
		add(durable.Ref{Kind: durable.Trust, ID: p.trusted.id})
		if p.prior != "" {
			add(durable.Ref{Kind: durable.Session, ID: p.prior})
		}
		if p.replaces != "" {
			add(durable.Ref{Kind: durable.Session, ID: p.replaces})
		}
	}
	before, err := c.read(refs...)
	if err != nil {
		return RatchetOutcome{}, err
	}
	account := string(before.Records[accountRef].Data)
	sentAt := ratchetBodySentAt(in.Body, float64(c.d.Now().UnixMilli())/1000)
	record := RatchetRecord{Protocol: ratchetProtocol, Provenance: "authenticated", Sender: m.Sender, SenderDevice: m.Device, LogicalID: in.LogicalID,
		Key: ratchetSemanticKey(m.Sender, in.LogicalID), Scope: in.Scope, Purpose: in.Purpose, Reference: in.Reference, Body: in.Body, SentAt: sentAt, Self: true}
	if in.Scope == "direct" {
		record.Peer = in.Peer
	} else {
		record.Conversation = in.Conversation
	}
	if in.Reference != "" {
		record.ReferenceKey = ratchetSemanticKey(m.Sender, in.Reference)
	}
	var packets []durable.SendCandidate
	var copies []admission.Copy
	for _, p := range plans {
		var prior *durable.SessionState
		if p.prior != "" {
			st, ok, err := readSession(before, p.prior)
			if err != nil {
				return RatchetOutcome{}, err
			}
			if !ok || st.Retired || st.Context != p.context {
				return RatchetOutcome{}, rfail("local_conflict")
			}
			// The pending claim decided above must still be the recorded one.
			if (st.Pending == nil) != (p.pendingClaim == nil) || st.Pending != nil && *st.Pending != *p.pendingClaim {
				return RatchetOutcome{}, rfail("local_conflict")
			}
			prior = &st
		}
		if p.replaces != "" {
			// Replace only a session STILL pending with a claim that cannot
			// cover this attempt; if another consumer established it meanwhile,
			// start over and reuse it instead of burning a claim.
			st, ok, err := readSession(before, p.replaces)
			if err != nil || !ok || st.Retired || st.Pending == nil || covers(st.Pending.ClaimUntil) {
				return RatchetOutcome{}, rfail("local_conflict")
			}
		}
		plaintext, err := admission.SealPayload(m, p.t, in.Body)
		if err != nil {
			return RatchetOutcome{}, rfail("invalid")
		}
		binding := p.binding
		r := ratchet.Request{Binding: &binding, Plaintext: plaintext}
		if prior != nil {
			r.Op, r.Session = ratchet.Encrypt, string(prior.Snapshot)
		} else {
			r.Op, r.Account, r.PeerIdentity, r.OneTimeKey = ratchet.Outbound, account, p.t.Identity, p.claim.key
		}
		result, err := c.coreApply(r)
		if err != nil {
			return RatchetOutcome{}, err
		}
		if result.Envelope == nil || result.SessionID == "" || result.Session == "" {
			return RatchetOutcome{}, rfail("core_unavailable")
		}
		sessionID := durable.SessionRef(result.SessionID)
		if p.prior != "" && p.prior != sessionID {
			return RatchetOutcome{}, rfail("local_conflict")
		}
		claim := p.pendingClaim
		if p.claim != nil {
			claim = &durable.SessionClaim{ClaimID: p.claim.id, ClaimUntil: p.claim.until}
		}
		envelope, _ := json.Marshal(result.Envelope)
		packet := durable.SendCandidate{SessionID: sessionID, TrustID: p.trusted.id, Replaces: p.replaces, State: []byte(result.Session), Envelope: envelope, Context: p.context, Claim: claim}
		cp := admission.Copy{Principal: p.t.Principal, Device: p.t.Device, AccountEpoch: p.t.AccountEpoch, SessionID: result.SessionID, Envelope: envelope}
		if claim != nil {
			cp.ClaimID = claim.ClaimID
		}
		packets, copies = append(packets, packet), append(copies, cp)
	}
	parts, err := admission.Freeze(&m, copies)
	if err != nil {
		return RatchetOutcome{}, rfail("invalid")
	}
	// Extend the exact pre-crypto snapshot with the new session refs; any
	// concurrent change to the prior reads aborts the whole immutable batch.
	for _, p := range packets {
		add(durable.Ref{Kind: durable.Session, ID: p.SessionID})
	}
	commit, err := c.read(refs...)
	if err != nil {
		return RatchetOutcome{}, err
	}
	for r, v := range before.Records {
		if commit.Records[r].Revision != v.Revision {
			return RatchetOutcome{}, rfail("local_conflict")
		}
	}
	if hook := ratchetBeforeCommit; hook != nil {
		if err := hook(c.d.Namespace); err != nil {
			return RatchetOutcome{}, err
		}
	}
	committed, err := c.d.Store.CommitAttempt(commit, m, parts, packets, ratchetJSON(record))
	if ratchetCode(err) == "conflict" {
		return RatchetOutcome{}, &ratchetError{Code: "local_conflict", Err: err}
	}
	if err != nil {
		return RatchetOutcome{}, err
	}
	// This consumer never re-commits a frozen attempt: "reused" means the
	// sequence belongs to another writer. Never resolve its archive as ours.
	if committed.Reused {
		return RatchetOutcome{}, rfail("local_conflict")
	}
	for _, p := range plans {
		if p.claim != nil {
			delete(c.claims, p.claimKey)
		}
	}
	if err = c.check(); err != nil {
		return RatchetOutcome{}, err
	}
	for _, p := range packets {
		sessions[p.Context] = p.SessionID
	}
	return c.resolve(durable.AttemptID(m), a, reused)
}

func (c *ratchetConsumer) outcome(id string) (durable.OutboundRecord, RatchetRecord, error) {
	ref := durable.Ref{Kind: durable.Outbox, ID: id}
	s, err := c.read(ref)
	if err != nil {
		return durable.OutboundRecord{}, RatchetRecord{}, err
	}
	r := s.Records[ref]
	if r.Revision == 0 {
		return durable.OutboundRecord{}, RatchetRecord{}, rfail("invalid")
	}
	var op durable.OutboundRecord
	var rec RatchetRecord
	if json.Unmarshal(r.Data, &op) != nil || json.Unmarshal(op.Archive, &rec) != nil {
		return op, rec, rfail("corrupt")
	}
	return op, rec, nil
}

func ratchetTerminal(state string) bool {
	return state == "accepted" || state == "rejected" || state == "aborted"
}

func ratchetAttemptSeq(id string) int64 {
	_, s, _ := strings.Cut(id, ":")
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

// resolve settles exactly one committed attempt; it never seals a new one.
func (c *ratchetConsumer) resolve(id string, a durable.AccountAuthority, reused bool) (RatchetOutcome, error) {
	op, record, err := c.outcome(id)
	if err != nil {
		return RatchetOutcome{}, err
	}
	if op.Receipt == nil {
		if err = c.settle(id, a); err != nil {
			// Still uncertain (or clock skew): views show the ORIGINAL committed
			// text as pending; a later drain resolves it exactly.
			pending := record
			pending.Transport, pending.Status = RatchetTransport{Attempt: id}, "pending"
			if ratchetCode(err) != "fenced" {
				_ = c.emit(pending, false)
			}
			return RatchetOutcome{}, withCommitted(err, RatchetOutcome{Record: pending, Reused: reused})
		}
		if op, record, err = c.outcome(id); err != nil {
			return RatchetOutcome{}, withCommitted(err, RatchetOutcome{Record: record, Reused: reused})
		}
	}
	receipt := *op.Receipt
	// The terminal receipt is committed: retirement is best-effort
	// housekeeping that a later drain repeats; it never changes the outcome.
	if err = c.retire(id); ratchetCode(err) == "fenced" {
		return RatchetOutcome{}, withCommitted(err, RatchetOutcome{Record: record, Receipt: &receipt, Reused: reused})
	}
	projected := record
	projected.Transport = RatchetTransport{Message: receipt.MessageID, Attempt: id}
	projected.Status, projected.Reason = receipt.State, receipt.Reason
	if err = c.emit(projected, false); ratchetCode(err) == "fenced" {
		return RatchetOutcome{}, withCommitted(err, RatchetOutcome{Record: projected, Receipt: &receipt, Reused: reused})
	}
	out := RatchetOutcome{Record: projected, Receipt: &receipt, Reused: reused}
	if receipt.State != "accepted" {
		code := receipt.Reason
		if code == "" {
			code = receipt.State
		}
		return RatchetOutcome{}, &ratchetError{Code: code, Committed: &out}
	}
	return out, nil
}

// settle obtains and commits the terminal receipt of an uncertain attempt.
func (c *ratchetConsumer) settle(id string, a durable.AccountAuthority) error {
	s, err := c.read()
	if err != nil {
		return err
	}
	meta, err := c.d.Store.AttemptStatusRequest(s.Fence, id)
	if err != nil {
		return err
	}
	m := meta.Manifest
	var result admission.Receipt
	if err = c.call(netproto.OlmAdmissionRequest{Action: "status", AccountEpoch: m.AccountEpoch, AttemptID: m.AttemptID}, &result); err != nil {
		return err
	}
	// An attempt the server never saw cannot be begun after its deadline:
	// fence it with a metadata-only abort. Margin: a begin that may land
	// at/after the deadline is aborted (and resealed), not misreported.
	expired := m.Deadline <= c.now()+ratchetDeadlineMargin
	if !expired && c.skewed[id] && !ratchetTerminal(result.State) {
		return rfail("clock_skew")
	}
	if !ratchetTerminal(result.State) {
		var wire *durable.AttemptDescriptor
		if s, err = c.read(); err != nil {
			return err
		}
		if w, err := c.d.Store.AttemptWire(s.Fence, id); err == nil {
			wire = &w
		} else if code := ratchetCode(err); code != "fenced" && code != "revoked" && code != "conflict" {
			return err
		}
		if err = c.check(); err != nil {
			return err
		}
		abort := func() error {
			return c.call(netproto.OlmAdmissionRequest{Action: "abort", AccountEpoch: m.AccountEpoch, Manifest: &m}, &result)
		}
		if wire != nil && m.AccountEpoch == a.Epoch && !(expired && result.State == "absent") {
			err = c.handoff(a, netproto.OlmAdmissionRequest{Action: "begin", Manifest: &wire.Manifest}, &result)
			if ratchetCode(err) == "invalid" {
				// The server refuses an unexpired deadline (client clock skew):
				// fail without aborting or resealing, so no attempt IDs are
				// burned. Once it is genuinely past, abort.
				if m.Deadline > c.now()+ratchetDeadlineMargin {
					c.skewed[id] = true
					return rfail("clock_skew")
				}
				err = abort()
			}
			if err != nil {
				return err
			}
			if !ratchetTerminal(result.State) {
				for part, data := range wire.Parts {
					if err = c.handoff(a, netproto.OlmAdmissionRequest{Action: "put_part", AttemptID: m.AttemptID, Part: part, Data: data}, nil); err != nil {
						return err
					}
				}
				if err = c.handoff(a, netproto.OlmAdmissionRequest{Action: "finalize", AttemptID: m.AttemptID}, &result); err != nil {
					return err
				}
			}
		} else if err = abort(); err != nil {
			// Trust/epoch changed: a metadata-only abort fences the old attempt
			// without handing its packets to anyone.
			return err
		}
	}
	if !ratchetTerminal(result.State) {
		return rfail("uncertain")
	}
	// Applying an accepted receipt also establishes its sessions, so a
	// concurrent session write elsewhere can lose the CAS: retry.
	return c.casRetry(func() error {
		s, err := c.read(durable.Ref{Kind: durable.Outbox, ID: id}, durable.Ref{Kind: durable.Logical, ID: m.LogicalID})
		if err != nil {
			return err
		}
		if err = c.d.Store.ApplyAttemptResult(s, id, result); err != nil {
			return err
		}
		return c.check()
	})
}

func (c *ratchetConsumer) retire(id string) error {
	if c.retired[id] {
		return nil
	}
	s, err := c.read()
	if err != nil {
		return err
	}
	a, err := c.d.Store.AccountAuthority(s.Fence)
	if err != nil {
		return err
	}
	if !a.Retired.Has(ratchetAttemptSeq(id)) || !strings.HasPrefix(id, strconv.FormatInt(a.Epoch, 10)+":") {
		r, err := c.d.Store.AttemptRetirementRequest(s.Fence, id)
		if err != nil {
			return err
		}
		if err = c.call(netproto.OlmAdmissionRequest{Action: "retire_receipt", AttemptID: r.AttemptID, AccountEpoch: r.AccountEpoch, Digest: r.Digest}, nil); err != nil && ratchetCode(err) != "retired" {
			return err
		}
		// Durable progress: later sessions skip this retirement.
		ns, err := c.read(durable.Ref{Kind: durable.Outbox, ID: id})
		if err != nil {
			return err
		}
		if err = c.d.Store.NoteRetired(ns, id); err != nil {
			return err
		}
	}
	c.retired[id] = true
	return nil
}

// --- Receiving -----------------------------------------------------------------

// history replays every authoritative archive (receipts and the latest attempt
// per own logical ID) as non-new, ordered by send time.
func (c *ratchetConsumer) history() ([]RatchetRecord, error) {
	c.serial.Lock()
	defer c.serial.Unlock()
	if err := c.check(); err != nil {
		return nil, err
	}
	if err := c.ensureReady(); err != nil {
		return nil, err
	}
	var out []RatchetRecord
	inboxRefs, err := c.refs(durable.Inbox)
	if err != nil {
		return nil, err
	}
	inbox, err := c.readMany(inboxRefs)
	if err != nil {
		return nil, err
	}
	for _, r := range inbox {
		var saved struct {
			Archive []byte `json:"archive"`
		}
		var rec RatchetRecord
		if json.Unmarshal(r.Data, &saved) == nil && json.Unmarshal(saved.Archive, &rec) == nil {
			out = append(out, rec)
		}
	}
	outboxRefs, err := c.refs(durable.Outbox)
	if err != nil {
		return nil, err
	}
	outbox, err := c.readMany(outboxRefs)
	if err != nil {
		return nil, err
	}
	records := map[string]RatchetRecord{}
	var ids []string
	for ref, r := range outbox {
		var op durable.OutboundRecord
		var rec RatchetRecord
		if json.Unmarshal(r.Data, &op) != nil || op.Attempt == nil || json.Unmarshal(op.Archive, &rec) != nil {
			continue
		}
		rec.Transport = RatchetTransport{Attempt: ref.ID}
		rec.Status = "pending"
		if op.Receipt != nil {
			rec.Transport.Message, rec.Status, rec.Reason = op.Receipt.MessageID, op.Receipt.State, op.Receipt.Reason
		}
		records[ref.ID] = rec
		ids = append(ids, ref.ID)
	}
	for _, id := range ratchetLatestAttempts(ids, func(id string) string { return records[id].LogicalID }) {
		out = append(out, records[id])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].SentAt < out[j].SentAt })
	return out, nil
}

type ratchetDelivery struct {
	SourceBundle *olmdir.Bundle      `json:"source_bundle"`
	TargetBundle *olmdir.Bundle      `json:"target_bundle"`
	ID           string              `json:"id"`
	Digest       string              `json:"digest"`
	Manifest     *admission.Manifest `json:"manifest"`
	Copy         *admission.Copy     `json:"copy"`
}

// drain resolves recent committed attempts, then pages deliveries: durable
// receive commit, projection, exact receive_ack. Unreadable copies are
// quarantined (never ACKed); the current placeholder list is returned.
func (c *ratchetConsumer) drain() ([]RatchetQuarantine, error) {
	c.serial.Lock()
	defer c.serial.Unlock()
	if err := c.check(); err != nil {
		return nil, err
	}
	if err := c.ensureReady(); err != nil {
		return nil, err
	}
	a, err := c.authority()
	if err != nil {
		return nil, err
	}
	// Resolve recent committed attempts first: uncertainty never reseals.
	outboxRefs, err := c.refs(durable.Outbox)
	if err != nil {
		return nil, err
	}
	prefix := strconv.FormatInt(a.Epoch, 10) + ":"
	var recent []string
	for _, r := range outboxRefs {
		if strings.HasPrefix(r.ID, prefix) && !a.Retired.Has(ratchetAttemptSeq(r.ID)) {
			recent = append(recent, r.ID)
		}
	}
	sort.Slice(recent, func(i, j int) bool { return ratchetAttemptSeq(recent[i]) < ratchetAttemptSeq(recent[j]) })
	if len(recent) > ratchetRetireWin {
		recent = recent[len(recent)-ratchetRetireWin:]
	}
	for _, id := range recent {
		if c.retired[id] {
			continue
		}
		if _, err := c.resolve(id, a, true); err != nil {
			var r *ratchetError
			if ratchetCode(err) == "fenced" || !errors.As(err, &r) || r.Committed == nil {
				return nil, err
			}
		}
	}
	after := ""
	for pages := 0; pages < 4096; pages++ {
		var page struct {
			Deliveries *[]json.RawMessage `json:"deliveries"`
			After      string             `json:"after"`
			HasMore    bool               `json:"has_more"`
		}
		if err := c.handoff(a, netproto.OlmAdmissionRequest{Action: "deliveries", After: after}, &page); err != nil {
			return nil, err
		}
		// A successful page always carries a deliveries array (possibly empty
		// after server-side filtering); anything else is malformed.
		if page.Deliveries == nil {
			return nil, rfail("invalid")
		}
		for i, raw := range *page.Deliveries {
			var d ratchetDelivery
			_ = json.Unmarshal(raw, &d)
			qid := d.ID
			if !ratchetValidDeliveryID(qid) {
				// A missing/invalid server ID still gets a stable local key,
				// so it is quarantined once instead of aborting the drain.
				qid = fmt.Sprintf("malformed:%s:%d", after, i)
			}
			// Quarantined copies are retried once per consumer (session) or
			// after resetTrust, not on every drain.
			if _, ok := c.quarantined[qid]; ok {
				continue
			}
			err := c.receive(d, a)
			if err == nil {
				c.unquarantine(qid)
				continue
			}
			var r *ratchetError
			// A transport loss after durable commit stops this drain; the next
			// drain replays the stored outcome and exact ACK.
			if code := ratchetCode(err); code == "fenced" || errors.As(err, &r) && r.AfterCommit {
				return nil, err
			}
			code := ratchetCode(err)
			committedElsewhere := false
			if qid == d.ID && (code == "lost_key" || code == "unauthenticated") {
				iref := durable.Ref{Kind: durable.Inbox, ID: qid}
				if s, e := c.d.Store.Read(iref); e == nil {
					committedElsewhere = s.Records[iref].Revision > 0
				}
			}
			if ratchetReceiveDisposition(code, committedElsewhere) != "quarantine" {
				continue
			}
			// Unreadable ciphertext stays pending on the server and is NEVER
			// passed to legacy decoders or acknowledged.
			q := RatchetQuarantine{ID: qid, Code: code, SeenAt: float64(c.d.Now().UnixMilli()) / 1000}
			if m := d.Manifest; m != nil {
				q.Sender, q.Scope = m.Sender, m.Scope
				switch m.Scope {
				case "direct":
					q.Peer = m.Sender
					if m.Sender == c.d.Namespace.Principal {
						q.Peer = m.Peer
					}
				case "group":
					q.Conversation = m.Conversation
				}
			}
			c.quarantined[qid] = q
			c.qorder = append(c.qorder, qid)
		}
		// Continue across filtered empty pages; a fresh drain restarts at "".
		if !page.HasMore {
			return c.quarantineList(), nil
		}
		if page.After == "" || page.After == after {
			return nil, rfail("invalid")
		}
		after = page.After
	}
	return nil, rfail("exhausted")
}

func (c *ratchetConsumer) unquarantine(id string) {
	if _, ok := c.quarantined[id]; !ok {
		return
	}
	delete(c.quarantined, id)
	for i, q := range c.qorder {
		if q == id {
			c.qorder = append(c.qorder[:i], c.qorder[i+1:]...)
			break
		}
	}
}

func (c *ratchetConsumer) quarantineList() []RatchetQuarantine {
	out := make([]RatchetQuarantine, 0, len(c.qorder))
	for _, id := range c.qorder {
		out = append(out, c.quarantined[id])
	}
	return out
}

// resetTrust runs after the user explicitly confirmed a contact's changed
// identity: it drops that principal's noXa/Olm pins so the next use pins the
// newly trusted chain and re-adopts Olm keys only from bundles it signs.
func (c *ratchetConsumer) resetTrust(principal string) error {
	c.serial.Lock()
	defer c.serial.Unlock()
	if err := c.check(); err != nil {
		return err
	}
	refs, err := c.refs(durable.Trust)
	if err != nil {
		return err
	}
	for _, ref := range refs {
		var parsed []string
		if json.Unmarshal([]byte(ref.ID), &parsed) != nil || len(parsed) != 2 || parsed[0] != principal {
			continue
		}
		s, err := c.read(ref)
		if err != nil {
			return err
		}
		_, revoked, saved, err := readTrust(s, ref.ID)
		if err != nil || revoked {
			continue
		}
		saved.Noxa, saved.Pin = nil, nil
		if err = c.d.Store.SetTrust(s, ref.ID, ratchetJSON(saved), false); err != nil {
			return err
		}
	}
	for _, id := range append([]string(nil), c.qorder...) {
		if c.quarantined[id].Sender == principal {
			c.unquarantine(id)
		}
	}
	return nil
}

func ratchetEnvelopeDigest(envelope []byte) string {
	h := sha256.Sum256(envelope)
	return hex.EncodeToString(h[:])
}

func compactJSON(raw json.RawMessage) []byte {
	var b bytes.Buffer
	if json.Compact(&b, raw) != nil {
		return raw
	}
	return b.Bytes()
}

func (c *ratchetConsumer) receive(d ratchetDelivery, a durable.AccountAuthority) error {
	// Malformed server data is a deterministic, coded failure (placeholder),
	// not an uncoded error retried silently forever.
	if d.Manifest == nil || d.Manifest.Validate() != nil || !ratchetValidDeliveryID(d.ID) || d.Copy == nil || d.SourceBundle == nil || d.TargetBundle == nil {
		return rfail("invalid")
	}
	m, cp, ns := *d.Manifest, *d.Copy, c.d.Namespace
	if m.Server != ns.Server || cp.Principal != ns.Principal || cp.Device != ns.Device || cp.AccountEpoch != a.Epoch {
		return rfail("fenced_copy")
	}
	if admission.CheckEnvelope(m, cp) != nil {
		return rfail("invalid")
	}
	envelope := compactJSON(cp.Envelope)
	iref := durable.Ref{Kind: durable.Inbox, ID: d.ID}
	s, err := c.read(iref)
	if err != nil {
		return err
	}
	var record RatchetRecord
	isNew := false
	if saved := s.Records[iref]; saved.Revision > 0 {
		// Committed before a lost ACK: exact stored outcome, no re-decrypt.
		var prior struct {
			Digest  string `json:"digest"`
			Archive []byte `json:"archive"`
		}
		if json.Unmarshal(saved.Data, &prior) != nil || json.Unmarshal(prior.Archive, &record) != nil {
			return rfail("corrupt")
		}
		if prior.Digest != ratchetEnvelopeDigest(envelope) {
			return rfail("digest_mismatch")
		}
	} else {
		var own, source *admission.Target
		for i := range m.Targets {
			t := &m.Targets[i]
			if t.Device == cp.Device && t.Principal == cp.Principal {
				own = t
			}
			if t.Device == m.Device && t.Principal == m.Sender && t.Local {
				source = t
			}
		}
		if own == nil || own.Local || own.Identity != c.identity || own.AccountEpoch != a.Epoch || d.TargetBundle.Identity != c.identity || d.TargetBundle.AccountEpoch != a.Epoch {
			return rfail("fenced_copy")
		}
		if source == nil || source.AccountEpoch != m.AccountEpoch {
			return rfail("invalid")
		}
		trusted, err := c.trust(m.Sender, m.Device)
		if err != nil {
			return err
		}
		expected := d.SourceBundle.Identity
		if trusted.pin != nil {
			expected = trusted.pin.Identity
		}
		// Retained admission evidence is authenticated under the current
		// independently trusted noXa device key, without publication TTL.
		if err = verifyOlmBundleEvidence(*d.SourceBundle, olmTrustExpectation{SigningKey: trusted.key, Server: m.Server, Principal: m.Sender, Device: m.Device,
			AccountEpoch: m.AccountEpoch, Identity: expected}); err != nil {
			return &ratchetError{Code: "invalid_evidence", Err: err}
		}
		if d.SourceBundle.Identity != source.Identity {
			return rfail("invalid")
		}
		if trusted, err = c.adopt(trusted, *d.SourceBundle); err != nil {
			return err
		}
		peer := admission.Target{Principal: m.Sender, Device: m.Device, AccountEpoch: m.AccountEpoch, Identity: trusted.pin.Identity}
		mine := admission.Target{Principal: cp.Principal, Device: cp.Device, AccountEpoch: cp.AccountEpoch}
		binding := admission.ExpectedBinding(m, mine)
		context := c.sessionContext(a, peer)
		sessionID := durable.SessionRef(cp.SessionID)
		sref := durable.Ref{Kind: durable.Session, ID: sessionID}
		accountRef := durable.Ref{Kind: durable.Account, ID: a.AccountID}
		refs := []durable.Ref{{Kind: durable.Trust, ID: trusted.id}, sref, iref, accountRef}
		consumed := ""
		probe, err := c.read(sref)
		if err != nil {
			return err
		}
		var env ratchet.Envelope
		if json.Unmarshal(envelope, &env) != nil {
			return rfail("invalid")
		}
		if probe.Records[sref].Revision == 0 {
			// Only an Olm prekey message may open a receive chain, and only
			// with a retained local obligation; never by TTL or server data.
			var msg struct {
				Type int    `json:"type"`
				Body string `json:"body"`
			}
			if json.Unmarshal(env.Message, &msg) != nil || msg.Type != 0 {
				return rfail("lost_key")
			}
			otk, err := ratchetInitialOneTimeKey(msg.Body)
			if err != nil {
				return err
			}
			obRefs, err := c.refs(durable.Obligation)
			if err != nil {
				return err
			}
			obligations, err := c.readMany(obRefs)
			if err != nil {
				return err
			}
			for _, ref := range obRefs {
				var o durable.OTKObligation
				if json.Unmarshal(obligations[ref].Data, &o) == nil && o.AccountID == a.AccountID && o.PublicKey == otk && !o.Closed && !o.Consumed {
					consumed = ref.ID
					break
				}
			}
			if consumed == "" {
				return rfail("lost_key")
			}
			refs = append(refs, durable.Ref{Kind: durable.Obligation, ID: consumed})
		}
		if s, err = c.read(refs...); err != nil {
			return err
		}
		state, exists, err := readSession(s, sessionID)
		if err != nil {
			return err
		}
		var result ratchet.Response
		if exists {
			if consumed != "" {
				return rfail("conflict")
			}
			// A pre-device-pair record or a session bound to another peer
			// identity/epoch is a deterministic failure.
			if state.Context.Scope != durable.DevicePairScope {
				return rfail("legacy_session")
			}
			if state.Context != context {
				return rfail("session_mismatch")
			}
			result, err = ratchetDeterministic("unauthenticated", func() (ratchet.Response, error) {
				return c.coreApply(ratchet.Request{Op: ratchet.Decrypt, Session: string(state.Snapshot), Binding: &binding, Envelope: &env})
			})
		} else {
			if consumed == "" {
				return rfail("conflict")
			}
			result, err = ratchetDeterministic("unauthenticated", func() (ratchet.Response, error) {
				return c.coreApply(ratchet.Request{Op: ratchet.Inbound, Account: string(s.Records[accountRef].Data), PeerIdentity: peer.Identity, Binding: &binding, Envelope: &env})
			})
			if err == nil && durable.SessionRef(result.SessionID) != sessionID {
				return rfail("invalid")
			}
		}
		if err != nil {
			return err
		}
		body, err := ratchetDeterministic("invalid", func() (json.RawMessage, error) { return admission.OpenPayload(m, mine, result.Plaintext) })
		if err != nil {
			return err
		}
		self := m.Sender == ns.Principal
		record = RatchetRecord{Protocol: ratchetProtocol, Provenance: "authenticated", Sender: m.Sender, SenderDevice: m.Device, LogicalID: m.LogicalID,
			Key: ratchetSemanticKey(m.Sender, m.LogicalID), Scope: m.Scope, Purpose: m.Purpose, Reference: m.Reference, Body: body,
			SentAt: ratchetBodySentAt(body, float64(c.d.Now().UnixMilli())/1000), Self: self, Copied: self, Transport: RatchetTransport{Delivery: d.ID}}
		if m.Scope == "direct" {
			record.Peer = m.Sender
			if self {
				record.Peer = m.Peer
			}
		} else {
			record.Conversation = m.Conversation
		}
		if m.Reference != "" {
			record.ReferenceKey = ratchetSemanticKey(m.Sender, m.Reference)
		}
		candidate := durable.ReceiveCandidate{AccountID: a.AccountID, SessionID: sessionID, TrustID: trusted.id, Session: []byte(result.Session), Envelope: envelope, Archive: ratchetJSON(record), Context: context}
		if consumed != "" {
			candidate.Account, candidate.ConsumedOTK = []byte(result.Account), consumed
		}
		if _, err = c.d.Store.CommitReceive(s, d.ID, candidate); err != nil {
			return err
		}
		if err = c.check(); err != nil {
			return err
		}
		if c.sessions != nil {
			c.sessions[context] = sessionID
		}
		isNew = true
	}
	// Archive/session/OTK/dedup are durable before projection and the exact
	// ACK. A replay after a lost ACK projects isNew=false (no unread/notify).
	if err = c.emit(record, isNew); err != nil {
		return err
	}
	if err = c.handoff(a, netproto.OlmAdmissionRequest{Action: "receive_ack", DeliveryID: d.ID, Digest: d.Digest}, nil); err != nil {
		return &ratchetError{Code: ratchetCode(err), AfterCommit: true, Err: err}
	}
	return nil
}
