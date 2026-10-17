// Recover structural receiver/playback failures, never infer failure from silence.
// The one silence that does prove a failure: the server reports a channel
// member as speaking (its VAD runs on the very packets it forwards to us), yet
// we have no receiver for that member, or its receiver got no packets since the
// previous sample. That is one broken (listener, speaker) path on the server;
// a client re-offer makes the server re-check and rebuild this subscriber's
// outputs. It costs nothing while audio flows.
import { remoteTrackID, parseTrackID } from "./media-track-id.js";

// SPEAKER_STRIKES consecutive samples (5 s apart) of a speaking member without
// fresh packets trigger a resync.
const SPEAKER_STRIKES = 2;

export function createVoiceRecovery({ state, attached, repair, resync, rebuild, now = () => performance.now() }) {
    let scope = "", peer = null, previous = new Map(), strikes = new Map(), busy = false, attempts = 0, next = 0;
    let speakerPackets = new Map(), speakerStrikes = new Map();
    return async (pc, stats) => {
        const key = JSON.stringify([state.activeTabID, state.serverGeneration, state.myChannelID]);
        if (key !== scope) { scope = key; attempts = 0; next = 0; previous = new Map(); strikes = new Map(); speakerPackets = new Map(); speakerStrikes = new Map(); }
        if (pc !== peer) { peer = pc; previous = new Map(); strikes = new Map(); speakerPackets = new Map(); speakerStrikes = new Map(); }
        const current = () => state.pc === pc && key === JSON.stringify([state.activeTabID, state.serverGeneration, state.myChannelID]);
        if (busy || !current() || pc.connectionState !== "connected" || pc.signalingState !== "stable") return;
        const failed = [];
        const seen = new Set();
        const micPackets = new Map(); // publisher client ID -> packets on its live mic receiver
        for (const stat of stats.values()) {
            if (stat.type !== "inbound-rtp" || (stat.kind || stat.mediaType) !== "audio") continue;
            const transceivers = pc.getTransceivers();
            const transceiver = transceivers.find(item => stat.mid != null && item.mid === stat.mid)
                || transceivers.find(item => item.receiver.track.id === stat.trackIdentifier);
            if (!transceiver) continue;
            const track = transceiver.receiver.track, id = remoteTrackID(pc, track, transceiver);
            if (!id || transceiver.stopped || !["recvonly", "sendrecv"].includes(transceiver.currentDirection) || parseTrackID(id).slot) continue;
            micPackets.set(parseTrackID(id).clientID, (micPackets.get(parseTrackID(id).clientID) || 0) + (stat.packetsReceived || 0));
            const token = `${stat.id}:${stat.ssrc}`;
            seen.add(token);
            const old = previous.get(token);
            previous.set(token, { packets: stat.packetsReceived, timestamp: stat.timestamp });
            const progressing = old && stat.timestamp > old.timestamp && stat.packetsReceived > old.packets;
            const broken = progressing && (track.readyState === "ended" || !attached(track, id));
            strikes.set(token, broken ? (strikes.get(token) || 0) + 1 : 0);
            if (strikes.get(token) >= 2) failed.push({track,id});
        }
        for (const token of previous.keys()) if (!seen.has(token)) { previous.delete(token); strikes.delete(token); }
        const silent = silentSpeakers(state, micPackets, speakerPackets, speakerStrikes);
        if ((!failed.length && !silent.length) || now() < next) return;
        busy = true;
        // Keep the budget across replacement peers in the same server/channel.
        next = now() + [2000, 5000, 15000, 30000][Math.min(attempts++, 3)];
        try {
            repair(pc);
            if (!current()) return;
            if (failed.some(({track}) => track.readyState === "ended")) await rebuild(pc);
            else if (silent.length || failed.some(({track,id}) => !attached(track,id))) {
                for (const id of silent) speakerStrikes.delete(id);
                await resync(pc);
                if (current()) repair(pc);
            }
        } finally { busy = false; }
    };
}

// silentSpeakers returns channel members the server reports as speaking whose
// microphone has produced no packets here for SPEAKER_STRIKES samples. A
// whisper to someone else is not addressed to us and is ignored.
export function silentSpeakers(state, micPackets, lastPackets, strikes) {
    const out = [];
    const speaking = new Set();
    for (const client of state.clients || []) {
        const id = String(client.client_id ?? "");
        if (!id || id === String(state.myClientID) || client.channel_id !== state.myChannelID) continue;
        if (!client.is_speaking || client.is_whispering) continue;
        speaking.add(id);
        const packets = micPackets.get(id);
        const before = lastPackets.get(id);
        const fresh = packets !== undefined && before !== undefined && packets > before;
        if (packets !== undefined) lastPackets.set(id, packets);
        // The first sample of a speaker with a live receiver only sets the
        // baseline; a missing receiver is evidence right away.
        const evidence = packets === undefined || (before !== undefined && !fresh);
        strikes.set(id, evidence ? (strikes.get(id) || 0) + 1 : 0);
        if (strikes.get(id) >= SPEAKER_STRIKES) out.push(id);
    }
    for (const id of strikes.keys()) if (!speaking.has(id)) strikes.delete(id);
    for (const [id, packets] of micPackets) if (!speaking.has(id)) lastPackets.set(id, packets);
    return out;
}
