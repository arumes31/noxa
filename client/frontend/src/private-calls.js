import { t } from "./i18n.js";
import { captureConstraints, getUserVolume, isUserMuted } from "./audio.js";
import { closeDialog, confirmDialog, isCurrentServerDialog, mountServerDialog } from "./modal.js";
import { sessionUserID } from "./session-identity.js";

const V = () => window.__noxa;
const app = () => window.go.main.App;
let session = null;
let starting = false;
const callVersions = new Map();
const callKey = (tabID, generation, id) => `${tabID}:${generation}:${id}`;
function rememberCall(key, revision) {
    callVersions.set(key, revision);
    if (callVersions.size > 256) callVersions.delete(callVersions.keys().next().value);
}
const label = uid => V().state.clients.find(client => client.unique_id === uid)?.nickname || uid;
const blocked = uid => (V().state.settings?.blocked_users || []).includes(uid);
const report = error => V().toast?.(t("call.failed", { error: String(error) }), "warn");
const current = owner => session === owner && owner.tabID === V().state.activeTabID && owner.generation === V().state.serverGeneration;
const acceptedPeer = (owner, uid) => current(owner) && !blocked(uid)
    && owner.call.participants.some(peer => peer.unique_id === owner.uid && peer.state === "accepted")
    && owner.call.participants.some(peer => peer.unique_id === uid && peer.state === "accepted");
function failCall(owner, error) {
    if (!current(owner)) return;
    report(error); stopPrivateCall();
}
function node(tag, text) { const element = document.createElement(tag); if (text !== undefined) element.textContent = text; return element; }
function action(key, callback) { const button = node("button", t(key)); button.type = "button"; button.onclick = callback; return button; }
function closePeer(peer) {
    clearTimeout(peer.candidateTimer); clearTimeout(peer.connectTimer);
    peer.pc.onicecandidate = null; peer.pc.close();
    peer.audio.srcObject = null; peer.audio.remove();
    peer.localCandidates.length = 0; peer.remoteCandidates.length = 0;
}

async function leaveChannelForCall(tabID, generation) {
    if (!V().state.myChannelID) return true;
    if (!await confirmDialog({ title: t("call.leaveVoice"), message: t("call.leaveHelp"), serverScoped: true })) return false;
    if (V().state.activeTabID !== tabID || V().state.serverGeneration !== generation) return false;
    const error = await app().JoinChannelForTab(tabID, 0);
    if (error) throw new Error(error);
    if (V().state.activeTabID !== tabID || V().state.serverGeneration !== generation) return false;
    V().resetVoiceSession();
    return true;
}

export async function startPrivateCall(target = "", conversationID = "") {
    if (session || starting) { V().toast?.(t("call.busy"), "warn"); return; }
    if (target && blocked(target)) return;
    starting = true;
    const tabID = V().state.activeTabID, generation = V().state.serverGeneration;
    try {
        if (!await leaveChannelForCall(tabID, generation)) return;
        const result = await app().PrivateCallForTab(tabID, { action: "start", target, conversation_id: conversationID });
        if (V().state.activeTabID !== tabID || V().state.serverGeneration !== generation) { void app().StopPrivateCallForTab(tabID, result.call.id).catch(() => {}); return; }
        await applyCall(result.call, tabID, generation);
    } catch (error) { if (generation === V().state.serverGeneration) report(error); }
    finally { starting = false; }
}

export function stopPrivateCall() {
    const owner = session;
    if (!owner) return;
    session = null;
    rememberCall(callKey(owner.tabID, owner.generation, owner.call.id), Infinity);
    clearInterval(owner.poll);
    clearInterval(owner.audioTick);
    owner.stream?.getTracks().forEach(track => track.stop());
    owner.monitorTrack?.stop();
    void owner.context?.close().catch(() => {});
    for (const peer of owner.peers.values()) closePeer(peer);
    owner.panel.remove();
    if (!owner.call.ended_at) void app().StopPrivateCallForTab(owner.tabID, owner.call.id).catch(() => {});
}

export async function privateCallChanged(event) {
    const tabID = V().state.activeTabID, generation = V().state.serverGeneration;
    try {
        const result = await app().PrivateCallForTab(tabID, { action: "get", id: event.id });
        if (tabID !== V().state.activeTabID || generation !== V().state.serverGeneration) return;
        await applyCall(result.call, tabID, generation);
    } catch (error) { if (session?.call.id === event.id && current(session)) report(error); }
}

async function applyCall(call, tabID, generation) {
    const key = callKey(tabID, generation, call.id);
    if (call.revision < (callVersions.get(key) || 0)) return;
    rememberCall(key, call.ended_at ? Infinity : call.revision);
    const uid = sessionUserID(V().state);
    const me = call.participants.find(peer => peer.unique_id === uid);
    if (!me) return;
    if (call.ended_at || !["accepted", "ringing"].includes(me.state)) {
        if (session?.call.id === call.id) { session.call = call; stopPrivateCall(); }
        return;
    }
    if (me.state === "ringing" && (blocked(call.caller) || (session && session.call.id !== call.id))) { void app().StopPrivateCallForTab(tabID, call.id).catch(() => {}); return; }
    if (session && session.call.id !== call.id) return;
    if (!session) {
        const panel = node("aside"); panel.className = "private-call-panel"; panel.setAttribute("aria-label", t("call.active"));
        session = { call, tabID, generation, uid, panel, peers: new Map(), stream: null, capture: null, ice: [], muted: false, deafened: false, pollBusy: false, signals: Promise.resolve(), queuedSignals: 0, nextSignalAt: 0 };
        document.body.append(panel);
        if (me.state === "ringing") window.__noxaNotify?.notify("poke", t("call.incomingFrom", { name: label(call.caller) }), { uid: call.caller });
        const owner = session;
        owner.audioTick = setInterval(() => { if (current(owner)) syncAudio(owner); }, 50);
        owner.poll = setInterval(async () => {
            if (!current(owner) || (owner.stream && V().state.myChannelID > 0)) { stopPrivateCall(); return; }
            syncAudio(owner);
            if (owner.pollBusy) return;
            owner.pollBusy = true;
            try { await privateCallChanged({ id: owner.call.id }); } finally { owner.pollBusy = false; }
        }, 2000);
    }
    const owner = session;
    if (call.revision < owner.call.revision) return;
    owner.call = call;
    if (owner.renderedRevision !== call.revision) { render(owner); owner.renderedRevision = call.revision; }
    const accepted = call.participants.filter(peer => peer.state === "accepted");
    for (const [peerID, peer] of owner.peers) {
        if (!accepted.some(member => member.unique_id === peerID) || blocked(peerID)) { closePeer(peer); owner.peers.delete(peerID); }
    }
    if (me.state !== "accepted" || accepted.length < 2) return;
    try {
        await ensureCapture(owner);
        if (!current(owner)) return;
        // Membership can change while capture and ICE configuration are pending.
        // Negotiate from the current revision, never the earlier accepted list.
        for (const participant of owner.call.participants) {
            if (participant.unique_id === uid || !acceptedPeer(owner, participant.unique_id)) continue;
            const peer = ensurePeer(owner, participant.unique_id);
            if (uid < participant.unique_id && !peer.started) {
                peer.started = true;
                peer.chain = peer.chain.then(async () => {
                    if (!acceptedPeer(owner, participant.unique_id) || peer.pc.signalingState === "closed") return;
                    await peer.pc.setLocalDescription(await peer.pc.createOffer());
                    await sendDescription(owner, participant.unique_id, peer.pc);
                }).catch(error => { if (acceptedPeer(owner, participant.unique_id)) failCall(owner, error); });
            }
        }
    } catch (error) { failCall(owner, error); }
}

function render(owner) {
    const me = owner.call.participants.find(peer => peer.unique_id === owner.uid);
    const incoming = me?.state === "ringing";
    const accepted = owner.call.participants.filter(peer => peer.state === "accepted").length;
    const title = node("strong", t(incoming ? "call.incoming" : accepted < 2 ? "call.ringing" : "call.active"));
    const people = node("p", owner.call.participants.filter(peer => ["accepted", "ringing"].includes(peer.state)).map(peer => label(peer.unique_id)).join(", "));
    const controls = node("div"); controls.className = "private-call-controls";
    if (incoming) {
        controls.append(action("call.accept", async () => {
            try {
                if (!current(owner) || !await leaveChannelForCall(owner.tabID, owner.generation) || !current(owner)) return;
                const result = await app().PrivateCallForTab(owner.tabID, { action: "accept", id: owner.call.id });
                if (current(owner)) await applyCall(result.call, owner.tabID, owner.generation);
            } catch (error) { if (current(owner)) report(error); }
        }), action("call.decline", stopPrivateCall));
    } else {
        controls.append(action(owner.muted ? "call.unmute" : "call.mute", () => { owner.muted = !owner.muted; syncAudio(owner); render(owner); }), action(owner.deafened ? "call.undeafen" : "call.deafen", () => { owner.deafened = !owner.deafened; syncAudio(owner); render(owner); }), action("call.end", stopPrivateCall));
        if ((V().state.settings?.activation_mode || "ptt") === "ptt") {
            const hold = action("workspace.ptt");
            const release = () => { owner.ptt = false; syncAudio(owner); };
            hold.onpointerdown = event => { hold.setPointerCapture(event.pointerId); owner.ptt = true; syncAudio(owner); };
            hold.onpointerup = release; hold.onpointercancel = release; hold.onlostpointercapture = release; hold.onblur = release;
            hold.onkeydown = event => { if (event.key === " " || event.key === "Enter") { event.preventDefault(); owner.ptt = true; syncAudio(owner); } };
            hold.onkeyup = release;
            controls.prepend(hold);
        }
    }
    owner.panel.replaceChildren(title, people, controls);
}

async function ensureCapture(owner) {
    if (owner.capture) return owner.capture;
    owner.capture = (async () => {
        let stream;
        try { stream = await navigator.mediaDevices.getUserMedia({ audio: captureConstraints(null), video: false }); }
        catch { stream = new MediaStream(); if (current(owner)) V().toast?.(t("call.audioFailed"), "warn"); }
        if (!current(owner)) { stream.getTracks().forEach(track => track.stop()); return; }
        owner.stream = stream;
        if (stream.getAudioTracks()[0]) {
            owner.context = new AudioContext();
            owner.monitorTrack = stream.getAudioTracks()[0].clone(); owner.monitorTrack.enabled = true;
            const source = owner.context.createMediaStreamSource(new MediaStream([owner.monitorTrack]));
            owner.analyser = owner.context.createAnalyser(); owner.analyser.fftSize = 512;
            source.connect(owner.analyser); owner.samples = new Uint8Array(owner.analyser.frequencyBinCount);
            void owner.context.resume().catch(() => {});
        }
        owner.ice = await app().GetICEServersForTab(owner.tabID);
        syncAudio(owner);
    })();
    return owner.capture;
}

function syncAudio(owner) {
    const state = V().state;
    const mode = state.settings?.activation_mode || "ptt";
    if (owner.analyser) {
        owner.analyser.getByteTimeDomainData(owner.samples);
        const level = owner.samples.reduce((sum, value) => sum + Math.abs(value-128), 0) / owner.samples.length / 128;
        if (level > (state.settings?.vad_threshold ?? 50) / 100 * 0.2) owner.lastVoice = Date.now();
    }
    const transmit = mode === "continuous" || (mode === "vad" ? Date.now() - (owner.lastVoice || 0) < 300 : owner.ptt || state.pttActive);
    for (const track of owner.stream?.getAudioTracks() || []) track.enabled = !!(!owner.muted && !state.muted && transmit);
    for (const [uid, peer] of owner.peers) {
        peer.audio.muted = !!(owner.deafened || state.deafened || blocked(uid) || isUserMuted(uid));
        peer.audio.volume = Math.min(1, Math.max(0, getUserVolume(uid)));
    }
}

function ensurePeer(owner, uid) {
    if (owner.peers.has(uid)) return owner.peers.get(uid);
    const pc = new RTCPeerConnection({ iceServers: owner.ice || [] });
    const audio = node("audio"); audio.autoplay = true;
    // Keep playback separate from the panel, which rerenders as call state changes.
    audio.hidden = true; document.body.append(audio);
    const peer = { pc, audio, started: false, chain: Promise.resolve(), localCandidates: [], remoteCandidates: [], localCandidateCount: 0, remoteCandidateCount: 0, descriptionSent: false, candidateTimer: null };
    owner.peers.set(uid, peer);
    peer.connectTimer = setTimeout(() => {
        if (acceptedPeer(owner, uid) && owner.peers.get(uid) === peer && pc.connectionState !== "connected") failCall(owner, t("call.connecting"));
    }, 30000);
    pc.onicecandidate = event => {
        if (!event.candidate?.candidate || !acceptedPeer(owner, uid) || owner.peers.get(uid) !== peer) return;
        if (++peer.localCandidateCount > 64) { failCall(owner, new Error("Too many call candidates")); return; }
        peer.localCandidates.push(event.candidate.toJSON());
        scheduleCandidates(owner, uid, peer);
    };
    const track = owner.stream.getAudioTracks()[0];
    // addTrack allows the answerer to reuse the offer's audio transceiver.
    // A pre-created addTransceiver on the answerer remains unassociated and
    // otherwise produces a receive-only answer despite a live microphone.
    if (track) pc.addTrack(track, owner.stream);
    else if (owner.uid < uid) pc.addTransceiver("audio", { direction: "recvonly" });
    pc.ontrack = event => {
        if (!current(owner)) return;
        audio.srcObject = event.streams[0] || new MediaStream([event.track]);
        syncAudio(owner);
        if (typeof audio.setSinkId === "function" && V().state.settings?.playback_device_id) void audio.setSinkId(V().state.settings.playback_device_id).catch(report);
        void audio.play().catch(report);
    };
    pc.onconnectionstatechange = () => {
        if (pc.connectionState === "connected") clearTimeout(peer.connectTimer);
        if (pc.connectionState === "failed" && acceptedPeer(owner, uid)) failCall(owner, t("call.connecting"));
    };
    return peer;
}

function sendSignal(owner, uid, kind, payload) {
    if (owner.queuedSignals >= 256) return Promise.reject(new Error("Call signaling capacity reached"));
    owner.queuedSignals++;
    const sending = owner.signals.then(async () => {
        if (!acceptedPeer(owner, uid)) return;
        const delay = owner.nextSignalAt - Date.now();
        if (delay > 0) await new Promise(resolve => setTimeout(resolve, delay));
        if (!acceptedPeer(owner, uid)) return;
        // One account shares a 32/s server allowance across all group peers.
        owner.nextSignalAt = Date.now() + 55;
        await app().SendPrivateCallDescriptionForTab(owner.tabID, owner.call.id, uid, kind, payload);
    }).finally(() => { owner.queuedSignals--; });
    owner.signals = sending.catch(() => {});
    return sending;
}

function scheduleCandidates(owner, uid, peer) {
    if (!peer.descriptionSent || peer.candidateTimer || !peer.localCandidates.length || !acceptedPeer(owner, uid)) return;
    peer.candidateTimer = setTimeout(() => {
        peer.candidateTimer = null;
        if (!acceptedPeer(owner, uid) || owner.peers.get(uid) !== peer) return;
        const candidates = peer.localCandidates.splice(0, 16);
        void sendSignal(owner, uid, "candidates", JSON.stringify(candidates)).then(() => scheduleCandidates(owner, uid, peer))
            .catch(error => { if (acceptedPeer(owner, uid)) failCall(owner, error); });
    }, 150);
}

async function sendDescription(owner, uid, pc) {
    if (!acceptedPeer(owner, uid) || pc.signalingState === "closed") return;
    await sendSignal(owner, uid, pc.localDescription.type, pc.localDescription.sdp);
    const peer = owner.peers.get(uid);
    if (!acceptedPeer(owner, uid) || peer?.pc !== pc) return;
    peer.descriptionSent = true;
    scheduleCandidates(owner, uid, peer);
}

async function applyCandidates(owner, uid, peer) {
    if (!peer.pc.remoteDescription) return;
    while (peer.remoteCandidates.length && acceptedPeer(owner, uid) && peer.pc.signalingState !== "closed") {
        await peer.pc.addIceCandidate(peer.remoteCandidates.shift());
    }
}

export async function privateCallSignal(signal) {
    if (blocked(signal.from)) return;
    const tabID = V().state.activeTabID, generation = V().state.serverGeneration;
    const accepted = owner => owner && current(owner) && owner.call.id === signal.call_id
        && owner.call.participants.some(peer => peer.unique_id === owner.uid && peer.state === "accepted")
        && owner.call.participants.some(peer => peer.unique_id === signal.from && peer.state === "accepted");
    if (!accepted(session)) {
        // The authorized signal can arrive before the asynchronous call-state
        // reply. Refresh membership before processing it or acquiring media.
        if (session && session.call.id !== signal.call_id) return;
        await privateCallChanged({ id: signal.call_id });
    }
    const owner = session;
    if (tabID !== V().state.activeTabID || generation !== V().state.serverGeneration || !accepted(owner)) return;
    try {
        await ensureCapture(owner);
        if (!acceptedPeer(owner, signal.from)) return;
        const peer = ensurePeer(owner, signal.from);
        peer.chain = peer.chain.then(async () => {
            const description = await app().OpenPrivateCallDescriptionForTab(owner.tabID, signal);
            if (!acceptedPeer(owner, signal.from) || peer.pc.signalingState === "closed") return;
            if (description.type === "candidates") {
                const candidates = JSON.parse(description.sdp);
                if (!Array.isArray(candidates) || !candidates.length || candidates.length > 16 || peer.remoteCandidateCount + candidates.length > 64) throw new Error("Invalid call candidates");
                peer.remoteCandidateCount += candidates.length;
                peer.remoteCandidates.push(...candidates);
                await applyCandidates(owner, signal.from, peer);
                return;
            }
            if (description.type === "offer") {
                if (owner.uid < signal.from || peer.pc.signalingState !== "stable") return;
                await peer.pc.setRemoteDescription({ type: "offer", sdp: description.sdp });
                await peer.pc.setLocalDescription(await peer.pc.createAnswer());
                await sendDescription(owner, signal.from, peer.pc);
            } else if (description.type === "answer" && peer.pc.signalingState === "have-local-offer") await peer.pc.setRemoteDescription({ type: "answer", sdp: description.sdp });
            await applyCandidates(owner, signal.from, peer);
        }).catch(error => { if (acceptedPeer(owner, signal.from)) failCall(owner, error); });
        await peer.chain;
    } catch (error) { failCall(owner, error); }
}

export async function showCallHistory() {
    const overlay = node("div"); overlay.className = "dlg-overlay";
    const dialog = node("section"); dialog.className = "dlg call-history";
    const list = node("div", t("call.connecting"));
    dialog.append(node("h2", t("call.history")), list, action("group.close", () => closeDialog(overlay, "cancel"))); overlay.append(dialog);
    const tabID = V().state.activeTabID;
    mountServerDialog(overlay);
    try {
        const result = await app().PrivateCallForTab(tabID, { action: "history" });
        if (!isCurrentServerDialog(overlay)) return;
        list.replaceChildren();
        for (const call of result.history || []) {
            const uid = sessionUserID(V().state);
            const me = call.participants.find(peer => peer.unique_id === uid);
            list.append(node("p", `${new Date(call.created_at * 1000).toLocaleString()} · ${call.participants.filter(peer => peer.unique_id !== uid).map(peer => label(peer.unique_id)).join(", ")} · ${t(me?.state === "missed" ? "call.missed" : call.ended_at ? "call.ended" : "call.active")}`));
        }
        if (!result.history?.length) list.textContent = t("call.empty");
    } catch (error) { if (isCurrentServerDialog(overlay)) list.textContent = t("call.failed", { error: String(error) }); }
}
