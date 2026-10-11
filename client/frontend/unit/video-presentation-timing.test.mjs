import test from "node:test";
import assert from "node:assert/strict";
import { createVideoPresentationTiming, observeVideoPresentation } from "../src/video-presentation-timing.js";

const frame = (time, number, overrides = {}) => ({ presentationTime: time, expectedDisplayTime: time + 5,
    mediaTime: 0, presentedFrames: number, processingDuration: 0.002, receiveTime: time - 10, captureTime: time - 30, ...overrides });

test("submission percentiles remain distinct from main-thread callback delays and latency estimates", () => {
    const timing = createVideoPresentationTiming();
    timing.record(100, frame(90, 1));
    timing.record(200, frame(110, 2));
    timing.record(210, frame(130, 3));
    const result = timing.snapshot(215);
    assert.equal(result.compositor_submission_interval_ms.mean, 20);
    assert.equal(result.compositor_submission_interval_ms.variance, 0);
    assert.equal(result.predicted_display_interval_ms.p95, 20);
    assert.equal(result.callback_interval_ms.mean, 55);
    assert.equal(result.callback_interval_ms.p99, 100);
    assert.equal(result.decoder_processing_ms.p50, 2);
    assert.equal(result.estimated_receive_to_display_ms.p50, 15);
    assert.equal(result.browser_estimated_capture_to_display_ms.p50, 35);
    assert.equal(result.last_callback_age_ms, 5);
    assert.equal(result.callback_coverage, 1);
});

test("missed callbacks preserve the full observed gap and are not counted as dropped video frames", () => {
    const timing = createVideoPresentationTiming();
    timing.record(100, frame(100, 10));
    timing.record(300, frame(300, 22));
    const result = timing.snapshot(310);
    assert.equal(result.unobserved_presentation_callbacks, 11);
    assert.equal(result.callback_coverage, 1 / 12);
    assert.equal(result.compositor_submission_interval_ms.p99, 200);
    assert.equal(result.callback_gaps_over_100_ms, 1);
    assert.equal(result.observed_callbacks, 2);
});

test("nearest-rank tail percentiles retain rare long presentation intervals", () => {
    const timing = createVideoPresentationTiming();
    let time = 100;
    timing.record(time, frame(time, 1));
    for (let number = 1; number <= 100; number++) {
        time += number <= 94 ? 16 : number <= 98 ? 40 : 200;
        timing.record(time, frame(time, number + 1));
    }
    const intervals = timing.snapshot(time).compositor_submission_interval_ms;
    assert.equal(intervals.count, 100);
    assert.equal(intervals.p50, 16);
    assert.equal(intervals.p95, 40);
    assert.equal(intervals.p99, 200);
    assert.equal(intervals.max, 200);
});

test("missing, invalid and inconsistent optional metadata stays unavailable", () => {
    const timing = createVideoPresentationTiming();
    timing.record(100, {});
    timing.record(120, { presentationTime: NaN, expectedDisplayTime: 110, receiveTime: 120, captureTime: -1, processingDuration: null });
    const result = timing.snapshot(130);
    assert.equal(result.callback_interval_ms.p50, 20);
    for (const name of ["compositor_submission_interval_ms", "predicted_display_interval_ms", "decoder_processing_ms",
        "estimated_receive_to_display_ms", "browser_estimated_capture_to_display_ms", "callback_coverage", "unobserved_presentation_callbacks"]) assert.equal(result[name], null, name);
});

test("live mediaTime zero is valid, while timestamp and frame counter regression start new epochs", () => {
    for (const changed of [{ presentationTime: 1 }, { expectedDisplayTime: 1 }, { mediaTime: 0 }, { presentedFrames: 1 }]) {
        const timing = createVideoPresentationTiming();
        timing.record(100, frame(100, 10, { mediaTime: 1 }));
        timing.record(120, frame(120, 11, { mediaTime: 1 }));
        timing.record(140, frame(140, 12, { mediaTime: 1, ...changed }));
        assert.equal(timing.snapshot(145).observed_callbacks, 1);
        assert.equal(timing.snapshot(145).compositor_submission_interval_ms, null);
    }
    const timing = createVideoPresentationTiming();
    timing.record(100, frame(100, 1)); timing.record(120, frame(120, 2));
    assert.equal(timing.snapshot(120).observed_callbacks, 2);
    timing.record(110, frame(140, 3));
    assert.equal(timing.snapshot(120).observed_callbacks, 1);
    timing.reset();
    assert.equal(timing.snapshot(120), null);
});

test("storage is bounded at high rates and measurements expire without hiding an ongoing callback gap", () => {
    const timing = createVideoPresentationTiming();
    for (let number = 1; number <= 4000; number++) timing.record(number, frame(number, number));
    assert.equal(timing.snapshot(4000).observed_callbacks, 2048);
    const expired = timing.snapshot(35001);
    assert.equal(expired.observed_callbacks, 0);
    assert.equal(expired.compositor_submission_interval_ms, null);
    assert.equal(expired.last_callback_age_ms, 31001);
    assert.equal(expired.stale, true);
    timing.record(35010, frame(35010, 4001));
    assert.equal(timing.snapshot(35020).observed_callbacks, 1);
    assert.equal(timing.snapshot(35020).compositor_submission_interval_ms, null);
});

test("rolling window does not include an interval crossing its start", () => {
    const timing = createVideoPresentationTiming();
    timing.record(100, frame(100, 1)); timing.record(10000, frame(10000, 2)); timing.record(20000, frame(20000, 3));
    const result = timing.snapshot(30101);
    assert.equal(result.observed_callbacks, 2);
    assert.equal(result.sample_ms, 10000);
    assert.equal(result.callback_interval_ms.count, 1);
});

function observerFixture() {
    const video = new EventTarget(), track = new EventTarget(), visibility = new EventTarget();
    video.paused = false; video.ended = false; track.readyState = "live"; visibility.visibilityState = "visible";
    let pending, requests = 0, cancelled = null, current = true, callbacks = 0;
    video.requestVideoFrameCallback = fn => { pending = fn; return ++requests; };
    video.cancelVideoFrameCallback = id => { cancelled = id; };
    const observer = observeVideoPresentation(video, { track, visibility, isCurrent: () => current, onFrame: () => callbacks++ });
    return { video, track, visibility, observer, dispatch: (time, number) => pending(time, frame(time, number)),
        stale: () => { current = false; }, state: () => ({ requests, cancelled, callbacks }) };
}

test("hidden, paused and ended video cannot retain a previous presentation epoch", () => {
    const f = observerFixture();
    f.dispatch(100, 1); f.dispatch(120, 2);
    assert.equal(f.observer.snapshot(125).observed_callbacks, 2);
    f.visibility.visibilityState = "hidden"; f.visibility.dispatchEvent(new Event("visibilitychange"));
    f.dispatch(140, 3); assert.equal(f.observer.snapshot(145), null);
    f.visibility.visibilityState = "visible"; f.visibility.dispatchEvent(new Event("visibilitychange"));
    f.dispatch(160, 4); assert.equal(f.observer.snapshot(165).observed_callbacks, 1);
    f.video.paused = true; f.video.dispatchEvent(new Event("pause"));
    assert.equal(f.observer.snapshot(170), null);
    f.video.paused = false; f.video.dispatchEvent(new Event("playing"));
    f.dispatch(180, 5); assert.equal(f.observer.snapshot(185).compositor_submission_interval_ms, null);
    f.track.readyState = "ended"; f.track.dispatchEvent(new Event("ended"));
    assert.equal(f.observer.snapshot(190), null);
    f.observer.dispose();
});

test("replacement and disposal cannot re-arm a callback that raced cancellation", () => {
    for (const dispose of [false, true]) {
        const f = observerFixture(); f.dispatch(100, 1);
        if (dispose) f.observer.dispose(); else f.stale();
        const before = f.state();
        f.dispatch(120, 2);
        assert.equal(f.state().requests, before.requests);
        assert.equal(f.state().callbacks, before.callbacks);
        assert.equal(f.observer.snapshot(130), null);
        if (dispose) assert.equal(before.cancelled, before.requests);
        f.observer.dispose();
    }
});

test("playing after starvation preserves the visible stall instead of resetting its timing epoch", () => {
    const f = observerFixture();
    f.dispatch(100, 1); f.dispatch(120, 2);
    f.video.dispatchEvent(new Event("waiting"));
    f.video.dispatchEvent(new Event("playing"));
    f.dispatch(3120, 3);
    const sample = f.observer.snapshot(3125);
    assert.equal(sample.observed_callbacks, 3);
    assert.equal(sample.callback_interval_ms.max, 3000);
    assert.equal(sample.compositor_submission_interval_ms.max, 3000);
    assert.equal(sample.callback_gaps_over_100_ms, 1);
    f.observer.dispose();
});

test("a runtime without frame callbacks or a tile without a track reports unavailable", () => {
    const video = new EventTarget(), visibility = new EventTarget();
    visibility.visibilityState = "visible"; video.paused = false;
    for (const track of [null, new EventTarget()]) {
        const observer = observeVideoPresentation(video, { track, visibility, isCurrent: () => true });
        assert.equal(observer.snapshot(100), null);
        observer.dispose();
    }
});
