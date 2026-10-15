import { screenBitrateHeadroom } from "./media-limits.js";
import { samplePeerStats } from "./peer-stats.js";

// A one-time initial estimate, never a bitrate floor. The caller owns the peer's
// attempt budget and rechecks publication/demand identity before negotiation.
export function screenStartupPlan({ stats, mid, parameters, profile, settings, limits, now = Date.now() }) {
    if (!stats?.values || !stats?.get || typeof mid !== "string" || !mid ||
        !Number.isFinite(now) || !profile || !Number.isFinite(profile.width) || profile.width < 1280 ||
        !Number.isFinite(profile.height) || profile.height < 720 || !Number.isFinite(profile.fps) || profile.fps < 50 || profile.fps > 240 ||
        parameters?.encodings?.length !== 1 || parameters.encodings[0].active !== false) return null;
    const encoding = parameters.encodings[0];
    if (!Number.isSafeInteger(encoding.maxBitrate) || encoding.maxBitrate <= 0) return null;
    const serverLimit = limits?.video_max_bitrate ?? 0;
    if (!Number.isSafeInteger(serverLimit) || serverLimit < 0) return null;
    const budget = Math.min(2000000, encoding.maxBitrate, screenBitrateHeadroom(profile, settings),
        serverLimit ? Math.floor(serverLimit * 0.85) : Infinity);
    const bitrateKbps = Math.floor(budget / 1000);
    if (bitrateKbps < 1) return null;
    const fresh = row => Number.isFinite(row?.timestamp) && row.timestamp <= now && now - row.timestamp <= 1500;
    const rows = [...stats.values()].filter(row => row.type === "outbound-rtp" && !row.isRemote &&
        (row.kind || row.mediaType) === "video" && row.mid === mid);
    if (rows.length !== 1 || !fresh(rows[0])) return null;
    const codec = stats.get(rows[0].codecId), transport = stats.get(rows[0].transportId);
    if (codec?.type !== "codec" || !/^video\/(?:h264|h265|vp8)$/i.test(codec.mimeType || "") ||
        !Number.isInteger(codec.payloadType) || codec.payloadType < 0 || codec.payloadType > 127 ||
        transport?.type !== "transport" || transport.dtlsState !== "connected" || !fresh(transport)) return null;
    const pair = stats.get(transport.selectedCandidatePairId);
    if (pair?.type !== "candidate-pair" || pair.state !== "succeeded" || !fresh(pair) ||
        !Number.isFinite(pair.availableOutgoingBitrate) || pair.availableOutgoingBitrate <= 0 ||
        pair.availableOutgoingBitrate >= bitrateKbps * 1000 / 2) return null;
    // Preserve the oldest authorizing sample through asynchronous negotiation.
    // A delayed answer must not reset BWE using an expired observation.
    const expiresAt = Math.min(rows[0].timestamp, transport.timestamp, pair.timestamp) + 1500;
    return { mid, payloadType: codec.payloadType, bitrateKbps, expiresAt };
}

// Only the first eligible video activation on a peer can seed its estimate.
// Failed/missing samples also spend the attempt: catalog polls cannot become a
// retry loop, and a second viewer must never reset an already running stream.
export function createScreenStartupAttempt({ sample = samplePeerStats, now = Date.now, timeoutMs = 1000 } = {}) {
    const attempted = new WeakSet();
    return async ({ pc, transceiver, track, profile, settings, limits, current, otherVideoActive }) => {
        let timer;
        try {
            const eligible = () => current() && pc.connectionState === "connected" && pc.signalingState === "stable" &&
                !transceiver.stopped && transceiver.sender.track === track && track.readyState === "live" && track.enabled &&
                !otherVideoActive() && transceiver.sender.getParameters().encodings?.length === 1 &&
                transceiver.sender.getParameters().encodings[0].active === false;
            if (attempted.has(pc) || !eligible()) return null;
            attempted.add(pc);
            const stats = await Promise.race([sample(pc, { fresh: true }), new Promise(resolve => {
                timer = setTimeout(() => resolve(null), timeoutMs);
            })]);
            if (!eligible()) return null;
            return screenStartupPlan({ stats, mid: transceiver.mid, parameters: transceiver.sender.getParameters(),
                profile, settings, limits, now: now() });
        } catch { return null; }
        finally { clearTimeout(timer); }
    };
}
