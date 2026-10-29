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
//
// The microphone follows the voice settings (core/settings.js): device and
// processing constraints, activation mode (continuous, voice activation,
// push-to-talk with a release delay) and the data saver. Received audio is
// mixed per member through Web Audio (core/voice-audio.js).
import { Msg } from "./frame.js";
import { get as setting } from "./settings.js";
import { CameraSource, capSender, preferReceiveCodecs, watchCodecNames, wireQuality } from "./video.js";
import * as settings from "./settings.js";
import { LevelMeter, Mixer, installAudioUnlock } from "./voice-audio.js";
import { MutedTalkingWarning, PushToTalkLatch, VoiceGate, parseKeyKind, playbackGain } from "./voice-gate.js";
import { MEDIA_E2EE_VERSION, MediaSession, MediaSessionError } from "./media-e2ee/media-session.js";

const MsgChannelJoined = 158;
const STATS_INTERVAL = 2000;
const LEVEL_INTERVAL = 50;
/** Outgoing bitrate caps of the data saver (bits/s). */
export const LOW_AUDIO_BITRATE = 24000;
export const LOW_VIDEO_BITRATE = 150000;
const MIC_SETTINGS = ["microphoneID", "echoCancellation", "noiseSuppression", "autoGainControl"];
const VOLUME_SETTINGS = ["masterVolume", "userVolumes", "mutedUsers", "shareVolumes"];

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

/** getUserMedia constraints for the microphone settings. */
export function micConstraints(s = settings.all(), { exactDevice = true } = {}) {
    const audio = { echoCancellation: s.echoCancellation !== false, noiseSuppression: s.noiseSuppression !== false, autoGainControl: s.autoGainControl !== false };
    if (exactDevice && s.microphoneID) audio.deviceId = { exact: s.microphoneID };
    return { audio };
}

/** Opens the chosen microphone; a vanished device falls back to the default. */
export async function openMicrophone(s = settings.all()) {
    try {
        return await navigator.mediaDevices.getUserMedia(micConstraints(s));
    } catch (error) {
        if (!s.microphoneID || !/Overconstrained|NotFound/.test(error?.name || "")) throw error;
        return navigator.mediaDevices.getUserMedia(micConstraints(s, { exactDevice: false }));
    }
}

export class Voice extends EventTarget {
    /**
     * previous: the Voice of a dropped connection; mute, deafen and the last whisperer carry over.
     * media: the encrypted-media adapter {status(), send(control)} (Task 2); without it
     * channel media cannot start (there is no plaintext mode).
     */
    constructor(session, previous = null, { media = null } = {}) {
        super();
        this.session = session;
        this.mediaAdapter = media;
        /** Encrypted media status for labels: {state: off|keying|encrypted|partial, ...}. */
        this.mediaStatus = { state: "off" };
        this.conn = session.conn;
        this.channelID = 0;
        this.pc = null;
        this.mic = null;
        this.camera = null;
        this.muted = !!previous?.muted;
        this.deafened = !!previous?.deafened;
        this.talking = false;
        /** Outgoing audio goes to the whisper targets while true. */
        this.whispering = false;
        /** Client IDs whispering to this phone right now. */
        this.whisperers = new Set();
        /** {uniqueID, clientID, nickname} of the last member who whispered here. */
        this.lastWhisperer = previous?.lastWhisperer || null;
        /** Current microphone level (RMS 0..1) while in a call. */
        this.level = 0;
        /** {channel, previous} while the network echo test runs. */
        this.echo = null;
        /** clientID -> <audio> for microphones and shared audio. */
        this.audio = new Map();
        /** "clientID|slot" -> MediaStreamTrack for watched video. */
        this.video = new Map();
        this.speaking = new Set();
        /** "clientID|slot" of streams this phone chose to watch. */
        this.watching = new Set();
        this.sinkId = "";
        this.bytes = { sent: 0, received: 0 };
        this.mixer = new Mixer();
        this.#latch = new PushToTalkLatch({ releaseMs: settings.get("pttReleaseMs"), onChange: active => {
            this.talking = active;
            this.#applyMic();
        } });
        this.#gate = new VoiceGate({ threshold: settings.get("voiceThreshold") });
        this.#chain = Promise.resolve();
        this.#pendingCandidates = [];
        this.#stop = [
            this.conn.on(Msg.WebRTCOffer, offer => this.#enqueue(() => this.#answerServer(offer.sdp))),
            this.conn.on(Msg.ICECandidate, candidate => this.#remoteCandidate(candidate)),
        ];
        this.#onServerEvent = event => this.#serverEvent(event.detail);
        session.addEventListener("server-event", this.#onServerEvent);
        this.#onWhisper = event => this.#whisper(event.detail);
        session.addEventListener("whisper", this.#onWhisper);
        this.#onSnapshot = () => this.#snapshot();
        session.addEventListener("snapshot", this.#onSnapshot);
        this.#stopSettings = settings.onChange(({ changed }) => this.#settingsChanged(changed));
        installAudioUnlock();
    }

    #chain;
    #pendingCandidates;
    #stop;
    #onServerEvent;
    #onWhisper;
    #onSnapshot;
    #stopSettings;
    #statsTimer = 0;
    #watchRevision = 0;
    #watchSession = "0";
    #latch;
    #gate;
    #meter = null;
    #meterTrack = null;
    #levelTimer = 0;
    #mutedWarning = new MutedTalkingWarning();
    #micTransceiver = null;
    #media = null;
    #wasCalled = false;
    #whisperSeq = 0;

    #emit(type, detail) {
        this.dispatchEvent(new CustomEvent(type, { detail }));
    }

    get active() {
        return this.channelID !== 0;
    }

    /** Activation mode from the settings: continuous | voice | ptt. */
    get activation() {
        return settings.get("activation");
    }

    get pushToTalk() {
        return this.activation === "ptt";
    }

    /** Data saver: capped outgoing bitrate and low receive quality (also read by the video screens). */
    get lowBandwidth() {
        return !!settings.get("lowBandwidth");
    }

    /** Whether the voice-activation gate is open right now. */
    get gateOpen() {
        return this.#gate.open;
    }

    /** Serializes negotiations; a failure never blocks the next one. */
    #enqueue(task) {
        const run = this.#chain.then(task);
        this.#chain = run.catch(() => {});
        return run;
    }

    // --- Joining and leaving ------------------------------------------------

    #joinGeneration = 0;
    #joining = false;
    #cancelStart = null;

    /** Joins a voice channel; false means the attempt was cancelled. */
    async join(channelID, password = "") {
        const generation = ++this.#joinGeneration;
        this.#cancelStart?.();
        this.#cancelStart = null;
        this.#joining = true;
        let acknowledged = false;
        try {
            const joined = await this.conn.request(Msg.JoinChannel, { ack_requested: true, channel_id: channelID, password: password || undefined }, MsgChannelJoined);
            if (generation !== this.#joinGeneration) return false;
            acknowledged = true;
            this.channelID = joined.channel_id ?? channelID;
            if (this.echo && this.channelID !== this.echo.channel) this.echo = null;
            // Same peer, new channel: a new key scope before any new media,
            // and never with the previous channel's roster.
            if (this.pc && this.#media) { this.#media.setRoster([]); this.#media.rescope(this.channelID); }
            if (!this.pc && !await this.#start(generation)) return false;
            if (generation !== this.#joinGeneration) return false;
            if (this.muted || this.deafened) this.#announce();
            this.#emit("state");
            return this.channelID;
        } catch (error) {
            // A rejected switch leaves the existing call untouched. Only roll
            // back media setup after the server accepted this channel.
            if (acknowledged && generation === this.#joinGeneration) await this.leave();
            throw error;
        } finally {
            if (generation === this.#joinGeneration) this.#joining = false;
        }
    }

    async #start(generation) {
        let cancel;
        let mic = null;
        const stopLocalMic = () => {
            for (const track of mic?.getTracks() || []) track.stop();
            mic = null;
        };
        const cancelled = new Promise(resolve => { cancel = () => { stopLocalMic(); resolve(null); }; });
        this.#cancelStart = cancel;
        try {
            try {
                await Promise.race([openMicrophone().then(stream => {
                    if (generation !== this.#joinGeneration) {
                        for (const track of stream.getTracks()) track.stop();
                    } else mic = stream;
                }), cancelled]);
            } catch (error) {
                if (generation === this.#joinGeneration) this.#emit("mic-error", error);
            }
            if (generation !== this.#joinGeneration) return false;
            const iceServers = await Promise.race([
                this.session.relayServers().catch(() => this.session.iceServers), cancelled,
            ]);
            if (generation !== this.#joinGeneration) return false;
            // Encryption first: no peer exists unless every frame can be
            // encrypted (explicit error otherwise, never plaintext).
            const media = new MediaSession({
                adapter: this.mediaAdapter || { status: async () => ({ supported: false, reason: "server_outdated" }) },
                onStatus: status => { this.mediaStatus = status; this.#emit("media-status", status); },
                onError: error => { this.#emit("media-error", error); this.leave().catch(() => {}); },
            });
            this.#media = media;
            media.setRoster(this.#voiceRoster());
            try {
                await Promise.race([media.start(this.channelID), cancelled]);
            } catch (error) {
                media.stop();
                if (this.#media === media) this.#media = null;
                // join() reports it (one explicit message, voice stays off).
                if (generation === this.#joinGeneration) throw error;
                return false;
            }
            if (generation !== this.#joinGeneration || this.#media !== media) { media.stop(); return false; }
            // Ownership moves to teardown only after all cancellable lookups finish.
            this.mic = mic;
            mic = null;
            const pc = media.protectPeer(new RTCPeerConnection(media.peerConfig({ iceServers })));
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
                this.#micTransceiver = pc.addTransceiver(track, { direction: "sendrecv", sendEncodings: [{ priority: "high", networkPriority: "high" }] });
            } else {
                this.#micTransceiver = pc.addTransceiver("audio", { direction: "recvonly" });
            }
            this.#startMeter();
            this.#statsTimer = setInterval(() => this.#collectStats(), STATS_INTERVAL);
            await this.#enqueue(() => this.#offer());
            if (generation !== this.#joinGeneration) return false;
            this.#applyBitrate();
            return true;
        } finally {
            stopLocalMic();
            if (this.#cancelStart === cancel) this.#cancelStart = null;
        }
    }

    /** Leaves voice: channel 0, then tears the connection down. */
    async leave() {
        const wasActive = this.active || this.#joining;
        this.channelID = 0;
        this.#teardown();
        this.#emit("state");
        if (!wasActive) return;
        try {
            if (this.conn.open) await this.conn.request(Msg.JoinChannel, { ack_requested: true, channel_id: 0 }, MsgChannelJoined);
        } catch {
            // The connection may already be gone.
        }
    }

    #stopRenewal = null;

    #teardown() {
        ++this.#joinGeneration;
        this.#joining = false;
        this.#cancelStart?.();
        this.#cancelStart = null;
        this.#stopMeter();
        this.#latch.cancel();
        this.#gate.reset();
        this.whispering = false;
        this.whisperers.clear();
        this.echo = null;
        this.#micTransceiver = null;
        this.#media?.stop();
        this.#media = null;
        this.mediaStatus = { state: "off" };
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
        for (const [key, element] of this.audio) {
            this.mixer.detach(key);
            element.srcObject = null;
            element.remove();
        }
        this.audio.clear();
        this.video.clear();
        this.speaking.clear();
        this.#pendingCandidates = [];
    }

    close() {
        this.channelID = 0;
        this.#teardown();
        this.#stop.forEach(stop => stop());
        this.#stopSettings();
        this.mixer.close();
        this.session.removeEventListener("server-event", this.#onServerEvent);
        this.session.removeEventListener("whisper", this.#onWhisper);
        this.session.removeEventListener("snapshot", this.#onSnapshot);
    }

    // --- Microphone level, gate and settings ----------------------------------

    /** Samples a private clone of the microphone, which stays enabled while muted. */
    #startMeter() {
        this.#stopMeter();
        const track = this.mic?.getAudioTracks?.()[0];
        if (!track) return;
        try {
            this.#meterTrack = track.clone();
            this.#meterTrack.enabled = true;
            this.#meter = new LevelMeter(new MediaStream([this.#meterTrack]));
        } catch {
            this.#meterTrack?.stop();
            this.#meterTrack = null;
            this.#meter = null;
            return;
        }
        this.#levelTimer = setInterval(() => this.#tick(), LEVEL_INTERVAL);
    }

    #stopMeter() {
        clearInterval(this.#levelTimer);
        this.#levelTimer = 0;
        this.#meter?.close();
        this.#meter = null;
        this.#meterTrack?.stop();
        this.#meterTrack = null;
        this.level = 0;
    }

    #tick() {
        const level = this.#meter?.level() ?? 0;
        this.level = level;
        const now = performance.now();
        const threshold = settings.get("voiceThreshold");
        this.#gate.threshold = threshold;
        if (this.#gate.update(level, now) && this.activation === "voice") this.#applyMic(true);
        if (settings.get("warnMutedTalking") && !this.deafened && this.#mutedWarning.check({ muted: this.muted, level, threshold: Math.max(threshold, 0.01), now })) {
            this.#emit("muted-talking");
        }
    }

    /** A private stream of the call's microphone for meters and test recordings, or null. */
    monitorStream() {
        const track = this.mic?.getAudioTracks?.()[0];
        if (!track) return null;
        const clone = track.clone();
        clone.enabled = true;
        return new MediaStream([clone]);
    }

    /** Takes the microphone again with new settings and swaps it into the call. */
    async #reacquire() {
        if (!this.pc || !this.#micTransceiver) return;
        const stream = await openMicrophone();
        const track = stream.getAudioTracks()[0];
        if (!this.pc || !track) {
            stream.getTracks().forEach(t => t.stop());
            return;
        }
        track.enabled = this.#micOpen();
        const old = this.mic;
        const transceiver = this.#micTransceiver;
        await transceiver.sender.replaceTrack(track);
        this.mic = stream;
        old?.getTracks().forEach(t => t.stop());
        if (transceiver.direction !== "sendrecv") {
            transceiver.direction = "sendrecv";
            await this.#enqueue(() => this.#offer());
        }
        this.#startMeter();
        await this.#applyBitrate();
        this.#emit("state");
    }

    #settingsChanged(changed) {
        const has = name => changed.includes(name);
        if (has("pttReleaseMs")) this.#latch.releaseMs = settings.get("pttReleaseMs");
        if (has("activation")) {
            this.#latch.cancel();
            this.#gate.reset();
            this.#applyMic();
        }
        if (VOLUME_SETTINGS.some(has)) this.#applyVolumes();
        if (has("lowBandwidth") || has("receiveQuality")) {
            this.#applyBitrate();
            this.applyQualities().catch(() => {});
        }
        if (this.active && MIC_SETTINGS.some(has)) this.#reacquire().catch(error => this.#emit("mic-error", error));
    }

    /** Caps the outgoing bitrate when the data saver is on. */
    async #applyBitrate() {
        const low = this.lowBandwidth;
        for (const [transceiver, cap] of [[this.#micTransceiver, LOW_AUDIO_BITRATE], [this.#cameraTransceiver, LOW_VIDEO_BITRATE]]) {
            const sender = transceiver?.sender;
            if (!sender?.track || !sender.getParameters) continue;
            try {
                const parameters = sender.getParameters();
                if (!parameters.encodings?.length) continue;
                if (low) parameters.encodings[0].maxBitrate = cap;
                else delete parameters.encodings[0].maxBitrate;
                await sender.setParameters(parameters);
            } catch {
                // Not negotiated yet; the next change applies it.
            }
        }
    }

    /** The outgoing microphone's current bitrate cap (bits/s), or 0. */
    sendBitrateCap() {
        try { return this.#micTransceiver?.sender.getParameters().encodings?.[0]?.maxBitrate || 0; } catch { return 0; }
    }

    // --- Negotiation ----------------------------------------------------------

    #trackSlots() {
        const out = [];
        // A replaced microphone keeps the MSID it was negotiated with.
        const ids = msidByMid(this.pc.localDescription?.sdp || "");
        for (const transceiver of this.pc.getTransceivers()) {
            const track = transceiver.sender.track;
            const id = ids.get(transceiver.mid) || track?.id;
            if (track?.kind === "audio") out.push({ track_id: id, slot: "mic" });
            else if (track?.kind === "video" && transceiver === this.#cameraTransceiver) out.push({ track_id: id, slot: "cam" });
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
            this.#media?.publicationsChanged();
            await capSender(this.#cameraTransceiver.sender, this.session.mediaLimits);
            const result = await this.conn.request(Msg.VideoStreamControl, { action: "publish", slot: "cam", generation: "0", revision: "0", session: "0", active: true, codecs: ["vp8"] },
                Msg.VideoStreamResult, { accept: r => r.action === "publish" });
            this.#cameraGeneration = String(result.generation ?? "0");
            this.#applyBitrate();
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
            this.#media?.publicationsChanged();
        }
        this.#emit("state");
    }

    async #offer(options) {
        const pc = this.pc;
        if (!pc) return;
        const media = this.#media;
        if (!media) throw new MediaSessionError("closed");
        const offer = await pc.createOffer(options);
        await pc.setLocalDescription(offer);
        try {
            const tracks = this.#trackSlots();
            // Binding runs alongside the offer: an unbound sender drops every
            // frame (never plaintext), and the offer must reach the server
            // before this peer's trickled ICE candidates.
            const binding = media.bindSenders(pc, tracks);
            binding.catch(() => {});
            const answer = await this.conn.request(Msg.WebRTCOffer, { sdp: pc.localDescription.sdp, tracks, media_e2ee: MEDIA_E2EE_VERSION }, Msg.WebRTCAnswer);
            if (this.pc !== pc) return;
            await pc.setRemoteDescription({ type: "answer", sdp: answer.sdp });
            await binding;
            await media.syncReceivers(pc);
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
        // Reused or new MIDs are bound to their current publisher first.
        // Fail closed: no encrypted media session, no answer.
        if (!this.#media) throw new Error("encrypted media session ended");
        await this.#media.syncReceivers(pc);
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
            // Presentation gate: no sink until the first authenticated frame.
            const gate = this.#media?.media;
            // Fail closed: without a media session (stopped or failed) nothing is presented.
            if (!gate?.authenticated(transceiver.receiver)) { gate?.onAuthenticated(transceiver.receiver, () => this.#reconcile()); continue; }
            // The gate only reports authentication; this owner enables output.
            track.enabled = true;
            const { clientID, slot } = parseTrackID(trackID);
            if (!clientID || clientID === this.session.clientID) continue;
            if (track.kind === "audio") audio.set(slot === "mic" ? clientID : `${clientID}|${slot}`, track);
            else video.set(`${clientID}|${slot}`, track);
        }
        for (const [key, element] of this.audio) {
            if (!audio.has(key) || element.srcObject?.getTracks()[0] !== audio.get(key)) {
                this.mixer.detach(key);
                element.srcObject = null;
                element.remove();
                this.audio.delete(key);
            }
        }
        for (const [key, track] of audio) {
            if (this.audio.has(key)) continue;
            const element = document.createElement("audio");
            element.autoplay = true;
            element.srcObject = new MediaStream([track]);
            if (this.sinkId && element.setSinkId) element.setSinkId(this.sinkId).catch(() => {});
            element.dataset.publisher = key;
            document.body.append(element);
            this.mixer.attach(key, element, this.gainFor(key));
            element.play().catch(() => {});
            this.audio.set(key, element);
        }
        this.video = video;
        this.#emit("tracks");
    }

    // --- Talking, mute and deafen -------------------------------------------

    #micOpen() {
        if (this.muted || this.deafened) return false;
        if (this.whispering) return true;
        const mode = this.activation;
        if (mode === "ptt") return this.talking;
        // Without Web Audio there is no level to gate on; stay open.
        if (mode === "voice") return this.#meter ? this.#gate.open : true;
        return true;
    }

    /** Whether the microphone is sending right now. */
    get transmitting() {
        return !!this.mic && this.#micOpen();
    }

    /** quiet: the gate toggles often, so only a real change is announced, as "transmit". */
    #applyMic(quiet = false) {
        const track = this.mic?.getAudioTracks?.()[0];
        const open = this.#micOpen();
        const changed = !!track && track.enabled !== open;
        if (track) track.enabled = open;
        if (!quiet) this.#emit("state");
        else if (changed) this.#emit("transmit");
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
        this.#applyVolumes();
        this.#applyMic();
        this.#announce();
    }

    /** Switches between push-to-talk and continuous transmission. */
    setPushToTalk(enabled) {
        if (enabled === this.pushToTalk) return;
        settings.set({ activation: enabled ? "ptt" : "continuous" });
    }

    /** Push-to-talk: open the microphone while held, close after the release delay. */
    setTalking(talking) {
        if (talking) this.#latch.press();
        else this.#latch.release();
    }

    // --- Per-member playback ---------------------------------------------------

    #client(clientID) {
        return this.session.clients?.().find(c => c.client_id === clientID) || null;
    }

    /** Playback gain of a received track key ("clientID" or "clientID|slot"). */
    gainFor(key) {
        const { clientID, kind } = parseKeyKind(key);
        const client = this.#client(clientID);
        const channel = this.session.channels?.().find(c => c.id === this.channelID);
        // Others are lowered while a priority speaker (not this phone) talks.
        const ducked = kind === "mic" && !!channel?.clients.some(c => c.priority_speaker && c.client_id !== clientID
            && c.client_id !== this.session.clientID && this.speaking.has(c.client_id));
        return playbackGain({ uid: client?.unique_id || "", kind, settings: settings.all(), deafened: this.deafened, ducked });
    }

    #applyVolumes() {
        for (const key of this.audio.keys()) this.mixer.setGain(key, this.gainFor(key));
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
        await this.mixer.setSink(deviceId);
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
        if (!data?.client_id) return;
        if (type === "priority_speaker_changed") {
            // No snapshot follows this event; update the tree in place.
            const client = this.#client(data.client_id);
            if (client) client.priority_speaker = !!data.active;
            this.#applyVolumes();
            this.#emit("state");
            return;
        }
        if (type !== "speaking_changed") return;
        if (data.speaking) this.speaking.add(data.client_id);
        else this.speaking.delete(data.client_id);
        this.#applyVolumes();
        this.#emit("speaking", data);
    }

    #snapshot() {
        // Track keys map to members only once the tree knows them.
        this.#media?.setRoster(this.#voiceRoster());
        this.#applyVolumes();
        const called = !!this.session.self?.()?.hand_called;
        if (called && !this.#wasCalled && this.active) this.#emit("called");
        this.#wasCalled = called;
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

    // --- Whisper ------------------------------------------------------------------

    #whisper(data) {
        if (!data?.from_client_id) return;
        if (data.speaking) {
            this.whisperers.add(data.from_client_id);
            this.lastWhisperer = { clientID: data.from_client_id, uniqueID: data.from_unique_id || "", nickname: data.from_nickname || "" };
        } else {
            this.whisperers.delete(data.from_client_id);
        }
        this.#emit("whisper", data);
    }

    /** Saved whisper targets: {uniqueIDs, channelIDs}. */
    get whisperTargets() {
        const targets = settings.get("whisperTargets") || {};
        return { uniqueIDs: targets.uniqueIDs || [], channelIDs: targets.channelIDs || [] };
    }

    setWhisperTargets(targets) {
        settings.set({ whisperTargets: { uniqueIDs: [...new Set(targets.uniqueIDs || [])], channelIDs: [...new Set(targets.channelIDs || [])] } });
    }

    #whisperRequest(active, targets) {
        return this.conn.request(Msg.WhisperSet, { ack_requested: true, active, unique_ids: targets.uniqueIDs, channel_ids: targets.channelIDs },
            Msg.MediaControlSaved, { accept: r => r.operation === Msg.WhisperSet });
    }

    /** Whispers to targets (default: the saved ones) until stopWhisper. */
    async startWhisper(targets = this.whisperTargets) {
        if (!this.active) return;
        // A whisper would route the microphone outside the channel's key
        // scope. Unavailable until a separate whisper key scope exists.
        if (this.#media) {
            const error = new MediaSessionError("whisper_unavailable");
            this.#emit("media-error", error);
            throw error;
        }
        const seq = ++this.#whisperSeq;
        await this.#whisperRequest(true, targets);
        // Released before the server confirmed: undo the activation.
        if (seq !== this.#whisperSeq) {
            await this.#whisperRequest(false, { uniqueIDs: [], channelIDs: [] }).catch(() => {});
            return;
        }
        this.whispering = true;
        this.#applyMic();
    }

    async stopWhisper() {
        this.#whisperSeq++;
        const was = this.whispering;
        this.whispering = false;
        this.#applyMic();
        if (was && this.conn.open && this.active) await this.#whisperRequest(false, { uniqueIDs: [], channelIDs: [] }).catch(() => {});
    }

    /**
     * Exactly the members the voice room renders (the channel node's
     * clients): the only devices that may receive this session's media keys.
     */
    #voiceRoster() {
        const channel = this.session.channels?.().find(c => c.id === this.channelID);
        return channel?.clients || [];
    }

    /** Hands a delivered media-key control (app.js onSecureControl) to the session. */
    receiveMediaKey(control) {
        return this.#media ? this.#media.receive(control) : { ok: false, reason: "inactive" };
    }

    /** Encoded-transform statistics of this session or one endpoint (diagnostics, tests). */
    mediaStats(endpoint) { return this.#media ? this.#media.stats(endpoint) : Promise.resolve(null); }

    /** The publication key an endpoint is bound to ("" = unbound: drops everything). */
    mediaBinding(endpoint) { return this.#media?.media?.boundPid(endpoint) || ""; }

    /** Whispers back to whoever whispered here last. */
    replyWhisper() {
        if (!this.lastWhisperer?.uniqueID) return Promise.resolve();
        return this.startWhisper({ uniqueIDs: [this.lastWhisperer.uniqueID], channelIDs: [] });
    }

    // --- Priority speaker and speaking queue ---------------------------------------

    get prioritySpeaker() {
        return !!this.session.self?.()?.priority_speaker;
    }

    async setPrioritySpeaker(active) {
        await this.conn.request(Msg.PrioritySpeaker, { active, ack_requested: true }, Msg.MediaControlSaved, { accept: r => r.operation === Msg.PrioritySpeaker });
        const self = this.session.self?.();
        if (self) self.priority_speaker = active;
        this.#emit("state");
    }

    /** raise | lower | next | clear on the current channel's hand queue. */
    queue(action, clientID = "") {
        return this.conn.request(Msg.VoiceQueueRequest, { action, channel_id: this.channelID, client_id: clientID }, Msg.VoiceQueueResult, { accept: r => r.action === action });
    }

    // --- Network echo test ------------------------------------------------------------

    /** Joins the server's loopback channel; false when the server has none. */
    async startEchoTest() {
        const info = await this.session.serverInfo();
        const channel = Number(info?.echo_channel_id || 0);
        if (!channel) return false;
        const previous = this.channelID;
        if (await this.join(channel) === false) return false;
        this.echo = { channel, previous };
        this.#emit("state");
        return true;
    }

    /** Returns to the channel before the echo test, or leaves voice. */
    async endEchoTest() {
        const echo = this.echo;
        this.echo = null;
        if (!echo) return;
        if (echo.previous) await this.join(echo.previous);
        else await this.leave();
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

    /** The global receive layer (per-stream choices go through wantedQuality): low with the data saver. */
    receiveLayer() {
        if (this.lowBandwidth) return "low";
        return { high: "high", medium: "mid", low: "low" }[settings.get("receiveQuality")] || "high";
    }
}
