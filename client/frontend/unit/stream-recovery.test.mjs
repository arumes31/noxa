import test from "node:test";
import assert from "node:assert/strict";
import { STREAM_AUTO_RETRIES, streamAutoRetry, streamRecovery } from "../src/stream-recovery.js";

test("a watch stuck without a picture retries itself a bounded number of times", () => {
    const entry = { watching: true, pending: false, recoverySince: 0 };
    const stuck = () => streamRecovery(entry, null, 9000);
    assert.equal(stuck().state, "waitingLong");
    for (let i = 0; i < STREAM_AUTO_RETRIES; i++) assert.equal(streamAutoRetry(entry, stuck()), true);
    assert.equal(streamAutoRetry(entry, stuck()), false, "the manual button remains after the budget");
});

test("no automatic retry before the wait is long, while pending, or when not watching", () => {
    assert.equal(streamAutoRetry({ watching: true }, streamRecovery({ watching: true, recoverySince: 0 }, null, 3000)), false);
    const pending = { watching: true, pending: true, recoverySince: 0 };
    assert.equal(streamAutoRetry(pending, streamRecovery(pending, null, 9000)), false);
    const idle = { watching: false };
    assert.equal(streamAutoRetry(idle, streamRecovery(idle, null, 9000)), false);
});

test("a live picture refills the retry budget", () => {
    const entry = { watching: true, autoRetries: STREAM_AUTO_RETRIES, tile: { dataset: { streamState: "live" } } };
    const track = { readyState: "live", muted: false };
    assert.equal(streamAutoRetry(entry, streamRecovery(entry, track, 1)), false);
    assert.equal(entry.autoRetries, 0);
});
