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
    for (const { encodings, preset = Infinity } of sources) {
        const cap = Math.min(budget, preset, localCap ? Math.floor(localCap / sources.length) : Infinity);
        const activeCount = Math.min(localCap ? 1 : encodings.length, cap);
        encodings.forEach((encoding, i) => {
            encoding.active = i < activeCount;
            const scale = encoding.rid === "h" ? 2 : encoding.rid === "q" ? 4 : 1;
            encoding.scaleResolutionDownBy = localCap ? Math.max(scale, 2) : scale;
            if (Number.isFinite(cap)) encoding.maxBitrate = activeCount ? Math.floor(cap / activeCount) : 1;
            else delete encoding.maxBitrate;
        });
    }
}
