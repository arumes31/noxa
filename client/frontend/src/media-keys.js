// Channel media key distribution (Task 2). One MediaKeyEngine per voice
// session. Keys travel ONLY as authenticated ratchet controls (Task 1c
// purpose "control": one target device, at most once, never stored at rest,
// never shown as chat/activity/history). The server and SFU see ciphertext.
//
// Sender: a new epoch is created when the voice session starts, when the set
// of recipient devices changes (join/leave/kick/revoke/reconnect), when a
// local publication starts or stops, and every RENEW_MS. Keys go only to the
// CURRENT recipient devices; a new joiner never receives an earlier epoch, a
// departed device never receives a later one. Nothing is ever sent without
// encryption.
//
// Fresh epochs (start, removal, renewal) carry new random keys. A fresh epoch
// that replaces an active one is activated only after its controls were
// admitted plus a drain delay (or failed per recipient, or a bounded wait), so
// listeners keep hearing the previous epoch meanwhile. The very first epoch
// activates at once: nobody can decrypt this sender before it anyway, so
// waiting only delays when a new joiner is heard.
//
// Ratchet epochs (join or publication change, nobody removed): epoch e+1's
// keys are HKDF(epoch e's keys), one-way, so the joiner still cannot decrypt
// anything earlier. Every receiver that holds epoch e has already installed
// e+1 (it pre-derives the successor of each sender's newest key), so the
// sender switches at once: existing listeners are never interrupted by a
// join, and the joiner hears this sender as soon as its one control lands.
// A ratchet is used only when every device that ever held a key of the
// current chain is still a recipient and announced ratchet support (the
// RATCHET_FEATURE tag on its own controls' session); otherwise the epoch is
// fresh. Any removal always starts a fresh chain.
//
// Receiver: a control is used only if it is for this channel, from the
// device the roster names for that publisher's client, and newer than every
// epoch already seen from that device. Older or replayed controls (expired
// retries included) can never restore a retired epoch. The previous epoch is
// kept until the sender has certainly switched, plus a grace for late packets.
import { SLOTS } from "./media-e2ee-core.js";

export const MEDIA_KEY_KIND = "media-key";
export const MEDIA_SCOPE_TYPE = "channel-media";
export const MEDIA_KEY_DATA_VERSION = 1;
export const RENEW_MS = 5 * 60 * 1000;
// A new epoch is activated only after its controls were admitted AND the
// receivers had time to drain them (the ratchet drain polls about every
// second during voice). A removal activates sooner: the departed device must
// stop being able to decrypt quickly; slow receivers may glitch briefly.
export const ACTIVATE_DELAY_MS = 2500;
export const REMOVAL_ACTIVATE_DELAY_MS = 1000;
// A removal epoch activates at the latest this long after the removal, whether
// or not every remaining recipient's key delivery settled (confidentiality
// over availability, review 2b P2-1): the removed device holds the previous
// epoch, so the sender never keeps encrypting under it while a slow or stalling
// server delays deliveries. Recipients still waiting get the key when their
// delivery completes (a brief gap for them, shown in the status).
export const REMOVAL_ACTIVATE_DEADLINE_MS = 2000;
// A fresh epoch replacing an active one activates at the latest this long
// after its distribution started, even if some deliveries are still pending:
// one slow or unreachable device never holds every listener on the old epoch
// (those devices catch up when their delivery completes).
export const ACTIVATE_MAX_WAIT_MS = 8_000;
const STATUS_THROTTLE_MS = 250;
const RETRY_REKEY_MAX_MS = 2_000;
export const RECEIVE_GRACE_MS = 10_000;
// A receiver keeps a sender's previous epoch until that sender has certainly
// switched to the newer one (it may wait up to ACTIVATE_MAX_WAIT_MS +
// ACTIVATE_DELAY_MS after sending it), plus the late-packet grace. Retiring
// it on arrival of the newer key cut listeners off while the sender still
// waited for slow deliveries.
export const SWITCH_GRACE_MS = ACTIVATE_MAX_WAIT_MS + ACTIVATE_DELAY_MS + RECEIVE_GRACE_MS;
// Session tag a sender appends to its control's session ("<random>.r1"): it
// pre-derives every sender's successor epoch and never numbers a fresh epoch
// e+1, so ratchet epochs need no wait. Older clients treat the session as
// opaque text, so the tag is backward compatible.
export const RATCHET_FEATURE = "r1";
const RATCHET_INFO = "noxa-media-ratchet-v1";
export const SENDER_RETIRE_MS = 1_500;
export const DEBOUNCE_MS = 200;
// The removal bound: the sender encrypts only under an epoch never sent to a
// removed device at most this long after the removal was seen in the roster
// (the debounce that coalesces roster changes plus the activation deadline).
export const REMOVAL_BOUND_MS = DEBOUNCE_MS + REMOVAL_ACTIVATE_DEADLINE_MS;
export const SEND_TIMEOUT_MS = 20_000;
export const RETRY_FAILED_MS = 10_000;
export const CONTROL_TTL_S = 120;
export const PENDING_MS = 15_000;
export const MEMBERSHIP_SETTLE_MS = 5_000;
const MAX_PENDING = 64;
export const MAX_RETRY_DELAY_MS = 60_000;
const CONCURRENCY = 6;
const ID_RE = /^[\x21-\x7e]{1,128}$/;
const KEYS_B64 = /^[A-Za-z0-9+/]{43}=$/;

const b64 = bytes => btoa(String.fromCharCode(...bytes));
const unb64 = text => Uint8Array.from(atob(text), c => c.charCodeAt(0));
const b64url = bytes => b64(bytes).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");

// The microphone track carries the bare client ID (slot "").
export function slotOf(slot) { return slot === "" || slot == null ? "mic" : slot; }
export function publicationPid(clientID, slot) { return `${clientID}|${slotOf(slot)}`; }
export function senderPid(slot) { return `self|${slotOf(slot)}`; }

// Recipient devices of my publications: every other session in my channel
// that announces a media device (its Olm device). Sorted and de-duplicated.
// The roster MUST be exactly the member list the voice UI renders for the
// channel (desktop: the channel tree's strict channel_id match; PWA: the
// channel node's clients). Only entries whose channel_id is the integer
// channel count; aliases, strings and other shapes never receive keys, so
// nobody the user cannot see gets the keys (review 2a-A P2-1).
export function inChannel(entry, channelID) {
    return typeof entry?.channel_id === "number" && Number.isInteger(entry.channel_id) && entry.channel_id === Number(channelID);
}

export function recipientsOf(roster, self) {
    const seen = new Map();
    for (const c of roster || []) {
        const device = typeof c.media_device === "string" ? c.media_device : "";
        const principal = typeof c.unique_id === "string" ? c.unique_id : "";
        const clientID = String(c.client_id ?? "");
        // Invisible sessions are not in other members' roster: no keys, no digest.
        if (!inChannel(c, self.channelID) || c.status === "invisible" || !device || !principal || !clientID) continue;
        if (clientID === String(self.clientID) || device === self.device) continue;
        if (!ID_RE.test(device) || !ID_RE.test(principal)) continue;
        seen.set(`${principal}\n${device}`, { principal, device, clientID, nickname: typeof c.nickname === "string" ? c.nickname : "" });
    }
    const order = r => `${r.principal}\n${r.device}`;
    return [...seen.values()].sort((a, b) => (order(a) < order(b) ? -1 : order(a) > order(b) ? 1 : 0));
}

// Every media device in the channel (sender included): what a publisher's
// `members` digest should cover, as seen from here.
export function channelDevices(roster, channelID) {
    const seen = new Map();
    for (const c of roster || []) {
        if (!inChannel(c, channelID) || c.status === "invisible" || typeof c.media_device !== "string" || !c.media_device || typeof c.unique_id !== "string") continue;
        seen.set(`${c.unique_id}\n${c.media_device}`, { principal: c.unique_id, device: c.media_device });
    }
    return [...seen.values()];
}

// The highest epoch issued or accepted per device in this page, across
// engines (rescope, reconnect): a new engine never goes below it, even if the
// wall clock stepped back (review 2a-A P3-3 / B P3-4).
const issuedEpochs = new Map();   // principal\ndevice -> last epoch I issued
const acceptedEpochs = new Map(); // principal\ndevice -> last epoch I accepted

// Feature tags carried in a control's session ("<random>.<tag>.<tag>").
export function sessionFeatures(session) {
    return typeof session === "string" ? session.split(".").slice(1) : [];
}

// The successor (ratchet) keys of one epoch: per slot HKDF-SHA-256 over the
// slot key. One-way: holding the successor never reveals the earlier key.
export async function ratchetKeys(keys) {
    const out = {};
    for (const slot of SLOTS) {
        const ikm = await crypto.subtle.importKey("raw", keys[slot], "HKDF", false, ["deriveBits"]);
        out[slot] = new Uint8Array(await crypto.subtle.deriveBits({ name: "HKDF", hash: "SHA-256", salt: new Uint8Array(32), info: new TextEncoder().encode(`${RATCHET_INFO}|${slot}`) }, ikm, 256));
    }
    return out;
}

export async function membershipDigest(entries) {
    const lines = [...new Set(entries.map(e => `${e.principal}\n${e.device}\n`))].sort().join("");
    return b64url(new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(lines))));
}

export function buildMediaKeyBody({ channelID, epoch, nowSeconds, client, session, kid, members, count, keys }) {
    return {
        kind: MEDIA_KEY_KIND,
        scope: { type: MEDIA_SCOPE_TYPE, id: String(channelID) },
        epoch,
        expires_at: nowSeconds + CONTROL_TTL_S,
        data: { v: MEDIA_KEY_DATA_VERSION, client: String(client), session, kid, members, n: count,
            keys: Object.fromEntries(SLOTS.map(slot => [slot, b64(keys[slot])])) },
    };
}

// Validates a delivered control against the current voice session. Returns
// {ok, reason} or {ok: true, value: {client, session, kid, members, n, keys}}.
export function parseMediaKeyControl(control, channelID) {
    if (!control || control.kind !== MEDIA_KEY_KIND) return { ok: false, reason: "kind" };
    if (control.scope?.type !== MEDIA_SCOPE_TYPE || control.scope?.id !== String(channelID)) return { ok: false, reason: "scope" };
    if (!Number.isSafeInteger(control.epoch) || control.epoch <= 0) return { ok: false, reason: "epoch" };
    const d = control.data;
    if (!d || typeof d !== "object" || Array.isArray(d)) return { ok: false, reason: "data" };
    const allowed = new Set(["v", "client", "session", "kid", "members", "n", "keys"]);
    if (Object.keys(d).some(k => !allowed.has(k)) || d.v !== MEDIA_KEY_DATA_VERSION) return { ok: false, reason: "version" };
    if (typeof d.client !== "string" || !ID_RE.test(d.client) || typeof d.session !== "string" || !ID_RE.test(d.session)) return { ok: false, reason: "data" };
    if (!Number.isInteger(d.kid) || d.kid < 0 || d.kid > 15 || typeof d.members !== "string" || d.members.length > 64 || !Number.isInteger(d.n) || d.n < 1 || d.n > 10000) return { ok: false, reason: "data" };
    if (!d.keys || typeof d.keys !== "object" || Object.keys(d.keys).length !== SLOTS.length || SLOTS.some(s => typeof d.keys[s] !== "string" || !KEYS_B64.test(d.keys[s]))) return { ok: false, reason: "keys" };
    const keys = Object.fromEntries(SLOTS.map(s => [s, unb64(d.keys[s])]));
    return { ok: true, value: { client: d.client, session: d.session, kid: d.kid, members: d.members, n: d.n, keys } };
}

// Memory only, during one channel join/worker startup. Delivery can precede
// getUserMedia permission or worker readiness; admission is not playback.
// Keep only the newest key per publisher device, then let the running engine
// authenticate its roster binding and monotonic epoch as usual.
export class MediaKeyStartupInbox {
    #pending = new Map();
    constructor({ now = Date.now } = {}) { this.now = now; }
    #fresh(control, at, receivedAt = at) {
        return Number.isSafeInteger(control.expiresAt) && control.expiresAt * 1000 > at && at - receivedAt < CONTROL_TTL_S * 1000;
    }
    receive(control, channelID) {
        const at = this.now();
        for (const [key, entry] of this.#pending) if (!this.#fresh(entry.control, at, entry.at)) this.#pending.delete(key);
        const parsed = parseMediaKeyControl(control, channelID);
        if (!parsed.ok) return parsed;
        if (typeof control.sender !== "string" || !ID_RE.test(control.sender) || typeof control.senderDevice !== "string" || !ID_RE.test(control.senderDevice)) return { ok: false, reason: "sender" };
        if (!this.#fresh(control, at)) return { ok: false, reason: "expired" };
        const key = `${control.sender}\n${control.senderDevice}\n${parsed.value.client}`;
        const previous = this.#pending.get(key);
        if (previous && previous.control.epoch >= control.epoch) return { ok: false, reason: "stale" };
        if (!previous && this.#pending.size >= MAX_PENDING) return { ok: false, reason: "pending-full" };
        // Own the bounded validated fields; caller mutation cannot alter scope.
        this.#pending.set(key, { at, control: structuredClone({ sender: control.sender, senderDevice: control.senderDevice, kind: control.kind, scope: control.scope,
            epoch: control.epoch, expiresAt: control.expiresAt, data: control.data }) });
        return { ok: false, reason: "pending" };
    }
    drain(receive) {
        const pending = [...this.#pending.values()];
        this.clear();
        const at = this.now();
        for (const entry of pending) if (this.#fresh(entry.control, at, entry.at)) receive(entry.control);
    }
    clear() { this.#pending.clear(); }
}

async function pool(items, limit, task) {
    const results = new Array(items.length);
    let next = 0;
    await Promise.all(Array.from({ length: Math.min(limit, items.length) }, async () => {
        while (next < items.length) { const i = next++; results[i] = await task(items[i], i).catch(error => ({ ok: false, error })); }
    }));
    return results;
}

export class MediaKeyEngine {
    #gen = 0; #roster = []; #recipients = []; #recipientKey = "";
    #epoch = 0; #kid = 15; #keys = null; #members = ""; #sentTo = new Map();
    #busy = false; #again = false; #debounce = 0; #renew = 0; #retry = 0; #busyOrPending = false;
    #publishers = new Map(); #pending = []; #failed = new Map(); #reason = ""; #pendingRemovals = []; #seq = 0; #removalDebounce = false; #runFailures = 0; #runRetry = 0; #paused = false; #activating = []; #interrupt = null; #activeRecipients = []; #deliveries = new Map(); #statusTimer = 0;
    #rosterAt = 0; #settle = 0; #mismatch = [];

    constructor({ media, send, onStatus = () => {}, now = () => Date.now(), timers = globalThis, random = n => crypto.getRandomValues(new Uint8Array(n)), uuid = () => crypto.randomUUID(), sendTimeoutMs = SEND_TIMEOUT_MS, activateMaxWaitMs = ACTIVATE_MAX_WAIT_MS }) {
        this.media = media; this.send = send; this.onStatus = onStatus; this.now = now;
        this.timers = timers; this.random = random; this.uuid = uuid; this.sendTimeoutMs = sendTimeoutMs; this.activateMaxWaitMs = activateMaxWaitMs;
        this.self = null; this.session = "";
        this.counters = { ignored: {}, applied: 0, sent: 0, failed: 0, epochs: 0 };
    }

    get running() { return this.self !== null; }
    get epoch() { return this.#epoch; }

    start({ channelID, clientID, principal, device }) {
        if (this.running) throw new Error("media key session already running");
        if (!channelID || !clientID || !principal || !device) throw new Error("encrypted media needs a signed-in device");
        this.self = { channelID: Number(channelID), clientID: String(clientID), principal: String(principal), device: String(device) };
        this.#epoch = issuedEpochs.get(`${this.self.principal}\n${this.self.device}`) || 0;
        this.session = `${b64url(this.random(16))}.${RATCHET_FEATURE}`;
        const gen = ++this.#gen;
        this.#renew = this.timers.setInterval(() => { if (gen === this.#gen) this.#schedule("renewal", 0); }, RENEW_MS);
        this.#schedule("start", 0);
        this.#status();
    }

    stop() {
        if (!this.running) return;
        this.#gen++;
        this.timers.clearTimeout(this.#debounce); this.timers.clearInterval(this.#renew); this.timers.clearTimeout(this.#retry); this.timers.clearTimeout(this.#settle);
        const pids = [...SLOTS.map(senderPid), ...[...this.#publishers.keys()].flatMap(c => SLOTS.map(s => publicationPid(c, s))), ...SLOTS.map(s => publicationPid(this.self.clientID, s))];
        this.self = null; this.#keys = null; this.#publishers.clear(); this.#pending = []; this.#failed.clear(); this.#sentTo.clear();
        for (const r of this.#pendingRemovals) this.timers.clearTimeout(r.timer);
        this.timers.clearTimeout(this.#runRetry); this.#runRetry = 0; this.#paused = false; this.#activating = [];
        this.#pendingRemovals = []; this.#removalDebounce = false; this.#deliveries.clear(); this.#activeRecipients = []; this.timers.clearTimeout(this.#statusTimer); this.#statusTimer = 0;
        const waiter = this.#interrupt; this.#interrupt = null; waiter?.resolve();
        for (const pid of pids) this.media.forget(pid).catch(() => {});
        this.#status();
    }

    // Roster: the server's client list (all channels or just mine).
    setRoster(clients) {
        this.#roster = Array.isArray(clients) ? clients.slice() : [];
        if (!this.running) return;
        // An invisible session is not in the others' roster: it would hear
        // nothing and leak its presence through key controls. Encrypted
        // voice is refused while invisible (parent ruling on review 2a-A P2-2).
        if (this.#invisible()) { this.onInvisible?.(); return; }
        this.#rosterAt = this.now();
        this.#scheduleMembershipCheck();
        const recipients = recipientsOf(this.#roster, this.self);
        const key = recipients.map(r => `${r.principal}\n${r.device}`).join("\n");
        // Publishers that left (or whose device/channel changed) lose their keys.
        for (const [client, state] of this.#publishers) {
            const entry = this.#roster.find(c => String(c.client_id) === client);
            if (!entry || !inChannel(entry, this.self.channelID) || entry.unique_id !== state.principal || entry.media_device !== state.device) {
                this.#publishers.delete(client);
                for (const slot of SLOTS) this.media.forget(publicationPid(client, slot)).catch(() => {});
            }
        }
        this.#drainPending();
        if (key !== this.#recipientKey) {
            const current = new Set(recipients.map(r => `${r.principal}\n${r.device}`));
            const gone = this.#recipients.filter(r => !current.has(`${r.principal}\n${r.device}`));
            const removal = gone.length > 0;
            if (removal) this.#noteRemoval();
            this.#recipientKey = key; this.#recipients = recipients;
            this.#schedule(removal ? "removal" : "membership", DEBOUNCE_MS);
        }
    }

    publicationsChanged() { if (this.running) this.#schedule("publication", DEBOUNCE_MS); }

    #schedule(reason, delay) {
        if (!this.running) return;
        // A pending removal stays urgent even if a join lands in the same window.
        if (this.#reason !== "removal" || !this.#busyOrPending) this.#reason = reason;
        this.#busyOrPending = true;
        // Debounce starvation guard (review 2b fix2 P2-1): once a removal armed
        // the coalescing timer, later roster changes never push it later; it
        // fires at most DEBOUNCE_MS after the first pending removal.
        if (this.#removalDebounce) return;
        this.#removalDebounce = reason === "removal";
        this.timers.clearTimeout(this.#debounce);
        const gen = this.#gen;
        this.#debounce = this.timers.setTimeout(() => { if (gen === this.#gen) { this.#removalDebounce = false; void this.#run(); } }, delay);
    }

    async #run() {
        if (this.#busy) { this.#again = true; return; }
        this.#busy = true;
        const gen = this.#gen;
        try {
            do { this.#again = false; await this.#rekey(gen); } while (this.#again && gen === this.#gen);
            this.#runFailures = 0;
            this.timers.clearTimeout(this.#runRetry); this.#runRetry = 0;
        } catch (error) {
            if (gen === this.#gen) {
                this.lastError = String(error?.message || error); this.#status();
                // Pending removals are retried promptly (review 2b fix2 P3-1):
                // 100, 200, 400 … ms, at most RETRY_REKEY_MAX_MS apart.
                if (this.#pendingRemovals.length) {
                    const wait = Math.min(RETRY_REKEY_MAX_MS, 100 * 2 ** Math.min(this.#runFailures++, 10));
                    // One stored retry: cleared before re-arming, on success and on stop.
                    this.timers.clearTimeout(this.#runRetry);
                    this.#runRetry = this.timers.setTimeout(() => { this.#runRetry = 0; if (gen === this.#gen) void this.#run(); }, wait);
                }
            }
        } finally { this.#busy = false; }
    }

    // A removal (Task 2b fix 2, parent ruling): every removed device D,
    // removed at tD, is excluded from every epoch created after tD, and the
    // sender encrypts only under such an epoch no later than tD +
    // REMOVAL_BOUND_MS, whatever the server stalls, however other rekeys
    // (join, publication, renewal) or later removals interleave:
    //   - the removal preempts the wait of a non-removal rekey in flight;
    //   - an epoch created after tD activates at the latest at the deadline
    //     of the earliest removal it covers (anchored at that removal's own
    //     time, never reset by later removals);
    //   - a later removal never aborts an epoch back to an older one: the
    //     epoch still activates (it excludes every earlier removed device)
    //     and the next epoch, created right after, covers the later removal
    //     within its own bound.
    // Removals and epochs are ordered by a monotonic sequence number (review
    // 2b fix2 P2-2), never by wall-clock time; each removal's deadline is a
    // timer armed at the removal (timers are monotonic).
    #noteRemoval() {
        const gen = this.#gen;
        const removal = { seq: ++this.#seq, due: false, wake: null };
        removal.reached = new Promise(resolve => { removal.wake = resolve; });
        removal.timer = this.timers.setTimeout(() => {
            if (gen !== this.#gen) return;
            removal.due = true; removal.wake();
            // Confidentiality first (review 2b fix3 P3-B, fix4 CodeRabbit): a
            // removal still pending at its deadline means no epoch excluding
            // the device has activated. Retire the active sender epoch now —
            // even if a covering epoch is installed and about to activate (its
            // activation may still fail or stall): the sender goes silent
            // (frames drop) until a safe epoch activates, and the status says
            // so. When the covering epoch activates right away the gap is a
            // few milliseconds.
            if (!this.#pendingRemovals.includes(removal)) return;
            const retire = ep => { for (const slot of SLOTS) { this.media.retireOne(senderPid(slot), ep).catch(() => {}); this.media.retireOne(publicationPid(this.self.clientID, slot), ep).catch(() => {}); } };
            // An epoch whose activation started but did not complete (a later
            // slot's activate rejected) may already encrypt on some slots in the
            // worker: retire it too when it predates this removal, whatever the
            // paused state (review 2b fix5 P3-1).
            const partial = this.#activating.filter(a => a.created < removal.seq);
            this.#activating = this.#activating.filter(a => a.created >= removal.seq);
            for (const a of partial) retire(a.epoch);
            if ((!this.activeEpoch || this.#paused) && !partial.length) return;
            if (this.activeEpoch && !this.#paused) retire(this.activeEpoch);
            this.#paused = true;
            this.#status();
        }, REMOVAL_BOUND_MS);
        this.#pendingRemovals.push(removal);
        const waiter = this.#interrupt;
        this.#interrupt = null;
        waiter?.resolve();
    }

    #interrupted() {
        if (!this.#interrupt) {
            let resolve;
            const promise = new Promise(r => { resolve = r; });
            this.#interrupt = { promise, resolve };
        }
        return this.#interrupt.promise;
    }

    async #rekey(gen) {
        const self = this.self;
        // The epoch's place in the removal order: taken synchronously with its
        // recipient snapshot below; removals with a lower sequence are excluded.
        const created = ++this.#seq;
        const urgent = this.#pendingRemovals.length > 0;
        const reason = this.#reason;
        this.#reason = ""; this.#busyOrPending = false;
        const previous = this.activeEpoch || 0;
        if (this.#invisible()) { this.onInvisible?.(); return; }
        const selfKey = `${self.principal}\n${self.device}`;
        const recipients = this.#recipients = recipientsOf(this.#roster, self);
        const recipientIDs = recipients.map(r => `${r.principal}\n${r.device}`);
        // A ratchet continues the active epoch's chain; anything else is fresh.
        const head = this.#ratchetHead(reason, recipientIDs, selfKey);
        const keys = head ? await ratchetKeys(head.keys) : Object.fromEntries(SLOTS.map(slot => [slot, this.random(32)]));
        if (gen !== this.#gen) return;
        // Fresh epochs skip e+1: that number is reserved for the ratchet
        // successor that receivers pre-derive.
        const epoch = head ? previous + 1 : Math.max(this.#epoch + 2, (issuedEpochs.get(selfKey) || 0) + 2, this.now());
        issuedEpochs.set(selfKey, epoch);
        // A fresh epoch also skips the successor's key slot: a device that
        // pre-derived the successor (one just removed, too) then has no key
        // in that slot at all, and drops the new frames as "nokey" instead
        // of attempting them with the wrong key.
        const kid = (head ? head.kid + 1 : this.#kid + 2) % 16;
        // Every device that received a key of this chain can derive its later
        // epochs: a ratchet is allowed only while all of them stay recipients.
        const chain = new Set([...(head ? head.chain : []), ...recipientIDs]);
        const members = await membershipDigest([...recipients, self]);
        for (const slot of SLOTS) {
            if (gen !== this.#gen) return; // a stale rekey never installs into a newer engine's worker
            await this.media.setKey(senderPid(slot), epoch, kid, keys[slot]);
            // Encrypted self-echo (echo test channel): my own publication
            // arrives under my client ID and decrypts with my own key.
            await this.media.setKey(publicationPid(self.clientID, slot), epoch, kid, keys[slot]);
        }
        if (gen !== this.#gen) return;
        this.#epoch = epoch; this.#kid = kid; this.#keys = keys; this.#members = members;
        this.counters.epochs++;
        this.#failed.clear(); this.#sentTo.clear();
        // Per-epoch material: a retry of an older epoch always sends that
        // epoch's own keys (never a newer, preempted epoch's).
        this.#deliveries.set(epoch, { ok: new Set(), failed: new Map(), kid, keys, members, count: recipients.length + 1, chain });
        const distribution = this.#distribute(gen, recipients, epoch);
        const delay = ms => new Promise(resolve => this.timers.setTimeout(resolve, ms));
        // The removals this epoch covers: detected before it was created
        // (recipientsOf already excludes those devices).
        const covered = this.#pendingRemovals.filter(r => r.seq < created);
        // A removal preempts an epoch that has not activated yet: its own epoch
        // follows immediately; the previous active epoch stays until then.
        const preempted = () => {
            // Never activated: its keys leave the worker; the previous
            // active epoch stays live until the removal epoch activates.
            for (const slot of SLOTS) { this.media.retireOne(senderPid(slot), epoch).catch(() => {}); this.media.retireOne(publicationPid(self.clientID, slot), epoch).catch(() => {}); }
            if (this.#epoch === epoch) {
                this.#epoch = previous || epoch;
                this.#sentTo.clear(); this.#failed.clear();
                this.timers.clearTimeout(this.#retry); // the removal epoch follows at once
                const restored = this.#deliveries.get(this.#epoch);
                if (restored) { this.#kid = restored.kid; this.#keys = restored.keys; this.#members = restored.members; }
            }
            this.#deliveries.delete(epoch);
            this.#again = true;
        };
        if (!previous || head) {
            // Nothing to wait for: before the first epoch nobody can decrypt
            // this sender at all, and a ratchet epoch is already installed at
            // every receiver of the previous one. Deliveries continue in the
            // background; the joiner hears this sender once its key lands.
            // A removal seen since this epoch was created still preempts it
            // (the removed device may hold this chain).
            if (this.#pendingRemovals.some(r => r.seq > created)) { preempted(); return; }
        } else if (urgent && covered.length) {
            // Activate after the deliveries plus a short delay, or at the
            // earliest covered removal's deadline, whichever comes first.
            // Late deliveries keep running and are recorded as they settle.
            // The earliest covered removal's own deadline timer (armed at that
            // removal, so it already includes the coalescing debounce).
            await Promise.race([distribution.then(() => delay(REMOVAL_ACTIVATE_DELAY_MS)), ...covered.map(r => r.reached)]);
        } else {
            // A removal preempts this wait at once (see preempted above).
            if (this.#pendingRemovals.some(r => r.seq > created)) { preempted(); return; }
            const interrupted = this.#interrupted();
            // Deliveries settle, or the bounded wait elapses, then the drain delay.
            const settled = Number.isFinite(this.activateMaxWaitMs) ? Promise.race([distribution, delay(this.activateMaxWaitMs)]) : distribution;
            const done = await Promise.race([settled.then(() => delay(ACTIVATE_DELAY_MS)).then(() => true), interrupted.then(() => false)]);
            if (!done) { if (gen === this.#gen) preempted(); return; }
        }
        if (gen !== this.#gen || this.#epoch !== epoch) return;
        // Never activate an epoch whose recipient snapshot predates a removal
        // whose deadline has already passed (that device may hold it): it is
        // retired and the next epoch, created at once, excludes the device
        // (review 2b fix4 CodeRabbit). The sender stays paused meanwhile.
        if (this.#pendingRemovals.some(r => r.due && r.seq > created)) {
            for (const slot of SLOTS) { this.media.retireOne(senderPid(slot), epoch).catch(() => {}); this.media.retireOne(publicationPid(self.clientID, slot), epoch).catch(() => {}); }
            if (this.#epoch === epoch) {
                // A retired epoch never stays "current" for the counters
                // (review 2b fix5 P3-3): back to the previous epoch's material.
                this.#epoch = previous || epoch;
                this.#sentTo.clear(); this.#failed.clear();
                const restored = this.#deliveries.get(this.#epoch);
                if (restored) { this.#kid = restored.kid; this.#keys = restored.keys; this.#members = restored.members; }
            }
            this.#deliveries.delete(epoch);
            this.#again = true;
            return;
        }
        // In flight until every slot activated: a deadline meanwhile (or after a
        // partial failure) retires it if it predates that removal.
        const activating = { epoch, created };
        this.#activating.push(activating);
        for (const slot of SLOTS) await this.media.activate(senderPid(slot), epoch);
        this.#activating = this.#activating.filter(a => a !== activating && a.epoch > epoch);
        if (gen !== this.#gen) return;
        this.activeEpoch = epoch;
        this.#activeRecipients = recipients;
        // Re-checked after activation (it can stall past another removal's
        // deadline): an epoch a removed device may hold never resumes sending.
        if (this.#pendingRemovals.some(r => r.due && r.seq > created)) {
            this.#paused = true;
            for (const slot of SLOTS) { this.media.retireOne(senderPid(slot), epoch).catch(() => {}); this.media.retireOne(publicationPid(self.clientID, slot), epoch).catch(() => {}); }
        } else this.#paused = false;
        for (const old of [...this.#deliveries.keys()]) if (old < epoch) this.#deliveries.delete(old);
        // Removals detected before this epoch was created are satisfied; a
        // later one is covered by the next epoch, created right away.
        for (const r of this.#pendingRemovals) if (r.seq < created) this.timers.clearTimeout(r.timer);
        this.#pendingRemovals = this.#pendingRemovals.filter(r => r.seq > created);
        if (this.#pendingRemovals.length) this.#again = true;
        if (previous) {
            this.timers.setTimeout(() => {
                if (gen !== this.#gen) return;
                for (const slot of SLOTS) { this.media.retire(senderPid(slot), previous).catch(() => {}); this.media.retire(publicationPid(self.clientID, slot), previous).catch(() => {}); }
            }, SENDER_RETIRE_MS);
        }
        this.#status();
        distribution.then(() => { if (gen === this.#gen && this.#epoch === epoch) { this.#status(); this.#scheduleRetry(gen, epoch); } });
    }

    // The active epoch's material when the next epoch may be its ratchet
    // successor, or null for a fresh epoch. Only for a join or a publication
    // change: never with a removal pending, never while paused, only when the
    // active epoch is also the newest one issued (a preempted later epoch may
    // have reached receivers), and only when every device that holds a key of
    // this chain is still a recipient and pre-derives successors.
    #ratchetHead(reason, recipientIDs, selfKey) {
        if ((reason !== "membership" && reason !== "publication") || this.#pendingRemovals.length || this.#paused) return null;
        const active = this.activeEpoch || 0;
        if (!active || this.#epoch !== active || (issuedEpochs.get(selfKey) || 0) !== active) return null;
        const head = this.#deliveries.get(active);
        if (!head?.chain || !head.keys) return null;
        const current = new Set(recipientIDs);
        for (const id of head.chain) {
            if (!current.has(id)) return null;
            const [principal, device] = id.split("\n");
            if (!this.#ratchetCapable(principal, device)) return null;
        }
        return head;
    }

    // Whether that recipient device announced ratchet support on its own
    // controls (its publisher state, bound to the same principal and device).
    #ratchetCapable(principal, device) {
        for (const state of this.#publishers.values()) {
            if (state.principal === principal && state.device === device) return sessionFeatures(state.session).includes(RATCHET_FEATURE);
        }
        return false;
    }

    #scheduleRetry(gen, epoch) {
        if (!this.#failed.size) return;
        this.timers.clearTimeout(this.#retry);
        this.#retry = this.timers.setTimeout(() => { if (gen === this.#gen && this.#epoch === epoch) void this.#retryFailed(gen, epoch); }, RETRY_FAILED_MS);
    }

    #isRecipient(r) { return this.#recipients.some(c => c.principal === r.principal && c.device === r.device); }

    // Status pushes while deliveries settle, at most one per STATUS_THROTTLE_MS.
    #statusSoon() {
        if (this.#statusTimer) return;
        const gen = this.#gen;
        this.#statusTimer = this.timers.setTimeout(() => { this.#statusTimer = 0; if (gen === this.#gen) this.#status(); }, STATUS_THROTTLE_MS);
    }

    async #distribute(gen, recipients, epoch = this.#epoch) {
        const self = this.self;
        const material = this.#deliveries.get(epoch);
        if (!material) return; // an epoch that is gone sends nothing
        const body = buildMediaKeyBody({ channelID: self.channelID, epoch, nowSeconds: Math.floor(this.now() / 1000), client: self.clientID,
            session: this.session, kid: material.kid, members: material.members, count: material.count, keys: material.keys });
        // Each result is recorded as it settles, per epoch (the active epoch's
        // waiting count stays exact while a newer epoch distributes).
        const record = (r, result) => {
            if (gen !== this.#gen) return;
            const id = `${r.principal}\n${r.device}`;
            const tally = this.#deliveries.get(epoch);
            const reason = String(result?.error?.code || result?.error?.message || result?.code || (typeof result?.error === "string" ? result.error : "") || result?.reason || "failed");
            if (tally) { if (result?.ok) { tally.ok.add(id); tally.failed.delete(id); } else tally.failed.set(id, reason); }
            if (this.#epoch === epoch) {
                if (result?.ok) { this.counters.sent++; this.#sentTo.set(id, epoch); this.#failed.delete(id); }
                else { this.counters.failed++; this.#failed.set(id, { ...r, reason }); }
            }
            this.#statusSoon();
        };
        await pool(recipients, CONCURRENCY, async recipient => {
            // Going invisible (or stopping) mid-distribution stops the remaining sends.
            if (gen !== this.#gen || this.#invisible()) { record(recipient, { ok: false, reason: "stopped" }); return; }
            // A device removed since this epoch was created gets nothing more.
            if (!this.#isRecipient(recipient)) return;
            let timer;
            const timeout = new Promise((_, reject) => { if (Number.isFinite(this.sendTimeoutMs)) timer = this.timers.setTimeout(() => reject(Object.assign(new Error("timed out"), { code: "timeout" })), this.sendTimeoutMs); });
            let result;
            try { result = await Promise.race([this.send({ logicalID: this.uuid(), principal: recipient.principal, device: recipient.device, body }), timeout]); }
            catch (error) { result = { ok: false, error }; }
            finally { this.timers.clearTimeout(timer); }
            record(recipient, result);
        });
    }

    async #retryFailed(gen, epoch, attempt = 1) {
        const current = new Set(this.#recipients.map(r => `${r.principal}\n${r.device}`));
        const again = [...this.#failed.values()].filter(r => current.has(`${r.principal}\n${r.device}`));
        if (!again.length) return;
        await this.#distribute(gen, again, epoch);
        if (gen !== this.#gen || this.#epoch !== epoch) return;
        this.#status();
        // Retry the current epoch until recovery, removal, stop or rekey, at a bounded rate.
        if (this.#failed.size) {
            this.#retry = this.timers.setTimeout(() => { if (gen === this.#gen && this.#epoch === epoch) void this.#retryFailed(gen, epoch, attempt + 1); }, Math.min(MAX_RETRY_DELAY_MS, RETRY_FAILED_MS * (attempt + 1)));
        }
    }

    receive(control) {
        if (!this.running) return this.#ignore("inactive");
        const parsed = parseMediaKeyControl(control, this.self.channelID);
        if (!parsed.ok) return this.#ignore(parsed.reason);
        if (control.senderDevice === this.self.device) return this.#ignore("own");
        if (!this.#bound(control, parsed.value)) {
            this.#pending = this.#pending.filter(p => this.now() - p.at < PENDING_MS);
            if (this.#pending.length >= MAX_PENDING) return this.#ignore("pending-full");
            this.#pending.push({ control, value: parsed.value, at: this.now(), gen: this.#gen });
            return { ok: false, reason: "pending" };
        }
        return this.#apply(control, parsed.value);
    }

    #bound(control, value) {
        const entry = this.#roster.find(c => String(c.client_id) === value.client);
        return !!entry && inChannel(entry, this.self.channelID) && entry.unique_id === control.sender && entry.media_device === control.senderDevice;
    }

    #drainPending() {
        const keep = [];
        for (const p of this.#pending) {
            if (p.gen !== this.#gen || this.now() - p.at >= PENDING_MS) continue;
            if (this.#bound(p.control, p.value)) this.#apply(p.control, p.value); else keep.push(p);
        }
        this.#pending = keep;
    }

    #apply(control, value) {
        const gen = this.#gen;
        // One monotonic epoch line per sending device: replays, expired
        // retries and reordered older controls never install a key.
        const line = `${control.sender}\n${control.senderDevice}`;
        const last = acceptedEpochs.get(line) || 0;
        if (control.epoch <= last) return this.#ignore("stale");
        acceptedEpochs.set(line, control.epoch);
        let state = this.#publishers.get(value.client);
        // A pre-derived successor this control does not continue (a fresh
        // epoch, or a new sender session) is retired before the new key lands.
        const staleNext = state?.next && state.next !== control.epoch ? state.next : 0;
        if (state) state.next = 0;
        if (staleNext) for (const slot of SLOTS) this.media.retireOne(publicationPid(value.client, slot), staleNext).catch(() => {});
        if (!state || state.device !== control.senderDevice) {
            state = { principal: control.sender, device: control.senderDevice, epochs: [], members: value.members, n: value.n };
            this.#publishers.set(value.client, state);
        }
        state.members = value.members; state.n = value.n; state.session = value.session;
        state.announcedEpoch = control.epoch;
        this.#statusSoon();
        this.#scheduleMembershipCheck();
        const install = (async () => {
            for (const slot of SLOTS) {
                // Revocation (roster removal, channel move, stop) can land
                // between slot writes: never install a further slot for a
                // publisher that has been forgotten. The completion handler
                // below retires every slot of this epoch in that case.
                if (gen !== this.#gen || this.#publishers.get(value.client) !== state) return;
                await this.media.setKey(publicationPid(value.client, slot), control.epoch, value.kid, value.keys[slot]);
            }
        })();
        install.then(() => {
            if (gen !== this.#gen || this.#publishers.get(value.client) !== state) {
                for (const slot of SLOTS) this.media.retire(publicationPid(value.client, slot), control.epoch).catch(() => {});
                return;
            }
            state.epochs.push(control.epoch);
            state.epochs.sort((a, b) => a - b);
            if (control.epoch === state.announcedEpoch) state.installedMembers = value.members;
            const previous = state.epochs.slice(0, -1);
            state.epochs = state.epochs.slice(-1);
            if (previous.length) {
                const through = Math.max(...previous);
                // The sender may still be encrypting under the previous epoch
                // while it waits for other deliveries: keep it until the sender
                // has certainly switched (a ratchet switch is immediate anyway).
                this.timers.setTimeout(() => {
                    for (const slot of SLOTS) this.media.retire(publicationPid(value.client, slot), through).catch(() => {});
                }, SWITCH_GRACE_MS);
            }
            this.counters.applied++;
            this.#status();
            if (control.epoch === state.announcedEpoch && sessionFeatures(value.session).includes(RATCHET_FEATURE)) void this.#preinstallSuccessor(gen, value.client, state, control.epoch, value.kid, value.keys);
        }, () => {
            // A partial installation must not leave earlier slots usable;
            // retire only the failed epoch, keeping the previous one live.
            for (const slot of SLOTS) this.media.retireOne(publicationPid(value.client, slot), control.epoch).catch(() => {});
            return this.#ignore("install");
        });
        return { ok: true, epoch: control.epoch };
    }

    // Installs the ratchet successor (epoch+1) of a sender's newest key, so a
    // join on that sender's side switches without any wait or gap here. It is
    // decrypt-only material derived from a key this device already holds; it
    // is never accepted as a newer epoch until the sender's own control for
    // it arrives, and a fresh epoch retires it.
    async #preinstallSuccessor(gen, client, state, epoch, kid, keys) {
        let next;
        try { next = await ratchetKeys(keys); } catch { return; }
        if (gen !== this.#gen || this.#publishers.get(client) !== state || state.announcedEpoch !== epoch || state.next) return;
        const successor = epoch + 1;
        if (successor <= (acceptedEpochs.get(`${state.principal}\n${state.device}`) || 0)) return;
        state.next = successor;
        try {
            for (const slot of SLOTS) {
                if (gen !== this.#gen || this.#publishers.get(client) !== state || state.next !== successor) return;
                await this.media.setKey(publicationPid(client, slot), successor, (kid + 1) % 16, next[slot]);
            }
        } catch {
            // Best effort: the sender's own control installs the rest. The
            // slots already installed hold the correct successor keys and stay
            // tracked in state.next, so a fresh epoch still retires them. Never
            // retire the successor here: the worker would then refuse that
            // sender's real control for it.
        }
    }

    // The account whose verified device delivered the key this publisher's
    // media decrypts with (Task 2b: voice tiles tie to it), or "" when no key
    // from it was installed or the roster no longer names that device.
    verifiedPublisher(clientID) {
        const state = this.#publishers.get(String(clientID));
        if (!state?.epochs.length) return "";
        const entry = this.#roster.find(c => String(c.client_id) === String(clientID));
        return entry && inChannel(entry, this.self?.channelID) && entry.unique_id === state.principal && entry.media_device === state.device ? state.principal : "";
    }

    // A publisher's authenticated membership digest that still differs from
    // what this device sees after the roster settled is shown as a warning
    // (never a decrypt gate: rosters legitimately race). Review 2a-A P3-2.
    #invisible() {
        return this.#roster.find(c => String(c.client_id) === this.self?.clientID)?.status === "invisible";
    }

    #scheduleMembershipCheck() {
        this.timers.clearTimeout(this.#settle);
        const gen = this.#gen;
        this.#settle = this.timers.setTimeout(() => { if (gen === this.#gen) void this.#checkMembership(gen); }, MEMBERSHIP_SETTLE_MS);
    }

    async #checkMembership(gen) {
        if (!this.running || this.now() - this.#rosterAt < MEMBERSHIP_SETTLE_MS - 50) return;
        const own = await membershipDigest(channelDevices(this.#roster, this.self.channelID));
        if (gen !== this.#gen) return;
        const mismatch = [...this.#publishers.entries()].filter(([, s]) => s.members && s.members !== own).map(([clientID, s]) => ({ clientID, principal: s.principal }));
        if (JSON.stringify(mismatch) !== JSON.stringify(this.#mismatch)) { this.#mismatch = mismatch; this.#status(); }
    }

    #ignore(reason) { this.counters.ignored[reason] = (this.counters.ignored[reason] || 0) + 1; return { ok: false, reason }; }

    publisherStatus(clientID) {
        const state = this.#publishers.get(String(clientID));
        return state ? { device: state.device, epoch: state.epochs.at(-1) || 0, ready: !!state.announcedEpoch && state.epochs.includes(state.announcedEpoch), members: state.members, n: state.n } : null;
    }

    // Every per-recipient field refers to the ACTIVE epoch, the one my media
    // is encrypted with (review 2b fix2 P3-3): who holds it, who is still
    // waiting for it, and whose delivery of it failed.
    status() {
        if (!this.running) return { state: "off" };
        const tally = this.#deliveries.get(this.activeEpoch);
        const current = this.#activeRecipients.filter(r => this.#isRecipient(r));
        const holders = tally ? current.filter(r => tally.ok.has(`${r.principal}\n${r.device}`)) : [];
        const failed = tally ? current.filter(r => tally.failed.has(`${r.principal}\n${r.device}`)) : [];
        return {
            // "paused": a removal's deadline passed before a safe epoch could
            // be installed; the sender's media is dropped until one activates.
            state: !this.activeEpoch ? "keying" : this.#paused ? "paused" : failed.length ? "partial" : "encrypted",
            epoch: this.activeEpoch || 0,
            // Recipients of the active epoch that are still current.
            recipients: current.length,
            delivered: holders.length,
            // Delivery is per device; incoming publications and member rows are per client.
            participants: this.#roster.flatMap(c => recipientsOf([c], this.self)).map(r => {
                const id = `${r.principal}\n${r.device}`;
                return { clientID: r.clientID, sent: !!tally?.ok.has(id),
                    received: this.publisherStatus(r.clientID)?.ready === true,
                    failed: !!tally?.failed.has(id) };
            }),
            waiting: this.#waiting(),
            failed: failed.map(r => ({ principal: r.principal, device: r.device, clientID: r.clientID, reason: tally.failed.get(`${r.principal}\n${r.device}`) || "failed" })),
            // Exactly who holds the keys my media is encrypted with (voice UI).
            keyHolders: holders.map(({ principal, device, clientID, nickname }) => ({ principal, device, clientID, nickname })),
            membershipMismatch: this.#mismatch,
            devices: this.#recipients.length,
            members: tally?.members || "", // the active epoch's membership digest
            publishers: this.#publishers.size,
            error: this.lastError || "",
        };
    }

    #waiting() {
        const tally = this.#deliveries.get(this.activeEpoch);
        if (!tally) return 0;
        return this.#activeRecipients.filter(r => { const id = `${r.principal}\n${r.device}`; return this.#isRecipient(r) && !tally.ok.has(id) && !tally.failed.has(id); }).length;
    }

    #status() { try { this.onStatus(this.status()); } catch { /* status listeners never break keying */ } }
}
