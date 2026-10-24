// voice.js — channel voice on the phone: one RTCPeerConnection to the
// server's SFU (internal/webrtc), signalled over the control connection.
//
//   18 WebRTCOffer {sdp, tracks:[{track_id, slot}]}  client offers; the
//      server also sends offers when members or watched streams change
//   19 WebRTCAnswer {sdp}
//   20 ICECandidate {candidate, sdp_mid, sdp_mline_index}
//
// Remote tracks carry their publisher in the MSID: "<clientID>" for a
// microphone, "<clientID>|<slot>" for cam, screen and screenaudio
// (client/frontend/src/media-track-id.js). Speaking state comes from the
// server's speaking_changed events.
import { Msg } from "./frame.js";
import { get as setting } from "./settings.js";
import { CameraSource, capSender, preferReceiveCodecs, watchCodecNames, wireQuality } from "./video.js";

const MsgChannelJoined = 158;
const STATS_INTERVAL = 2000;

/** Publisher and slot of a remote track id. */
export function parseTrackID(id) {
    const s = String(id ?? "");
    const i = s.indexOf("|");
    return i < 0 ? { clientID: s, slot: "mic" } : { clientID: s.slice(0, i), slot: s.slice(i + 1) };
}

/** mid -> track id from the a=msid lines of an SDP. */
export function msidByMid(sdp = "") {
    const out = new Map();
    let mid = null;
    let track = "";
    const finish = () => { if (mid !== null) out.set(mid, track); };
    for (const line of sdp.split(/\r?\n/)) {
        if (line.startsWith("m=")) {
            finish();
            mid = null;
            track = "";
        } else if (line.startsWith("a=mid:")) {
            mid = line.slice(6).trim();
        } else if (line.startsWith("a=msid:")) {
            track = line.slice(7).trim().split(/\s+/)[1] || "";
        }
    }
    finish();
    return out;
}

/** Codecs this browser can receive for watched streams, as the SFU names them (vp8 always). */
export function watchCodecs() {
    let codecs = [];
    try { codecs = globalThis.RTCRtpReceiver?.getCapabilities?.("video")?.codecs || []; } catch { codecs = []; }
    return watchCodecNames(codecs);
}

export class Voice extends EventTarget {
    constructor(session) {
        super();
        this.session = session;
        this.conn = session.conn;
        this.channelID = 0;
        this.pc = null;
        this.mic = null;
        this.camera = null;
        this.muted = false;
        this.deafened = false;
        this.pushToTalk = false;
        this.talking = false;
        /** clientID -> <audio> for microphones and shared audio. */
        this.audio = new Map();
        /** "clientID|slot" -> MediaStreamTrack for watched video. */
        this.video = new Map();
        this.speaking = new Set();
        /** "clientID|slot" of streams this phone chose to watch. */
        this.watching = new Set();
        this.sinkId = "";
        this.bytes = { sent: 0, received: 0 };
        this.#chain = Promise.resolve();
        this.#pendingCandidates = [];
        this.#stop = [
            this.conn.on(Msg.WebRTCOffer, offer => this.#enqueue(() => this.#answerServer(offer.sdp))),
            this.conn.on(Msg.ICECandidate, candidate => this.#remoteCandidate(candidate)),
        ];
        this.#onServerEvent = event => this.#serverEvent(event.detail);
        session.addEventListener("server-event", this.#onServerEvent);
    }

    #chain;
    #pendingCandidates;
    #stop;
    #onServerEvent;
    #statsTimer = 0;
    #watchRevision = 0;
    #watchSession = "0";

    #emit(type, detail) {
        this.dispatchEvent(new CustomEvent(type, { detail }));
    }

    get active() {
        return this.channelID !== 0;
    }

    /** Serializes negotiations; a failure never blocks the next one. */
    #enqueue(task) {
        const run = this.#chain.then(task);
        this.#chain = run.catch(() => {});
        return run;
    }

    // --- Joining and leaving ------------------------------------------------

    /** Joins a voice channel: microphone, peer connection, first offer. */
    async join(channelID, password = "") {
        const joined = await this.conn.request(Msg.JoinChannel, { ack_requested: true, channel_id: channelID, password: password || undefined }, MsgChannelJoined);
        this.channelID = joined.channel_id ?? channelID;
        try {
            if (!this.pc) await this.#start();
        } catch (error) {
            // The channel ack preceded media setup. Roll it back and release
            // tracks/timers so another Join really starts a fresh connection.
            await this.leave();
            throw error;
        }
        this.#emit("state");
        return this.channelID;
    }

    async #start() {
        try {
            this.mic = await navigator.mediaDevices.getUserMedia({ audio: { echoCancellation: true, noiseSuppression: true, autoGainControl: true } });
        } catch (error) {
            this.mic = null;
            this.#emit("mic-error", error);
        }
        const iceServers = await this.session.relayServers().catch(() => this.session.iceServers);
        const pc = new RTCPeerConnection({ iceServers });
        // Renewed TURN credentials keep a long session's relay alive.
        this.#stopRenewal = this.session.onRelayRenewal(servers => {
            if (this.pc !== pc) return;
            try { pc.setConfiguration({ iceServers: servers }); } catch { return; }
            this.#enqueue(() => this.#offer({ iceRestart: true })).catch(() => {});
        });
        this.pc = pc;
        pc.onicecandidate = event => {
            if (!event.candidate || !this.conn.open) return;
            this.conn.send(Msg.ICECandidate, { candidate: event.candidate.candidate, sdp_mid: event.candidate.sdpMid ?? "", sdp_mline_index: event.candidate.sdpMLineIndex ?? 0 });
        };
        pc.ontrack = () => this.#reconcile();
        pc.onconnectionstatechange = () => this.#emit("state");
        const track = this.mic?.getAudioTracks()[0];
        if (track) {
            track.enabled = this.#micOpen();
            pc.addTransceiver(track, { direction: "sendrecv", sendEncodings: [{ priority: "high", networkPriority: "high" }] });
        } else {
            pc.addTransceiver("audio", { direction: "recvonly" });
        }
        this.#statsTimer = setInterval(() => this.#collectStats(), STATS_INTERVAL);
        await this.#enqueue(() => this.#offer());
    }

    /** Leaves voice: channel 0, then tears the connection down. */
    async leave() {
        if (!this.active) return;
        this.channelID = 0;
        try {
            if (this.conn.open) await this.conn.request(Msg.JoinChannel, { ack_requested: true, channel_id: 0 }, MsgChannelJoined);
        } catch {
            // The connection may already be gone.
        }
        this.#teardown();
        this.#emit("state");
    }

    #stopRenewal = null;

    #teardown() {
        this.#stopRenewal?.();
        this.#stopRenewal = null;
        clearInterval(this.#statsTimer);
        this.pc?.close();
        this.pc = null;
        for (const track of this.mic?.getTracks() || []) track.stop();
        this.mic = null;
        for (const track of this.camera?.getTracks() || []) track.stop();
        this.#cameraSource?.stop();
        this.#cameraSource = null;
        this.camera = null;
        this.watched.clear();
        this.#applied.clear();
        this.confirmed.clear();
        this.#cameraTransceiver = null;
        this.#cameraGeneration = "0";
        for (const element of this.audio.values()) {
            element.srcObject = null;
            element.remove();
        }
        this.audio.clear();
        this.video.clear();
        this.speaking.clear();
        this.#pendingCandidates = [];
    }

    close() {
        this.#teardown();
        this.#stop.forEach(stop => stop());
        this.session.removeEventListener("server-event", this.#onServerEvent);
    }

    // --- Negotiation ----------------------------------------------------------

    #trackSlots() {
        const out = [];
        for (const transceiver of this.pc.getTransceivers()) {
            const track = transceiver.sender.track;
            if (track?.kind === "audio") out.push({ track_id: track.id, slot: "mic" });
            else if (track?.kind === "video" && transceiver === this.#cameraTransceiver) out.push({ track_id: track.id, slot: "cam" });
        }
        return out;
    }

    // --- Camera -------------------------------------------------------------------

    #cameraTransceiver = null;
    #cameraGeneration = "0";

    #cameraSource = null;
    /** "user" (front) or "environment" (back) for the published camera. */
    facingMode = "user";

    /** Publishes the camera to the channel (VP8, which every viewer decodes). */
    async startCamera() {
        if (!this.pc || this.camera) return;
        const source = new CameraSource({ facingMode: this.facingMode, effect: setting("cameraEffect"), blurStrength: setting("blurStrength"), limits: this.session.mediaLimits });
        const stream = await source.start();
        const track = stream.getVideoTracks()[0];
        this.#cameraSource = source;
        // Flip or a changed effect yields a new track: swap it in place,
        // the publication and the SDP stay the same.
        source.addEventListener("track", () => this.#cameraTrackChanged());
        this.camera = stream;
        try {
            if (this.#cameraTransceiver) {
                await this.#cameraTransceiver.sender.replaceTrack(track);
                this.#cameraTransceiver.direction = "sendonly";
            } else {
                this.#cameraTransceiver = this.pc.addTransceiver(track, { direction: "sendonly", streams: [stream] });
            }
            const vp8 = globalThis.RTCRtpSender?.getCapabilities?.("video")?.codecs.filter(c => /vp8|rtx/i.test(c.mimeType)) || [];
            if (vp8.length && this.#cameraTransceiver.setCodecPreferences) this.#cameraTransceiver.setCodecPreferences(vp8);
            await this.#enqueue(() => this.#offer());
            await capSender(this.#cameraTransceiver.sender, this.session.mediaLimits);
            const result = await this.conn.request(Msg.VideoStreamControl, { action: "publish", slot: "cam", generation: "0", revision: "0", session: "0", active: true, codecs: ["vp8"] },
                Msg.VideoStreamResult, { accept: r => r.action === "publish" });
            this.#cameraGeneration = String(result.generation ?? "0");
        } catch (error) {
            await this.stopCamera().catch(() => {});
            throw error;
        }
        this.#emit("state");
    }

    /** Switches the published camera between front and back. */
    async flipCamera() {
        if (!this.#cameraSource) {
            this.facingMode = this.facingMode === "user" ? "environment" : "user";
            this.#emit("state");
            return this.facingMode;
        }
        try {
            this.facingMode = await this.#cameraSource.flip();
        } finally {
            await this.#cameraTrackChanged();
        }
        return this.facingMode;
    }

    /** Applies a changed camera effect to the running camera. */
    async setCameraEffect(effect, blurStrength) {
        this.#cameraSource?.setEffect(effect, blurStrength);
        await this.#cameraTrackChanged();
    }

    async #cameraTrackChanged() {
        const source = this.#cameraSource;
        if (!source) return;
        this.camera = source.stream;
        const sender = this.#cameraTransceiver?.sender;
        if (sender && sender.track !== source.track) await sender.replaceTrack(source.track).catch(() => {});
        this.#emit("state");
    }

    async stopCamera() {
        const stream = this.camera;
        this.camera = null;
        for (const track of stream?.getTracks() || []) track.stop();
        this.#cameraSource?.stop();
        this.#cameraSource = null;
        if (this.#cameraGeneration !== "0" && this.conn.open) {
            await this.conn.request(Msg.VideoStreamControl, { action: "publish", slot: "cam", generation: this.#cameraGeneration, revision: "0", session: "0", active: false },
                Msg.VideoStreamResult, { accept: r => r.action === "publish" }).catch(() => {});
        }
        this.#cameraGeneration = "0";
        if (this.#cameraTransceiver && this.pc) {
            await this.#cameraTransceiver.sender.replaceTrack(null).catch(() => {});
            this.#cameraTransceiver.direction = "inactive";
            await this.#enqueue(() => this.#offer()).catch(() => {});
        }
        this.#emit("state");
    }

    async #offer(options) {
        const pc = this.pc;
        if (!pc) return;
        const offer = await pc.createOffer(options);
        await pc.setLocalDescription(offer);
        try {
            const answer = await this.conn.request(Msg.WebRTCOffer, { sdp: pc.localDescription.sdp, tracks: this.#trackSlots() }, Msg.WebRTCAnswer);
            if (this.pc !== pc) return;
            await pc.setRemoteDescription({ type: "answer", sdp: answer.sdp });
            await this.#flushCandidates();
            this.#reconcile();
        } catch (error) {
            await pc.setLocalDescription({ type: "rollback" }).catch(() => {});
            // The server offered at the same time: answer it first (queued
            // behind us), then offer again.
            if (/collision/i.test(error.message)) this.#enqueue(() => this.#offer());
            else throw error;
        }
    }

    async #answerServer(sdp) {
        const pc = this.pc;
        if (!pc) return;
        await pc.setRemoteDescription({ type: "offer", sdp });
        await this.#flushCandidates();
        preferReceiveCodecs(pc);
        const answer = await pc.createAnswer();
        await pc.setLocalDescription(answer);
        if (this.conn.open) this.conn.send(Msg.WebRTCAnswer, { sdp: pc.localDescription.sdp });
        this.#reconcile();
    }

    async #remoteCandidate(candidate) {
        const init = { candidate: candidate.candidate, sdpMid: candidate.sdp_mid || null, sdpMLineIndex: candidate.sdp_mline_index ?? 0 };
        if (!this.pc?.remoteDescription) {
            this.#pendingCandidates.push(init);
            return;
        }
        await this.pc.addIceCandidate(init).catch(() => {});
    }

    async #flushCandidates() {
        const pending = this.#pendingCandidates.splice(0);
        for (const init of pending) await this.pc?.addIceCandidate(init).catch(() => {});
    }

    /** Maps receivers to publishers after every description change. */
    #reconcile() {
        const pc = this.pc;
        if (!pc?.remoteDescription) return;
        const ids = msidByMid(pc.remoteDescription.sdp);
        const audio = new Map();
        const video = new Map();
        for (const transceiver of pc.getTransceivers()) {
            const track = transceiver.receiver.track;
            const trackID = ids.get(transceiver.mid);
            if (!track || !trackID || transceiver.currentDirection === "inactive" || transceiver.currentDirection === "sendonly") continue;
            const { clientID, slot } = parseTrackID(trackID);
            if (!clientID || clientID === this.session.clientID) continue;
            if (track.kind === "audio") audio.set(slot === "mic" ? clientID : `${clientID}|${slot}`, track);
            else video.set(`${clientID}|${slot}`, track);
        }
        for (const [key, element] of this.audio) {
            if (!audio.has(key) || element.srcObject?.getTracks()[0] !== audio.get(key)) {
                element.srcObject = null;
                element.remove();
                this.audio.delete(key);
            }
        }
        for (const [key, track] of audio) {
            if (this.audio.has(key)) continue;
            const element = document.createElement("audio");
            element.autoplay = true;
            element.muted = this.deafened;
            element.srcObject = new MediaStream([track]);
            if (this.sinkId && element.setSinkId) element.setSinkId(this.sinkId).catch(() => {});
            element.dataset.publisher = key;
            document.body.append(element);
            element.play().catch(() => {});
            this.audio.set(key, element);
        }
        this.video = video;
        this.#emit("tracks");
    }

    // --- Talking, mute and deafen -------------------------------------------

    #micOpen() {
        if (this.muted || this.deafened) return false;
        return this.pushToTalk ? this.talking : true;
    }

    #applyMic() {
        const track = this.mic?.getAudioTracks()[0];
        if (track) track.enabled = this.#micOpen();
        this.#emit("state");
    }

    #announce() {
        if (this.conn.open) this.conn.send(Msg.AudioStateSet, { muted: this.muted, deafened: this.deafened });
    }

    setMuted(muted) {
        this.muted = muted;
        this.#applyMic();
        this.#announce();
    }

    setDeafened(deafened) {
        this.deafened = deafened;
        for (const element of this.audio.values()) element.muted = deafened;
        this.#applyMic();
        this.#announce();
    }

    setPushToTalk(enabled) {
        this.pushToTalk = enabled;
        this.talking = false;
        this.#applyMic();
    }

    /** Push-to-talk: open the microphone while held. */
    setTalking(talking) {
        this.talking = talking;
        this.#applyMic();
    }

    // --- Audio output ------------------------------------------------------------

    static outputSelectable() {
        return typeof HTMLMediaElement !== "undefined" && "setSinkId" in HTMLMediaElement.prototype;
    }

    async outputs() {
        const devices = await navigator.mediaDevices.enumerateDevices().catch(() => []);
        return devices.filter(d => d.kind === "audiooutput");
    }

    /** Routes all call audio to an output device (Android Chrome). */
    async setOutput(deviceId) {
        this.sinkId = deviceId;
        await Promise.all([...this.audio.values()].map(element => element.setSinkId?.(deviceId).catch(() => {})));
        this.#emit("state");
    }

    /** Lets the browser show its own output chooser where supported. */
    async chooseOutput() {
        if (!navigator.mediaDevices.selectAudioOutput) return null;
        const device = await navigator.mediaDevices.selectAudioOutput();
        await this.setOutput(device.deviceId);
        return device;
    }

    // --- Server events and statistics ---------------------------------------

    #serverEvent({ type, data }) {
        if (type !== "speaking_changed" || !data?.client_id) return;
        if (data.speaking) this.speaking.add(data.client_id);
        else this.speaking.delete(data.client_id);
        this.#emit("speaking", data);
    }

    async #collectStats() {
        if (!this.pc) return;
        const report = await this.pc.getStats().catch(() => null);
        let sent = 0;
        let received = 0;
        report?.forEach(stat => {
            if (stat.type === "transport") {
                sent += stat.bytesSent || 0;
                received += stat.bytesReceived || 0;
            }
        });
        this.bytes = { sent, received };
        this.#emit("stats", this.bytes);
    }

    // --- Watching streams ---------------------------------------------------------

    /** Streams published in the channel: [{publisher_id, slot, generation, codec}]. */
    async streams() {
        const result = await this.conn.request(Msg.VideoStreamControl, { action: "list" }, Msg.VideoStreamResult, { accept: r => r.action === "list" });
        this.#watchSession = String(result.session ?? "0");
        return result.streams || [];
    }

    /** Starts or stops receiving one published stream. */
    async watch(stream, active = true) {
        this.#watchRevision++;
        const key = `${stream.publisher_id}|${stream.slot}`;
        if (active) {
            this.watching.add(key);
            this.watched.set(key, stream);
        } else {
            this.watching.delete(key);
            this.watched.delete(key);
            this.#applied.delete(key);
        }
        this.#emit("tracks");
        const result = await this.conn.request(Msg.VideoStreamControl, {
            action: "watch", publisher_id: stream.publisher_id, slot: stream.slot, generation: String(stream.generation),
            revision: String(this.#watchRevision), session: this.#watchSession, active, codecs: watchCodecs(),
        }, Msg.VideoStreamResult, { accept: r => r.action === "watch" });
        if (result?.session && result.session !== "0") this.#watchSession = String(result.session);
        if (active) await this.applyQualities().catch(() => {});
        return result;
    }

    // --- Receive quality ------------------------------------------------------------

    /** Watched publications by "clientID|slot" (generation for scoped requests). */
    watched = new Map();
    /** Per-stream preference: "auto" | "high" | "medium" | "low". */
    quality = new Map();
    /** The watched stream with focus on the stage, for "auto". */
    focusKey = null;
    /** Last layer the server confirmed per stream ("default", "high", "mid", "low"). */
    confirmed = new Map();
    #applied = new Map();

    /** Whether this server takes a receive layer per watched stream. */
    get streamQualitySupported() {
        return !!this.session.capabilities?.has("stream_video_quality_v1");
    }

    /** The layer the phone wants for one watched stream right now. */
    wantedQuality(key) {
        const stream = this.watched.get(key);
        return wireQuality(this.quality.get(key) || setting("receiveQuality"), {
            lowBandwidth: !!setting("lowBandwidth"), focused: key === this.focusKey, slot: stream?.slot, watched: this.watched.size,
        });
    }

    /** Sets a stream's preference ("auto" follows the global default) and applies it. */
    async setStreamQuality(key, preference) {
        if (preference && preference !== "auto") this.quality.set(key, preference);
        else this.quality.delete(key);
        this.#applied.delete(key);
        await this.applyQualities();
    }

    /**
     * Sends Msg.VideoQuality for every watched stream whose wanted layer
     * changed. Scoped by publication and watch session, so a reconnect or
     * a re-publication can never apply it to another stream.
     */
    async applyQualities() {
        if (!this.streamQualitySupported || this.#watchSession === "0") return;
        for (const [key, stream] of this.watched) {
            if (!this.video.has(key) && !this.watching.has(key)) continue;
            const quality = this.wantedQuality(key);
            if (this.#applied.get(key) === quality) continue;
            this.#applied.set(key, quality);
            try {
                await this.conn.request(Msg.VideoQuality, {
                    quality, ack_requested: true, publisher_id: stream.publisher_id, slot: stream.slot,
                    generation: String(stream.generation), session: this.#watchSession,
                }, Msg.MediaControlSaved, { accept: r => r.operation === Msg.VideoQuality && r.publisher_id === stream.publisher_id && r.slot === stream.slot });
                this.confirmed.set(key, quality);
            } catch (error) {
                this.#applied.delete(key);
                this.confirmed.delete(key);
                this.#emit("quality", { key, error });
                throw error;
            }
            this.#emit("quality", { key, quality });
        }
    }
}
