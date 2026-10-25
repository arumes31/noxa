import assert from "node:assert/strict";
import { test } from "node:test";
import { createUploadOrphanSweeper } from "../src/upload-orphans.js";
import { setLanguage } from "../src/i18n.js";

function harness(results) {
    const calls = [], toasts = [];
    let clock = 1_000_000;
    const state = { activeTabID: "tab-a", serverGeneration: 1, myClientID: "self" };
    const app = { CleanupOrphanUploadsForTab: async tabID => { calls.push(tabID); const next = results.shift(); if (next instanceof Error) throw next; return next; } };
    let tick = null;
    const sweeper = createUploadOrphanSweeper({
        app: () => app, state: () => state, toast: (text, kind) => toasts.push([text, kind]),
        now: () => clock, setTimer: fn => { tick = fn; return 1; }, clearTimer: () => { tick = null; },
    });
    return { calls, toasts, state, sweeper, advance: ms => { clock += ms; }, tick: () => tick?.() };
}

test("sweeps once after connecting, announces removals, and rate-limits per server session", async () => {
    setLanguage("de");
    const h = harness([{ removed: 3, failed: 0, kept: 0 }, { removed: 0, failed: 0, kept: 0 }, { removed: 1, failed: 0, kept: 0 }]);
    h.state.myClientID = "";
    assert.equal(await h.sweeper.sweep(), null, "offline tabs are never swept");
    h.state.myClientID = "self";
    assert.deepEqual(await h.sweeper.sweep(), { removed: 3, failed: 0, kept: 0 });
    assert.deepEqual(h.toasts, [["3 verwaiste Uploads bereinigt", "info"]]);
    assert.equal(await h.sweeper.sweep(), null, "second poll inside the interval is skipped");
    h.advance(60_000);
    assert.deepEqual(await h.sweeper.sweep(), { removed: 0, failed: 0, kept: 0 });
    assert.equal(h.toasts.length, 1, "nothing removed, nothing announced");
    h.state.serverGeneration = 2; // reconnect: a fresh session sweeps immediately
    assert.deepEqual(await h.sweeper.sweep(), { removed: 1, failed: 0, kept: 0 });
    assert.deepEqual(h.calls, ["tab-a", "tab-a", "tab-a"]);
});

test("uncertain uploads are reported once and bridge failures stay silent", async () => {
    setLanguage("en");
    const h = harness([{ removed: 0, failed: 1, kept: 2 }, { removed: 0, failed: 0, kept: 2 }, new Error("boom")]);
    await h.sweeper.sweep(true);
    await h.sweeper.sweep(true);
    assert.deepEqual(h.toasts, [["2 uploads kept: their delivery was interrupted. Check the history before sending again.", "warn"]]);
    assert.equal(await h.sweeper.sweep(true), null);
    assert.equal(h.toasts.length, 1);
    h.sweeper.dispose();
    assert.equal(h.tick(), undefined);
});

test("a missing bridge method disables the sweep", async () => {
    const sweeper = createUploadOrphanSweeper({ app: () => ({}), state: () => ({ activeTabID: "t", serverGeneration: 1, myClientID: "me" }), toast: () => assert.fail("toast"), setTimer: () => 0, clearTimer: () => {} });
    assert.equal(await sweeper.sweep(true), null);
});
