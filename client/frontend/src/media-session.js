// One encrypted channel-media session (Task 2): the frame transform worker
// (MediaE2EE) plus the key epochs (MediaKeyEngine) for one voice session on
// one server. Shared by the desktop frontend and the PWA (an identical copy
// lives in mobile/src/core/media-e2ee/; a unit test keeps them identical).
//
// The adapter supplies the transport and the signed-in identity:
//   status()            -> {supported, principal, device, clientID, reason}
//   send({logicalID, principal, device, body}) -> {ok, code?}
//
// There is no plaintext mode: start() throws MediaSessionError when the
// runtime, the server or the session cannot encrypt, and the caller must not
// create a peer connection then.
import { MediaE2EE, MediaE2EEUnsupported, MEDIA_E2EE_VERSION, mediaE2EESupport } from "./media-e2ee.js";
import { MediaKeyEngine, publicationPid, senderPid } from "./media-keys.js";

export { MEDIA_E2EE_VERSION, mediaE2EESupport };

export class MediaSessionError extends Error {
    constructor(reason, detail = "") { super(detail || reason); this.name = "MediaSessionError"; this.reason = reason; }
}

// Reason code of any encrypted-media failure, for one localized message:
// a MediaSessionError, or a server refusal whose message starts with a
// netproto.MediaE2EEReason* code.
const SERVER_REASONS = {
    media_e2ee_client_outdated: "client_outdated",
    media_e2ee_keyless_guest: "keyless_guest",
    media_e2ee_unavailable: "unavailable",
    media_e2ee_codec_unsupported: "codec",
    media_e2ee_invisible: "invisible",
    media_e2ee_channel_full: "channel_full",
    server_recording_disabled_e2ee: "recording",
    whisper_unavailable_e2ee: "whisper_unavailable",
    stream_preview_disabled_e2ee: "preview",
};
export const MEDIA_REASONS = Object.freeze(["server_outdated", "client_outdated", "keyless_guest", "no_device", "secure_unavailable",
    "unsupported_runtime", "worker", "unavailable", "codec", "recording", "whisper_unavailable", "preview", "closed", "invisible", "channel_full"]);
export function mediaErrorReason(error) {
    if (error instanceof MediaSessionError || (error?.name === "MediaSessionError" && error.reason)) return MEDIA_REASONS.includes(error.reason) ? error.reason : "unavailable";
    const text = String(error?.message ?? error ?? "");
    const code = text.match(/^([a-z0-9_]+):/)?.[1] || text.match(/\b(media_e2ee_[a-z_]+|server_recording_disabled_e2ee|whisper_unavailable_e2ee|stream_preview_disabled_e2ee)\b/)?.[1];
    return code && SERVER_REASONS[code] ? SERVER_REASONS[code] : "";
}

// Publisher and video slot a keyframe request names for a receiver binding
// ("<client>|cam" or "<client>|screen"), or null.
export function keyframeTarget(pid) {
    const [publisher, slot] = String(pid || "").split("|");
    return publisher && (slot === "cam" || slot === "screen") ? { publisher, slot } : null;
}

// Publisher and slot of a negotiated remote track ID ("<client>" for the
// microphone, "<client>|<slot>" otherwise).
export function trackPid(trackID) {
    const s = String(trackID || "");
    if (!s) return "";
    const i = s.indexOf("|");
    return i < 0 ? publicationPid(s, "") : publicationPid(s.slice(0, i), s.slice(i + 1));
}

// mid -> active remote track ID from an SDP (inactive/rejected sections map to "").
export function remoteTrackIDsByMid(sdp = "") {
    const result = new Map();
    let section = null;
    const finish = () => { if (section?.mid != null) result.set(section.mid, section.active ? section.trackID : ""); };
    for (const line of String(sdp).split(/\r?\n/)) {
        if (line.startsWith("m=")) {
            finish();
            const [kind, port] = line.slice(2).split(/\s+/);
            section = { active: (kind === "audio" || kind === "video") && port !== "0", trackID: "" };
        } else if (section) {
            if (line.startsWith("a=mid:")) section.mid = line.slice(6).trim();
            else if (line.startsWith("a=msid:")) section.trackID = line.slice(7).trim().split(/\s+/)[1] || "";
            else if (line === "a=inactive" || line === "a=recvonly") section.active = false;
        }
    }
    finish();
    return result;
}

export class MediaSession {
    constructor({ adapter, onStatus = () => {}, onError = () => {}, onKeyframeNeeded = () => {}, api, workerURL, scope = globalThis }) {
        this.adapter = adapter; this.onStatus = onStatus; this.onError = onError; this.onKeyframeNeeded = onKeyframeNeeded;
        this.api = api; this.workerURL = workerURL; this.scope = scope;
        this.media = null; this.engine = null; this.identity = null; this.closed = false;
    }

    // Starts encryption for a voice session in channelID. Throws
    // MediaSessionError(reason): unsupported_runtime, server_outdated,
    // keyless_guest, no_device, secure_unavailable, worker.
    async start(channelID) {
        const status = await this.adapter.status();
        if (this.closed) throw new MediaSessionError("closed");
        if (!status?.supported) throw new MediaSessionError(status?.reason || "server_outdated");
        if (status.reason) throw new MediaSessionError(status.reason);
        if (!status.principal || !status.device || !status.clientID) throw new MediaSessionError("no_device");
        if (this.roster?.find?.(c => String(c.client_id) === String(status.clientID))?.status === "invisible") throw new MediaSessionError("invisible");
        try {
            this.media = new MediaE2EE({ api: this.api, workerURL: this.workerURL, scope: this.scope, onError: error => this.#fail(error),
                onKeyframeNeeded: (receiver, pid) => { const target = keyframeTarget(pid); if (target) this.onKeyframeNeeded(target, receiver); } });
        } catch (error) {
            throw new MediaSessionError(error instanceof MediaE2EEUnsupported ? "unsupported_runtime" : "worker", String(error?.message || error));
        }
        try { await this.media.ready; } catch (error) { this.media.close(); throw new MediaSessionError("worker", String(error?.message || error)); }
        if (this.closed) { this.media.close(); throw new MediaSessionError("closed"); }
        this.identity = { channelID: Number(channelID), clientID: String(status.clientID), principal: status.principal, device: status.device };
        this.#startEngine();
        return this;
    }

    #startEngine() {
        this.engine = new MediaKeyEngine({
            media: this.media,
            send: control => this.adapter.send(control),
            onStatus: s => this.onStatus({ ...s, api: this.media?.api }),
        });
        this.engine.onInvisible = () => this.#fail(new MediaSessionError("invisible"));
        if (this.roster) this.engine.setRoster(this.roster);
        this.engine.start(this.identity);
    }

    // The same peer moved to another channel: a new key scope. Every key of
    // the old scope is forgotten (retired) before the new epochs start.
    rescope(channelID) {
        if (!this.media || this.closed) throw new MediaSessionError("closed");
        if (Number(channelID) === this.identity.channelID) return;
        this.engine.stop();
        this.identity = { ...this.identity, channelID: Number(channelID) };
        this.#startEngine();
    }

    get version() { return MEDIA_E2EE_VERSION; }
    get active() { return !!this.engine?.running && !this.closed; }

    peerConfig(config = {}) { return this.media ? this.media.peerConfig(config) : config; }

    // Must be called right after the RTCPeerConnection is created, before
    // any transceiver exists.
    protectPeer(pc) { this.media.protectPeer(pc); return pc; }

    // Binds every sender to its slot key before an offer is sent. slots is
    // [{track_id, slot}] as declared to the server; senders without a
    // declared slot stay unbound (they send nothing).
    async bindSenders(pc, slots = []) {
        if (!this.media) throw new MediaSessionError("closed");
        this.media.syncPeer(pc);
        const sdpIDs = new Map();
        for (const section of String(pc.localDescription?.sdp || "").split(/(?=^m=)/m)) {
            const mid = section.match(/^a=mid:([^\r\n]+)/m)?.[1];
            const trackID = section.match(/^a=msid:[^\s]+ ([^\r\n\s]+)/m)?.[1];
            if (mid && trackID) sdpIDs.set(trackID, mid);
        }
        const bySlot = new Map(slots.map(s => [s.track_id, s.slot]));
        const work = [];
        for (const transceiver of pc.getTransceivers()) {
            const track = transceiver.sender.track;
            let slot = "";
            if (track) {
                for (const [trackID, s] of bySlot) {
                    if (trackID === track.id || (sdpIDs.get(trackID) !== undefined && sdpIDs.get(trackID) === transceiver.mid)) { slot = s; break; }
                }
            }
            work.push(this.media.bind(transceiver.sender, slot ? senderPid(slot) : ""));
        }
        await Promise.all(work);
    }

    // Binds a single sender (e.g. after replaceTrack into a known slot).
    bindSender(sender, slot) { return this.media.bind(sender, slot ? senderPid(slot) : ""); }

    // Protects new transceivers and binds every receiver to the publisher
    // the CURRENT remote description names for its MID. A reused MID is
    // rebound (old publisher frames are dropped before the new key is used).
    //
    // Attach-before-answer invariant (Task 2b): callers run this after
    // applying a remote offer and before creating the answer, so every
    // receiver the offer added carries its transform before the SFU can route
    // a frame to it. A receiver that could not be protected aborts the
    // negotiation (fail closed) instead of being answered.
    async syncReceivers(pc) {
        if (!this.media) throw new MediaSessionError("closed");
        this.media.syncPeer(pc);
        const ids = remoteTrackIDsByMid(pc.remoteDescription?.sdp);
        const work = [];
        for (const transceiver of pc.getTransceivers()) {
            const receiver = transceiver.receiver;
            if (receiver?.track && !this.media.endpointID(receiver)) throw new MediaSessionError("unavailable", "a receiver has no encryption transform");
            if (!receiver || !this.media.endpointID(receiver)) continue;
            const id = receiver.track?.readyState === "ended" ? "" : ids.get(transceiver.mid) || "";
            work.push(this.media.bind(receiver, trackPid(id)));
        }
        await Promise.all(work);
    }

    setRoster(clients) { this.roster = clients; this.engine?.setRoster(clients); }
    receive(control) { return this.engine ? this.engine.receive(control) : { ok: false, reason: "inactive" }; }
    publicationsChanged() { this.engine?.publicationsChanged(); }
    status() { return this.engine ? { ...this.engine.status(), api: this.media?.api } : { state: "off" }; }
    stats(endpoint) { return this.media ? this.media.stats(endpoint) : Promise.resolve(null); }

    #fail(error) {
        if (this.closed) return;
        this.onError(error);
        this.stop();
    }

    stop() {
        if (this.closed) return;
        this.closed = true;
        try { this.engine?.stop(); } catch { /* best effort */ }
        try { this.media?.close(); } catch { /* best effort */ }
        this.engine = null; this.media = null;
        this.onStatus({ state: "off" });
    }
}
