import assert from "node:assert/strict";
import { test } from "node:test";
import { initClosingAudio } from "../src/closing-audio.js";

test("native closing waits for speech and repeated close requests play only once", async () => {
    let onClose, finishSpeech, ready = false, plays = 0, completions = 0;
    initClosingAudio({
        runtime: { EventsOn(name, callback) { assert.equal(name, "app_closing"); onClose = callback; } },
        app: {
            ReadyForCloseNotifications() { assert.ok(onClose); ready = true; },
            CompleteClose() { completions++; },
        },
        play: () => { plays++; return new Promise(resolve => { finishSpeech = resolve; }); },
    });
    assert.equal(ready, true);
    const first = onClose(), repeated = onClose();
    await Promise.resolve();
    assert.equal(plays, 1);
    assert.equal(completions, 0);
    finishSpeech();
    await Promise.all([first, repeated]);
    assert.equal(completions, 1);
});

test("disabled or failed audio still completes the close handshake", async () => {
    for (const play of [() => {}, () => { throw new Error("audio unavailable"); }]) {
        let onClose, completions = 0;
        initClosingAudio({
            runtime: { EventsOn(_name, callback) { onClose = callback; } },
            app: { ReadyForCloseNotifications() {}, CompleteClose() { completions++; } },
            play,
        });
        await onClose();
        assert.equal(completions, 1);
    }
});

test("a disappearing native bridge does not cause an unhandled rejection", async () => {
    let onClose;
    initClosingAudio({
        runtime: { EventsOn(_name, callback) { onClose = callback; } },
        app: {
            ReadyForCloseNotifications: () => Promise.reject(new Error("unavailable")),
            CompleteClose: () => Promise.reject(new Error("already closing")),
        },
        play: () => {},
    });
    await onClose();
});
