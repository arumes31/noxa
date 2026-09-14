import { test } from "node:test";
import assert from "node:assert/strict";
import { createTrayVoiceSync } from "../src/tray-state.js";

const settle = () => new Promise((resolve) => setImmediate(resolve));

test("tray sync coalesces duplicate renders and sends the latest pending state in order", async () => {
    const calls = [];
    let finish;
    const sync = createTrayVoiceSync((...state) => {
        calls.push(state);
        return new Promise((resolve) => { finish = resolve; });
    });
    sync(false, false, false);
    for (let i = 0; i < 1000; i++) sync(false, false, false);
    sync(true, false, false);
    sync(false, true, true);
    assert.deepEqual(calls, [[false, false, false]]);
    finish();
    await settle();
    assert.deepEqual(calls, [[false, false, false], [false, true, true]]);
    finish();
    await settle();
    sync(false, true, true);
    await settle();
    assert.equal(calls.length, 2);
});

test("tray sync contains failures and retries only on a later update", async () => {
    let calls = 0;
    const sync = createTrayVoiceSync(async () => { calls++; throw new Error("bridge unavailable"); });
    sync(true, false, false);
    await settle();
    assert.equal(calls, 1);
    sync(true, false, false);
    await settle();
    assert.equal(calls, 2);
});
