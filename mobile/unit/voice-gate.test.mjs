import test from "node:test";
import assert from "node:assert/strict";
import {
    MAX_VOLUME, MutedTalkingWarning, PRIORITY_DUCK, PushToTalkLatch, VoiceGate, meterFraction, METER_MAX,
    parseKeyKind, playbackGain, rms, speakingQueue, volumeFromPercent, volumePercent,
} from "../src/core/voice-gate.js";
import { micConstraints } from "../src/core/voice.js";

/** Fake timers: setTimer/clearTimer plus advance(ms). */
function fakeClock() {
    let now = 0;
    let next = 1;
    const timers = new Map();
    return {
        setTimer: (fn, ms) => { const id = next++; timers.set(id, { fn, at: now + ms }); return id; },
        clearTimer: id => timers.delete(id),
        advance(ms) {
            now += ms;
            for (const [id, timer] of [...timers].sort((a, b) => a[1].at - b[1].at)) {
                if (timer.at <= now) { timers.delete(id); timer.fn(); }
            }
        },
        get pending() { return timers.size; },
    };
}

test("rms and meter scale", () => {
    assert.equal(rms([]), 0);
    assert.equal(rms([0.5, -0.5, 0.5, -0.5]), 0.5);
    assert.equal(meterFraction(0), 0);
    assert.equal(meterFraction(METER_MAX / 2), 0.5);
    assert.equal(meterFraction(10), 1);
});

test("voice gate opens at the threshold and holds after the level drops", () => {
    const gate = new VoiceGate({ threshold: 0.05, holdMs: 400 });
    assert.equal(gate.update(0.01, 0), false);
    assert.equal(gate.open, false);
    assert.equal(gate.update(0.05, 100), true, "reaching the threshold opens");
    assert.equal(gate.open, true);
    assert.equal(gate.update(0.0, 300), false, "still holding");
    assert.equal(gate.update(0.0, 499), false);
    assert.equal(gate.open, true);
    assert.equal(gate.update(0.0, 500), true, "closes after holdMs of silence");
    assert.equal(gate.open, false);
    // Speech during the hold extends it.
    gate.update(0.2, 1000);
    gate.update(0.0, 1300);
    gate.update(0.1, 1350);
    gate.update(0.0, 1700);
    assert.equal(gate.open, true);
    gate.update(0.0, 1750);
    assert.equal(gate.open, false);
    // A raised threshold is honoured.
    gate.threshold = 0.3;
    gate.update(0.2, 2000);
    assert.equal(gate.open, false);
    gate.update(0.9, 2100);
    gate.reset();
    assert.equal(gate.open, false);
});

test("push-to-talk closes after the release delay", () => {
    const clock = fakeClock();
    const changes = [];
    const latch = new PushToTalkLatch({ releaseMs: 200, onChange: active => changes.push(active), setTimer: clock.setTimer, clearTimer: clock.clearTimer });
    latch.press();
    assert.equal(latch.active, true);
    latch.release();
    assert.equal(latch.active, true, "still open during the delay");
    clock.advance(199);
    assert.equal(latch.active, true);
    clock.advance(1);
    assert.equal(latch.active, false);
    assert.deepEqual(changes, [true, false]);

    // Pressing again during the delay keeps the microphone open without a gap.
    latch.press();
    latch.release();
    clock.advance(100);
    latch.press();
    clock.advance(500);
    assert.equal(latch.active, true);
    latch.release();
    clock.advance(200);
    assert.equal(latch.active, false);
    assert.deepEqual(changes, [true, false, true, false]);

    // Zero delay closes at once; cancel closes and drops the timer.
    latch.releaseMs = 0;
    latch.press();
    latch.release();
    assert.equal(latch.active, false);
    latch.releaseMs = 300;
    latch.press();
    latch.release();
    latch.cancel();
    assert.equal(latch.active, false);
    assert.equal(clock.pending, 0);
    // A release without a press is ignored.
    latch.release();
    assert.equal(latch.active, false);
});

test("playback gain: per-user volume, mute for me, master, deafen, share and ducking", () => {
    const settings = { masterVolume: 0.5, userVolumes: { a: 2, b: 9 }, mutedUsers: ["m"], shareVolumes: { a: 0.25 } };
    assert.equal(playbackGain({ uid: "a", settings }), 1);
    assert.equal(playbackGain({ uid: "b", settings }), MAX_VOLUME * 0.5, "clamped to 200 %");
    assert.equal(playbackGain({ uid: "x", settings }), 0.5, "default 100 % times master");
    assert.equal(playbackGain({ uid: "m", settings }), 0, "muted for me");
    assert.equal(playbackGain({ uid: "m", kind: "share", settings }), 0.5, "mute for me leaves shared audio to its own slider");
    assert.equal(playbackGain({ uid: "a", kind: "share", settings }), 0.125);
    assert.equal(playbackGain({ uid: "a", settings, deafened: true }), 0);
    assert.equal(playbackGain({ uid: "x", settings, ducked: true }), 0.5 * PRIORITY_DUCK);
    assert.equal(playbackGain({ uid: "x", settings: { masterVolume: "nope" } }), 1);
    assert.equal(playbackGain({ uid: "x", settings: {} }), 1);
});

test("volume percent conversion and track keys", () => {
    assert.equal(volumePercent(1), 100);
    assert.equal(volumePercent(1.555), 156);
    assert.equal(volumePercent(5), 200);
    assert.equal(volumeFromPercent("150"), 1.5);
    assert.equal(volumeFromPercent(-5), 0);
    assert.deepEqual(parseKeyKind("abc"), { clientID: "abc", kind: "mic" });
    assert.deepEqual(parseKeyKind("abc|screenaudio"), { clientID: "abc", kind: "share" });
});

test("speaking queue orders raised hands of the channel", () => {
    const clients = [
        { client_id: "a", channel_id: 1, hand_raised_order: 3 },
        { client_id: "b", channel_id: 1, hand_raised_order: 1 },
        { client_id: "c", channel_id: 2, hand_raised_order: 2 },
        { client_id: "d", channel_id: 1 },
    ];
    assert.deepEqual(speakingQueue(clients, 1).map(c => c.client_id), ["b", "a"]);
    assert.deepEqual(speakingQueue(undefined, 1), []);
});

test("talking while muted warns at most once a minute", () => {
    const warning = new MutedTalkingWarning({ intervalMs: 60000 });
    assert.equal(warning.check({ muted: false, level: 1, threshold: 0.02, now: 0 }), false);
    assert.equal(warning.check({ muted: true, level: 0.01, threshold: 0.02, now: 0 }), false);
    assert.equal(warning.check({ muted: true, level: 0.1, threshold: 0.02, now: 1000 }), true);
    assert.equal(warning.check({ muted: true, level: 0.1, threshold: 0.02, now: 30000 }), false);
    assert.equal(warning.check({ muted: true, level: 0.1, threshold: 0.02, now: 61000 }), true);
});

test("microphone constraints follow the settings", () => {
    assert.deepEqual(micConstraints({ echoCancellation: false, noiseSuppression: true, autoGainControl: false, microphoneID: "" }),
        { audio: { echoCancellation: false, noiseSuppression: true, autoGainControl: false } });
    assert.deepEqual(micConstraints({ microphoneID: "dev1" }).audio.deviceId, { exact: "dev1" });
    assert.equal(micConstraints({ microphoneID: "dev1" }, { exactDevice: false }).audio.deviceId, undefined);
});
