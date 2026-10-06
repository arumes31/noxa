const levels = ["low", "mid", "high"];
const costScale = { low: 1 / 16, mid: 1 / 4, high: 1 };

// Estimates guide Auto only. They are not bitrate caps: actual content, codec
// and congestion control determine traffic. Unknown capacity keeps the desired
// quality; received traffic alone cannot establish a connection's capacity.
export function allocateReceiveQuality(streams, { availableBitrate, focusedID, cpuPressure = false, lowBandwidth = false } = {}) {
    const result = new Map();
    const cost = (stream, quality) => (Number.isFinite(stream.highBitrate) && stream.highBitrate > 0
        ? stream.highBitrate : stream.slot === "screen" ? 4000000 : 1000000) * costScale[quality];
    const desired = stream => cpuPressure ? "mid" : stream.key === focusedID || stream.slot === "screen" ? "high" : "mid";
    const automatic = streams.filter(stream => !levels.includes(stream.preference));
    let budget = Number.isFinite(availableBitrate) && availableBitrate > 0
        ? Math.max(0, availableBitrate * 0.9 - 128000) : Infinity;
    for (const stream of streams) {
        if (lowBandwidth) result.set(stream.key, "low");
        else if (levels.includes(stream.preference)) {
            result.set(stream.key, stream.preference);
            // A publisher may offer only one layer. A confirmed preference does
            // not prove that the SFU could select the requested resolution.
            budget -= Number.isFinite(stream.bitrate) && stream.bitrate > 0 ? stream.bitrate : cost(stream, stream.preference);
        }
    }
    if (lowBandwidth) return result;
    if (budget === Infinity) {
        for (const stream of automatic) result.set(stream.key, desired(stream));
    } else {
        // Keep every watched stream live at its lowest available layer, then
        // distribute upgrades fairly. Focus breaks ties, followed by screen text.
        automatic.sort((a, b) => Number(b.key === focusedID) - Number(a.key === focusedID) ||
            Number(b.slot === "screen") - Number(a.slot === "screen") || a.key.localeCompare(b.key));
        for (const stream of automatic) { result.set(stream.key, "low"); budget -= cost(stream, "low"); }
        for (const quality of ["mid", "high"]) {
            for (const stream of automatic) {
                if (levels.indexOf(quality) > levels.indexOf(desired(stream))) continue;
                const extra = cost(stream, quality) - cost(stream, result.get(stream.key));
                if (extra <= budget) { result.set(stream.key, quality); budget -= extra; }
            }
        }
    }
    return new Map(streams.map(stream => [stream.key, result.get(stream.key)]));
}

export function settleAutoQuality(previous, next, immediate = false) {
    if (!previous?.applied || immediate || next === previous.applied) return { applied: next };
    const count = previous.candidate === next ? previous.count + 1 : 1;
    const required = levels.indexOf(next) < levels.indexOf(previous.applied) ? 2 : 3;
    return count >= required ? { applied: next } : { applied: previous.applied, candidate: next, count };
}
