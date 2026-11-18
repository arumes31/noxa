// End-to-end encrypted private-call media (#12). Shared by the desktop
// frontend and the PWA (an identical copy lives in
// mobile/src/core/media-e2ee/; a unit test keeps them identical).
//
// A private call is a peer-to-peer mesh over relay-only TURN. Its media now
// carries the same frame encryption as channel media: one MediaSession (the
// LiveKit FrameCryptor worker plus the MediaKeyEngine) per call, keyed by the
// call's own scope ("call:<id>", wire scope {type: "call-media", id}).
//
// Keys: every participant device creates its own epochs and sends them, one
// authenticated ratchet control (Olm) per recipient device, to the CURRENT
// accepted participants only. A participant who leaves triggers a fresh epoch
// it never receives; a joiner never receives an earlier one (MediaKeyEngine).
// The roster is the call state the server reports: an accepted participant
// with the client and device that answered. A control is only accepted from
// exactly that (account, device); the server and the TURN relay only ever see
// ciphertext.
//
// Transceivers: both ends order them the same way (private-call-media.js and
// calls.js): audio [microphone, screen audio] and video [camera, screen]. A
// sender is bound to this device's slot key and a receiver to the peer's.
//
// Native screen share (desktop, PR 5): a peer that shares natively opens a
// second relay-only connection per viewer, the "screen" sub-connection,
// carrying only its screen and screen audio under the same call keys. Its
// sealed signals carry sub "screen" (offer, candidates; from the sharer) or
// "screen-reply" (answer, candidates; from the viewer). A sharer opens one
// only to a peer whose main offer or answer listed SCREEN_SUB_FEATURE.
//
// There is no plaintext fallback. A peer whose app does not announce call
// encryption (CALL_E2EE_MARKER on the call-media data channel) cannot be
// heard or seen here; the caller ends the call with an "update required"
// message instead.
import { MediaSession, MediaSessionError, MEDIA_E2EE_VERSION } from "./media-session.js";
import { CALL_MEDIA_SCOPE_TYPE, MediaKeyStartupInbox, callScopeKey } from "./media-keys.js";

export { MediaSessionError };

/** Announced in every call-media data channel state message. */
export const CALL_E2EE_MARKER = MEDIA_E2EE_VERSION;
const AUDIO_SLOTS = Object.freeze(["mic", "screenaudio"]);
const VIDEO_SLOTS = Object.freeze(["cam", "screen"]);
// Only codecs the frame cipher admits (media-e2ee-core.js codecAllowed),
// VP8 first, plus their repair formats.
const VIDEO_CODECS = Object.freeze(["video/vp8", "video/h264"]);
const REPAIR_CODECS = Object.freeze(["video/rtx", "video/red", "video/ulpfec", "video/flexfec-03"]);

/**
 * The media-key roster of a call: every accepted participant whose answering
 * client and device are known. Entries name their call (never a channel), so
 * a call key can never reach a channel member or the other way round.
 */
export function callRoster(call) {
    if (!call || typeof call.id !== "string" || !call.id || call.ended_at) return [];
    const seen = new Set();
    const roster = [];
    for (const p of call.participants || []) {
        if (p?.state !== "accepted" || typeof p.unique_id !== "string" || !p.unique_id) continue;
        if (typeof p.device_id !== "string" || !p.device_id || p.client_id == null || p.client_id === "") continue;
        if (seen.has(p.unique_id)) continue;
        seen.add(p.unique_id);
        roster.push({ call_id: call.id, unique_id: p.unique_id, client_id: String(p.client_id), media_device: p.device_id, nickname: typeof p.nickname === "string" ? p.nickname : "" });
    }
    return roster;
}

/** [{transceiver, kind, slot}] of a call peer, in the fixed slot order. */
export function callTransceiverSlots(pc) {
    const out = [];
    const counts = { audio: 0, video: 0 };
    for (const transceiver of pc?.getTransceivers?.() || []) {
        const kind = transceiver.receiver?.track?.kind || transceiver.sender?.track?.kind;
        if (kind !== "audio" && kind !== "video") continue;
        const slots = kind === "audio" ? AUDIO_SLOTS : VIDEO_SLOTS;
        const slot = slots[counts[kind]++];
        if (slot) out.push({ transceiver, kind, slot });
    }
    return out;
}

/** [{transceiver, kind, slot}] of a screen sub-connection: video "screen", audio "screenaudio". */
export function callScreenSlots(pc) {
    const out = [];
    const seen = new Set();
    for (const transceiver of pc?.getTransceivers?.() || []) {
        const kind = transceiver.receiver?.track?.kind || transceiver.sender?.track?.kind;
        if ((kind !== "audio" && kind !== "video") || seen.has(kind)) continue;
        seen.add(kind);
        out.push({ transceiver, kind, slot: kind === "video" ? "screen" : "screenaudio" });
    }
    return out;
}

export const SCREEN_SUB_FEATURE = "screen-sub";
export const SCREEN_SUB = "screen";
export const SCREEN_SUB_REPLY = "screen-reply";
/** Features this app lists in its main call offers and answers. */
export const CALL_FEATURES = Object.freeze([SCREEN_SUB_FEATURE]);

/** Whether a peer's main offer or answer advertised the screen sub-connection. */
export function advertisesScreenSub(description) {
    return Array.isArray(description?.features) && description.features.includes(SCREEN_SUB_FEATURE);
}

/**
 * The viewer side of one peer's screen sub-connection: receives only, binds
 * the receivers to the peer's "screen"/"screenaudio" keys before answering,
 * and replies through send(kind, payload) (sealed with sub "screen-reply").
 * A new offer replaces the connection (the sharer reconnected). Errors
 * reject signal(); the caller closes this sub-connection, never the call.
 */
export class ScreenSubReceiver {
    constructor({ e2ee, config, peerClientID, send, onTrack = () => {}, onClose = () => {}, createPeer = c => new RTCPeerConnection(c) }) {
        Object.assign(this, { e2ee, config, peerClientID, send, onTrack, onClose, createPeer });
        this.pc = null;
        this.remote = [];
        this.remoteCount = 0;
    }

    async signal(description) {
        if (description?.sub !== SCREEN_SUB) throw new Error("not a screen sub-connection signal");
        if (description.type === "offer") return this.#offer(description.sdp);
        if (description.type !== "candidates") throw new Error("invalid screen sub-connection signal");
        const candidates = JSON.parse(description.sdp);
        if (!Array.isArray(candidates) || !candidates.length || candidates.length > 16 || this.remoteCount + candidates.length > 64) throw new Error("invalid screen sub-connection candidates");
        this.remoteCount += candidates.length;
        this.remote.push(...candidates);
        await this.#apply(this.pc);
    }

    async #offer(sdp) {
        this.close();
        const config = await this.config(); // may fetch the current relay lease
        const pc = this.e2ee.protectPeer(this.createPeer(this.e2ee.peerConfig(config)));
        Object.assign(this, { pc, remote: [], remoteCount: 0, local: [], localCount: 0, sent: false });
        pc.onicecandidate = event => {
            if (this.pc !== pc || !event.candidate?.candidate || ++this.localCount > 64) return;
            this.local.push(event.candidate.toJSON());
            this.#flush(pc);
        };
        pc.ontrack = event => { if (this.pc === pc) this.onTrack(event.track); };
        pc.onconnectionstatechange = () => { if (this.pc === pc && pc.connectionState === "failed") this.close(); };
        await pc.setRemoteDescription({ type: "offer", sdp });
        if (this.pc !== pc) return;
        for (const transceiver of pc.getTransceivers()) transceiver.direction = "recvonly";
        await this.e2ee.bindPeer(pc, this.peerClientID(), callScreenSlots);
        if (this.pc !== pc) return;
        await pc.setLocalDescription(await pc.createAnswer());
        if (this.pc !== pc) return;
        await this.send("answer", pc.localDescription.sdp);
        if (this.pc !== pc) return;
        this.sent = true;
        this.#flush(pc);
        await this.#apply(pc);
    }

    #flush(pc) {
        if (!this.sent || this.timer || !this.local.length || this.pc !== pc) return;
        this.timer = setTimeout(() => {
            this.timer = 0;
            if (this.pc !== pc) return;
            const batch = this.local.splice(0, 16);
            this.send("candidates", JSON.stringify(batch)).then(() => this.#flush(pc), () => { if (this.pc === pc) this.close(); });
        }, 150);
    }

    async #apply(pc) {
        if (!pc?.remoteDescription) return;
        while (this.remote.length && this.pc === pc) await pc.addIceCandidate(this.remote.shift()).catch(() => {});
    }

    close() {
        const pc = this.pc;
        if (!pc) return;
        this.pc = null;
        clearTimeout(this.timer); this.timer = 0;
        pc.onicecandidate = null; pc.ontrack = null; pc.onconnectionstatechange = null;
        void Promise.resolve(this.e2ee.releasePeer(pc)).catch(() => {});
        pc.close();
        try { this.onClose(); } catch { /* listeners never break teardown */ }
    }
}

/** Codec preferences of a call video transceiver: VP8, H.264 and repair only. */
export function callCodecPreferences(capabilities) {
    const codecs = capabilities?.codecs || [];
    const rank = codec => VIDEO_CODECS.indexOf(String(codec.mimeType || "").toLowerCase());
    const media = codecs.filter(codec => rank(codec) >= 0).sort((a, b) => rank(a) - rank(b));
    const repair = codecs.filter(codec => REPAIR_CODECS.includes(String(codec.mimeType || "").toLowerCase()));
    return media.length ? [...media, ...repair] : [];
}

function preferCodecs(transceiver, scope) {
    if (typeof transceiver.setCodecPreferences !== "function") return;
    const preferred = callCodecPreferences(scope.RTCRtpReceiver?.getCapabilities?.("video"));
    if (!preferred.length) return;
    try { transceiver.setCodecPreferences(preferred); } catch { /* the cipher still drops any other codec */ }
}

/**
 * Whether a call-media data channel state message announces call encryption.
 * Older apps send {camera, screen} only.
 */
export function announcesCallE2EE(state) {
    return !!state && typeof state === "object" && state.e2ee === CALL_E2EE_MARKER;
}

export class CallMediaE2EE {
    /**
     * adapter: the encrypted-media adapter of the signed-in session
     * ({status(), send(control)}, as for channel media).
     * sessionFactory (tests only): replaces MediaSession construction.
     */
    // owner: the media session's opaque owner (desktop native capture
    // mirrors the call's own screen keys into Go through it).
    constructor({ adapter, sessionFactory = null, onStatus = () => {}, onError = () => {}, scope = globalThis, workerURL, owner } = {}) {
        this.adapter = adapter;
        this.owner = owner;
        this.sessionFactory = sessionFactory;
        this.onStatus = onStatus;
        this.onError = onError;
        this.scope = scope;
        this.workerURL = workerURL;
        this.session = null;
        this.callID = "";
        this.starting = null;
        // Keys can land before this device applied the call state that makes
        // it a participant (the peer started first): hold them briefly.
        this.early = new MediaKeyStartupInbox();
        this.lastStatus = { state: "off" };
    }

    get active() { return !!this.session?.active; }

    /** Starts (or keeps) encryption for a call this device accepted. */
    async start(call) {
        if (!call?.id) throw new MediaSessionError("unavailable", "invalid call");
        if (this.callID === call.id && (this.session || this.starting)) {
            this.update(call);
            if (this.starting) await this.starting;
            return this.session;
        }
        this.#teardown();
        this.callID = call.id;
        const options = { adapter: this.adapter, hardwareVideoRequired: false, scope: this.scope, workerURL: this.workerURL, owner: this.owner,
            onStatus: status => { if (this.session === session) this.#publish(status); },
            onError: error => { if (this.session === session) this.onError(error); } };
        const session = this.sessionFactory ? this.sessionFactory(options) : new MediaSession(options);
        this.session = session;
        session.setRoster(callRoster(call));
        const starting = (async () => {
            await session.start(callScopeKey(call.id));
            if (this.session !== session) { session.stop(); throw new MediaSessionError("closed"); }
            this.early.drain(control => { if (control?.scope?.id === this.callID) session.receive(control); });
        })();
        this.starting = starting;
        try { await starting; }
        catch (error) {
            if (this.session === session) { this.session = null; this.callID = ""; }
            session.stop();
            throw error;
        } finally {
            if (this.starting === starting) this.starting = null;
        }
        return session;
    }

    /** The call's membership changed: rekey for the current participants. */
    update(call) {
        if (this.session && call?.id === this.callID) this.session.setRoster(callRoster(call));
    }

    /** A delivered media-key control; only call scopes are handled here. */
    receive(control) {
        if (control?.scope?.type !== CALL_MEDIA_SCOPE_TYPE || typeof control.scope.id !== "string") return { ok: false, reason: "scope" };
        if (this.session) return control.scope.id === this.callID ? this.session.receive(control) : { ok: false, reason: "scope" };
        return this.early.receive(control, callScopeKey(control.scope.id));
    }

    /** RTCPeerConnection configuration with the transform requirements. */
    peerConfig(config = {}) {
        if (!this.session) throw new MediaSessionError("closed");
        return this.session.peerConfig(config);
    }

    /** Must run right after the peer is created, before any transceiver exists. */
    protectPeer(pc) {
        if (!this.session?.active) throw new MediaSessionError("closed");
        return this.session.protectPeer(pc);
    }

    /**
     * Binds every transceiver of a call peer: senders to this device's slot
     * keys, receivers to the peer's (peerClientID: the client that answered
     * for that participant). Run after transceivers exist and before each
     * offer or answer is created; it is idempotent. slotsOf maps the
     * transceivers to slots (callScreenSlots for a screen sub-connection).
     */
    async bindPeer(pc, peerClientID, slotsOf = callTransceiverSlots) {
        const session = this.session;
        if (!session?.active) throw new MediaSessionError("closed");
        if (peerClientID == null || peerClientID === "") throw new MediaSessionError("unavailable", "call peer has no client");
        session.syncPeer(pc);
        const work = [];
        for (const { transceiver, kind, slot } of slotsOf(pc)) {
            if (kind === "video") preferCodecs(transceiver, this.scope);
            if (transceiver.sender && session.endpointID(transceiver.sender)) work.push(session.bindSender(transceiver.sender, slot));
            const receiver = transceiver.receiver;
            if (receiver && session.endpointID(receiver)) {
                // A decode binding keeps the track disabled until its first
                // authenticated frame; a call plays every authenticated
                // receiver (mute and volume are applied elsewhere).
                work.push(session.bindReceiver(receiver, String(peerClientID), slot).then(() => session.onAuthenticated?.(receiver, () => {
                    if (this.session === session && receiver.track && receiver.track.readyState !== "ended") receiver.track.enabled = true;
                })));
            }
        }
        await Promise.all(work);
    }

    // Always a promise: a stopped session returns nothing to release.
    releasePeer(pc) { return Promise.resolve(this.session?.releasePeer(pc)); }

    status() { return this.lastStatus; }

    #publish(status) {
        this.lastStatus = status || { state: "off" };
        try { this.onStatus(this.lastStatus); } catch { /* status listeners never break media */ }
    }

    // Ends the current call's session; keys held for a call not yet started stay.
    #teardown() {
        const session = this.session;
        this.session = null;
        this.callID = "";
        this.starting = null;
        if (session) { session.stop(); this.#publish({ state: "off" }); }
    }

    stop() {
        this.#teardown();
        this.early.clear();
        this.#publish({ state: "off" });
    }
}
