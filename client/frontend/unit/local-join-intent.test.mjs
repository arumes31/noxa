import assert from "node:assert/strict";
import { test } from "node:test";

import { consumeLocalChannelJoin, forgetLocalChannelJoin, joinChannelLocally, noteLocalChannelJoin, settleLocalChannelJoinAt } from "../src/local-join-intent.js";

test("a server move without a local join request never authorizes undeafening", () => {
    assert.equal(consumeLocalChannelJoin("tab-a", 3, 1_000), false);
});

test("a local join request authorizes exactly one matching move on its own tab", () => {
    noteLocalChannelJoin("tab-a", 3, 1_000);
    assert.equal(consumeLocalChannelJoin("tab-b", 3, 1_001), false);
    assert.equal(consumeLocalChannelJoin("tab-a", 4, 1_001), false);
    assert.equal(consumeLocalChannelJoin("tab-a", 3, 1_002), true);
    assert.equal(consumeLocalChannelJoin("tab-a", 3, 1_003), false);
});

test("a default-channel request matches the server's placement; intents expire and can be withdrawn", () => {
    noteLocalChannelJoin("tab-a", 0, 1_000);
    assert.equal(consumeLocalChannelJoin("tab-a", 9, 2_000), true);
    noteLocalChannelJoin("tab-a", 3, 1_000);
    assert.equal(consumeLocalChannelJoin("tab-a", 3, 1_000 + 30_001), false);
    noteLocalChannelJoin("tab-a", 3, 1_000);
    forgetLocalChannelJoin("tab-a");
    assert.equal(consumeLocalChannelJoin("tab-a", 3, 1_001), false);
    noteLocalChannelJoin("", 3, 1_000);
    assert.equal(consumeLocalChannelJoin("", 3, 1_001), false);
});

test("joinChannelLocally records the intent before the native join and withdraws it on failure", async () => {
    const calls = [];
    globalThis.window = { go: { main: { App: { JoinChannelForTab: async (tab, channel) => {
        if (channel === 5) return "channel is full";
        calls.push([tab, channel, consumeLocalChannelJoin(tab, channel)]);
        return "";
    } } } } };
    try {
        assert.equal(await joinChannelLocally("tab-a", 4), "");
        assert.deepEqual(calls.at(-1), ["tab-a", 4, true]);
        assert.equal(await joinChannelLocally("tab-a", 5), "channel is full");
        assert.equal(consumeLocalChannelJoin("tab-a", 5), false);
        window.go.main.App.JoinChannelForTab = async () => { throw new Error("offline"); };
        await assert.rejects(joinChannelLocally("tab-a", 6), /offline/);
        assert.equal(consumeLocalChannelJoin("tab-a", 6), false);
    } finally { delete globalThis.window; }
});

test("a failed older join never deletes a newer join's intent", () => {
    const older = noteLocalChannelJoin("tab-t", 3, 1_000);
    noteLocalChannelJoin("tab-t", 4, 1_001);
    forgetLocalChannelJoin("tab-t", older);
    assert.equal(consumeLocalChannelJoin("tab-t", 4, 1_002), true);
});

test("a completed home join retires its default-channel intent", () => {
    noteLocalChannelJoin("tab-h", 0, 1_000);
    settleLocalChannelJoinAt("tab-h", 7);
    settleLocalChannelJoinAt("tab-h", 0);
    assert.equal(consumeLocalChannelJoin("tab-h", 9, 1_001), false);
    noteLocalChannelJoin("tab-h", 5, 1_000);
    settleLocalChannelJoinAt("tab-h", 0);
    assert.equal(consumeLocalChannelJoin("tab-h", 5, 1_001), true, "a specific intent is unaffected");
});
