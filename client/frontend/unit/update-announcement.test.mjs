import test from "node:test";
import assert from "node:assert/strict";
import { announceConnectedUpdate, beginQuietRestore, endQuietRestore, quietRestore, UPDATE_RESTORE_SETTLE_MS } from "../src/update-announcement.js";

test("update announcement requires a live native session", async () => {
    for (const connected of [true, false, undefined]) {
        let played = 0;
        await announceConnectedUpdate({ getState: () => ({ activeTabID: "one", serverGeneration: 4 }),
            sessionInfo: async () => ({ connected }), play: async () => { played++; } });
        assert.equal(played, connected === true ? 1 : 0);
    }
});
test("offline startup never queries or announces", async () => {
    await announceConnectedUpdate({ getState: () => ({}), sessionInfo: () => assert.fail("offline query"), play: () => assert.fail("offline speech") });
});
test("tab changes and failed native lookups suppress the update announcement", async () => {
    for (const mode of ["switch", "generation", "disconnect", "failure", "timeout"]) {
        const state = { activeTabID: "one", serverGeneration: 4, sessionGeneration: 1 };
        await announceConnectedUpdate({ getState: () => state, timeoutMs: 5,
            sessionInfo: async () => {
                if (mode === "failure") throw Error("disconnected");
                if (mode === "timeout") return new Promise(() => {});
                if (mode === "switch") state.activeTabID = "two";
                if (mode === "generation") state.serverGeneration++;
                if (mode === "disconnect") state.sessionGeneration++;
                return { connected: true };
            }, play: () => assert.fail("stale announcement") });
    }
});

test("disconnect during audio preload invalidates playback ownership", async () => {
    const state = { activeTabID: "one", serverGeneration: 4, sessionGeneration: 1 };
    await announceConnectedUpdate({ getState: () => state, sessionInfo: async () => ({ connected: true }),
        play: async current => { assert.equal(current(), true); state.sessionGeneration++; assert.equal(current(), false); } });
});
test("audio failure does not prevent updating", async () => {
    await announceConnectedUpdate({ getState: () => ({ activeTabID: "one" }), sessionInfo: async () => ({ connected: true }),
        play: async () => { throw Error("audio device unavailable"); } });
});

test("a fast update cancels the delayed announcement before it speaks", async () => {
    const state = { activeTabID: "one", serverGeneration: 4, sessionGeneration: 1 };
    let played = 0;
    const pending = announceConnectedUpdate({ getState: () => state, sessionInfo: async () => ({ connected: true }),
        delayMs: 50, play: async () => { played++; } });
    await new Promise(resolve => setTimeout(resolve, 5));
    pending.cancel(); // the handover finished first
    await pending;
    assert.equal(played, 0);
});
test("a slow update speaks after the delay, still owning the session", async () => {
    const state = { activeTabID: "one", serverGeneration: 4, sessionGeneration: 1 };
    let played = 0;
    const started = Date.now();
    await announceConnectedUpdate({ getState: () => state, sessionInfo: async () => ({ connected: true }),
        delayMs: 30, play: async current => { assert.equal(current(), true); played++; } });
    assert.equal(played, 1);
    assert.ok(Date.now() - started >= 25);
});
test("an update restore is silent until it settles; a skipped restore never silences", () => {
    const state = {};
    assert.equal(quietRestore(state), false);
    beginQuietRestore(state);
    assert.equal(quietRestore(state, Date.now() + 3600000), true);
    endQuietRestore(state, true, 1000);
    assert.equal(quietRestore(state, 1000 + UPDATE_RESTORE_SETTLE_MS - 1), true);
    assert.equal(quietRestore(state, 1000 + UPDATE_RESTORE_SETTLE_MS), false);
    beginQuietRestore(state);
    endQuietRestore(state, false);
    assert.equal(quietRestore(state), false);
});
