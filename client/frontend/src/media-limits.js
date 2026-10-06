// The native bridge validates these limits at authentication and on live updates.
export function videoConstraints(width, height, fps, limits) {
    const bounded = limits?.video_max_width > 0 && limits?.video_max_height > 0;
    const scale = bounded ? Math.min(1, limits.video_max_width / width, limits.video_max_height / height) : 1;
    return {
        width: { ideal: Math.max(1, Math.floor(width * scale)), ...(bounded ? { max: limits.video_max_width } : {}) },
        height: { ideal: Math.max(1, Math.floor(height * scale)), ...(bounded ? { max: limits.video_max_height } : {}) },
        frameRate: { ideal: fps },
    };
}

export function trackFitsVideoLimits(track, limits) {
    if (!limits?.video_max_width) return true;
    const { width, height } = track.getSettings?.() || {};
    return width > 0 && height > 0 && width <= limits.video_max_width && height <= limits.video_max_height;
}

// Split the publisher ceiling between live sources, then active encodings.
// Reserve 15% for RTP overhead; the server's packet meter remains authoritative.
export function capVideoEncodings(sources, limits, localCap) {
    const budget = limits?.video_max_bitrate > 0 ? Math.floor(limits.video_max_bitrate * 0.85 / sources.length) : Infinity;
    for (const { encodings, bitrateHeadroom = Infinity, weightedSimulcast = false } of sources) {
        const cap = Math.min(budget, bitrateHeadroom, localCap ? Math.floor(localCap / sources.length) : Infinity);
        const activeCount = Math.min(localCap ? 1 : encodings.length, cap);
        const weights = encodings.map(encoding => weightedSimulcast ? encoding.rid === "f" ? 16 : encoding.rid === "h" ? 4 : 1 : 1);
        const totalWeight = weights.slice(0, activeCount).reduce((sum, weight) => sum + weight, 0);
        // Reserve a positive budget per active layer before weighting the rest;
        // rounding a tiny weighted share up must not exceed the source ceiling.
        const layerCaps = weights.map((weight, i) => i < activeCount && totalWeight ? 1 + Math.floor((cap - activeCount) * weight / totalWeight) : 1);
        const primary = encodings.findIndex((encoding, i) => i < activeCount && encoding.rid === "f");
        if (weightedSimulcast && Number.isFinite(cap) && primary >= 0) {
            // Oversized fallback-layer ceilings raise Chromium's simulcast
            // activation thresholds: even a clear LAN can get stuck below the
            // full-resolution layer. Keep fallback streams modest and give the
            // reclaimed budget to the primary, preserving aggregate headroom.
            encodings.forEach((encoding, i) => {
                if (i >= activeCount || i === primary) return;
                const ceiling = encoding.rid === "q" ? 250000 : encoding.rid === "h" ? 1000000 : layerCaps[i];
                const released = Math.max(0, layerCaps[i] - ceiling);
                layerCaps[i] -= released;
                layerCaps[primary] += released;
            });
        }
        encodings.forEach((encoding, i) => {
            encoding.active = i < activeCount;
            const scale = encoding.rid === "h" ? 2 : encoding.rid === "q" ? 4 : 1;
            encoding.scaleResolutionDownBy = localCap ? Math.max(scale, 2) : scale;
            if (Number.isFinite(cap)) encoding.maxBitrate = layerCaps[i];
            else delete encoding.maxBitrate;
        });
    }
}
