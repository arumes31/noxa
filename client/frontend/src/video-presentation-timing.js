// rVFC describes submission to the compositor, not physical screen scanout.
// Store samples in bounded numeric storage; sort only when diagnostics are requested.
const CAPACITY = 2048, WINDOW_MS = 30000, STRIDE = 8;
const fields = ["presentationTime", "expectedDisplayTime", "mediaTime", "processingDuration", "captureTime", "receiveTime", "presentedFrames"];
const monotonicFields = [0, 1, 2, 6];
const valid = value => Number.isFinite(value) && value >= 0;
const distribution = values => {
    if (!values.length) return null;
    values.sort((a, b) => a - b);
    const mean = values.reduce((sum, value) => sum + value, 0) / values.length;
    const percentile = fraction => values[Math.max(0, Math.ceil(values.length * fraction) - 1)];
    return { count: values.length, mean, variance: values.reduce((sum, value) => sum + (value - mean) ** 2, 0) / values.length,
        p50: percentile(0.5), p95: percentile(0.95), p99: percentile(0.99), max: values.at(-1) };
};

export function createVideoPresentationTiming() {
    const samples = new Float64Array(CAPACITY * STRIDE);
    let count = 0, next = 0;
    const reset = () => { count = 0; next = 0; };
    function record(now, metadata = {}) {
        if (!valid(now)) return;
        if (count) {
            const previous = ((next + CAPACITY - 1) % CAPACITY) * STRIDE;
            // Live mediaTime may stay zero. Regression, not equality, starts an epoch.
            let regressed = now <= samples[previous];
            for (const index of monotonicFields) {
                if (valid(metadata[fields[index]]) && valid(samples[previous + index + 1]) && metadata[fields[index]] < samples[previous + index + 1]) regressed = true;
            }
            if (regressed) reset();
        }
        const offset = next * STRIDE;
        samples[offset] = now;
        for (let index = 0; index < fields.length; index++) samples[offset + index + 1] = valid(metadata[fields[index]]) ? metadata[fields[index]] : NaN;
        next = (next + 1) % CAPACITY;
        count = Math.min(CAPACITY, count + 1);
    }
    function snapshot(now) {
        if (!valid(now) || !count) return null;
        const rows = [];
        for (let index = 0; index < count; index++) {
            const offset = ((next - count + index + CAPACITY) % CAPACITY) * STRIDE;
            if (samples[offset] <= now && now - samples[offset] <= WINDOW_MS) rows.push(offset);
        }
        const callback = [], submission = [], display = [], processing = [], receive = [], capture = [];
        let submitted = 0, covered = 0, unobserved = 0, longGaps = 0;
        const difference = (target, offset, first, second, factor = 1) => {
            const value = (samples[offset + first] - samples[offset + second]) * factor;
            if (Number.isFinite(value) && value >= 0) target.push(value);
        };
        for (let index = 0; index < rows.length; index++) {
            const offset = rows[index];
            if (valid(samples[offset + 4])) processing.push(samples[offset + 4] * 1000);
            difference(receive, offset, 2, 6);
            difference(capture, offset, 2, 5);
            if (!index) continue;
            const previous = rows[index - 1];
            const interval = samples[offset] - samples[previous];
            callback.push(interval);
            if (interval > 100) longGaps++;
            for (const [target, field] of [[submission, 1], [display, 2]]) {
                const delta = samples[offset + field] - samples[previous + field];
                if (Number.isFinite(delta) && delta >= 0) target.push(delta);
            }
            const frames = samples[offset + 7] - samples[previous + 7];
            if (Number.isSafeInteger(frames) && frames > 0) { submitted += frames; covered++; unobserved += frames - 1; }
        }
        const last = ((next + CAPACITY - 1) % CAPACITY) * STRIDE;
        if (now < samples[last]) return null;
        const age = now - samples[last];
        return { sample_ms: rows.length ? samples[rows.at(-1)] - samples[rows[0]] : 0, observed_callbacks: rows.length,
            last_callback_age_ms: age, stale: age > 3000, callback_gaps_over_100_ms: longGaps,
            unobserved_presentation_callbacks: covered ? unobserved : null, callback_coverage: submitted ? covered / submitted : null,
            callback_interval_ms: distribution(callback), compositor_submission_interval_ms: distribution(submission),
            predicted_display_interval_ms: distribution(display), decoder_processing_ms: distribution(processing),
            estimated_receive_to_display_ms: distribution(receive), browser_estimated_capture_to_display_ms: distribution(capture) };
    }
    return { record, reset, snapshot };
}

// Each binding owns its callback and listeners. A replaced track cannot append
// samples or re-arm its old callback, even if cancellation raced dispatch.
export function observeVideoPresentation(video, { track, visibility, isCurrent, onFrame = () => {} }) {
    const timing = createVideoPresentationTiming();
    let callback = null, disposed = false;
    const active = () => !disposed && isCurrent() && !video.paused && !video.ended && track?.readyState !== "ended" && visibility.visibilityState === "visible";
    const reset = () => timing.reset();
    const presented = (now, metadata) => {
        if (disposed || !isCurrent()) return;
        onFrame();
        if (active()) timing.record(now, metadata); else reset();
        callback = video.requestVideoFrameCallback(presented);
    };
    // `playing` also follows starvation without a pause. Keep that gap visible;
    // explicit pause, source replacement and visibility changes already reset.
    for (const event of ["pause", "emptied", "ended"]) video.addEventListener(event, reset);
    track?.addEventListener("ended", reset);
    visibility.addEventListener("visibilitychange", reset);
    if (track && video.requestVideoFrameCallback) callback = video.requestVideoFrameCallback(presented);
    return {
        snapshot: now => active() ? timing.snapshot(now) : null,
        dispose() {
            disposed = true;
            if (callback != null) video.cancelVideoFrameCallback(callback);
            for (const event of ["pause", "emptied", "ended"]) video.removeEventListener(event, reset);
            track?.removeEventListener("ended", reset);
            visibility.removeEventListener("visibilitychange", reset);
            reset();
        },
    };
}
