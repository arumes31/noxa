import test from "node:test";
import assert from "node:assert/strict";
import { setWhisperRouting, currentWhisperRouting, whisperChannelsFor, bindWhisperChannels } from "../src/media-controls.js";
import { publishAudioState } from "../src/audio-state.js";

test("audio state coalesces rapid toggles and reports a failed intent only once", async () => {
    const calls = [], errors = [];
    let finish;
    globalThis.window = {
        __noxa: { state: { activeTabID: "audio", myClientID: "me", myChannelID: 1, serverGeneration: 1 }, sysMsg: error => errors.push(error) },
        go: { main: { App: { SetAudioStateForTab: (...args) => {
            calls.push(args);
            return new Promise(resolve => { finish = resolve; });
        } } } },
    };
    publishAudioState();
    window.__noxa.state.muted = true;
    publishAudioState();
    window.__noxa.state.deafened = true;
    publishAudioState();
    assert.deepEqual(calls, [["audio", false, false]]);
    finish("");
    await Promise.resolve();
    assert.deepEqual(calls, [["audio", false, false], ["audio", true, true]]);
    finish("permission denied");
    await Promise.resolve();
    publishAudioState();
    assert.equal(calls.length, 2);
    assert.equal(errors.length, 1);
});

test("audio state ignores obsolete replies and publishes the new server state", async () => {
    const calls = [], errors = [];
    let finish;
    globalThis.window = {
        __noxa: { state: { activeTabID: "old", myClientID: "me", myChannelID: 1, serverGeneration: 1 }, sysMsg: error => errors.push(error) },
        go: { main: { App: { SetAudioStateForTab: (...args) => {
            calls.push(args);
            return new Promise(resolve => { finish = resolve; });
        } } } },
    };
    publishAudioState();
    window.__noxa.state.activeTabID = "new";
    window.__noxa.state.serverGeneration++;
    window.__noxa.state.deafened = true;
    publishAudioState();
    finish("disconnected");
    await Promise.resolve();
    assert.deepEqual(calls, [["old", false, false], ["new", true, true]]);
    assert.deepEqual(errors, []);
    finish("");
    await Promise.resolve();
    publishAudioState();
    assert.equal(calls.length, 2);
    delete window.go.main.App.SetAudioStateForTab;
    window.__noxa.state.muted = true;
    assert.doesNotThrow(publishAudioState);
});

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

test("whisper channel IDs apply only on the server they were chosen on", () => {
    const settings = { whisper_channels: [2, 3], whisper_channels_server: "a.example:12333" };
    assert.deepEqual(whisperChannelsFor(settings, "a.example:12333"), [2, 3]);
    assert.deepEqual(whisperChannelsFor(settings, "b.example:12333"), []);
    assert.deepEqual(whisperChannelsFor({ whisper_channels: [2] }, "a.example:12333"), [], "an unbound legacy list is applied nowhere");
    assert.deepEqual(whisperChannelsFor(settings, ""), []);
    // Editing on another server starts that server's own list.
    const list = bindWhisperChannels(settings, "b.example:12333");
    assert.deepEqual(list, []);
    assert.equal(settings.whisper_channels_server, "b.example:12333");
    list.push(7);
    assert.deepEqual(whisperChannelsFor(settings, "b.example:12333"), [7]);
    assert.equal(bindWhisperChannels(settings, "b.example:12333"), settings.whisper_channels, "same server keeps its list");
});
