import { test } from "node:test";
import assert from "node:assert/strict";
import { createTrayVoiceSync, createTrayUnreadModel, trayInChannel } from "../src/tray-state.js";

const settle = () => new Promise((resolve) => setImmediate(resolve));

test("tray sync coalesces duplicate renders and sends the latest pending state in order", async () => {
    const calls = [];
    let finish;
    const sync = createTrayVoiceSync((...state) => {
        calls.push(state);
        return new Promise((resolve) => { finish = resolve; });
    });
    sync(false, false, false, true);
    for (let i = 0; i < 1000; i++) sync(false, false, false, true);
    sync(true, false, false, true);
    sync(false, true, true, true);
    assert.deepEqual(calls, [[false, false, false, true]]);
    finish();
    await settle();
    assert.deepEqual(calls, [[false, false, false, true], [false, true, true, true]]);
    finish();
    await settle();
    sync(false, true, true, true);
    await settle();
    assert.equal(calls.length, 2);
    // Leaving the channel is a distinct state even with identical voice flags.
    sync(false, true, true, false);
    await settle();
    assert.deepEqual(calls.at(-1), [false, true, true, false]);
});

test("tray sync contains failures and retries only on a later update", async () => {
    let calls = 0;
    const sync = createTrayVoiceSync(async () => { calls++; throw new Error("bridge unavailable"); });
    sync(true, false, false, true);
    await settle();
    assert.equal(calls, 1);
    sync(true, false, false, true);
    await settle();
    assert.equal(calls, 2);
});

test("in-channel requires a live peer, a channel, and no journal replay", () => {
    const pc = {};
    assert.equal(trayInChannel({ pc, myChannelID: 4, replayingTabID: "" }), true);
    assert.equal(trayInChannel({ pc: null, myChannelID: 4, replayingTabID: "" }), false);
    assert.equal(trayInChannel({ pc, myChannelID: 0, replayingTabID: "" }), false);
    assert.equal(trayInChannel({ pc, myChannelID: 4, replayingTabID: "tab-2" }), false);
});

test("unread model sums sources and unseen messages, publishing only changed totals", async () => {
    const sent = [];
    const model = createTrayUnreadModel(async (count) => { sent.push(count); });
    model.set("chat", 2);
    await settle();
    model.set("chat", 2); // unchanged source: no bridge call
    model.set("groups", 3);
    await settle();
    model.set("tabs", 0);
    await settle();
    assert.deepEqual(sent, [2, 5]);
    model.noteUnseen();
    model.noteUnseen();
    await settle();
    assert.equal(model.total(), 7);
    // The first change is sent at once; the second is coalesced behind it.
    assert.deepEqual(sent, [2, 5, 6, 7]);
    model.clearUnseen(); // window focused: messages in the open chat were seen
    model.clearUnseen();
    await settle();
    assert.deepEqual(sent, [2, 5, 6, 7, 5]);
    model.set("chat", 0);
    model.set("groups", 0);
    await settle();
    assert.equal(sent.at(-1), 0);
    assert.equal(model.total(), 0);
});

test("unread model sanitizes counts and caps the bridge value", async () => {
    const sent = [];
    const model = createTrayUnreadModel(async (count) => { sent.push(count); });
    model.set("chat", -4);
    model.set("groups", Number.NaN);
    model.set("tabs", "3.7");
    await settle();
    assert.equal(model.total(), 3);
    model.set("chat", 1e9);
    await settle();
    assert.equal(sent.at(-1), 9999);
});
