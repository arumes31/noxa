package main

// ratchet_recovery.go: explicit, user-initiated recovery of a restored or
// lost secure-message account (1c-iii-b item 2).
//
// A restored device (identity backup imported, store missing) or a device
// whose store was lost is refused by the server as recovery_required. This
// flow gives it a fresh authenticated account epoch:
//
//  1. Read this device's OWN latest signed publication from the server
//     (directory "own_state"). It is only input: it must verify under this
//     device's own noXa key, and its epoch may never be older than the local
//     store's. A server that lies is refused before anything is signed.
//  2. Create a fresh Olm account, sign its bundle and a noxa-olm-reset-v1
//     transition (old epoch/identity/signing from step 1 -> new) with the noXa
//     key, and durably record the intent (PrepareRecoveryTransition; old
//     sending stops in the same commit).
//  3. Submit the reset; the server verifies it against its own state. Peers
//     verify the signed chain from their cached pins before they adopt it.
//  4. Promote only on the exact receipt for this intent.
//
// The old readable archive and receive-only state are kept. Copies sent to
// the lost keys cannot be opened; they stay quarantined with a truthful
// placeholder and are never acknowledged.
//
// Fork limit (no local state to compare against): a hostile server may
// present an older, genuinely signed publication of this device as
// own_state, and this device then signs a second successor of that old
// epoch. The honest directory refuses it and peers pinned later reject it;
// peers that see two different signed successors of one epoch in the chain
// adopt neither (olmdir.VerifyResetChain) and require explicit confirmation.
// A server that withholds the genuine successor can still steer peers pinned
// at or before the old epoch onto the new branch, which carries this
// device's own fresh keys (no key compromise).
//
// The applied/not-applied evidence (recoveryEvidence) is server data: it is
// trustworthy only from an honest server. A hostile server can replay the
// genuine pre-recovery publication after applying a reset and make the
// device abandon an applied intent; that costs a wasted epoch and leaves a
// second signed successor of the old epoch (handled by the fork rule above),
// which such a server could cause anyway by refusing before applying.

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"

	"noxa/internal/e2ee/admission"
	"noxa/internal/e2ee/durable"
	"noxa/internal/e2ee/olmdir"
	"noxa/internal/e2ee/ratchet"
	"noxa/internal/netproto"
)

// recoverAccount runs the explicit recovery. reason is "restored" or "lost".
func (c *ratchetConsumer) recoverAccount(reason string) error {
	c.serial.Lock()
	defer c.serial.Unlock()
	if reason != "restored" && reason != "lost" {
		return rfail("invalid")
	}
	if c.d.SignReset == nil || len(c.d.OwnKey) != ed25519.PublicKeySize {
		return rfail("unavailable")
	}
	if err := c.check(); err != nil {
		return err
	}
	ns := c.d.Namespace
	s, err := c.read()
	if err != nil {
		return err
	}
	local, err := c.d.Store.AccountAuthority(s.Fence)
	if err != nil {
		return err
	}
	// A transition prepared earlier (crash before promotion) is resumed with
	// its exact recorded intent, never re-derived from new server data.
	if t, id, ok := c.pendingRecovery(s.Fence); ok {
		return c.finishRecovery(local, t, id)
	}
	if local.Blocked {
		return rfail("fenced")
	}
	own, err := c.directory(netproto.OlmDirectoryRequest{Action: "own_state", RequestID: newClientMsgID()})
	if ratchetCode(err) == "absent" {
		// Nothing published yet: an ordinary first publication applies.
		return rfail("not_needed")
	}
	if err != nil {
		return err
	}
	if own.Error != "" || own.Bundle == nil {
		if own.Error == "absent" {
			// Nothing published yet: an ordinary first publication applies.
			return rfail("not_needed")
		}
		return rfail("unavailable")
	}
	cur := *own.Bundle
	// Hostile-server guards: the server's claim about this device must be a
	// publication this device's independently held noXa key signed for this
	// principal/device, and never older than the local store. Its signed
	// historical server binding may precede an explicitly accepted TLS change.
	// This exception is only recovery evidence: the new reset and publication
	// remain bound to ns.Server, and no old namespace or peer trust is imported.
	if cur.VerifyEvidence(c.d.OwnKey, cur.Server, ns.Principal, ns.Device) != nil || cur.AccountEpoch < 1 || cur.Generation < 1 ||
		cur.AccountEpoch < local.Epoch || cur.AccountEpoch >= 9007199254740991 {
		return rfail("invalid_evidence")
	}
	if latest, _ := c.latestPublication(local); latest != nil && latest.AccountEpoch == cur.AccountEpoch && latest.Identity == cur.Identity {
		// The local store still holds the server's current account.
		return rfail("not_needed")
	}
	created, err := c.coreApply(ratchet.Request{Op: ratchet.NewAccount, Count: ratchetPreKeys})
	if err != nil {
		return err
	}
	if created.Bundle == nil || created.Account == "" {
		return rfail("core_unavailable")
	}
	epoch, generation := cur.AccountEpoch+1, cur.Generation+1
	accountID := fmt.Sprintf("epoch-%d-%s", epoch, newClientMsgID())
	transition := fmt.Sprintf("pub:%s:%010d", accountID, generation)
	issued := c.now()
	b := olmdir.Bundle{Version: olmdir.Version, Protocol: olmdir.Protocol, Server: ns.Server, Principal: ns.Principal, DeviceID: ns.Device,
		AccountEpoch: epoch, Generation: generation, IssuedAt: issued, ExpiresAt: issued + 6*86400, Identity: created.Bundle.Identity, Signing: created.Bundle.Signing}
	for _, k := range created.Bundle.OneTimeKeys {
		b.OneTimeKeys = append(b.OneTimeKeys, olmdir.Key{ID: k.ID, Key: k.Key})
	}
	if b, err = c.d.SignBundle(b); err != nil {
		return err
	}
	digest, err := olmdir.PublicationDigest(b)
	if err != nil {
		return rfail("invalid")
	}
	r, err := c.d.SignReset(olmdir.Reset{Version: olmdir.ResetVersion, Server: ns.Server, Principal: ns.Principal, Device: ns.Device, TransitionID: transition,
		OldEpoch: cur.AccountEpoch, OldIdentity: cur.Identity, OldSigning: cur.Signing, NewEpoch: epoch, NewIdentity: b.Identity, NewSigning: b.Signing,
		Generation: generation, PublicationDigest: digest, Reason: reason})
	if err != nil {
		return err
	}
	otks := map[string]durable.OTKObligation{}
	for _, k := range b.OneTimeKeys {
		otks[accountID+":"+k.ID] = durable.OTKObligation{AccountID: accountID, AccountEpoch: epoch, PublicationID: transition, ExpiresAt: b.ExpiresAt, KeyID: k.ID, PublicKey: k.Key}
	}
	if err = c.check(); err != nil {
		return err
	}
	if err = c.d.Store.PrepareRecoveryTransition(s, accountID, []byte(created.Account), r, b, otks); err != nil {
		return err
	}
	return c.finishRecovery(local, durable.AccountTransition{AccountID: accountID, Reset: r, Bundle: b}, transition)
}

// pendingRecovery finds a prepared, unpromoted recovery transition.
func (c *ratchetConsumer) pendingRecovery(fence uint64) (durable.AccountTransition, string, bool) {
	refs, err := c.refs(durable.Transition)
	if err != nil {
		return durable.AccountTransition{}, "", false
	}
	for _, ref := range refs {
		t, err := c.d.Store.AccountTransitionRequest(fence, ref.ID)
		if err == nil && t.Recovery && t.Receipt == nil && !t.Abandoned {
			return t, ref.ID, true
		}
	}
	return durable.AccountTransition{}, "", false
}

// finishRecovery submits the recorded reset and promotes on its exact receipt.
//
// A failed submission never gives the intent up on the refusal alone: the
// server may already have applied it (the response was lost, and a replay is
// refused by a rules/role gate or a hostile server). The server's own_state
// decides: this intent's exact publication at the new epoch is proof it was
// applied (promote); the unchanged pre-recovery epoch with another
// publication is proof it was not (abandon on a deterministic refusal);
// anything else keeps the intent pending for a later retry.
func (c *ratchetConsumer) finishRecovery(local durable.AccountAuthority, t durable.AccountTransition, id string) error {
	r, b := t.Reset, t.Bundle
	var receipt olmdir.ResetReceipt
	// The source epoch of the reset is the server's current one (r.OldEpoch),
	// not the restored store's.
	if err := c.call(netproto.OlmAdmissionRequest{Action: "reset", AccountEpoch: r.OldEpoch, Reset: &r, Bundle: &b}, &receipt); err != nil {
		switch c.recoveryEvidence(r) {
		case recoveryApplied:
			return c.promoteRecovery(id, recoveryReceipt(r))
		case recoveryNotApplied:
			switch ratchetCode(err) {
			case "authority-required", "account-change-required", "conflict", "invalid", "limit":
				// A deterministic refusal of an intent the server positively
				// did not apply: give it up so sending authority is restored
				// and a later explicit recovery can start over.
				if e := c.abandonRecovery(id); e != nil {
					return e
				}
				return err
			}
		}
		return recoveryPending(err)
	}
	encoded, _ := json.Marshal(r)
	digest, _ := olmdir.PublicationDigest(b)
	if receipt.Server != r.Server || receipt.Device != r.Device || receipt.TransitionID != r.TransitionID || receipt.AccountEpoch != r.NewEpoch ||
		receipt.PublicationDigest != digest || receipt.Digest != admission.Digest(encoded) {
		return recoveryPending(rfail("invalid"))
	}
	return c.promoteRecovery(id, receipt)
}

// recoveryPending marks a failure that leaves the recovery intent pending
// (sending stays paused until it completes or is cancelled).
func recoveryPending(err error) error {
	return &ratchetError{Code: "recovery_pending", Err: err}
}

func (c *ratchetConsumer) promoteRecovery(id string, receipt olmdir.ResetReceipt) error {
	s, err := c.read(durable.Ref{Kind: durable.Transition, ID: id})
	if err != nil {
		return err
	}
	if err = c.d.Store.PromoteAccount(s, id, receipt); err != nil {
		return err
	}
	// The new account is ready: the sticky refusal is lifted and the next
	// operation publishes (idempotently) and refreshes normally.
	c.setSticky(nil)
	return c.check()
}

func (c *ratchetConsumer) abandonRecovery(id string) error {
	s, err := c.read(durable.Ref{Kind: durable.Transition, ID: id})
	if err != nil {
		return err
	}
	return c.d.Store.AbandonRecoveryTransition(s, id)
}

// recoveryReceipt is the receipt the server returns for r. It carries no
// server secret: every field is derived from the recorded reset, so the
// server holding r's exact publication at the new epoch proves the same.
func recoveryReceipt(r olmdir.Reset) olmdir.ResetReceipt {
	encoded, _ := json.Marshal(r)
	return olmdir.ResetReceipt{Server: r.Server, Device: r.Device, TransitionID: r.TransitionID, AccountEpoch: r.NewEpoch,
		Digest: admission.Digest(encoded), PublicationDigest: r.PublicationDigest}
}

type recoveryState int

const (
	recoveryUnknown recoveryState = iota
	recoveryApplied
	recoveryNotApplied
)

// recoveryEvidence reads this device's own_state: applied when the server
// holds exactly the recorded publication at the new epoch, not applied when
// it still holds another publication at the pre-recovery epoch. Unverifiable
// or other answers (and errors) are unknown.
func (c *ratchetConsumer) recoveryEvidence(r olmdir.Reset) recoveryState {
	own, err := c.directory(netproto.OlmDirectoryRequest{Action: "own_state", RequestID: newClientMsgID()})
	if err != nil || own.Error != "" || own.Bundle == nil {
		return recoveryUnknown
	}
	ns := c.d.Namespace
	b := *own.Bundle
	// The unchanged old publication may retain the previous TLS binding.
	if b.VerifyEvidence(c.d.OwnKey, b.Server, ns.Principal, ns.Device) != nil {
		return recoveryUnknown
	}
	digest, err := olmdir.PublicationDigest(b)
	if err != nil {
		return recoveryUnknown
	}
	switch {
	case b.Server == ns.Server && b.AccountEpoch == r.NewEpoch && digest == r.PublicationDigest:
		return recoveryApplied
	case b.AccountEpoch == r.OldEpoch && digest != r.PublicationDigest:
		return recoveryNotApplied
	}
	return recoveryUnknown
}

// cancelRecovery is the user's explicit exit from a pending recovery intent.
// It abandons the intent only on positive evidence that the server did not
// apply it; an applied intent is completed instead. It returns "completed"
// or "cancelled".
func (c *ratchetConsumer) cancelRecovery() (string, error) {
	c.serial.Lock()
	defer c.serial.Unlock()
	if err := c.check(); err != nil {
		return "", err
	}
	s, err := c.read()
	if err != nil {
		return "", err
	}
	t, id, ok := c.pendingRecovery(s.Fence)
	if !ok {
		return "", rfail("not_needed")
	}
	switch c.recoveryEvidence(t.Reset) {
	case recoveryApplied:
		return "completed", c.promoteRecovery(id, recoveryReceipt(t.Reset))
	case recoveryNotApplied:
		return "cancelled", c.abandonRecovery(id)
	}
	return "", rfail("recovery_unknown")
}

// RecoverSecureMessagingForTab is the explicit, user-initiated recovery
// (Settings > Security). It returns "" on success or a user-facing reason.
func (a *App) RecoverSecureMessagingForTab(tabID string) string {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return err.Error()
	}
	c, err := cm.ratchetConsumerOn(nil)
	if err != nil {
		return "secure messaging is not active on this connection"
	}
	if err = c.recoverAccount("restored"); err != nil {
		return recoveryFailureText(err)
	}
	return ""
}

// CancelSecureMessagingRecoveryForTab is the explicit exit from a recovery
// that stays pending. It returns the outcome ("completed": the server had
// applied it; "cancelled": it was not applied and was given up) or a
// user-facing error.
func (a *App) CancelSecureMessagingRecoveryForTab(tabID string) (string, error) {
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return "", err
	}
	c, err := cm.ratchetConsumerOn(nil)
	if err != nil {
		return "", errors.New("secure messaging is not active on this connection")
	}
	outcome, err := c.cancelRecovery()
	if err != nil {
		return "", errors.New(recoveryFailureText(err))
	}
	return outcome, nil
}

// recoveryFailureText maps recovery failures to user-facing reasons.
func recoveryFailureText(err error) string {
	switch ratchetCode(err) {
	case "not_needed":
		return "secure messaging on this device does not need recovery"
	case "invalid_evidence":
		return "the server's record of this device could not be verified; recovery was refused"
	case "authority-required", "account-change-required", "conflict":
		return "the server refused the recovery; try again later or link this device again"
	case "fenced":
		return "another window is changing secure messaging on this device; try again"
	case "recovery_pending":
		return "the recovery is waiting for the server, so sending secure messages stays paused; try again later, or cancel the recovery"
	case "recovery_unknown":
		return "the server's record does not show whether the recovery was applied, so it cannot be cancelled safely yet; try again later"
	}
	return "secure messaging recovery failed: " + err.Error()
}
