// Serializes offers and answers for one peer, including ICE restart collisions.
import { SLOT_SCREEN, SLOT_SCREEN_AUDIO } from "./media-track-id.js";
import { reconcileRemoteMedia } from "./remote-media.js";
import { createStartupHintOwnership } from "./video-startup-ownership.js";
import { channelMedia, MediaSessionError, MEDIA_E2EE_VERSION } from "./channel-media.js";
const startupHints = createStartupHintOwnership();
const V = () => window.__noxa;

// trackSlots declares which slot each outbound track occupies (70). The router
// carries one source per slot, so the share's system audio has to be named or
// it contends with the microphone for the default audio slot and one of the
// two is dropped for the rest of the session. The declaration REPLACES the
// previous one, so it must be complete on EVERY offer — including the ICE
// restart, which would otherwise tear the share's output track off every
// subscriber exactly when the network is already unstable.
export function trackSlots(sdp) {
    const { state } = V();
    if (!state.pc) return [];
    const shareAudio = state.shareAudioSender?.track || null;
    const shareVideo = state.shareVideoTransceiver?.sender.track || null;
    const negotiatedIDs = new Map();
    for (const section of String(sdp || "").split(/(?=^m=)/m)) {
        const mid = section.match(/^a=mid:([^\r\n]+)/m)?.[1];
        const trackID = section.match(/^a=msid:[^\s]+ ([^\r\n\s]+)/m)?.[1];
        if (mid && trackID) negotiatedIDs.set(mid, trackID);
    }
    const out = [];
    for (const s of state.pc.getSenders()) {
        const t = s.track;
        if (!t) continue;
        const transceiver = state.pc.getTransceivers().find(item => item.sender === s);
        const trackID = negotiatedIDs.get(transceiver?.mid) || (negotiatedIDs.size === 0 ? t.id : "");
        if (trackID) out.push({ track_id: trackID, slot: t === shareAudio ? SLOT_SCREEN_AUDIO : t === shareVideo ? SLOT_SCREEN : t.kind === "audio" ? "mic" : "cam" });
    }
    return out;
}

// renegotiate runs a client-initiated offer/answer round (used after adding a
// share track); the server treats re-offers idempotently. A failed round is
// rolled back to "stable": without that the pc stays in have-local-offer and
// every later renegotiation dies with InvalidStateError.
const peerNegotiations = new WeakMap();

// All local offers and remote answers share one queue per peer. A failed round
// cannot roll back a different camera, screen or ICE negotiation.
export function queuePeerNegotiation(peerConnection, operation) {
    const previous = peerNegotiations.get(peerConnection) || Promise.resolve();
    const pending = previous.catch(() => {}).then(operation);
    peerNegotiations.set(peerConnection, pending);
    const finished = () => { if (peerNegotiations.get(peerConnection) === pending) peerNegotiations.delete(peerConnection); };
    pending.then(finished, finished);
    return pending;
}

function remoteICEIdentity(sdp) {
    const usernames = new Set();
    const passwords = new Set();
    for (const line of String(sdp || "").split(/\r?\n/)) {
        if (line.startsWith("a=ice-ufrag:")) usernames.add(line.slice(12));
        if (line.startsWith("a=ice-pwd:")) passwords.add(line.slice(10));
    }
    if (!usernames.size || !passwords.size) return null;
    return JSON.stringify([[...usernames].sort(), [...passwords].sort()]);
}

// Ignore offers from a transport superseded by a reconnect or ICE restart.
// Compare ICE credentials after any earlier local round finishes.
const pendingRemoteOffers = new WeakMap();
export function answerRemoteOffer(peerConnection, generation, offerSDP) {
    const tabID = V().state.activeTabID;
    const pending = { generation, tabID, offerSDP };
    pendingRemoteOffers.set(peerConnection, pending);
    return queuePeerNegotiation(peerConnection, () => applyPendingRemoteOffer(peerConnection, pending));
}

async function applyRemoteDescription(peerConnection, description, ensureCurrent, startupPlan) {
    const transaction = startupHints.prepare(peerConnection, description, startupPlan);
    let applied = transaction.description;
    try { await peerConnection.setRemoteDescription(applied); }
    catch (error) {
        ensureCurrent();
        const retryState = description.type === "answer" ? "have-local-offer" : "stable";
        if (!transaction.changed || peerConnection.signalingState !== retryState) throw error;
        // Rejecting an optional hint must not strand an already accepted round.
        applied = description;
        await peerConnection.setRemoteDescription(applied);
    }
    // A remote offer is still pending until the local answer succeeds. Keep
    // the old ledger intact if answer creation/application fails and rolls back.
    const commit = () => transaction.commit(applied);
    if (description.type === "answer") commit();
    return commit;
}

async function applyPendingRemoteOffer(peerConnection, pending) {
        const { generation, tabID, offerSDP } = pending;
        const current = () => pendingRemoteOffers.get(peerConnection) === pending && V().state.pc === peerConnection && V().state.serverGeneration === generation && V().state.activeTabID === tabID;
        if (!current()) {
            if (pendingRemoteOffers.get(peerConnection) === pending) pendingRemoteOffers.delete(peerConnection);
            return false;
        }
        const identity = remoteICEIdentity(peerConnection.remoteDescription?.sdp);
        if (!identity || identity !== remoteICEIdentity(offerSDP)) {
            pendingRemoteOffers.delete(peerConnection);
            return false;
        }
        try {
            for (let attempt = 0; ; attempt++) {
                try {
                    if (!current()) return false;
                    if (!pending.applied) {
                        pending.commitStartupHint = await applyRemoteDescription(peerConnection, { type: "offer", sdp: offerSDP }, () => {
                            if (!current()) throw new DOMException("server session changed", "AbortError");
                        });
                        if (!current()) return false;
                        // Reused or new MIDs are bound to their CURRENT publisher
                        // (former publishers' frames are dropped) before playback.
                        await channelMedia()?.syncReceivers(peerConnection);
                        if (!current()) return false;
                        pending.applied = true;
                        reconcileRemoteMedia(peerConnection);
                    }
                    if (!pending.answer) {
                        const answer = await peerConnection.createAnswer();
                        if (!current()) return false;
                        await peerConnection.setLocalDescription(answer);
                        pending.commitStartupHint();
                        if (!current()) return false;
                        pending.answer = answer;
                        pending.serverAnswer = startupHints.serverDescription(peerConnection, answer);
                    }
                    // Retry a failed native write with the exact committed answer.
                    await window.go.main.App.WebRTCAnswerForTab(tabID, pending.serverAnswer.sdp);
                    return current();
                } catch (error) {
                    if (!current()) return false;
                    // requireTabCM can observe native activation before tab_reset
                    // reaches JS. This is a retired session, not a failed write.
                    if (String(error?.message ?? error) === "server tab changed; refresh the editor") return false;
                    if (attempt >= 2) throw error;
                    await new Promise(resolve => setTimeout(resolve, [100, 300][attempt]));
                }
            }
        } finally {
            if (pendingRemoteOffers.get(peerConnection) === pending) pendingRemoteOffers.delete(peerConnection);
        }
}

export function renegotiate(peerConnection = V().state.pc, generation = V().state.serverGeneration, offerOptions, stillRelevant = () => true) {
    if (!peerConnection) return Promise.reject(new DOMException("voice session ended", "AbortError"));
    const tabID = V().state.activeTabID;
    return queuePeerNegotiation(peerConnection, () => negotiateOffer(peerConnection, generation, offerOptions, stillRelevant, tabID));
}

export async function negotiateOffer(peerConnection, generation, offerOptions, stillRelevant, tabID, startupPlan) {
    const current = () => V().state.pc === peerConnection && V().state.serverGeneration === generation && V().state.activeTabID === tabID && stillRelevant();
    const ensureCurrent = () => {
        if (!current()) throw new DOMException("server session changed", "AbortError");
    };
    for (let attempt = 0; ; attempt++) {
      try {
        ensureCurrent();
        const offer = await peerConnection.createOffer(offerOptions);
        ensureCurrent();
        await peerConnection.setLocalDescription(offer);
        ensureCurrent();
        const serverOffer = startupHints.serverDescription(peerConnection, offer);
        const slots = trackSlots(serverOffer.sdp);
        // Channel offers always declare the encrypted transform; every sender
        // is bound to its slot key first. No session, no offer.
        const media = channelMedia();
        if (!media) throw new MediaSessionError("closed");
        // Binding runs alongside the offer: an unbound sender drops every
        // frame, so nothing leaves unencrypted meanwhile, and the offer must
        // reach the server before this peer's trickled ICE candidates.
        const binding = media.bindSenders(peerConnection, slots);
        binding.catch(() => {});
        const answerSDP = await window.go.main.App.WebRTCOfferSecureForTab(tabID, serverOffer.sdp, slots, MEDIA_E2EE_VERSION);
        ensureCurrent();
        await applyRemoteDescription(peerConnection, { type: "answer", sdp: answerSDP }, ensureCurrent, startupPlan);
        ensureCurrent();
        await binding;
        await media.syncReceivers(peerConnection);
        ensureCurrent();
        reconcileRemoteMedia(peerConnection);
        return;
      } catch (e) {
        await peerConnection?.setLocalDescription({ type: "rollback" }).catch(() => {});
        if (!current() || attempt >= 2 || !String(e).includes("webrtc negotiation collision")) throw e;
        // The server's offer may still be crossing the native event bridge.
        // Consume it under our queue ownership so its queued callback cannot
        // deadlock this retry or apply the same offer twice.
        for (let wait = 0; wait < 20 && current() && !pendingRemoteOffers.has(peerConnection); wait++) {
            await new Promise(resolve => setTimeout(resolve, 25));
        }
        ensureCurrent();
        const pending = pendingRemoteOffers.get(peerConnection);
        if (!pending || !await applyPendingRemoteOffer(peerConnection, pending)) throw e;
      }
    }
}
