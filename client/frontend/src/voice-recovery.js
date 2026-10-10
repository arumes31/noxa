// Recover structural receiver/playback failures, never infer failure from silence.
import { remoteTrackID, parseTrackID } from "./media-track-id.js";

export function createVoiceRecovery({ state, attached, repair, resync, rebuild, now = () => performance.now() }) {
    let scope = "", peer = null, previous = new Map(), strikes = new Map(), busy = false, attempts = 0, next = 0;
    return async (pc, stats) => {
        const key = JSON.stringify([state.activeTabID, state.serverGeneration, state.myChannelID]);
        if (key !== scope) { scope = key; attempts = 0; next = 0; previous = new Map(); strikes = new Map(); }
        if (pc !== peer) { peer = pc; previous = new Map(); strikes = new Map(); }
        const current = () => state.pc === pc && key === JSON.stringify([state.activeTabID, state.serverGeneration, state.myChannelID]);
        if (busy || !current() || pc.connectionState !== "connected" || pc.signalingState !== "stable") return;
        const failed = [];
        const seen = new Set();
        for (const stat of stats.values()) {
            if (stat.type !== "inbound-rtp" || (stat.kind || stat.mediaType) !== "audio") continue;
            const transceivers = pc.getTransceivers();
            const transceiver = transceivers.find(item => stat.mid != null && item.mid === stat.mid)
                || transceivers.find(item => item.receiver.track.id === stat.trackIdentifier);
            if (!transceiver) continue;
            const track = transceiver.receiver.track, id = remoteTrackID(pc, track, transceiver);
            if (!id || transceiver.stopped || !["recvonly", "sendrecv"].includes(transceiver.currentDirection) || parseTrackID(id).slot) continue;
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
        if (!failed.length || now() < next) return;
        busy = true;
        // Keep the budget across replacement peers in the same server/channel.
        next = now() + [2000, 5000, 15000, 30000][Math.min(attempts++, 3)];
        try {
            repair(pc);
            if (!current()) return;
            if (failed.some(({track}) => track.readyState === "ended")) await rebuild(pc);
            else if (failed.some(({track,id}) => !attached(track,id))) {
                await resync(pc);
                if (current()) repair(pc);
            }
        } finally { busy = false; }
    };
}
