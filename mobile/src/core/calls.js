// calls.js — private 1:1 and group calls on the phone, interoperable with
// the desktop (client/frontend/src/private-calls.js):
//   control  172 CallRequest {action, id, target | conversation_id, signal}
//            173 CallResult {call}; events private_call {id} and
//            private_call_signal {call_id, from, to, body}
//   media    peer-to-peer mesh, relay-only ICE; the lower unique ID offers
//            with transceivers mic, camera, screen, screen audio and a
//            "call-media" data channel carrying {camera, screen}
//   signals  sealDM(JSON {call_id, from, to, type, sdp}) to the peer's
//            verified call key; candidates go 16 per signal, 64 per peer
import { openDM, sealDM } from "./crypto.js";
import { get as setting } from "./settings.js";
import { CameraSource, callOutcome, callPeer, capSender, classifyCall } from "./video.js";

const MsgCallRequest = 172;
const MsgCallResult = 173;
const CONNECT_TIMEOUT = 30000;

/** Relay-only ICE: private calls never expose local addresses to the peer. */
export function callRTCConfiguration(iceServers = []) {
    const relay = iceServers.filter(server => {
        const urls = Array.isArray(server.urls) ? server.urls : [server.urls];
        return urls.some(url => /^turns?:/i.test(String(url || ""))) && server.username && server.credential;
    });
    if (!relay.length) throw Object.assign(new Error("Private calls need a TURN relay on this server."), { code: "no_relay" });
    return { iceServers: relay, iceTransportPolicy: "relay" };
}

export class Calls extends EventTarget {
    constructor(session, dm) {
        super();
        this.session = session;
        this.dm = dm;
        this.conn = session.conn;
        this.uid = session.uniqueID;
        this.call = null;
        this.peers = new Map();
        this.stream = null;
        this.camera = null;
        this.screen = null;
        this.muted = false;
        this.deafened = false;
        /** "user" or "environment" for the next or current camera. */
        this.facingMode = "user";
        /** A second call ringing while this one is in progress. */
        this.waiting = null;
        this.#onEvent = event => this.#serverEvent(event.detail).catch(error => this.#emit("error", error));
        session.addEventListener("server-event", this.#onEvent);
    }

    #onEvent;
    #signals = Promise.resolve();
    #source = null;
    #connectedAt = 0;
    #video = false;
    #reported = "";

    #emit(type, detail) {
        if (type === "error") console.warn("call:", detail?.message || detail);
        this.dispatchEvent(new CustomEvent(type, { detail }));
    }

    #request(request) {
        return this.conn.request(MsgCallRequest, request, MsgCallResult, { accept: r => r.action === request.action });
    }

    me(call = this.call) {
        return call?.participants.find(p => p.unique_id === this.uid) || null;
    }

    /** The call is ringing on this device. */
    get incoming() {
        return !!this.call && !this.call.ended_at && this.me()?.state === "ringing" && this.call.caller !== this.uid;
    }

    get active() {
        return !!this.call && !this.call.ended_at && ["ringing", "accepted"].includes(this.me()?.state);
    }

    async #serverEvent({ type, data }) {
        if (type === "private_call" && data?.id) {
            const result = await this.#request({ action: "get", id: data.id });
            if (result.call) await this.#apply(result.call);
        } else if (type === "private_call_signal" && data?.call_id) {
            await this.#signal(data);
        }
    }

    async refresh(id) {
        const result = await this.#request({ action: "get", id });
        if (result.call) await this.#apply(result.call);
        return this.call;
    }

    // --- Actions ---------------------------------------------------------------

    async start(target, { video = false } = {}) {
        this.rtc = callRTCConfiguration(await this.session.relayServers());
        this.wantVideo = video;
        const result = await this.#request({ action: "start", target });
        await this.#apply(result.call);
        return result.call;
    }

    /** Calls everyone in a private group. */
    async startGroup(conversationID, { video = false } = {}) {
        this.rtc = callRTCConfiguration(await this.session.relayServers());
        this.wantVideo = video;
        const result = await this.#request({ action: "start", conversation_id: conversationID });
        await this.#apply(result.call);
        return result.call;
    }

    async accept({ video = false } = {}) {
        this.rtc = callRTCConfiguration(await this.session.relayServers());
        this.wantVideo = video;
        const result = await this.#request({ action: "accept", id: this.call.id });
        await this.#apply(result.call);
    }

    async decline() {
        if (!this.call) return;
        const result = await this.#request({ action: "decline", id: this.call.id }).catch(() => null);
        if (result?.call) await this.#apply(result.call);
    }

    /** Hangs up: cancel while still ringing out, otherwise leave. */
    async hangUp() {
        if (!this.call) return;
        const others = this.call.participants.filter(p => p.unique_id !== this.uid && p.state === "accepted");
        const action = this.call.caller === this.uid && !others.length ? "cancel" : "leave";
        const result = await this.#request({ action, id: this.call.id }).catch(() => null);
        if (result?.call) await this.#apply(result.call);
        else this.#end();
    }

    /** Call waiting: leaves the current call and answers the waiting one. */
    async acceptWaiting({ video = false } = {}) {
        const waiting = this.waiting;
        if (!waiting) return;
        this.waiting = null;
        await this.hangUp();
        this.call = waiting;
        await this.accept({ video });
    }

    /** Call waiting: declines the second call and stays in the current one. */
    async declineWaiting() {
        const waiting = this.waiting;
        if (!waiting) return;
        this.waiting = null;
        this.#emit("state");
        await this.#request({ action: "decline", id: waiting.id }).catch(() => null);
    }

    setMuted(muted) {
        this.muted = muted;
        for (const track of this.stream?.getAudioTracks() || []) track.enabled = !muted && !this.deafened;
        this.#emit("state");
    }

    /** Silences the other side, and our microphone like channel deafen. */
    setDeafened(deafened) {
        this.deafened = deafened;
        for (const peer of this.peers.values()) if (peer.audio) peer.audio.muted = deafened;
        for (const track of this.stream?.getAudioTracks() || []) track.enabled = !this.muted && !deafened;
        this.#emit("state");
    }

    async setCamera(on) {
        if (on && !this.camera) {
            const source = new CameraSource({ facingMode: this.facingMode, effect: setting("cameraEffect"), blurStrength: setting("blurStrength"), limits: this.session.mediaLimits });
            await source.start();
            source.addEventListener("track", () => this.#cameraChanged());
            this.#source = source;
            this.camera = source.stream;
            this.#video = true;
        } else if (!on && this.camera) {
            this.#source?.stop();
            this.#source = null;
            this.camera = null;
        }
        for (const [uid, peer] of this.peers) await this.#syncMedia(uid, peer);
        this.#emit("state");
    }

    /** Front/back camera; the new track replaces the old one on every peer. */
    async flipCamera() {
        if (!this.#source) {
            this.facingMode = this.facingMode === "user" ? "environment" : "user";
            this.#emit("state");
            return this.facingMode;
        }
        try {
            this.facingMode = await this.#source.flip();
        } finally {
            await this.#cameraChanged();
        }
        return this.facingMode;
    }

    /** Applies the camera effect setting to a running camera. */
    async setCameraEffect(effect, blurStrength) {
        this.#source?.setEffect(effect, blurStrength);
        await this.#cameraChanged();
    }

    async #cameraChanged() {
        if (!this.#source) return;
        this.camera = this.#source.stream;
        for (const [uid, peer] of this.peers) await this.#syncMedia(uid, peer).catch(error => this.#emit("error", error));
        this.#emit("state");
    }

    static screenShareSupported() {
        return typeof navigator !== "undefined" && typeof navigator.mediaDevices?.getDisplayMedia === "function";
    }

    /** Shares the screen into the call's screen slot (tablets and desktops). */
    async setScreen(on) {
        if (on && !this.screen) {
            this.screen = await navigator.mediaDevices.getDisplayMedia({ video: true, audio: false });
            this.screen.getVideoTracks()[0]?.addEventListener("ended", () => this.setScreen(false).catch(() => {}));
        } else if (!on && this.screen) {
            for (const track of this.screen.getTracks()) track.stop();
            this.screen = null;
        }
        for (const [uid, peer] of this.peers) await this.#syncMedia(uid, peer);
        this.#emit("state");
    }

    // --- Call state ------------------------------------------------------------

    async #apply(call) {
        // Already in a call: a second one that rings here (the server rings
        // every free session and phone of the account) waits for a decision.
        switch (classifyCall(this.active ? this.call : null, call, this.uid, this.waiting)) {
            case "waiting":
                if (this.waiting?.id === call.id && call.revision < this.waiting.revision) return;
                this.waiting = call;
                this.#emit("state");
                return;
            case "waiting-ended":
                this.waiting = null;
                this.#emit("state");
                if (this.active && this.call.id !== call.id) return;
                break;
            case "ignore":
                return;
        }
        if (this.call?.id === call.id && call.revision < this.call.revision) return;
        if (this.call?.id !== call.id) {
            this.#connectedAt = 0;
            this.#video = !!this.wantVideo;
        }
        this.call = call;
        const me = this.me(call);
        if (call.ended_at || !me || !["ringing", "accepted"].includes(me.state)) {
            this.#emit("state");
            this.#end();
            return;
        }
        const accepted = call.participants.filter(p => p.state === "accepted");
        for (const [uid, peer] of this.peers) {
            if (!accepted.some(p => p.unique_id === uid)) {
                this.#closePeer(peer);
                this.peers.delete(uid);
            }
        }
        this.#emit("state");
        if (me.state !== "accepted" || accepted.length < 2) return;
        await this.#capture();
        for (const participant of accepted) {
            if (participant.unique_id === this.uid) continue;
            const peer = await this.#ensurePeer(participant.unique_id);
            if (peer && this.uid < participant.unique_id && !peer.started) {
                peer.started = true;
                peer.chain = peer.chain.then(() => this.#sendOffer(participant.unique_id, peer));
            }
        }
    }

    #stopRenewal = null;

    /** New TURN credentials: update every peer; offerers restart ICE. */
    #renew(servers) {
        let rtc;
        try { rtc = callRTCConfiguration(servers); } catch { return; }
        this.rtc = rtc;
        for (const [uid, peer] of this.peers) {
            try { peer.pc.setConfiguration(rtc); } catch { continue; }
            if (this.uid >= uid) continue;
            peer.chain = peer.chain.then(async () => {
                if (peer.pc.signalingState !== "stable") return;
                peer.localCount = 0;
                peer.remoteCount = 0;
                peer.local = [];
                peer.sent = false;
                await peer.pc.setLocalDescription(await peer.pc.createOffer({ iceRestart: true }));
                await this.#sendDescription(uid, peer);
            }).catch(error => this.#emit("error", error));
        }
    }

    #end() {
        this.#stopRenewal?.();
        this.#stopRenewal = null;
        this.rtc = null;
        for (const peer of this.peers.values()) this.#closePeer(peer);
        this.peers.clear();
        this.#source?.stop();
        this.#source = null;
        for (const stream of [this.stream, this.camera, this.screen]) for (const track of stream?.getTracks() || []) track.stop();
        this.stream = null;
        this.camera = null;
        this.screen = null;
        this.deafened = false;
        this.#reportEnded();
        this.#emit("ended");
    }

    /** One "call-ended" per call, for the call history (Stores.calls). */
    #reportEnded() {
        const call = this.call;
        if (!call?.id || this.#reported === call.id) return;
        this.#reported = call.id;
        const endedAt = call.ended_at ? call.ended_at * 1000 : Date.now();
        this.#emit("call-ended", {
            id: call.id, peer: callPeer(call, this.uid), conversationID: call.conversation_id || "",
            outcome: callOutcome(call, this.uid, !!this.#connectedAt),
            startedAt: this.#connectedAt || (call.created_at ? call.created_at * 1000 : endedAt), endedAt,
            video: this.#video || !!this.wantVideo,
        });
        this.#connectedAt = 0;
        this.#video = false;
    }

    async #capture() {
        if (this.stream) return;
        try {
            this.stream = await navigator.mediaDevices.getUserMedia({ audio: { echoCancellation: true, noiseSuppression: true, autoGainControl: true } });
            for (const track of this.stream.getAudioTracks()) track.enabled = !this.muted && !this.deafened;
        } catch (error) {
            this.stream = new MediaStream();
            this.#emit("error", error);
        }
        if (this.wantVideo && !this.camera) await this.setCamera(true).catch(error => this.#emit("error", error));
    }

    // --- Peers --------------------------------------------------------------------

    async #peerKey(uid) {
        const participant = this.call?.participants.find(p => p.unique_id === uid);
        if (!participant?.public_key) return null;
        return this.dm.callKey(uid, participant.public_key, participant.device_id);
    }

    async #ensurePeer(uid) {
        if (this.peers.has(uid)) return this.peers.get(uid);
        this.rtc ||= callRTCConfiguration(await this.session.relayServers());
        this.#stopRenewal ||= this.session.onRelayRenewal(servers => this.#renew(servers));
        const pc = new RTCPeerConnection(this.rtc);
        const peer = { pc, started: false, chain: Promise.resolve(), local: [], remote: [], localCount: 0, remoteCount: 0, sent: false, timer: 0, audio: null, video: null, channel: null };
        this.peers.set(uid, peer);
        peer.connectTimer = setTimeout(() => {
            if (pc.connectionState !== "connected") this.#emit("error", Object.assign(new Error("The call could not connect."), { code: "connect" }));
        }, CONNECT_TIMEOUT);
        pc.onicecandidate = event => {
            if (!event.candidate?.candidate || ++peer.localCount > 64) return;
            peer.local.push(event.candidate.toJSON());
            this.#scheduleCandidates(uid, peer);
        };
        pc.onconnectionstatechange = () => {
            if (pc.connectionState === "connected") {
                clearTimeout(peer.connectTimer);
                this.#connectedAt ||= Date.now();
            }
            this.#emit("state");
        };
        pc.ontrack = event => {
            const audio = pc.getTransceivers().filter(t => t.receiver.track.kind === "audio");
            const video = pc.getTransceivers().filter(t => t.receiver.track.kind === "video");
            if (event.track.kind === "audio" && audio.indexOf(event.transceiver) <= 1) {
                peer.audio ||= Object.assign(document.createElement("audio"), { autoplay: true, hidden: true });
                if (!peer.audio.isConnected) document.body.append(peer.audio);
                peer.audio.muted = this.deafened;
                const stream = peer.audio.srcObject || new MediaStream();
                stream.addTrack(event.track);
                peer.audio.srcObject = stream;
                peer.audio.play().catch(() => {});
            } else if (event.track.kind === "video") {
                const slot = video.indexOf(event.transceiver) === 1 ? "screen" : "camera";
                peer.video ||= {};
                peer.video[slot] = event.track;
                this.#emit("state");
            }
        };
        const track = this.stream?.getAudioTracks()[0];
        if (this.uid < uid) {
            if (track) peer.voice = pc.addTrack(track, this.stream);
            else peer.voice = pc.addTransceiver("audio", { direction: "sendrecv" }).sender;
            peer.senders = {
                camera: pc.addTransceiver("video", { direction: "sendrecv" }).sender,
                screen: pc.addTransceiver("video", { direction: "sendrecv" }).sender,
                screenAudio: pc.addTransceiver("audio", { direction: "sendrecv" }).sender,
            };
            this.#attachChannel(peer, pc.createDataChannel("call-media"));
            await this.#syncMedia(uid, peer);
        } else {
            pc.ondatachannel = event => this.#attachChannel(peer, event.channel);
        }
        return peer;
    }

    #attachChannel(peer, channel) {
        peer.channel = channel;
        channel.onopen = () => this.#sendMediaState(peer);
        channel.onmessage = event => {
            try {
                const state = JSON.parse(event.data);
                peer.remoteMedia = { camera: !!state.camera, screen: !!state.screen };
                this.#emit("state");
            } catch {
                // Ignore malformed state.
            }
        };
    }

    #sendMediaState(peer) {
        if (peer.channel?.readyState === "open") peer.channel.send(JSON.stringify({ camera: !!this.camera, screen: !!this.screen }));
    }

    async #syncMedia(uid, peer) {
        const camera = this.camera?.getVideoTracks()[0] || null;
        if (peer.senders?.camera && peer.senders.camera.track !== camera) {
            await peer.senders.camera.replaceTrack(camera);
            if (camera) await capSender(peer.senders.camera, this.session.mediaLimits);
        }
        const screen = this.screen?.getVideoTracks()[0] || null;
        if (peer.senders?.screen && peer.senders.screen.track !== screen) await peer.senders.screen.replaceTrack(screen);
        this.#sendMediaState(peer);
    }

    #closePeer(peer) {
        clearTimeout(peer.connectTimer);
        clearTimeout(peer.timer);
        peer.pc.close();
        if (peer.audio) {
            peer.audio.srcObject = null;
            peer.audio.remove();
        }
    }

    // --- Signalling -------------------------------------------------------------

    async #send(uid, type, sdp) {
        const key = await this.#peerKey(uid);
        if (!key) throw Object.assign(new Error("The other device's key could not be verified."), { code: "untrusted" });
        const body = sealDM(JSON.stringify({ call_id: this.call.id, from: this.uid, to: uid, type, sdp }), key, this.session.identity.xSecret);
        // One account shares a small signal allowance across a call.
        const sending = this.#signals.then(async () => {
            await new Promise(resolve => setTimeout(resolve, 55));
            await this.#request({ action: "signal", id: this.call.id, target: uid, signal: body });
        });
        this.#signals = sending.catch(() => {});
        return sending;
    }

    async #sendOffer(uid, peer) {
        await peer.pc.setLocalDescription(await peer.pc.createOffer());
        await this.#sendDescription(uid, peer);
    }

    async #sendDescription(uid, peer) {
        await this.#send(uid, peer.pc.localDescription.type, peer.pc.localDescription.sdp);
        peer.sent = true;
        this.#scheduleCandidates(uid, peer);
    }

    #scheduleCandidates(uid, peer) {
        if (!peer.sent || peer.timer || !peer.local.length) return;
        peer.timer = setTimeout(() => {
            peer.timer = 0;
            const batch = peer.local.splice(0, 16);
            this.#send(uid, "candidates", JSON.stringify(batch)).then(() => this.#scheduleCandidates(uid, peer)).catch(error => this.#emit("error", error));
        }, 150);
    }

    async #signal(signal) {
        if (signal.to !== this.uid || !this.call || this.call.id !== signal.call_id) {
            await this.refresh(signal.call_id).catch(() => {});
        }
        const from = this.call?.participants.find(p => p.unique_id === signal.from);
        if (!from || from.state !== "accepted" || this.me()?.state !== "accepted") return;
        await this.#capture();
        const peer = await this.#ensurePeer(signal.from);
        peer.chain = peer.chain.then(async () => {
            const key = await this.#peerKey(signal.from);
            const plain = key ? openDM(signal.body, key, this.session.identity.xSecret) : null;
            if (!plain) throw Object.assign(new Error("A call message could not be verified."), { code: "untrusted" });
            const description = JSON.parse(plain);
            if (description.call_id !== this.call.id || description.from !== signal.from || description.to !== this.uid) throw new Error("misaddressed call message");
            if (description.type === "candidates") {
                const candidates = JSON.parse(description.sdp);
                if (!Array.isArray(candidates) || candidates.length > 16 || peer.remoteCount + candidates.length > 64) throw new Error("invalid call candidates");
                peer.remoteCount += candidates.length;
                peer.remote.push(...candidates);
            } else if (description.type === "offer") {
                if (peer.pc.signalingState !== "stable") {
                    if (this.uid < signal.from) return;
                    await peer.pc.setLocalDescription({ type: "rollback" });
                }
                await peer.pc.setRemoteDescription({ type: "offer", sdp: description.sdp });
                const video = peer.pc.getTransceivers().filter(t => t.receiver.track.kind === "video");
                const audio = peer.pc.getTransceivers().filter(t => t.receiver.track.kind === "audio");
                for (const transceiver of [...video.slice(0, 2), ...audio.slice(0, 2)]) transceiver.direction = "sendrecv";
                peer.senders = { camera: video[0]?.sender, screen: video[1]?.sender, screenAudio: audio[1]?.sender };
                const microphone = audio[0];
                const track = this.stream?.getAudioTracks()[0] || null;
                if (microphone && microphone.sender.track !== track) await microphone.sender.replaceTrack(track);
                await this.#syncMedia(signal.from, peer);
                await peer.pc.setLocalDescription(await peer.pc.createAnswer());
                await this.#sendDescription(signal.from, peer);
            } else if (description.type === "answer" && peer.pc.signalingState === "have-local-offer") {
                await peer.pc.setRemoteDescription({ type: "answer", sdp: description.sdp });
            }
            if (peer.pc.remoteDescription) {
                while (peer.remote.length) await peer.pc.addIceCandidate(peer.remote.shift()).catch(() => {});
            }
        }).catch(error => this.#emit("error", error));
        await peer.chain;
    }

    /** Bytes sent and received across this call's peers. */
    async usage() {
        let sent = 0;
        let received = 0;
        for (const peer of this.peers.values()) {
            const report = await peer.pc.getStats().catch(() => null);
            report?.forEach(stat => {
                if (stat.type === "transport") {
                    sent += stat.bytesSent || 0;
                    received += stat.bytesReceived || 0;
                }
            });
        }
        return { sent, received };
    }

    close() {
        this.session.removeEventListener("server-event", this.#onEvent);
        this.#end();
    }
}

