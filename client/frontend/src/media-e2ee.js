// Encrypted channel media, page side (Task 2). One MediaE2EE per voice
// session owns one worker (media-e2ee.worker.js). Every channel sender and
// receiver is protected the moment it exists (addTransceiver / addTrack /
// track event), before any negotiation can carry its media. Unsupported
// runtimes fail with an explicit error; there is no plaintext mode.
import { MEDIA_E2EE_VERSION, SLOTS, validEpoch, validKid, validPid } from "./media-e2ee-core.js";

export { MEDIA_E2EE_VERSION, SLOTS };

export class MediaE2EEUnsupported extends Error {
    constructor(reason) { super(`Encrypted media is not supported here (${reason})`); this.name = "MediaE2EEUnsupported"; this.reason = reason; }
}

// "script" (RTCRtpScriptTransform) is preferred; "streams"
// (createEncodedStreams) is the older Chromium path. Both are tested.
export function mediaE2EESupport(scope = globalThis) {
    if (typeof scope.Worker !== "function") return { ok: false, reason: "worker" };
    if (typeof scope.crypto?.subtle !== "object") return { ok: false, reason: "webcrypto" };
    if (typeof scope.RTCRtpScriptTransform === "function") return { ok: true, api: "script" };
    if (typeof scope.RTCRtpSender?.prototype?.createEncodedStreams === "function"
        && typeof scope.RTCRtpReceiver?.prototype?.createEncodedStreams === "function") return { ok: true, api: "streams" };
    return { ok: false, reason: "transform" };
}

let endpointCounter = 0;
const nextEndpointID = prefix => `${prefix}:${(++endpointCounter).toString(36)}${crypto.getRandomValues(new Uint32Array(1))[0].toString(36)}`;

export class MediaE2EE {
    constructor({ api, onError = () => {}, workerURL, scope = globalThis } = {}) {
        const support = mediaE2EESupport(scope);
        if (!support.ok) throw new MediaE2EEUnsupported(support.reason);
        if (api && api !== support.api && !(api === "streams" && typeof scope.RTCRtpSender?.prototype?.createEncodedStreams === "function")) throw new MediaE2EEUnsupported(api);
        // An injected scope must bring its own worker URL: the default worker
        // is built for (and constructed in) the page's global scope only.
        if (scope !== globalThis && !workerURL) throw new MediaE2EEUnsupported("worker");
        this.api = api || support.api;
        this.scope = scope;
        this.onError = onError;
        this.closed = false;
        this.seq = 0;
        this.pending = new Map();
        this.endpoints = new WeakMap();
        this.peers = new WeakSet();
        // The literal `new Worker(new URL(...), ...)` form is what makes the
        // bundler emit the worker as a real bundle (a regression test loads the
        // built worker); workerURL is a test/harness override only.
        this.worker = workerURL
            ? new scope.Worker(workerURL, { type: "module" })
            : new Worker(new URL("./media-e2ee.worker.js", import.meta.url), { type: "module" });
        this.byTrack = new WeakMap();   // receiver track -> receiver
        this.byId = new Map();          // endpoint id -> receiver
        this.worker.onmessage = ({ data }) => {
            if (data?.event === "attach-failed") { this.onError(new Error(`Encrypted media transform failed: ${data.error}`)); return; }
            if (data?.event === "first-frame") { this.#authenticate(data.id, data.gen); return; }
            const pending = this.pending.get(data?.seq);
            if (!pending) return;
            this.pending.delete(data.seq); clearTimeout(pending.timer);
            if (data.ok) pending.resolve(data.value); else pending.reject(new Error(data.value || "Encrypted media worker failed"));
        };
        this.worker.onerror = () => { this.fail(new Error("Encrypted media worker stopped")); };
        this.ready = this.request("init", {});
    }

    request(op, data, transfer = []) {
        if (this.closed) return Promise.reject(new Error("Encrypted media session ended"));
        if (this.pending.size >= 256) return Promise.reject(new Error("Encrypted media request capacity reached"));
        const seq = ++this.seq;
        return new Promise((resolve, reject) => {
            const timer = setTimeout(() => { this.pending.delete(seq); reject(new Error("Encrypted media worker timed out")); }, 10000);
            this.pending.set(seq, { resolve, reject, timer });
            try { this.worker.postMessage({ op, seq, ...data }, transfer); }
            catch (error) { clearTimeout(timer); this.pending.delete(seq); reject(error); }
        });
    }

    fail(error) { if (this.closed) return; this.close(); this.onError(error); }

    // RTCPeerConnection configuration for this session. The streams API
    // needs encodedInsertableStreams so that no frame flows untransformed.
    peerConfig(config = {}) {
        return this.api === "streams" ? { ...config, encodedInsertableStreams: true } : { ...config };
    }

    #attach(endpoint, operation, kind) {
        if (this.closed) throw new Error("Encrypted media session ended");
        if (kind !== "audio" && kind !== "video") throw new Error("Invalid encrypted media endpoint");
        const known = this.endpoints.get(endpoint);
        if (known) return known.id;
        const id = nextEndpointID(operation === "encode" ? "tx" : "rx");
        if (this.api === "script") {
            endpoint.transform = new this.scope.RTCRtpScriptTransform(this.worker, { id, operation, kind });
        } else {
            const { readable, writable } = endpoint.createEncodedStreams();
            this.worker.postMessage({ op: "attach", id, operation, kind, readable, writable }, [readable, writable]);
        }
        this.endpoints.set(endpoint, { id, pid: "", operation, kind, gen: 0, authenticated: false, waiters: [] });
        if (operation === "decode") {
            // Presentation gate: no output until the first authenticated frame.
            if (endpoint.track) { endpoint.track.enabled = false; this.byTrack.set(endpoint.track, endpoint); }
            this.byId.set(id, endpoint);
        }
        return id;
    }

    #authenticate(id, gen) {
        const receiver = this.byId.get(id);
        const entry = receiver && this.endpoints.get(receiver);
        if (!entry || entry.authenticated || (entry.gen && gen !== entry.gen)) return;
        entry.authenticated = true;
        if (receiver.track) receiver.track.enabled = true;
        for (const fn of entry.waiters.splice(0)) { try { fn(); } catch { /* listener errors never break media */ } }
    }

    #receiverOf(trackOrReceiver) { return this.endpoints.has(trackOrReceiver) ? trackOrReceiver : this.byTrack.get(trackOrReceiver); }

    // Whether a receiver (or its track) has output its first authenticated
    // frame for its current binding. Unknown endpoints are not channel media.
    authenticated(trackOrReceiver) {
        const receiver = this.#receiverOf(trackOrReceiver);
        const entry = receiver && this.endpoints.get(receiver);
        return entry ? entry.authenticated : undefined;
    }

    // Calls fn once the receiver is authenticated (immediately if it is).
    onAuthenticated(trackOrReceiver, fn) {
        const receiver = this.#receiverOf(trackOrReceiver);
        const entry = receiver && this.endpoints.get(receiver);
        if (!entry || entry.authenticated) { fn(); return; }
        if (entry.waiters.length < 16) entry.waiters.push(fn);
    }

    protectSender(sender, kind) { return this.#attach(sender, "encode", kind || sender.track?.kind); }
    protectReceiver(receiver, kind) { return this.#attach(receiver, "decode", kind || receiver.track?.kind); }
    endpointID(endpoint) { return this.endpoints.get(endpoint)?.id || ""; }
    boundPid(endpoint) { return this.endpoints.get(endpoint)?.pid || ""; }

    protectTransceiver(transceiver, kind) {
        const k = kind || transceiver.receiver?.track?.kind || transceiver.sender?.track?.kind;
        if (transceiver.sender) this.protectSender(transceiver.sender, k);
        if (transceiver.receiver) this.protectReceiver(transceiver.receiver, k);
    }

    // Protects every current and future transceiver of a channel peer. The
    // wrappers run synchronously at creation, before any offer exists.
    protectPeer(pc) {
        if (this.peers.has(pc)) return pc;
        this.peers.add(pc);
        const addTransceiver = pc.addTransceiver.bind(pc);
        pc.addTransceiver = (trackOrKind, init) => {
            const transceiver = addTransceiver(trackOrKind, init);
            this.protectTransceiver(transceiver, typeof trackOrKind === "string" ? trackOrKind : trackOrKind.kind);
            return transceiver;
        };
        const addTrack = pc.addTrack?.bind(pc);
        if (addTrack) {
            pc.addTrack = (track, ...streams) => {
                const sender = addTrack(track, ...streams);
                const transceiver = pc.getTransceivers().find(item => item.sender === sender);
                if (transceiver) this.protectTransceiver(transceiver, track.kind); else this.protectSender(sender, track.kind);
                return sender;
            };
        }
        pc.addEventListener("track", event => {
            try { this.protectTransceiver(event.transceiver || { sender: null, receiver: event.receiver }, event.track.kind); }
            catch (error) { this.fail(error); }
        });
        this.syncPeer(pc);
        return pc;
    }

    // Covers transceivers created by a remote offer before its track event.
    syncPeer(pc) {
        for (const transceiver of pc.getTransceivers?.() || []) {
            const kind = transceiver.receiver?.track?.kind;
            if (kind) this.protectTransceiver(transceiver, kind);
        }
    }

    async bind(endpoint, pid) {
        const entry = this.endpoints.get(endpoint);
        if (!entry || (pid !== "" && !validPid(pid))) throw new Error("Invalid encrypted media binding");
        if (entry.pid === pid && !entry.binding) return;
        // Recorded only once the worker accepted it: a failed or timed-out
        // bind is retried by the next bind instead of being skipped forever.
        if (entry.operation === "decode" && entry.pid !== pid) {
            entry.authenticated = false;
            if (endpoint.track) endpoint.track.enabled = false;
        }
        const binding = this.request("bind", { id: entry.id, pid });
        entry.binding = binding;
        try {
            const result = await binding;
            if (entry.binding === binding) { entry.pid = pid; entry.gen = result?.gen || 0; }
        } catch (error) {
            if (entry.binding === binding) entry.pid = "";
            throw error;
        } finally {
            if (entry.binding === binding) entry.binding = null;
        }
    }

    async setKey(pid, epoch, kid, key) {
        if (!validPid(pid) || !validEpoch(epoch) || !validKid(kid) || !(key instanceof Uint8Array) || key.length !== 32) throw new Error("Invalid media key");
        const copy = new Uint8Array(key);
        return this.request("key", { pid, epoch, kid, key: copy });
    }
    activate(pid, epoch) { return this.request("activate", { pid, epoch }); }
    retire(pid, through) { return this.request("retire", { pid, through }); }
    retireOne(pid, epoch) { return this.request("retireOne", { pid, epoch }); }
    forget(pid) { return this.request("forget", { pid }); }
    stats(endpoint) { return this.request("stats", { id: endpoint ? this.endpointID(endpoint) : undefined }); }

    close() {
        if (this.closed) return;
        this.closed = true;
        try { this.worker.terminate(); } catch { /* already gone */ }
        for (const pending of this.pending.values()) { clearTimeout(pending.timer); pending.reject(new Error("Encrypted media session ended")); }
        this.pending.clear();
    }
}
