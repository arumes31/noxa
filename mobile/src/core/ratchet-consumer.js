// ratchet-consumer.js — session-owned noxa-olm-v1 product consumer for the PWA.
//
// One instance belongs to exactly one authenticated connection, account fence
// and protected store namespace. Activation is supplied by that lifecycle; no
// capability, trust or storage failure ever selects legacy NaCl sending or
// decoding. Callers (DirectMessages/Groups) receive projections through
// project(); the protected archive in RatchetStore is authoritative.
//
// Identities are kept distinct:
//   protocol/provenance   record.protocol / record.provenance
//   semantic reference    record.key = semanticKey(sender, logicalID)
//   transport identifiers record.transport = {message, delivery}
// Edit/delete references always name the ORIGINAL sender's logical ID.
import {
    ADMISSION_VERSION,
    checkEnvelope,
    expectedBinding,
    equalJSON,
    freezeAttempt,
    jsonBytes,
    validateManifest,
    openPayload,
    recoveryReceipt,
    sealPayload,
} from "./olm-admission.js";
import { verifyBundleEvidence, verifyReservedClaim, OLM_DIRECTORY_VERSION } from "./olm-directory.js";
import { verifyResetChain } from "./olm-reset.js";

export const RATCHET_PROTOCOL = "noxa-olm-v1";
/** Mirror of ratchet-store DEVICE_PAIR_SCOPE / durable.DevicePairScope. */
export const DEVICE_PAIR_SCOPE = "device-pair";
export const Limits = Object.freeze({ direct: 20, group: 160, prekeys: 20, refill: 10, retireWindow: 64 });
const TERMINAL = new Set(["accepted", "rejected", "aborted"]);
const enc = new TextEncoder(), dec = new TextDecoder();
const parse = b => JSON.parse(dec.decode(b instanceof Uint8Array ? b : Uint8Array.from(b)));
const rk = (kind, id) => JSON.stringify([kind, id]);
const rec = (s, kind, id) => s.records[rk(kind, id)];
const b64 = b => { let s = ""; for (let i = 0; i < b.length; i += 8192) s += String.fromCharCode(...b.subarray(i, i + 8192)); return btoa(s); };
const fail = (code, extra = {}) => Object.assign(new Error(`ratchet: ${code}`), { code, ...extra });
const now = () => Math.floor(Date.now() / 1000);
// A pin left behind by a verified reset receives delayed deliveries only this
// long after its retirement (never later: the old key may have been lost).
const HISTORICAL_RECEIVE_WINDOW = 7 * 24 * 3600;
const delay = ms => new Promise(r => setTimeout(r, ms));
/** Local CAS losses are retried from scratch at most this often. */
const LOCAL_RETRY = 3;
/** Seconds before a deadline at which an unstarted attempt is aborted, not begun. */
const DEADLINE_MARGIN = 30;

/** Pure fanout bound: direct <= 20 and group <= 160 complete targets. */
export function checkFanout(scope, targets) {
    if (!Array.isArray(targets) || !targets.length || targets.length > Limits[scope]) throw fail("fanout_limit");
}

/**
 * The unretired committed attempts one drain resolves: the OLDEST window first
 * (an unresolved old attempt is a gap that keeps the retired-through watermark,
 * and so admission backpressure, stuck: it is never stranded behind newer
 * ones) plus the newest window, in sequence order without duplicates. Callers
 * pass only attempts not yet retired (durably or in this session). Reach is
 * conditional: attempts in the middle are reached as the oldest window
 * retires; an old attempt whose retirement fails permanently keeps its slot.
 */
export function resolveOrder(ids, window) {
    const sorted = [...ids].sort((x, y) => Number(x.split(":")[1]) - Number(y.split(":")[1]));
    return sorted.length <= 2 * window ? sorted : [...sorted.slice(0, window), ...sorted.slice(-window)];
}

/**
 * Archive replay keeps only the newest attempt per logical ID, ordered by
 * numeric attempt sequence (a reseal after abort/reject supersedes it).
 */
export function latestAttempts(entries) {
    const order = id => String(id).split(":").map(Number);
    const cmp = (x, y) => { const [ex, sx] = order(x.id), [ey, sy] = order(y.id); return ex - ey || sx - sy; };
    const latest = new Map();
    for (const e of [...entries].sort(cmp)) latest.set(e.record.logicalID, e);
    return [...latest.values()];
}

/**
 * Receive failures that a retry cannot fix (authentication, evidence, trust,
 * lost key, legacy/mismatched session). Only these are quarantined and
 * skipped; anything else (transport, rate limit, unavailable, local CAS) is
 * retried on the next poll.
 */
export const DETERMINISTIC_RECEIVE = new Set(["invalid", "unauthenticated", "invalid_evidence", "lost_key", "fenced_copy", "untrusted_device",
    "revoked", "account_changed", "identity_changed", "legacy_session", "session_mismatch", "digest_mismatch"]);
/** Retryable receive conditions (environment, transport, local CAS). */
export const TRANSIENT_RECEIVE = new Set(["core_unavailable", "fenced", "rate-limit", "unavailable", "conflict", "aborted", "local_conflict"]);
/**
 * Runs one verification/crypto stage. Only known STRING codes from our own
 * allow-lists pass through unchanged; anything else (uncoded core or
 * verification rejections, DOMException numeric codes, TypeErrors from
 * malformed data) becomes the stage's deterministic code.
 */
export async function deterministic(code, work) {
    try { return await work(); }
    catch (e) {
        if (typeof e?.code === "string" && (DETERMINISTIC_RECEIVE.has(e.code) || TRANSIENT_RECEIVE.has(e.code))) throw e;
        throw fail(code, { cause: e });
    }
}
/** Deterministic failures that a concurrent commit of the SAME delivery by another tab can cause. */
const RACE_RECEIVE = new Set(["lost_key", "unauthenticated"]);
/**
 * What #drain does with a receive failure: "retry" next poll, "skip" (another
 * tab committed this delivery; its stored outcome is used next poll, no
 * placeholder) or "quarantine" (locked placeholder, never ACKed).
 */
export function receiveDisposition(code, committedElsewhere) {
    if (!DETERMINISTIC_RECEIVE.has(code)) return "retry";
    if (committedElsewhere && RACE_RECEIVE.has(code)) return "skip";
    return "quarantine";
}
const validDeliveryID = id => typeof id === "string" && id.length > 0 && new TextEncoder().encode(id).length <= 512 && !/[\p{Cc}\p{Cf}]/u.test(id);
/** Collision-free UI/semantic key for one sender's logical message ID. */
export const semanticKey = (sender, logicalID) => JSON.stringify([RATCHET_PROTOCOL, sender, logicalID]);

/**
 * deps:
 *  store, core          protected RatchetStore + authenticated WASM core
 *  namespace            {server, principal, device} of the authenticated session
 *  admission(request)   frame 202 -> 203 data; throws {code} on result.error
 *  directory(request)   frame 200 -> 201 result; throws {code}
 *  deviceKey(p, d)      Ed25519 noXa device key from the independently pinned
 *                       root chain (NOT from Olm/admission data); throws if absent
 *  signBundle(bundle)   signs with the EXISTING noXa device identity
 *  assertCurrent()      throws unless connection/account/identity generation
 *                       that created this consumer is still current
 *  trustReset(p), trustResetDone(p)   optional: a durable, explicitly confirmed
 *                       identity change of p whose reset is still pending
 */
export class RatchetConsumer {
    constructor({ store, core, namespace, admission, directory, deviceKey, signBundle, assertCurrent, trustReset = null, trustResetDone = null, attemptTTL = 300, refreshMS = 3600000, signReset = null, ownKey = null }) {
        // attemptTTL must exceed the abort margin (else every attempt expires at
        // once and loops) and leave a fresh 1 h claim able to cover a resend
        // (else every pending resend spends a new claim).
        if (!Number.isSafeInteger(attemptTTL) || attemptTTL < DEADLINE_MARGIN + 5 || attemptTTL > 3000) throw fail("invalid_config");
        Object.assign(this, { store, core, namespace, admission, directoryRequest: directory, deviceKey, signBundle, assertCurrent, trustReset, trustResetDone, attemptTTL, refreshMS, signReset, ownKey });
        this.readyAt = 0;
        this.readyValue = null;   // last successful readiness {identity, epoch, expiresAt}
        this.claims = new Map();  // verified, not yet committed claims kept across local_conflict retries
        this.skewed = new Set();  // attempts the server refuses as invalid while locally unexpired
        this.closed = false;
        this.tail = Promise.resolve();
        this.projectors = [];
        this.quarantined = new Map();
        this.sessions = null;      // JSON(context) -> session ref id
        this.retired = new Set();  // attempt IDs retired during this session
        this.unretirable = new Set(); // obligations the core could not retire (not re-asked this session)
        this.readiness = null;
        this.identity = "";
    }

    /** Registers async fn(record, {isNew}) awaited after each durable commit. */
    project(fn) { this.projectors.push(fn); }
    #check() { if (this.closed) throw fail("fenced"); this.assertCurrent(); }
    #serial(work) {
        const next = this.tail.then(() => { this.#check(); return work(); });
        this.tail = next.catch(() => {});
        return next;
    }
    async close() { this.closed = true; await this.tail; }
    async #emit(record, isNew) {
        this.#check();
        for (const fn of this.projectors) {
            try { await fn(record, { isNew }); } catch { /* views are not receipt proof */ }
            this.#check();
        }
    }
    /**
     * Server rate limits are checked before any handler action, so the same
     * request is retried with bounded backoff; afterwards the caller sees a
     * retryable rate-limit with a retry hint.
     */
    async #net(fn) {
        for (let i = 0; ; i++) {
            this.#check();
            try {
                const result = await fn();
                this.#check();
                return result;
            } catch (e) {
                if (e.code !== "rate-limit") throw e;
                if (i >= 7) throw Object.assign(e, { retryAfterMS: 3000 });
                await delay(400 * (i + 1));
            }
        }
    }
    #call(request) { return this.#net(() => this.admission(request)); }
    directory(request) { return this.#net(() => this.directoryRequest(request)); }
    async #authority() {
        this.#check();
        const a = await this.store.accountAuthority(await this.store.read());
        this.#check();
        if (a.blocked || !a.accountID || !(a.epoch >= 1)) throw fail("recovery_required");
        return a;
    }
    /** Authority is rechecked immediately before every network handoff. */
    async #handoff(a, request) {
        await this.store.checkAccountAuthority(await this.store.read(), a);
        return this.#call({ account_epoch: a.epoch, ...request });
    }

    // --- Readiness and own publication ----------------------------------------

    /**
     * Only recovery_required (an account the server refuses) is sticky. Other
     * failures retry on the next call, and a long-lived session re-runs the
     * idempotent publication step so it refreshes before expiry.
     */
    ready() {
        if (this.sticky) { const p = Promise.reject(this.sticky); p.catch(() => {}); return p; }
        const due = this.readiness && this.readyAt && Date.now() - this.readyAt > this.refreshMS;
        if (due && !this.refreshing) {
            // Background refresh: the last good readiness stays in force while
            // its publication is still valid; only recovery_required replaces it.
            const previous = this.readiness;
            this.refreshing = this.#serial(() => this.#ready());
            this.refreshing.then(v => { this.readyValue = v; this.readiness = Promise.resolve(v); this.readyAt = Date.now(); },
                e => {
                    if (e.code === "recovery_required") { this.sticky = e; this.readiness = null; }
                    else if (!(this.readyValue?.expiresAt > now() + 300)) this.readiness = null;
                    else {
                        // Keep the valid readiness; retry the refresh in a minute.
                        this.readiness = previous;
                        this.readyAt = Date.now() - this.refreshMS + 60000;
                    }
                }).finally(() => { this.refreshing = null; });
            if (this.readyValue?.expiresAt > now() + 300) return this.readiness;
            return this.refreshing;
        }
        if (!this.readiness) {
            const attempt = this.#serial(() => this.#ready());
            this.readiness = attempt;
            attempt.then(v => { if (this.readiness === attempt) { this.readyAt = Date.now(); this.readyValue = v; } },
                e => {
                    if (this.readiness !== attempt) return;
                    if (e.code === "recovery_required") this.sticky = e;
                    this.readiness = null;
                });
        }
        return this.readiness;
    }
    async #ready() {
        const a = await this.#authority();
        let s = await this.store.read([["account", a.accountID]]);
        if (!rec(s, "account", a.accountID).revision) {
            // Only a fresh epoch-1 namespace may create an account. The server
            // refuses epoch 1 over an existing device account; that blocks below
            // instead of silently replacing old keys.
            if (a.epoch !== 1) throw fail("recovery_required");
            const created = await this.core.apply({ op: "new_account", key: this.store.coreKey(s), count: Limits.prekeys });
            this.#check();
            await this.#commitBundle(s, a, created, 1);
        }
        let latest = await this.#latestPublication(a);
        if (!latest) throw fail("recovery_required");
        // Close what the server proves can no longer be admitted before the
        // refill (the account holds at most 100 private one-time keys).
        try { await this.#closeKeys(a); }
        catch (e) { if (e.code === "fenced" || e.code === "recovery_required") throw e; }
        if (latest.bundle.expires_at - now() < 86400) {
            // Refresh before expiry with new one-time keys and next generation.
            s = await this.store.read([["account", a.accountID]]);
            const refreshed = await this.core.apply({ op: "prekeys", key: this.store.coreKey(s), account: dec.decode(rec(s, "account", a.accountID).data), count: Limits.refill });
            this.#check();
            await this.#commitBundle(s, a, refreshed, latest.bundle.generation + 1, latest.bundle);
            latest = await this.#latestPublication(a);
        }
        try {
            await this.store.checkAccountAuthority(await this.store.read(), a);
            await this.directory({ action: "publish", request_id: crypto.randomUUID(), bundle: latest.bundle });
        } catch (e) {
            if (e.code === "account-change-required") throw fail("recovery_required");
            throw e;
        }
        this.#check();
        this.identity = latest.bundle.identity;
        this.epoch = a.epoch;
        return { identity: this.identity, epoch: a.epoch, expiresAt: latest.bundle.expires_at };
    }
    /**
     * One-time-key closure lifecycle (server rule: close_keys closes an
     * available key whose publication expired or whose account epoch moved
     * on, and a reserved key whose claim lapsed). Asks only about unconsumed,
     * unclosed obligations of the current account whose publication expired,
     * runs the core retire_prekey for each exact public key the TLS-pinned
     * server reports as "closed" (its proof is a digest, not a signature: the
     * server is trusted not to close a key it still admits), then commits the
     * candidate account with those closure receipts atomically
     * (retireUnsubmittedKeys). Keys still available/reserved/admitted stay; a
     * timer is never evidence. A key the core cannot find is not asked about
     * again in this session.
     */
    async #closeKeys(a) {
        const due = [];
        for (const [ref, r] of await this.#readMany(await this.#refs("obligation"))) {
            const o = parse(r.data);
            if (o.account_id === a.accountID && !o.consumed && !o.closed && o.expires_at <= now() && !this.unretirable.has(ref[1])) due.push([ref[1], o]);
        }
        for (let i = 0; i < due.length; i += 100) {
            const batch = due.slice(i, i + 100);
            const lifecycle = await this.#handoff(a, { action: "close_keys", keys: batch.map(([, o]) => o.key_id) });
            this.#check();
            if (!Array.isArray(lifecycle)) throw fail("invalid");
            const closed = new Map(lifecycle.filter(l => l?.state === "closed").map(l => [l.key_id, l]));
            const closing = batch.filter(([, o]) => closed.has(o.key_id));
            if (!closing.length) continue;
            await this.#casRetry(async () => {
                const s = await this.store.read([["account", a.accountID], ...closing.map(([id]) => ["obligation", id])]);
                this.#check();
                let account = dec.decode(rec(s, "account", a.accountID).data);
                const proofs = {};
                for (const [id, o] of closing) {
                    const current = parse(rec(s, "obligation", id).data);
                    if (current.closed || current.consumed) continue;
                    let next;
                    try { next = await this.core.apply({ op: "retire_prekey", key: this.store.coreKey(s), account, one_time_key: o.public_key }); }
                    catch (e) { if (e.code) throw e; this.unretirable.add(id); continue; } // not in the account: stays open, not re-asked
                    this.#check();
                    account = next.account;
                    proofs[id] = closed.get(o.key_id);
                }
                if (!Object.keys(proofs).length) return;
                await this.store.retireUnsubmittedKeys(s, a.accountID, enc.encode(account), proofs);
                this.#check();
            });
        }
    }
    async #commitBundle(s, a, created, generation, previous = null) {
        const issued = now();
        const bundle = {
            version: OLM_DIRECTORY_VERSION, protocol: RATCHET_PROTOCOL, server: this.namespace.server,
            principal: this.namespace.principal, device_id: this.namespace.device, account_epoch: a.epoch,
            generation, issued_at: issued, expires_at: issued + 6 * 86400,
            identity: previous ? previous.identity : created.bundle.identity,
            signing: previous ? previous.signing : created.bundle.signing,
            one_time_keys: created.bundle.one_time_keys.map(k => ({ id: k.id, key: k.key })),
        };
        const signed = await this.signBundle(bundle);
        this.#check();
        const id = `pub:${a.accountID}:${String(generation).padStart(10, "0")}`;
        const otks = {};
        for (const k of bundle.one_time_keys) otks[`${a.accountID}:${k.id}`] = { publication_id: id, expires_at: bundle.expires_at, key_id: k.id, public_key: k.key, account_id: a.accountID };
        await this.store.commitPublication(s, id, enc.encode(created.account), jsonBytes(signed), otks, { accountID: a.accountID });
        this.#check();
    }
    async #latestPublication(a) {
        const prefix = `pub:${a.accountID}:`;
        const refs = (await this.#refs("publication")).filter(r => r[1].startsWith(prefix)).sort((x, y) => (x[1] < y[1] ? -1 : 1));
        const last = refs.at(-1);
        if (!last) return null;
        const s = await this.store.read([last]);
        return { id: last[1], bundle: parse(parse(rec(s, ...last).data).payload) };
    }
    async #refs(kind) {
        const refs = [];
        let after = "";
        for (;;) {
            const page = await this.store.pendingRefs(kind, after, 200);
            this.#check();
            refs.push(...page);
            if (page.length < 200) return refs;
            after = page.at(-1)[1];
        }
    }
    async #readMany(refs) {
        const out = [];
        for (let i = 0; i < refs.length; i += 200) {
            const s = await this.store.read(refs.slice(i, i + 200));
            this.#check();
            for (const r of refs.slice(i, i + 200)) out.push([r, rec(s, ...r)]);
        }
        return out;
    }

    /**
     * Explicit, user-initiated recovery of a restored or lost account
     * (mirror of client/ratchet_recovery.go). The server's own_state is only
     * input: it must verify under this device's own noXa key and may never be
     * older than the local epoch. A fresh account, bundle and noxa-olm-reset-v1
     * transition are signed with the noXa key, durably recorded (old sending
     * stops), submitted at the server's epoch and promoted only on the exact
     * receipt. A deterministic server refusal abandons the intent. Old readable
     * history stays; copies sent to the lost keys stay quarantined.
     */
    recover(reason = "restored") { return this.#serial(() => this.#recover(reason)); }
    async #recover(reason) {
        if (reason !== "restored" && reason !== "lost") throw fail("invalid");
        if (!this.signReset || !(this.ownKey instanceof Uint8Array) || this.ownKey.length !== 32) throw fail("unavailable");
        const ns = this.namespace;
        let s = await this.store.read();
        const local = await this.store.accountAuthority(s);
        this.#check();
        for (const ref of await this.#refs("transition")) {
            const t = await this.store.accountTransitionRequest(await this.store.read([ref]), ref[1]).catch(() => null);
            if (t?.recovery && !t.receipt && !t.abandoned) return this.#finishRecovery(t, ref[1]);
        }
        if (local.blocked) throw fail("fenced");
        let own;
        try { own = await this.directory({ action: "own_state", request_id: crypto.randomUUID() }); }
        catch (e) { if (e.code === "absent") throw fail("not_needed"); throw e; }
        this.#check();
        if (own?.error === "absent") throw fail("not_needed");
        const cur = own?.bundle;
        if (own?.error || !cur) throw fail("unavailable");
        try {
            verifyBundleEvidence(cur, { trustedSigningKey: this.ownKey, server: ns.server, principal: ns.principal, deviceID: ns.device, accountEpoch: cur.account_epoch, pinnedOlmIdentity: cur.identity });
        } catch { throw fail("invalid_evidence"); }
        if (!Number.isSafeInteger(cur.account_epoch) || cur.account_epoch < 1 || cur.account_epoch < local.epoch || !Number.isSafeInteger(cur.generation) || cur.generation < 1)
            throw fail("invalid_evidence");
        const latest = await this.#latestPublication(local).catch(() => null);
        if (latest && latest.bundle.account_epoch === cur.account_epoch && latest.bundle.identity === cur.identity) throw fail("not_needed");
        s = await this.store.read();
        const created = await this.core.apply({ op: "new_account", key: this.store.coreKey(s), count: Limits.prekeys });
        this.#check();
        const epoch = cur.account_epoch + 1, generation = cur.generation + 1;
        const accountID = `epoch-${epoch}-${crypto.randomUUID()}`;
        const transition = `pub:${accountID}:${String(generation).padStart(10, "0")}`;
        const issued = now();
        const bundle = await this.signBundle({
            version: OLM_DIRECTORY_VERSION, protocol: RATCHET_PROTOCOL, server: ns.server, principal: ns.principal, device_id: ns.device,
            account_epoch: epoch, generation, issued_at: issued, expires_at: issued + 6 * 86400,
            identity: created.bundle.identity, signing: created.bundle.signing,
            one_time_keys: created.bundle.one_time_keys.map(k => ({ id: k.id, key: k.key })),
        });
        this.#check();
        const { publicationDigest } = await import("./olm-reset.js");
        const reset = await this.signReset({
            version: "noxa-olm-reset-v1", server: ns.server, principal: ns.principal, device: ns.device, transition_id: transition,
            old_epoch: cur.account_epoch, old_identity: cur.identity, old_signing: cur.signing, new_epoch: epoch,
            new_identity: bundle.identity, new_signing: bundle.signing, generation, publication_digest: await publicationDigest(bundle), reason,
        });
        this.#check();
        const otks = {};
        for (const k of bundle.one_time_keys)
            otks[`${accountID}:${k.id}`] = { account_id: accountID, account_epoch: epoch, publication_id: transition, expires_at: bundle.expires_at, key_id: k.id, public_key: k.key, consumed: false, closed: false };
        await this.store.prepareRecoveryTransition(await this.store.read(), accountID, enc.encode(created.account), reset, bundle, otks);
        this.#check();
        return this.#finishRecovery({ account_id: accountID, reset, bundle }, transition);
    }
    /**
     * Submits the recorded reset; promotes on its exact receipt. A refusal
     * alone never abandons the intent (the server may have applied it and the
     * response was lost): own_state decides, as in client/ratchet_recovery.go.
     */
    async #finishRecovery(t, id) {
        const r = t.reset;
        let receipt;
        try {
            receipt = await this.#call({ action: "reset", account_epoch: r.old_epoch, reset: r, bundle: t.bundle });
        } catch (e) {
            const evidence = await this.#recoveryEvidence(r);
            if (evidence === "applied") return this.#promoteRecovery(id, await recoveryReceipt(r));
            if (evidence === "not_applied" && ["authority-required", "account-change-required", "conflict", "invalid", "limit"].includes(e.code)) {
                await this.store.abandonRecoveryTransition(await this.store.read([["transition", id]]), id);
                throw e;
            }
            throw Object.assign(new Error(`recovery pending: ${e.code || e.message}`), { code: "recovery_pending", cause: e });
        }
        return this.#promoteRecovery(id, receipt);
    }
    async #promoteRecovery(id, receipt) {
        this.#check();
        await this.store.promoteAccount(await this.store.read([["transition", id]]), id, receipt);
        this.#check();
        this.sticky = null;
        this.readiness = null;
        this.readyValue = null;
        return true;
    }
    /** "applied", "not_applied" or "unknown", from this device's own_state. */
    async #recoveryEvidence(r) {
        const ns = this.namespace;
        try {
            const own = await this.directory({ action: "own_state", request_id: crypto.randomUUID() });
            const b = own?.bundle;
            if (own?.error || !b) return "unknown";
            verifyBundleEvidence(b, { trustedSigningKey: this.ownKey, server: ns.server, principal: ns.principal, deviceID: ns.device, accountEpoch: b.account_epoch, pinnedOlmIdentity: b.identity });
            const { publicationDigest } = await import("./olm-reset.js");
            const digest = await publicationDigest(b);
            if (b.account_epoch === r.new_epoch && digest === r.publication_digest) return "applied";
            if (b.account_epoch === r.old_epoch && digest !== r.publication_digest) return "not_applied";
        } catch { /* unverifiable or unreachable: unknown */ }
        return "unknown";
    }
    /**
     * The user's explicit exit from a pending recovery: abandons only on
     * positive not-applied evidence; an applied intent is completed instead.
     * Resolves "completed" or "cancelled".
     */
    cancelRecovery() { return this.#serial(() => this.#cancelRecovery()); }
    async #cancelRecovery() {
        this.#check();
        for (const ref of await this.#refs("transition")) {
            const t = await this.store.accountTransitionRequest(await this.store.read([ref]), ref[1]).catch(() => null);
            if (!t?.recovery || t.receipt || t.abandoned) continue;
            const evidence = await this.#recoveryEvidence(t.reset);
            if (evidence === "applied") { await this.#promoteRecovery(ref[1], await recoveryReceipt(t.reset)); return "completed"; }
            if (evidence === "not_applied") {
                await this.store.abandonRecoveryTransition(await this.store.read([["transition", ref[1]]]), ref[1]);
                return "cancelled";
            }
            throw fail("recovery_unknown");
        }
        throw fail("not_needed");
    }

    // --- Trust ----------------------------------------------------------------

    static trustID(principal, device) { return JSON.stringify([principal, device]); }
    /**
     * Returns {id, key, pin}. The noXa key always comes from deviceKey (root
     * chain). An Olm pin is adopted only from a bundle signed by that key, and a
     * changed epoch/identity blocks (verified reset promotion is required).
     */
    /** Idempotent re-read+write steps retried after a cross-tab CAS loss. */
    async #casRetry(fn) {
        for (let i = 0; ; i++) {
            try { return await fn(); }
            catch (e) { if (e.code !== "conflict" || i >= LOCAL_RETRY) throw e; this.#check(); }
        }
    }
    #trust(principal, device) { return this.#casRetry(() => this.#trustOnce(principal, device)); }
    async #trustOnce(principal, device) {
        // deviceKey first: it re-pins a confirmed contact, so the durable reset
        // intent is visible on the FIRST trust check after an offline confirm.
        const key = await this.deviceKey(principal, device);
        this.#check();
        const token = this.trustReset ? await this.trustReset(principal) : "";
        if (token) {
            // Apply the durable confirmation; Olm keys are re-adopted only from
            // bundles the newly confirmed chain signs.
            this.#check();
            await this.#resetTrustRecords(principal);
            this.#check();
        }
        if (!(key instanceof Uint8Array) || key.length !== 32) throw fail("untrusted_device");
        const id = RatchetConsumer.trustID(principal, device);
        const s = await this.store.read([["trust", id]]), r = rec(s, "trust", id);
        const done = async () => { if (token) { await this.trustResetDone?.(principal, token); this.#check(); } };
        if (!r.revision) {
            await this.store.setTrust(s, id, { principal, device, noxa: b64(key) }, false);
            this.#check();
            await done();
            return { id, key, pin: null };
        }
        const saved = parse(r.data);
        if (saved.revoked) throw fail("revoked");
        if (saved.value.noxa === null) {
            // Explicitly confirmed changed root (resetTrust): pin the newly
            // trusted noXa key; an Olm pin must be re-adopted from a bundle it
            // signs. Only now the intent is cleared (compare-and-clear).
            await this.store.setTrust(s, id, { principal, device, noxa: b64(key) }, false);
            this.#check();
            await done();
            return { id, key, pin: null };
        }
        if (saved.value.noxa !== b64(key)) throw fail("account_changed");
        return { id, key, pin: saved.value.pin || null, history: saved.value.history || [] };
    }
    /**
     * Brings a cached Olm pin to a peer's (epoch, identity). A newer epoch is
     * adopted ONLY through the signed reset chain verified from the cached pin
     * under the independently trusted noXa key (verifyResetChain); the pins
     * left behind are kept by epoch. An older epoch matches only a retained
     * historical pin, and only for receiving. Anything else blocks with
     * account_changed: no server data ever replaces an independent pin, and
     * the explicit identity-change confirmation (resetTrust) stays the
     * authoritative way out.
     */
    #promote(a, trusted, principal, device, epoch, identity, { receiveOnly = false } = {}) {
        const pin = trusted.pin;
        if (!pin || (pin.epoch === epoch && pin.identity === identity)) return trusted;
        if (epoch < pin.epoch) {
            // A retired pin receives only within the delayed-delivery window
            // after its retirement (the old key may have been lost).
            const old = (trusted.history || []).find(h => h.epoch === epoch && h.identity === identity &&
                h.retired_at > 0 && now() - h.retired_at <= HISTORICAL_RECEIVE_WINDOW);
            if (receiveOnly && old) return { ...trusted, pin: { epoch: old.epoch, identity: old.identity, signing: old.signing } };
            throw fail("account_changed");
        }
        if (epoch === pin.epoch) throw fail("account_changed");
        return this.#casRetry(() => this.#promoteOnce(a, trusted, principal, device, epoch, identity));
    }
    async #promoteOnce(a, trusted, principal, device, epoch, identity) {
        const s0 = await this.store.read([["trust", trusted.id]]), saved0 = parse(rec(s0, "trust", trusted.id).data);
        if (saved0.revoked) throw fail("revoked");
        if (saved0.value.noxa !== b64(trusted.key)) throw fail("account_changed");
        const from = saved0.value.pin;
        if (!from) throw fail("account_changed");
        if (from.epoch === epoch && from.identity === identity) return { ...trusted, pin: from, history: saved0.value.history || [] };
        let verified;
        try {
            verified = await verifyResetChain(from, { epoch, identity }, {
                server: this.namespace.server, principal, device, noxaSigning: trusted.key,
                fetch: async after => { const page = await this.#handoff(a, { action: "reset_chain", target_device: device, after_epoch: after }); this.#check(); return page; },
            });
        } catch (e) {
            if (e.code) throw e; // transport/fence failures stay transient
            throw fail("account_changed");
        }
        // Commit against the exact pin the chain started from (cross-tab CAS).
        const s = await this.store.read([["trust", trusted.id]]), saved = parse(rec(s, "trust", trusted.id).data);
        if (saved.revoked) throw fail("revoked");
        if (saved.value.noxa !== b64(trusted.key) || !equalJSON(saved.value.pin, from)) throw fail("conflict");
        const retiredAt = now();
        const history = [...(saved.value.history || []), ...verified.passed.map(p => ({ ...p, retired_at: retiredAt }))].slice(-32);
        await this.store.setTrust(s, trusted.id, { ...saved.value, pin: verified.pin, history }, false);
        this.#check();
        return { ...trusted, pin: verified.pin, history };
    }
    #adopt(trusted, principal, device, bundle) { return this.#casRetry(() => this.#adoptOnce(trusted, principal, device, bundle)); }
    async #adoptOnce(trusted, principal, device, bundle) {
        if (trusted.pin) {
            if (trusted.pin.epoch !== bundle.account_epoch || trusted.pin.identity !== bundle.identity ||
                (trusted.pin.signing && trusted.pin.signing !== bundle.signing)) throw fail("account_changed");
            return trusted;
        }
        const s = await this.store.read([["trust", trusted.id]]), r = rec(s, "trust", trusted.id);
        const saved = parse(r.data);
        if (saved.value.noxa !== b64(trusted.key)) throw fail("account_changed");
        if (saved.value.pin) {
            // Another tab/consumer adopted concurrently: identical pin is fine.
            const p = saved.value.pin;
            if (p.epoch !== bundle.account_epoch || p.identity !== bundle.identity) throw fail("account_changed");
            return { ...trusted, pin: p };
        }
        const pin = { epoch: bundle.account_epoch, identity: bundle.identity, signing: bundle.signing };
        await this.store.setTrust(s, trusted.id, { ...saved.value, pin }, false);
        this.#check();
        return { ...trusted, pin };
    }

    // --- Sessions -------------------------------------------------------------

    /** Device-pair session context (no conversation: bound per message). */
    #context(a, t) {
        return { local_account_id: a.accountID, local_epoch: a.epoch, peer_epoch: t.account_epoch, peer_principal: t.principal,
            peer_device: t.device, peer_identity: t.identity, scope: DEVICE_PAIR_SCOPE, protocol: RATCHET_PROTOCOL };
    }
    async #sessionIndex() {
        if (!this.sessions) {
            const map = new Map();
            for (const [ref, r] of await this.#readMany(await this.#refs("session"))) {
                const state = parse(r.data);
                // Legacy per-conversation records are never selected for sending.
                if (!state.retired && state.context?.scope === DEVICE_PAIR_SCOPE) map.set(JSON.stringify(state.context), ref[1]);
            }
            this.sessions = map;
        }
        return this.sessions;
    }

    // --- Sending --------------------------------------------------------------

    /**
     * intent {logicalID, scope:"direct"|"group", peer|conversation, purpose,
     * reference, body}. Returns {record, receipt, reused}. A logical ID that
     * already has a committed operation ALWAYS yields its original archive,
     * whatever body the caller passes now.
     */
    send(intent) {
        const run = async (tries = 0) => {
            await this.ready();
            try { return await this.#serial(() => this.#send(intent)); }
            catch (e) {
                // A concurrent tab/consumer won this snapshot. Nothing was
                // committed or transported for this intent; start over. A raw
                // store "conflict" before any commit is the same case.
                if (e.code === "conflict" && !e.committed) e.code = "local_conflict";
                if (e.code !== "local_conflict" || tries >= LOCAL_RETRY) throw e;
                this.sessions = null;
                return run(tries + 1);
            }
        };
        return run();
    }
    async #send(intent) {
        const a = await this.#authority();
        const s = await this.store.read([["logical", intent.logicalID]]), lr = rec(s, "logical", intent.logicalID);
        if (lr.revision) {
            const logical = parse(lr.data);
            const original = await this.#outcome(logical.attemptID);
            if (!["rejected", "aborted"].includes(logical.state)) return this.#resolve(logical.attemptID, a, true);
            // A terminal non-accepted intent may be resealed with NEW ratchet
            // state, but only with its original committed content.
            const r = original.record;
            return this.#seal(a, { logicalID: r.logicalID, scope: r.scope, peer: r.scope === "direct" ? r.peer : "", conversation: r.conversation,
                purpose: r.purpose, reference: r.reference, body: r.body }, true);
        }
        return this.#seal(a, intent, false);
    }
    async #seal(a, { logicalID, scope, peer = "", conversation = "", purpose = "chat", reference = "", body }, reused) {
        if (!["direct", "group"].includes(scope) || !["chat", "edit", "delete"].includes(purpose) || (purpose === "chat") === !!reference) throw fail("invalid");
        const roster = await this.#handoff(a, { action: "snapshot", scope, ...(scope === "direct" ? { peer } : { conversation }) });
        const m = {
            version: ADMISSION_VERSION, server: this.namespace.server, sender: this.namespace.principal, device: this.namespace.device,
            account_epoch: a.epoch, attempt_id: String(a.nextAttempt + 1), logical_id: logicalID, purpose,
            ...(reference ? { reference } : {}), scope,
            ...(scope === "direct" ? { peer } : { conversation, revision: roster.revision, epoch: roster.epoch }),
            deadline: now() + this.attemptTTL, rosters: roster.rosters, targets: roster.targets,
        };
        checkFanout(scope, m.targets);
        const locals = m.targets.filter(t => t.local);
        if (locals.length !== 1 || locals[0].principal !== m.sender || locals[0].device !== m.device ||
            locals[0].account_epoch !== a.epoch || locals[0].identity !== this.identity) throw fail("local_identity_mismatch");
        const sessions = await this.#sessionIndex();
        const plans = [];
        // Every non-local target is required: an unverifiable device fails the
        // whole operation rather than being silently omitted.
        for (const t of m.targets) {
            if (t.local) continue;
            let trusted = await this.#promote(a, await this.#trust(t.principal, t.device), t.principal, t.device, t.account_epoch, t.identity);
            const binding = expectedBinding(m, t), context = this.#context(a, t);
            // Without a verified Olm pin (first contact or a confirmed root
            // change) a fresh signed claim is required even if a session exists.
            let prior = trusted.pin ? sessions.get(JSON.stringify(context)) : undefined;
            let claim = null, pendingClaim = null, replaces;
            // A claim must cover the new attempt's deadline plus a margin.
            const covers = until => until > now() + this.attemptTTL + 60;
            if (prior) {
                // F1: a first-contact session whose claim-carrying attempt was
                // never accepted has no server session row yet. Resend with its
                // ORIGINAL reserved claim while that still covers the deadline;
                // otherwise replace it (retired in the same commit) with a fresh
                // claim. Never use a pending session without its claim.
                const ps = rec(await this.store.read([["session", prior]]), "session", prior);
                if (!ps.revision) throw fail("local_conflict");
                const pending = parse(ps.data).pending;
                if (pending && covers(pending.claim_until)) pendingClaim = pending;
                else if (pending) { replaces = prior; prior = undefined; }
            }
            const claimKey = JSON.stringify([a.accountID, a.epoch, t.principal, t.device, t.account_epoch, t.identity]);
            const kept = this.claims.get(claimKey);
            if (!prior && kept && covers(kept.until) && trusted.pin) {
                // The reservation from a lost local_conflict race is still ours
                // on the server; reuse it instead of spending more claim budget.
                claim = { id: kept.id, key: kept.key, until: kept.until, cacheKey: claimKey };
            } else if (!prior) {
                const claimID = crypto.randomUUID(), until = now() + 3600;
                const request = { action: "claim", request_id: claimID, principal: t.principal, device_id: t.device, account_epoch: a.epoch, claim_until: until };
                const cs = await this.store.read([["trust", trusted.id], ["claim", claimID]]);
                await this.store.reserveClaim(cs, claimID, trusted.id, jsonBytes(request));
                await this.store.checkAccountAuthority(await this.store.read(), a);
                this.#check();
                const result = await this.directory(request);
                this.#check();
                const selected = verifyReservedClaim(result.bundle, { trustedSigningKey: trusted.key, server: m.server, principal: t.principal,
                    deviceID: t.device, accountEpoch: t.account_epoch, pinnedOlmIdentity: trusted.pin?.identity || t.identity, keyID: result.key_id, claimUntil: until });
                trusted = await this.#adopt(trusted, t.principal, t.device, result.bundle);
                claim = { id: claimID, key: selected.key, until, cacheKey: claimKey };
                this.claims.set(claimKey, { id: claimID, key: selected.key, until });
            }
            plans.push({ t, trusted, binding, context, prior, claim, pendingClaim, replaces });
        }
        const refs = [["logical", logicalID], ["account", a.accountID]];
        for (const p of plans) refs.push(["trust", p.trusted.id], ...(p.prior ? [["session", p.prior]] : []), ...(p.replaces ? [["session", p.replaces]] : []));
        const before = await this.store.read(refs);
        this.#check();
        const key = this.store.coreKey(before), account = dec.decode(rec(before, "account", a.accountID).data);
        const record = { protocol: RATCHET_PROTOCOL, provenance: "authenticated", sender: m.sender, senderDevice: m.device,
            logicalID, key: semanticKey(m.sender, logicalID), scope, peer: scope === "direct" ? peer : "", conversation: scope === "group" ? conversation : "",
            purpose, reference, referenceKey: reference ? semanticKey(m.sender, reference) : "", body, sentAt: Number(body?.sentAt) || Date.now() / 1000,
            self: true, copied: false };
        const packets = [], copies = [];
        for (const p of plans) {
            if (p.prior && !rec(before, "session", p.prior).revision) throw fail("local_conflict");
            const prior = p.prior ? parse(rec(before, "session", p.prior).data) : null;
            if (prior && (prior.retired || JSON.stringify(prior.context) !== JSON.stringify(p.context))) throw fail("local_conflict");
            // The pending claim decided above must still be the recorded one.
            if (prior && !equalJSON(prior.pending || null, p.pendingClaim || null)) throw fail("local_conflict");
            if (p.replaces) {
                // Replace only a session that is STILL pending with a claim that
                // cannot cover this attempt; if another tab established it
                // meanwhile, start over and reuse it instead of burning a claim.
                const old = rec(before, "session", p.replaces);
                const st = old.revision ? parse(old.data) : null;
                if (!st || st.retired || !st.pending || st.pending.claim_until > now() + this.attemptTTL + 60) throw fail("local_conflict");
            }
            const result = await this.core.apply({ op: prior ? "encrypt" : "outbound", key,
                ...(prior ? { session: dec.decode(Uint8Array.from(prior.snapshot)) } : { account, peer_identity: p.t.identity, one_time_key: p.claim.key }),
                binding: p.binding, plaintext: sealPayload(m, p.t, body) });
            this.#check();
            const sessionID = "olm:" + result.session_id;
            if (p.prior && p.prior !== sessionID) throw fail("local_conflict");
            const sessionClaim = p.claim ? { claim_id: p.claim.id, claim_until: p.claim.until } : p.pendingClaim;
            packets.push({ sessionID, trustID: p.trusted.id, state: enc.encode(result.session), envelope: jsonBytes(result.envelope), context: p.context,
                ...(sessionClaim ? { claim: sessionClaim } : {}), ...(p.replaces ? { replaces: p.replaces } : {}) });
            copies.push({ principal: p.t.principal, device: p.t.device, account_epoch: p.t.account_epoch, session_id: result.session_id,
                ...(sessionClaim ? { claim_id: sessionClaim.claim_id } : {}), envelope: result.envelope });
        }
        const frozen = await freezeAttempt(m, copies);
        // Extend the exact pre-crypto snapshot with new session refs; any
        // concurrent change to the prior reads aborts the whole immutable batch.
        let committed;
        try {
            const commit = await this.store.extended(before, packets.map(p => ["session", p.sessionID]));
            this.store.compare(before, commit);
            committed = await this.store.commitAttempt(commit, frozen.manifest, frozen.parts, packets, jsonBytes(record));
        } catch (e) {
            if (e.code === "conflict") throw fail("local_conflict");
            throw e;
        }
        // This consumer never re-commits a frozen attempt: "reused" means the
        // sequence belongs to another writer. Never resolve its archive as ours,
        // and only cache session IDs of a verified fresh commit.
        if (committed.reused) throw fail("local_conflict");
        for (const p of plans) if (p.claim) this.claims.delete(p.claim.cacheKey);
        this.#check();
        for (const p of packets) sessions.set(JSON.stringify(p.context), p.sessionID);
        return this.#resolve(a.epoch + ":" + m.attempt_id, a, reused);
    }
    async #outcome(id) {
        const s = await this.store.read([["outbox", id]]), r = rec(s, "outbox", id);
        if (!r.revision) throw fail("invalid");
        const op = parse(r.data);
        return { op, record: parse(op.archive) };
    }
    /** Resolves exactly one committed attempt; never seals a new one. */
    async #resolve(id, a, reused) {
        let { op, record } = await this.#outcome(id);
        try {
            if (!op.receipt) {
                const meta = await this.store.attemptStatusRequest(await this.store.read(), id), m = meta.manifest;
                let result = await this.#call({ action: "status", account_epoch: m.account_epoch, attempt_id: m.attempt_id });
                // An attempt the server never saw cannot be begun after its
                // deadline: fence it with a metadata-only abort instead.
                // Margin: a begin that may land at/after the deadline is aborted
                // (and resealed) rather than misreported as clock skew.
                const expired = m.deadline <= now() + DEADLINE_MARGIN;
                if (!expired && this.skewed.has(id) && !TERMINAL.has(result.state)) throw fail("clock_skew");
                if (!TERMINAL.has(result.state)) {
                    let wire = null;
                    try { wire = await this.store.attemptWire(await this.store.read(), id); }
                    catch (e) { if (!["fenced", "revoked", "conflict"].includes(e.code)) throw e; }
                    this.#check();
                    if (wire && m.account_epoch === a.epoch && !(expired && result.state === "absent")) {
                        try { result = await this.#handoff(a, { action: "begin", manifest: wire.manifest }); }
                        catch (e) {
                            if (e.code !== "invalid") throw e;
                            // The server refuses an unexpired deadline (client clock
                            // skew): fail without aborting or resealing, so no attempt
                            // IDs are burned. Once it is genuinely past, abort.
                            if (m.deadline > now() + DEADLINE_MARGIN) { this.skewed.add(id); throw fail("clock_skew"); }
                            result = await this.#call({ action: "abort", account_epoch: m.account_epoch, manifest: m });
                        }
                        if (!TERMINAL.has(result.state)) {
                            for (let part = 0; part < wire.parts.length; part++) await this.#handoff(a, { action: "put_part", attempt_id: m.attempt_id, part, data: b64(wire.parts[part]) });
                            result = await this.#handoff(a, { action: "finalize", attempt_id: m.attempt_id });
                        }
                    } else {
                        // Trust/epoch changed: metadata-only abort fences the old
                        // attempt without handing its packets to anyone.
                        result = await this.#call({ action: "abort", account_epoch: m.account_epoch, manifest: m });
                    }
                }
                if (!TERMINAL.has(result.state)) throw fail("uncertain");
                // Applying an accepted receipt also establishes its sessions, so a
                // concurrent session write elsewhere can lose the CAS: retry.
                await this.#casRetry(async () => this.store.applyAttemptResult(await this.store.read([["outbox", id], ["logical", m.logical_id]]), id, result));
                this.#check();
                ({ op, record } = await this.#outcome(id));
            }
        } catch (e) {
            // The committed original remains authoritative for the caller's view.
            throw Object.assign(e, { committed: { record, receipt: op.receipt || null, reused } });
        }
        // The terminal receipt is committed: retirement is best-effort
        // housekeeping that a later drain repeats; it never changes the outcome.
        try {
            if (!this.retired.has(id) && !retiredIn(await this.store.accountAuthority(await this.store.read()), id)) {
                const retirement = await this.store.attemptRetirementRequest(await this.store.read(), id);
                try { await this.#call({ action: "retire_receipt", ...retirement }); }
                catch (e) { if (e.code !== "retired") throw e; }
                // Durable progress: later sessions skip this retirement.
                await this.store.noteRetired(await this.store.read(), id);
            }
            this.retired.add(id);
        } catch (e) {
            if (e.code === "fenced") throw Object.assign(e, { committed: { record, receipt: op.receipt, reused } });
        }
        const receipt = op.receipt;
        const projected = { ...record, transport: { message: receipt.message_id || "", attempt: id }, status: receipt.state, reason: receipt.reason || "" };
        try { await this.#emit(projected, false); }
        catch (e) { if (e.code !== "fenced") throw e; }
        if (receipt.state !== "accepted") throw fail(receipt.reason || receipt.state, { committed: { record: projected, receipt, reused } });
        return { record: projected, receipt, reused };
    }

    // --- Receiving --------------------------------------------------------------

    /** Replays every authoritative archive (own sends and receipts) as non-new. */
    history() { return this.ready().then(() => this.#serial(() => this.#history())); }
    async #history() {
        const out = [];
        for (const [, r] of await this.#readMany(await this.#refs("inbox"))) out.push(parse(parse(r.data).archive));
        const attempts = [];
        for (const [ref, r] of await this.#readMany(await this.#refs("outbox"))) {
            const op = parse(r.data);
            if (!op.attempt) continue;
            attempts.push({ id: ref[1], record: { ...parse(op.archive), transport: { message: op.receipt?.message_id || "", attempt: ref[1] }, status: op.receipt?.state || "pending", reason: op.receipt?.reason || "" } });
        }
        for (const e of latestAttempts(attempts)) out.push(e.record);
        return out.sort((x, y) => x.sentAt - y.sentAt);
    }

    drain() { return this.ready().then(() => this.#serial(() => this.#drain())); }
    async #drain() {
        const a = await this.#authority();
        // Resolve recent committed attempts first: uncertainty never reseals.
        // Session-retired attempts never take a window slot.
        const unretired = (await this.#refs("outbox")).map(r => r[1]).filter(id => id.startsWith(a.epoch + ":") && !retiredIn(a, id) && !this.retired.has(id));
        for (const id of resolveOrder(unretired, Limits.retireWindow)) {
            try { await this.#resolve(id, a, true); }
            catch (e) {
                if (e.code === "fenced") throw e;
                // One attempt that cannot be resolved now (even a deterministic
                // local failure) never stops the drain from paging deliveries.
                if (!e.committed) console.warn(`ratchet: attempt ${id} not resolved: ${e.code || e.message}`);
            }
        }
        let after = "";
        for (let pages = 0; pages < 4096; pages++) {
            const page = await this.#handoff(a, { action: "deliveries", after });
            // A successful page always carries a deliveries array (possibly
            // empty after server-side filtering); anything else is a malformed
            // response, never an empty page that could end the drain early.
            if (!page || typeof page !== "object" || !Array.isArray(page.deliveries)) throw fail("invalid");
            const deliveries = page.deliveries;
            for (let i = 0; i < deliveries.length; i++) {
                const d = deliveries[i];
                // A missing/invalid server ID still gets a stable local key, so
                // it is quarantined once instead of aborting the whole drain.
                const qid = validDeliveryID(d?.id) ? d.id : `malformed:${after}:${i}`;
                // Quarantined copies are retried once per consumer (session) or
                // after resetTrust, not on every poll.
                if (this.quarantined.has(qid)) continue;
                try {
                    await this.#receive(d, a);
                    this.quarantined.delete(qid);
                } catch (e) {
                    // A transport loss after durable commit stops this drain; the
                    // next drain replays the stored outcome and exact ACK.
                    if (e.code === "fenced" || e.afterCommit) throw e;
                    // Did another tab commit this same delivery? Only a fresh-
                    // decrypt failure (lost key / key already used) can be that
                    // race; a digest mismatch etc. is a genuine failure.
                    let committedElsewhere = false;
                    if (qid === d?.id && RACE_RECEIVE.has(e.code)) {
                        try { committedElsewhere = !!rec(await this.store.read([["inbox", qid]]), "inbox", qid).revision; } catch { /* treat as not committed */ }
                    }
                    if (receiveDisposition(e.code, committedElsewhere) !== "quarantine") continue;
                    // Unreadable ciphertext stays pending on the server and is
                    // NEVER passed to legacy decoders or acknowledged.
                    const m = d?.manifest && typeof d.manifest === "object" ? d.manifest : {}, own = m.sender === this.namespace.principal;
                    const text = v => (typeof v === "string" ? v : "");
                    this.quarantined.set(qid, { id: qid, sender: text(m.sender), code: e.code, scope: text(m.scope),
                        peer: m.scope === "direct" ? text(own ? m.peer : m.sender) : "", conversation: m.scope === "group" ? text(m.conversation) : "", seenAt: Date.now() / 1000 });
                }
            }
            // Continue across filtered empty pages; a fresh drain restarts at "".
            if (!page.has_more) return { quarantined: [...this.quarantined.values()] };
            if (!page.after || page.after === after) throw fail("invalid");
            after = page.after;
        }
        throw fail("exhausted");
    }
    /**
     * After the user explicitly confirmed a contact's changed identity: drop
     * that principal's noXa/Olm pins so the next use pins the newly trusted
     * chain and re-adopts Olm keys only from bundles it signs. Never adopts
     * anything by itself.
     */
    resetTrust(principal) { return this.#serial(() => this.#resetTrustRecords(principal)); }
    async #resetTrustRecords(principal) {
        for (const ref of await this.#refs("trust")) {
            let parsed;
            try { parsed = JSON.parse(ref[1]); } catch { continue; }
            if (!Array.isArray(parsed) || parsed[0] !== principal) continue;
            const s = await this.store.read([ref]), saved = parse(rec(s, ...ref).data);
            if (saved.revoked) continue;
            await this.store.setTrust(s, ref[1], { principal, device: parsed[1], noxa: null }, false);
            this.#check();
        }
        for (const [id, q] of this.quarantined) if (q.sender === principal) this.quarantined.delete(id);
    }

    async #receive(d, a) {
        // Malformed server data is a deterministic, coded failure (placeholder),
        // not an uncoded TypeError retried silently forever.
        await deterministic("invalid", () => {
            validateManifest(d?.manifest);
            if (!validDeliveryID(d.id) || !d.copy || typeof d.copy !== "object" || !d.source_bundle || !d.target_bundle) throw fail("invalid");
        });
        const m = d.manifest, c = d.copy, ns = this.namespace;
        if (m.server !== ns.server || c.principal !== ns.principal || c.device !== ns.device || c.account_epoch !== a.epoch) throw fail("fenced_copy");
        await deterministic("invalid", () => checkEnvelope(m, c));
        let s = await this.store.read([["inbox", d.id]]);
        const saved = rec(s, "inbox", d.id);
        let record, isNew = false;
        if (saved.revision) {
            // Committed before a lost ACK: exact stored outcome, no re-decrypt.
            const prior = parse(saved.data);
            if (prior.digest !== await sha256(jsonBytes(c.envelope))) throw fail("digest_mismatch");
            record = parse(prior.archive);
        } else {
            const own = m.targets.find(t => t.device === c.device && t.principal === c.principal);
            if (!own || own.local || own.identity !== this.identity || own.account_epoch !== a.epoch ||
                d.target_bundle.identity !== this.identity || d.target_bundle.account_epoch !== a.epoch) throw fail("fenced_copy");
            const sourceTarget = m.targets.find(t => t.device === m.device && t.principal === m.sender && t.local);
            if (!sourceTarget || sourceTarget.account_epoch !== m.account_epoch) throw fail("invalid");
            let trusted = await this.#promote(a, await this.#trust(m.sender, m.device), m.sender, m.device, m.account_epoch, d.source_bundle?.identity, { receiveOnly: true });
            // Retained admission evidence is authenticated under the current
            // independently trusted noXa device key, without publication TTL.
            await deterministic("invalid_evidence", () => verifyBundleEvidence(d.source_bundle, { trustedSigningKey: trusted.key, server: m.server, principal: m.sender, deviceID: m.device,
                accountEpoch: m.account_epoch, pinnedOlmIdentity: trusted.pin?.identity || d.source_bundle.identity }));
            if (d.source_bundle.identity !== sourceTarget.identity) throw fail("invalid");
            trusted = await this.#adopt(trusted, m.sender, m.device, d.source_bundle);
            const source = { principal: m.sender, device: m.device, account_epoch: m.account_epoch, identity: trusted.pin.identity };
            const binding = expectedBinding(m, c), context = this.#context(a, source);
            const sessionID = "olm:" + c.session_id;
            const refs = [["trust", trusted.id], ["session", sessionID], ["inbox", d.id], ["account", a.accountID]];
            let consumed = "";
            if (!rec(await this.store.read([["session", sessionID]]), "session", sessionID).revision) {
                // Only an Olm prekey message may open a receive chain, and only
                // with a retained local obligation; never by TTL or server data.
                if (c.envelope?.message?.type !== 0) throw fail("lost_key");
                const otk = initialOneTimeKey(c.envelope.message.body);
                for (const [ref, r] of await this.#readMany(await this.#refs("obligation"))) {
                    const o = parse(r.data);
                    if (o.account_id === a.accountID && o.public_key === otk && !o.closed && !o.consumed) { consumed = ref[1]; break; }
                }
                if (!consumed) throw fail("lost_key");
                refs.push(["obligation", consumed]);
            }
            s = await this.store.read(refs);
            this.#check();
            const existing = rec(s, "session", sessionID), key = this.store.coreKey(s);
            let result;
            if (existing.revision) {
                if (consumed) throw fail("conflict");
                const state = parse(existing.data);
                // A pre-device-pair (per-conversation) record or a session bound
                // to another peer identity/epoch is a deterministic failure.
                if (state.context?.scope !== DEVICE_PAIR_SCOPE) throw fail("legacy_session");
                if (JSON.stringify(state.context) !== JSON.stringify(context)) throw fail("session_mismatch");
                // Core failures (MAC, binding, session mismatch) are deterministic.
                result = await deterministic("unauthenticated", () => this.core.apply({ op: "decrypt", key, session: dec.decode(Uint8Array.from(state.snapshot)), binding, envelope: c.envelope }));
            } else {
                if (!consumed) throw fail("conflict");
                result = await deterministic("unauthenticated", () => this.core.apply({ op: "inbound", key, account: dec.decode(rec(s, "account", a.accountID).data), peer_identity: source.identity, binding, envelope: c.envelope }));
                if ("olm:" + result.session_id !== sessionID) throw fail("invalid");
            }
            this.#check();
            const body = await deterministic("invalid", () => openPayload(m, c, result.plaintext));
            const self = m.sender === ns.principal;
            record = { protocol: RATCHET_PROTOCOL, provenance: "authenticated", sender: m.sender, senderDevice: m.device, logicalID: m.logical_id,
                key: semanticKey(m.sender, m.logical_id), scope: m.scope, peer: m.scope === "direct" ? (self ? m.peer : m.sender) : "",
                conversation: m.scope === "group" ? m.conversation : "", purpose: m.purpose, reference: m.reference || "",
                referenceKey: m.reference ? semanticKey(m.sender, m.reference) : "", body, sentAt: Number(body?.sentAt) || Date.now() / 1000,
                self, copied: self, transport: { delivery: d.id } };
            await this.store.commitReceive(s, d.id, { sessionID, trustID: trusted.id, session: enc.encode(result.session), envelope: jsonBytes(c.envelope),
                archive: jsonBytes(record), context, ...(consumed ? { accountID: a.accountID, account: enc.encode(result.account), consumedOTK: consumed } : {}) });
            this.#check();
            if (this.sessions) this.sessions.set(JSON.stringify(context), sessionID);
            isNew = true;
        }
        // Archive/session/OTK/dedup are durable before projection and the exact
        // ACK. A replay after a lost ACK projects isNew=false (no unread/notify).
        await this.#emit(record, isNew);
        try { await this.#handoff(a, { action: "receive_ack", delivery_id: d.id, digest: d.digest }); }
        catch (e) { throw Object.assign(e, { afterCommit: true }); }
    }
}

function retiredIn(a, id) {
    const seq = Number(String(id).split(":")[1]);
    return !!a.retired && (seq <= a.retired.through || a.retired.above.includes(seq));
}

async function sha256(bytes) {
    return Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256", bytes)), x => x.toString(16).padStart(2, "0")).join("");
}

// Olm v1 prekey message: version 3, protobuf field 1 = recipient one-time key.
// The key only selects a local obligation; the core authenticates the message.
export function initialOneTimeKey(body) {
    if (typeof body !== "string" || body.length > 8192) throw fail("invalid");
    const bytes = Uint8Array.from(atob(body + "=".repeat((4 - (body.length % 4)) % 4)), ch => ch.charCodeAt(0));
    if (bytes.length < 35 || bytes[0] !== 3 || bytes[1] !== 0x0a || bytes[2] !== 32) throw fail("invalid");
    return b64(bytes.subarray(3, 35)).replace(/=+$/, "");
}

/** Frame 200/202 request functions over one authenticated Connection. */
export function connectionTransport(conn, Msg) {
    const correlated = async (type, result, request) => {
        // Claims keep their reserved request ID: it is the admitted claim ID.
        const id = request.request_id || crypto.randomUUID();
        const response = await conn.request(type, { ...request, request_id: id }, result, { accept: r => r.request_id === id, timeout: 15000 });
        if (response.error) throw fail(response.error);
        return response;
    };
    return {
        admission: async request => (await correlated(Msg.OlmAdmissionRequest, Msg.OlmAdmissionResult, request)).data,
        directory: request => correlated(Msg.OlmDirectoryRequest, Msg.OlmDirectoryResult, request),
    };
}
