import test from "node:test";
import assert from "node:assert/strict";
import { announceConnectedUpdate } from "../src/update-announcement.js";

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
