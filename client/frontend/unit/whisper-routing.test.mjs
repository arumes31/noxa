import test from "node:test";
import assert from "node:assert/strict";
import { setWhisperRouting, currentWhisperRouting } from "../src/media-controls.js";

test("whisper routing only confirms current acknowledged recipients", async () => {
    let reply = "permission denied", finish;
    globalThis.window = { __noxa: { state: { activeTabID: "a", myClientID: "me", myChannelID: 1, serverGeneration: 1 }, renderVoiceStatus() {} },
        go: { main: { App: { WhisperSetForTab: async () => reply } } } };
    const config = { active: true, clients: ["alice"], channels: [] };
    assert.equal(await setWhisperRouting(config), "permission denied");
    assert.equal(currentWhisperRouting().status, "failed");
    assert.equal(currentWhisperRouting().config, undefined);
    reply = "";
    await setWhisperRouting(config);
    config.clients.push("bob");
    assert.deepEqual(currentWhisperRouting().config.clients, ["alice"]);
    window.go.main.App.WhisperSetForTab = () => new Promise(resolve => { finish = resolve; });
    const pending = setWhisperRouting({ active: false, clients: [], channels: [] });
    assert.equal(currentWhisperRouting().status, "pending");
    window.__noxa.state.serverGeneration++;
    finish("");
    assert.equal(await pending, null);
    assert.equal(currentWhisperRouting(), null);
});
