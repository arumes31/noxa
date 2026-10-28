// Channel media key distribution (Task 2). One MediaKeyEngine per voice
// session. Keys travel ONLY as authenticated ratchet controls (Task 1c
// purpose "control": one target device, at most once, never stored at rest,
// never shown as chat/activity/history). The server and SFU see ciphertext.
//
// Sender: a fresh random key per publication slot and epoch. A new epoch is
// created when the voice session starts, when the set of recipient devices
// changes (join/leave/kick/revoke/reconnect), when a local publication
// starts or stops, and every RENEW_MS. Keys go only to the CURRENT recipient
// devices; a new joiner never receives an earlier epoch, a departed device
// never receives a later one. The new epoch is activated only after its
// controls were admitted (or failed per recipient: those devices simply
// cannot decrypt), so nothing is ever sent without encryption.
//
// Receiver: a control is used only if it is for this channel, from the
// device the roster names for that publisher's client, and newer than every
// epoch already seen from that device. Older or replayed controls (expired
// retries included) can never restore a retired epoch. A small grace keeps
// the previous epoch for late packets.
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
export const RECEIVE_GRACE_MS = 10_000;
export const SENDER_RETIRE_MS = 1_500;
export const DEBOUNCE_MS = 200;
export const SEND_TIMEOUT_MS = 20_000;
export const RETRY_FAILED_MS = 10_000;
export const CONTROL_TTL_S = 120;
export const PENDING_MS = 15_000;
const MAX_PENDING = 64;
const MAX_RETRIES = 5;
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
export function recipientsOf(roster, self) {
    const seen = new Map();
    for (const c of roster || []) {
        const channel = Number(c.channel_id ?? c.channelID ?? 0);
        const device = String(c.media_device || "");
        const principal = String(c.unique_id || "");
        const clientID = String(c.client_id ?? "");
        if (channel !== Number(self.channelID) || !device || !principal || !clientID) continue;
        if (clientID === String(self.clientID) || device === self.device) continue;
        if (!ID_RE.test(device) || !ID_RE.test(principal)) continue;
        seen.set(`${principal}\n${device}`, { principal, device, clientID });
    }
    const order = r => `${r.principal}\n${r.device}`;
    return [...seen.values()].sort((a, b) => (order(a) < order(b) ? -1 : order(a) > order(b) ? 1 : 0));
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
    #publishers = new Map(); #pending = []; #failed = new Map(); #reason = "";
    #lastReceived = new Map();

    constructor({ media, send, onStatus = () => {}, now = () => Date.now(), timers = globalThis, random = n => crypto.getRandomValues(new Uint8Array(n)), uuid = () => crypto.randomUUID(), sendTimeoutMs = SEND_TIMEOUT_MS }) {
        this.media = media; this.send = send; this.onStatus = onStatus; this.now = now;
        this.timers = timers; this.random = random; this.uuid = uuid; this.sendTimeoutMs = sendTimeoutMs;
        this.self = null; this.session = "";
        this.counters = { ignored: {}, applied: 0, sent: 0, failed: 0, epochs: 0 };
    }

    get running() { return this.self !== null; }
    get epoch() { return this.#epoch; }

    start({ channelID, clientID, principal, device }) {
        if (this.running) throw new Error("media key session already running");
        if (!channelID || !clientID || !principal || !device) throw new Error("encrypted media needs a signed-in device");
        this.self = { channelID: Number(channelID), clientID: String(clientID), principal: String(principal), device: String(device) };
        this.session = b64url(this.random(16));
        const gen = ++this.#gen;
        this.#renew = this.timers.setInterval(() => { if (gen === this.#gen) this.#schedule("renewal", 0); }, RENEW_MS);
        this.#schedule("start", 0);
        this.#status();
    }

    stop() {
        if (!this.running) return;
        this.#gen++;
        this.timers.clearTimeout(this.#debounce); this.timers.clearInterval(this.#renew); this.timers.clearTimeout(this.#retry);
        const pids = [...SLOTS.map(senderPid), ...[...this.#publishers.keys()].flatMap(c => SLOTS.map(s => publicationPid(c, s))), ...SLOTS.map(s => publicationPid(this.self.clientID, s))];
        this.self = null; this.#keys = null; this.#publishers.clear(); this.#pending = []; this.#failed.clear(); this.#sentTo.clear();
        for (const pid of pids) this.media.forget(pid).catch(() => {});
        this.#status();
    }

    // Roster: the server's client list (all channels or just mine).
    setRoster(clients) {
        this.#roster = Array.isArray(clients) ? clients.slice() : [];
        if (!this.running) return;
        const recipients = recipientsOf(this.#roster, this.self);
        const key = recipients.map(r => `${r.principal}\n${r.device}`).join("\n");
        // Publishers that left (or whose device/channel changed) lose their keys.
        for (const [client, state] of this.#publishers) {
            const entry = this.#roster.find(c => String(c.client_id) === client);
            if (!entry || Number(entry.channel_id) !== this.self.channelID || entry.unique_id !== state.principal || entry.media_device !== state.device) {
                this.#publishers.delete(client);
                for (const slot of SLOTS) this.media.forget(publicationPid(client, slot)).catch(() => {});
            }
        }
        this.#drainPending();
        if (key !== this.#recipientKey) {
            const current = new Set(recipients.map(r => `${r.principal}\n${r.device}`));
            const removal = this.#recipients.some(r => !current.has(`${r.principal}\n${r.device}`));
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
        this.timers.clearTimeout(this.#debounce);
        const gen = this.#gen;
        this.#debounce = this.timers.setTimeout(() => { if (gen === this.#gen) void this.#run(); }, delay);
    }

    async #run() {
        if (this.#busy) { this.#again = true; return; }
        this.#busy = true;
        const gen = this.#gen;
        try {
            do { this.#again = false; await this.#rekey(gen); } while (this.#again && gen === this.#gen);
        } catch (error) {
            if (gen === this.#gen) { this.lastError = String(error?.message || error); this.#status(); }
        } finally { this.#busy = false; }
    }

    async #rekey(gen) {
        const self = this.self;
        const urgent = this.#reason === "removal";
        this.#reason = ""; this.#busyOrPending = false;
        const previous = this.#epoch;
        const epoch = Math.max(previous + 1, this.now());
        const kid = (this.#kid + 1) % 16;
        const keys = Object.fromEntries(SLOTS.map(slot => [slot, this.random(32)]));
        const recipients = this.#recipients = recipientsOf(this.#roster, self);
        const members = await membershipDigest([...recipients, self]);
        for (const slot of SLOTS) {
            await this.media.setKey(senderPid(slot), epoch, kid, keys[slot]);
            // Encrypted self-echo (echo test channel): my own publication
            // arrives under my client ID and decrypts with my own key.
            await this.media.setKey(publicationPid(self.clientID, slot), epoch, kid, keys[slot]);
        }
        if (gen !== this.#gen) return;
        this.#epoch = epoch; this.#kid = kid; this.#keys = keys; this.#members = members;
        this.counters.epochs++;
        this.#failed.clear(); this.#sentTo.clear();
        await this.#distribute(gen, recipients);
        if (gen !== this.#gen || this.#epoch !== epoch) return;
        await new Promise(resolve => this.timers.setTimeout(resolve, urgent ? REMOVAL_ACTIVATE_DELAY_MS : ACTIVATE_DELAY_MS));
        if (gen !== this.#gen || this.#epoch !== epoch) return;
        for (const slot of SLOTS) await this.media.activate(senderPid(slot), epoch);
        this.activeEpoch = epoch;
        if (previous) {
            this.timers.setTimeout(() => {
                if (gen !== this.#gen) return;
                for (const slot of SLOTS) { this.media.retire(senderPid(slot), previous).catch(() => {}); this.media.retire(publicationPid(self.clientID, slot), previous).catch(() => {}); }
            }, SENDER_RETIRE_MS);
        }
        this.#status();
        if (this.#failed.size) {
            this.timers.clearTimeout(this.#retry);
            this.#retry = this.timers.setTimeout(() => { if (gen === this.#gen && this.#epoch === epoch) void this.#retryFailed(gen, epoch); }, RETRY_FAILED_MS);
        }
    }

    async #distribute(gen, recipients) {
        const self = this.self;
        const body = buildMediaKeyBody({ channelID: self.channelID, epoch: this.#epoch, nowSeconds: Math.floor(this.now() / 1000), client: self.clientID,
            session: this.session, kid: this.#kid, members: this.#members, count: recipients.length + 1, keys: this.#keys });
        const results = await pool(recipients, CONCURRENCY, async recipient => {
            let timer;
            const timeout = new Promise((_, reject) => { if (Number.isFinite(this.sendTimeoutMs)) timer = this.timers.setTimeout(() => reject(Object.assign(new Error("timed out"), { code: "timeout" })), this.sendTimeoutMs); });
            try { return await Promise.race([this.send({ logicalID: this.uuid(), principal: recipient.principal, device: recipient.device, body }), timeout]); }
            finally { this.timers.clearTimeout(timer); }
        });
        if (gen !== this.#gen) return;
        results.forEach((result, i) => {
            const r = recipients[i];
            if (result?.ok) { this.counters.sent++; this.#sentTo.set(`${r.principal}\n${r.device}`, this.#epoch); this.#failed.delete(`${r.principal}\n${r.device}`); }
            else { this.counters.failed++; this.#failed.set(`${r.principal}\n${r.device}`, { ...r, reason: String(result?.error?.code || result?.error?.message || result?.reason || "failed") }); }
        });
    }

    async #retryFailed(gen, epoch, attempt = 1) {
        const current = new Set(this.#recipients.map(r => `${r.principal}\n${r.device}`));
        const again = [...this.#failed.values()].filter(r => current.has(`${r.principal}\n${r.device}`));
        if (!again.length) return;
        await this.#distribute(gen, again);
        if (gen !== this.#gen || this.#epoch !== epoch) return;
        this.#status();
        // Bounded: later epochs (membership, publication, renewal) retry anyway.
        if (this.#failed.size && attempt < MAX_RETRIES) {
            this.#retry = this.timers.setTimeout(() => { if (gen === this.#gen && this.#epoch === epoch) void this.#retryFailed(gen, epoch, attempt + 1); }, RETRY_FAILED_MS * (attempt + 1));
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
        return !!entry && Number(entry.channel_id) === this.self.channelID && entry.unique_id === control.sender && entry.media_device === control.senderDevice;
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
        const last = this.#lastReceived.get(control.senderDevice) || 0;
        if (control.epoch <= last) return this.#ignore("stale");
        this.#lastReceived.set(control.senderDevice, control.epoch);
        let state = this.#publishers.get(value.client);
        if (!state || state.device !== control.senderDevice) {
            state = { principal: control.sender, device: control.senderDevice, epochs: [], members: value.members, n: value.n };
            this.#publishers.set(value.client, state);
        }
        state.members = value.members; state.n = value.n; state.session = value.session;
        const install = (async () => {
            for (const slot of SLOTS) await this.media.setKey(publicationPid(value.client, slot), control.epoch, value.kid, value.keys[slot]);
        })();
        install.then(() => {
            if (gen !== this.#gen || this.#publishers.get(value.client) !== state) {
                for (const slot of SLOTS) this.media.retire(publicationPid(value.client, slot), control.epoch).catch(() => {});
                return;
            }
            state.epochs.push(control.epoch);
            const previous = state.epochs.slice(0, -1);
            state.epochs = state.epochs.slice(-1);
            if (previous.length) {
                const through = Math.max(...previous);
                this.timers.setTimeout(() => {
                    for (const slot of SLOTS) this.media.retire(publicationPid(value.client, slot), through).catch(() => {});
                }, RECEIVE_GRACE_MS);
            }
            this.counters.applied++;
            this.#status();
        }, () => this.#ignore("install"));
        return { ok: true, epoch: control.epoch };
    }

    #ignore(reason) { this.counters.ignored[reason] = (this.counters.ignored[reason] || 0) + 1; return { ok: false, reason }; }

    publisherStatus(clientID) {
        const state = this.#publishers.get(String(clientID));
        return state ? { device: state.device, epoch: state.epochs.at(-1) || 0, members: state.members, n: state.n } : null;
    }

    status() {
        if (!this.running) return { state: "off" };
        return {
            state: !this.activeEpoch ? "keying" : this.#failed.size ? "partial" : "encrypted",
            epoch: this.activeEpoch || 0,
            recipients: this.#recipients.length,
            delivered: [...this.#sentTo.values()].filter(e => e === this.#epoch).length,
            failed: [...this.#failed.values()].map(({ principal, device, clientID, reason }) => ({ principal, device, clientID, reason })),
            members: this.#members,
            publishers: this.#publishers.size,
            error: this.lastError || "",
        };
    }

    #status() { try { this.onStatus(this.status()); } catch { /* status listeners never break keying */ } }
}
