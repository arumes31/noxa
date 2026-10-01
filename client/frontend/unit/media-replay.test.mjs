import test from "node:test";
import assert from "node:assert/strict";
import { MediaReplayWindow } from "../src/media-replay.js";

function iv(ssrc, timestamp, counter = 0) {
    const bytes = new Uint8Array(12), view = new DataView(bytes.buffer);
    view.setUint32(0, ssrc); view.setUint32(4, timestamp); view.setUint32(8, timestamp - counter);
    return bytes;
}
test("authenticated replay window accepts reordering and rejects duplicates and aged frames", () => {
    const window = new MediaReplayWindow();
    assert.equal(window.accept(iv(42, 10000000)), true);
    assert.equal(window.accept(iv(42, 9997000)), true);
    assert.equal(window.accept(iv(42, 9997000)), false);
    assert.equal(window.accept(iv(42, 10000000, 1)), true);
    assert.equal(window.accept(iv(42, 20000000)), true);
    assert.equal(window.accept(iv(42, 10000000)), false, "eviction must not make old ciphertext replayable");
    assert.equal(window.accept(iv(43, 10000000)), true, "independent SSRC");
});
test("timestamp rollover retains duplicate protection", () => {
    const window = new MediaReplayWindow();
    assert.equal(window.accept(iv(2, 0xfffffff0)), true);
    assert.equal(window.accept(iv(2, 0x100)), true);
    assert.equal(window.accept(iv(2, 0xfffffff0)), false);
    assert.equal(window.accept(iv(2, 0x80)), true);
});
test("invalid and exhausted windows fail closed", () => {
    const window = new MediaReplayWindow({ maxSources: 2, maxFrames: 2 });
    assert.equal(window.accept(new Uint8Array(11)), false);
    assert.equal(window.accept(iv(1, 100, 0)), true);
    assert.equal(window.accept(iv(1, 100, 1)), true);
    assert.equal(window.accept(iv(1, 100, 2)), false);
    assert.equal(window.accept(iv(2, 100)), true);
    assert.equal(window.accept(iv(3, 100)), false);
    assert.equal(window.accept(iv(1, 2000000)), true, "old entries expire by age");
    assert.equal(window.accept(iv(1, 100)), false);
});
