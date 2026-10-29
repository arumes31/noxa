// Encrypted channel media, worker side (Task 2). The frame cipher is the
// pinned, maintained LiveKit FrameCryptor (AES-GCM, codec-aware clear
// header as AAD, IV = SSRC | RTP timestamp | send counter). This module owns
// everything around it, for BOTH browser transform APIs:
//
//   - every endpoint (sender or receiver) gets exactly one pipeline, set up
//     through the same function whether its streams came from
//     createEncodedStreams (posted) or RTCRtpScriptTransform (onrtctransform);
//     the library's own onrtctransform and message handler are never
//     reachable from the page, so neither path can bypass these checks;
//   - keys are per publication slot and per epoch ("pid" + epoch, never a
//     shared or per-participant key); a retired epoch can never be installed
//     or accepted again, and stale (pid, epoch) frames are dropped;
//   - an endpoint is bound to one pid at a time; rebinding (a receiver MID
//     reassigned to another publisher) drops every frame tagged with the old
//     binding and only resumes after the new keys are installed (fence);
//   - encode output must be exactly the library's encrypted frame (length
//     +30, trailer, active key slot, Opus/VP8 only); anything else is dropped,
//     so a disabled/keyless/unknown state can never emit plaintext;
//   - decode input must carry the encrypted trailer of a live epoch, and the
//     output must be exactly the authenticated plaintext (length -30) before
//     the replay window accepts it.
//
// The page talks to this module only through {op: ...} messages.
import { MediaReplayWindow } from "./media-replay.js";

export const MEDIA_E2EE_VERSION = "noxa-media-e2ee-v1";
export const SLOTS = Object.freeze(["mic", "cam", "screen", "screenaudio"]);
const IV_LENGTH = 12;
const OVERHEAD = 16 + IV_LENGTH + 2; // GCM tag + IV + trailer
const MIN_ENCRYPTED = 1 + OVERHEAD;
const KEYRING = 16;
const MAX_LIVE_EPOCHS = 3;
const MAX_PIDS = 512;
const MAX_ENDPOINTS = 512;
const MAX_RETIRED_MARKERS = 4096;
const FENCE = "noxa-fence";
// 2 x the 5-minute renewal plus grace.
export const MAX_RECEIVE_EPOCH_AGE = 12 * 60 * 1000;
const PID_RE = /^[\x21-\x7e]{1,160}$/;
const ENDPOINT_RE = /^[a-z]+:[A-Za-z0-9_-]{1,64}$/;
const DROP_REASONS = ["unbound", "empty", "plaintext", "nokey", "codec", "stale", "unauthenticated", "replay", "unencrypted"];

export function validPid(pid) { return typeof pid === "string" && PID_RE.test(pid); }
export function validEpoch(epoch) { return Number.isSafeInteger(epoch) && epoch > 0; }
export function validKid(kid) { return Number.isInteger(kid) && kid >= 0 && kid < KEYRING; }

// mimeType policy: Opus and VP8 are the only proven encrypted codecs.
export function codecAllowed(frame, kind) {
    let mime = "";
    try { mime = String(frame.getMetadata?.().mimeType || "").toLowerCase(); } catch { mime = ""; }
    if (kind === "video") return mime === "video/vp8";
    return mime === "" || mime === "audio/opus";
}

export function installMediaE2EEWorker(scope, { reply } = {}) {
    const delegate = scope.onmessage;
    if (typeof delegate !== "function") throw new Error("maintained media worker missing");
    const original = typeof scope.postMessage === "function" ? scope.postMessage.bind(scope) : () => {};
    const send = reply || original;
    const fences = new Map();
    let fenceSeq = 0;
    let initialized = null;
    // The library's own replies never reach the page; only fence/init acks
    // are consumed here. Library errors become counters.
    scope.postMessage = (message) => {
        if (message?.kind === "enable" && message.data?.participantIdentity === FENCE) {
            const done = fences.get(message.data.uuid); fences.delete(message.data.uuid); done?.();
        } else if (message?.kind === "initAck") initialized?.();
        else if (message?.kind === "error") stats.cryptorErrors++;
    };
    const lk = (kind, data) => delegate({ data: { kind, data } });
    const fence = () => new Promise(resolve => {
        const uuid = `f${++fenceSeq}`;
        fences.set(uuid, resolve);
        lk("enable", { participantIdentity: FENCE, enabled: true, uuid });
    });

    const stats = { encrypted: 0, decrypted: 0, cryptorErrors: 0, dropped: Object.fromEntries(DROP_REASONS.map(r => [r, 0])) };
    const pids = new Map();      // pid -> {epochs: Map<epoch,{kid,key,fp}>, active, retired}
    const retiredMarks = new Map(); // pid -> highest retired epoch, survives forget
    const endpoints = new Map(); // id -> endpoint
    const windows = new Map();   // pid#epoch -> MediaReplayWindow
    const pendingBinds = new Map(); // endpoint id -> pid, bound before its rtctransform event
    let queue = Promise.resolve();
    const serial = task => { const run = queue.then(task); queue = run.catch(() => {}); return run; };
    const drop = (ep, reason) => { stats.dropped[reason]++; if (ep) ep.dropped[reason] = (ep.dropped[reason] || 0) + 1; };

    async function junk() {
        const bytes = crypto.getRandomValues(new Uint8Array(32));
        return crypto.subtle.importKey("raw", bytes, "HKDF", false, ["deriveBits", "deriveKey"]);
    }
    function retiredThrough(pid) { return Math.max(pids.get(pid)?.retired || 0, retiredMarks.get(pid) || 0); }
    function mark(pid, epoch) {
        retiredMarks.delete(pid);
        retiredMarks.set(pid, Math.max(epoch, retiredThrough(pid)));
        while (retiredMarks.size > MAX_RETIRED_MARKERS) retiredMarks.delete(retiredMarks.keys().next().value);
    }
    function liveEpoch(pid, epoch) { return pids.get(pid)?.epochs.has(epoch) === true; }

    // Remove one epoch everywhere: maps first (frames drop synchronously),
    // then scrub the library slot with unrelated junk material.
    async function retireEpoch(pid, epoch) {
        const state = pids.get(pid); const entry = state?.epochs.get(epoch);
        if (!entry) return;
        state.epochs.delete(epoch);
        state.retired = Math.max(state.retired, epoch);
        if (state.active === epoch) state.active = 0;
        mark(pid, epoch);
        windows.delete(`${pid}#${epoch}`);
        for (const ep of endpoints.values()) {
            if (ep.pid !== pid || ep.kids.get(entry.kid) !== epoch) continue;
            ep.kids.delete(entry.kid);
            lk("setKey", { participantIdentity: ep.lk, key: await junk(), keyIndex: entry.kid, updateCurrentKeyIndex: false });
        }
    }

    async function installInto(ep) {
        const state = pids.get(ep.pid);
        const wanted = new Map();
        for (const [epoch, entry] of state?.epochs || []) wanted.set(entry.kid, { epoch, entry });
        for (const [kid] of ep.kids) {
            if (!wanted.has(kid) || wanted.get(kid).epoch !== ep.kids.get(kid)) {
                ep.kids.delete(kid);
                lk("setKey", { participantIdentity: ep.lk, key: await junk(), keyIndex: kid, updateCurrentKeyIndex: false });
            }
        }
        const installed = [];
        for (const [kid, { epoch, entry }] of wanted) {
            if (ep.kids.get(kid) === epoch) continue;
            lk("setKey", { participantIdentity: ep.lk, key: entry.key, keyIndex: kid, updateCurrentKeyIndex: ep.operation === "encode" && state.active === epoch });
            installed.push([kid, epoch]);
        }
        return installed;
    }

    function attach(id, operation, kind, readable, writable, transformer = null) {
        if (typeof id !== "string" || !ENDPOINT_RE.test(id) || (operation !== "encode" && operation !== "decode") || (kind !== "audio" && kind !== "video")) throw new Error("invalid media endpoint");
        if (endpoints.has(id)) throw new Error("media endpoint already attached");
        if (endpoints.size >= MAX_ENDPOINTS) throw new Error("media endpoint capacity reached");
        const ep = { id, operation, kind, lk: `ep:${id}`, pid: "", gen: 0, ready: false, kids: new Map(), dropped: {}, frames: 0, authenticated: false, transformer };
        endpoints.set(id, ep);
        if (pendingBinds.has(id)) {
            const pid = pendingBinds.get(id);
            pendingBinds.delete(id);
            serial(() => ops.bind({ id, pid })).catch(error => send({ event: "attach-failed", id, error: String(error?.message || error) }));
        }
        lk("enable", { participantIdentity: ep.lk, enabled: true });
        const inputs = new WeakMap();
        const pre = new TransformStream({
            transform(frame, controller) {
                if (!ep.ready || !ep.pid) return drop(ep, "unbound");
                const n = frame.data?.byteLength || 0;
                if (n === 0) return drop(ep, "empty");
                if (!codecAllowed(frame, ep.kind)) return drop(ep, "codec");
                if (ep.operation === "encode") {
                    if (!ep.kids.size || !pids.get(ep.pid)?.active) return drop(ep, "nokey");
                    inputs.set(frame, { gen: ep.gen, pid: ep.pid, n });
                    return controller.enqueue(frame);
                }
                const bytes = new Uint8Array(frame.data);
                if (n < MIN_ENCRYPTED || bytes[n - 2] !== IV_LENGTH) return drop(ep, "plaintext");
                const kid = bytes[n - 1];
                const epoch = ep.kids.get(kid);
                if (!epoch || !liveEpoch(ep.pid, epoch)) return drop(ep, "nokey");
                // A receive epoch older than MAX_RECEIVE_EPOCH_AGE is stale: keys
                // renew every 5 minutes, and the replay window assumes an epoch
                // never lives near half the RTP timestamp range.
                // Monotonic clock: a wall-clock step never expires keys early.
                if (performance.now() - pids.get(ep.pid).epochs.get(epoch).at > MAX_RECEIVE_EPOCH_AGE) return drop(ep, "stale");
                inputs.set(frame, { gen: ep.gen, pid: ep.pid, epoch, n, iv: bytes.slice(n - 2 - IV_LENGTH, n - 2) });
                controller.enqueue(frame);
            },
        });
        const post = new TransformStream({
            transform(frame, controller) {
                const input = inputs.get(frame); inputs.delete(frame);
                if (!input || input.gen !== ep.gen || input.pid !== ep.pid || !ep.ready) return drop(ep, "stale");
                const n = frame.data?.byteLength || 0;
                if (ep.operation === "encode") {
                    const bytes = new Uint8Array(frame.data);
                    if (n !== input.n + OVERHEAD || bytes[n - 2] !== IV_LENGTH || !liveEpoch(ep.pid, ep.kids.get(bytes[n - 1]))) return drop(ep, "unencrypted");
                    stats.encrypted++; ep.frames++;
                    return controller.enqueue(frame);
                }
                if (!liveEpoch(input.pid, input.epoch)) return drop(ep, "stale");
                // The library forwards a frame unchanged when it does not
                // decrypt it; only an authenticated frame shrinks by exactly
                // the tag, IV and trailer.
                if (n !== input.n - OVERHEAD) return drop(ep, "unauthenticated");
                const key = `${input.pid}#${input.epoch}`;
                let window = windows.get(key);
                if (!window) { window = new MediaReplayWindow(); windows.set(key, window); }
                if (!window.accept(input.iv)) return drop(ep, "replay");
                stats.decrypted++; ep.frames++;
                // The page keeps a new or rebound receiver's track disabled and
                // unattached until this first authenticated frame of its binding
                // (nothing decoded before the transform existed reaches a sink);
                // a keyframe request then refreshes the picture.
                if (!ep.authenticated) {
                    ep.authenticated = true;
                    send({ event: "first-frame", id: ep.id, gen: ep.gen });
                    if (ep.kind === "video") { try { ep.transformer?.sendKeyFrameRequest?.()?.catch?.(() => {}); } catch { /* not all runtimes */ } }
                }
                controller.enqueue(frame);
            },
        });
        void post.readable.pipeTo(writable).catch(() => {});
        lk(operation, { participantIdentity: ep.lk, trackId: id, codec: kind === "video" ? "vp8" : undefined, hasPacketTrailer: false,
            readableStream: readable.pipeThrough(pre), writableStream: post.writable });
        return ep;
    }

    const ops = {
        async init() {
            if (!initialized) {
                const ready = new Promise(resolve => { initialized = resolve; });
                lk("init", { keyProviderOptions: { sharedKey: false, ratchetSalt: "LKFrameEncryptionKey", ratchetWindowSize: 0, failureTolerance: -1, keyringSize: KEYRING, keySize: 128 }, loglevel: "silent" });
                await ready;
            }
            return { version: MEDIA_E2EE_VERSION };
        },
        attach({ id, operation, kind, readable, writable }) { attach(id, operation, kind, readable, writable); return {}; },
        async bind({ id, pid }) {
            if (typeof id !== "string" || !ENDPOINT_RE.test(id) || (pid !== "" && !validPid(pid))) throw new Error("invalid media binding");
            const ep = endpoints.get(id);
            if (!ep) {
                // RTCRtpScriptTransform attaches on the worker's rtctransform
                // event, which may arrive after this message: keep the binding
                // and apply it the moment the endpoint attaches.
                if (!pendingBinds.has(id) && pendingBinds.size >= MAX_ENDPOINTS) throw new Error("media endpoint capacity reached");
                pendingBinds.set(id, pid);
                return { pending: true };
            }
            if (ep.pid === pid && ep.ready) return { gen: ep.gen };
            const changed = ep.pid !== pid;
            ep.ready = false; ep.gen++; ep.pid = pid; ep.authenticated = false;
            const gen = ep.gen;
            // Key slots are only meaningful within one publication: on any pid
            // change every slot is scrubbed, so a colliding (kid, epoch) of the
            // former publication can never be mistaken for the new one.
            if (changed) {
                ep.kids.clear();
                for (let kid = 0; kid < KEYRING; kid++) lk("setKey", { participantIdentity: ep.lk, key: await junk(), keyIndex: kid, updateCurrentKeyIndex: false });
            }
            if (!pid) { await installInto(ep); return { gen }; }
            const installed = await installInto(ep);
            await fence();
            if (ep.gen !== gen) return { gen: ep.gen, superseded: true };
            for (const [kid, epoch] of installed) if (liveEpoch(pid, epoch)) ep.kids.set(kid, epoch);
            ep.ready = true;
            return { gen };
        },
        async key({ pid, epoch, kid, key }) {
            if (!validPid(pid) || !validEpoch(epoch) || !validKid(kid) || !(key instanceof Uint8Array) || key.length !== 32) throw new Error("invalid media key");
            if (epoch <= retiredThrough(pid)) throw new Error("retired media epoch");
            const fp = Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256", key))).join(",");
            let state = pids.get(pid);
            const existing = state?.epochs.get(epoch);
            if (existing) {
                if (existing.fp !== fp || existing.kid !== kid) throw new Error("conflicting media key for epoch");
                return { installed: false };
            }
            if (!state) {
                if (pids.size >= MAX_PIDS) throw new Error("media key capacity reached");
                state = { epochs: new Map(), active: 0, retired: retiredThrough(pid) };
                pids.set(pid, state);
            }
            const material = await crypto.subtle.importKey("raw", key, "HKDF", false, ["deriveBits", "deriveKey"]);
            if (epoch <= retiredThrough(pid) || state.epochs.has(epoch)) throw new Error("retired media epoch");
            // A key slot is reused only by a newer epoch; whatever held it retires.
            // A key slot is only ever taken over by a NEWER epoch.
            for (const [old, entry] of state.epochs) if (entry.kid === kid && old > epoch) throw new Error("a newer media epoch holds this key slot");
            for (const [old, entry] of [...state.epochs]) if (entry.kid === kid) await retireEpoch(pid, old);
            while (state.epochs.size >= MAX_LIVE_EPOCHS) await retireEpoch(pid, Math.min(...state.epochs.keys()));
            state.epochs.set(epoch, { kid, key: material, fp, at: performance.now() });
            const waiting = [];
            for (const ep of endpoints.values()) {
                if (ep.pid !== pid) continue;
                lk("setKey", { participantIdentity: ep.lk, key: material, keyIndex: kid, updateCurrentKeyIndex: false });
                waiting.push(ep);
            }
            await fence();
            for (const ep of waiting) if (ep.pid === pid && liveEpoch(pid, epoch)) ep.kids.set(kid, epoch);
            return { installed: true };
        },
        async activate({ pid, epoch }) {
            const state = pids.get(pid); const entry = state?.epochs.get(epoch);
            if (!entry) throw new Error("unknown media epoch");
            if (state.active && epoch < state.active) throw new Error("media epoch is older than the active one");
            state.active = epoch;
            for (const ep of endpoints.values()) {
                if (ep.pid === pid && ep.operation === "encode") lk("setKey", { participantIdentity: ep.lk, key: entry.key, keyIndex: entry.kid, updateCurrentKeyIndex: true });
            }
            await fence();
            return {};
        },
        async retire({ pid, through }) {
            if (!validPid(pid) || !Number.isSafeInteger(through)) throw new Error("invalid media retirement");
            const state = pids.get(pid);
            for (const epoch of [...(state?.epochs.keys() || [])]) if (epoch <= through) await retireEpoch(pid, epoch);
            if (through > 0) mark(pid, through);
            if (state) state.retired = Math.max(state.retired, through);
            await fence();
            return {};
        },
        // Retires exactly one epoch (older live epochs stay usable).
        async retireOne({ pid, epoch }) {
            if (!validPid(pid) || !validEpoch(epoch)) throw new Error("invalid media retirement");
            await retireEpoch(pid, epoch);
            await fence();
            return {};
        },
        async forget({ pid }) {
            if (!validPid(pid)) throw new Error("invalid media pid");
            const state = pids.get(pid);
            for (const epoch of [...(state?.epochs.keys() || [])]) await retireEpoch(pid, epoch);
            pids.delete(pid);
            await fence();
            return {};
        },
        stats({ id } = {}) {
            const ep = id ? endpoints.get(id) : null;
            return { ...structuredClone(stats), endpoint: ep ? { pid: ep.pid, ready: ep.ready, frames: ep.frames, dropped: { ...ep.dropped }, kids: [...ep.kids.entries()] } : undefined,
                pids: pids.size, endpoints: endpoints.size };
        },
    };

    scope.onmessage = event => {
        const { op, seq, ...data } = event.data || {};
        const handler = Object.hasOwn(ops, op) ? ops[op] : null;
        const respond = (ok, value) => { if (seq !== undefined) send({ seq, ok, value }); };
        if (!handler) { respond(false, "unknown media operation"); return; }
        // attach must run synchronously so no frame can arrive unhandled.
        if (op === "attach") {
            try { respond(true, handler(data)); } catch (error) { respond(false, String(error?.message || error)); }
            return;
        }
        serial(() => handler(data)).then(value => respond(true, value), error => respond(false, String(error?.message || error)));
    };
    // RTCRtpScriptTransform delivers its streams here. The options carry
    // only the endpoint identity; keys and bindings come via messages.
    scope.onrtctransform = event => {
        const { transformer } = event;
        const { id, operation, kind } = transformer?.options || {};
        try { attach(id, operation, kind, transformer.readable, transformer.writable, transformer); }
        catch (error) { send({ event: "attach-failed", id, error: String(error?.message || error) }); }
    };
    return { stats, endpoints, pids };
}
