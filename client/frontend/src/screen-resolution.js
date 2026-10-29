// One source encoding, with a floating-point scale rather than resolution tiers.
// Use the publisher's own capture/encode counters. RTT, packet loss and a single
// subscriber's capacity are deliberately not resolution signals.
export function createScreenResolutionController() {
    let previous = null, pressureMS = 0, healthyMS = 0;
    return ({ sample, scale = 1, width, height, fps = 60 }) => {
        const hold = () => { previous = null; pressureMS = 0; healthyMS = 0; return scale; };
        if (!sample || !Number.isFinite(scale) || scale < 1 ||
            !Number.isFinite(width) || !Number.isFinite(height) || Math.min(width, height) <= 0 ||
            !Number.isFinite(fps) || fps <= 0 ||
            !Number.isFinite(sample.time) || !Number.isFinite(sample.captureTime) ||
            !Number.isSafeInteger(sample.captured) || sample.captured < 0 ||
            !Number.isSafeInteger(sample.encoded) || sample.encoded < 0) return hold();
        const old = previous;
        // Repeated snapshots cannot advance the timers or erase valid evidence.
        if (old && sample.id === old.id && sample.time === old.time) return scale;
        previous = sample;
        const elapsed = sample.time - (old?.time ?? sample.time);
        const captureElapsed = sample.captureTime - (old?.captureTime ?? sample.captureTime);
        if (!old || sample.id !== old.id || elapsed < 500 || elapsed > 10000 ||
            captureElapsed < 500 || captureElapsed > 10000 ||
            sample.captured < old.captured || sample.encoded < old.encoded) {
            pressureMS = 0; healthyMS = 0; return scale;
        }
        const captureFPS = (sample.captured - old.captured) * 1000 / captureElapsed;
        const encodeFPS = (sample.encoded - old.encoded) * 1000 / elapsed;
        const expectedFPS = Math.min(captureFPS, fps);
        // Static desktops do not supply enough changing frames to diagnose loss
        // of encoder throughput. CPU pressure belongs to hardware recovery.
        const pressured = sample.reason === "bandwidth" && expectedFPS >= 8 && encodeFPS < expectedFPS * 0.85;
        const healthy = ["none", "bandwidth"].includes(sample.reason) && expectedFPS >= 8 && encodeFPS >= expectedFPS * 0.95;
        pressureMS = pressured ? pressureMS + elapsed : 0;
        healthyMS = healthy ? healthyMS + elapsed : 0;
        // Preserve at least the source's short side or 540 pixels, whichever is
        // smaller. Explicit low-bandwidth mode remains a separate user choice.
        const maxScale = Math.max(1, Math.min(width, height) / 540);
        if (pressureMS >= 20000) return Math.max(1, Math.min(maxScale, scale * 1.08 ** (Math.min(elapsed, 5000) / 5000)));
        if (healthyMS >= 5000) return Math.max(1, scale * 0.8 ** (Math.min(elapsed, 5000) / 5000));
        return scale;
    };
}
