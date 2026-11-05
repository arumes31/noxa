import test from "node:test";
import assert from "node:assert/strict";
import * as settings from "../src/core/settings.js";
import { Voice } from "../src/core/voice.js";

// Turning the camera off (or leaving) while its permission prompt is open
// must stop the late grant instead of publishing it.

function fakeSession() {
    const requests = [];
    const self = { client_id: "me", unique_id: "u-me", channel_id: 5 };
    const session = Object.assign(new EventTarget(), {
        clientID: "me",
        capabilities: new Set(),
        clients: () => [self],
        channels: () => [{ id: 5, clients: [self] }],
        self: () => self,
        serverInfo: async () => ({ echo_channel_id: 0 }),
        conn: { open: true, on: () => () => {}, send() {}, request: async (type, body) => { requests.push({ type, body }); return {}; } },
    });
    return { session, requests };
}

function cameraPrompt(t) {
    const prompts = [];
    const previous = Object.getOwnPropertyDescriptor(globalThis, "navigator");
    Object.defineProperty(globalThis, "navigator", { configurable: true, value: { mediaDevices: {
        getUserMedia: () => new Promise(resolve => prompts.push(resolve)),
    } } });
    t.after(() => {
        if (previous) Object.defineProperty(globalThis, "navigator", previous);
        else delete globalThis.navigator;
    });
    const grant = () => {
        const track = { kind: "video", stopped: false, stop() { this.stopped = true; }, addEventListener() {} };
        prompts.shift()({ getTracks: () => [track], getVideoTracks: () => [track] });
        return track;
    };
    return { prompts, grant };
}

test("a camera granted after stopCamera is stopped, not published", async t => {
    settings._resetCache();
    const { session, requests } = fakeSession();
    const { prompts, grant } = cameraPrompt(t);
    const voice = new Voice(session);
    t.after(() => voice.close());
    const transceivers = [];
    voice.pc = { close() {}, addTransceiver: (...args) => { transceivers.push(args); return { sender: {} }; } };
    const starting = voice.startCamera();
    await new Promise(resolve => setTimeout(resolve, 0));
    assert.equal(prompts.length, 1);
    // A second start while the first prompt is open does not open another camera.
    await voice.startCamera();
    assert.equal(prompts.length, 1);
    await voice.stopCamera();
    const track = grant();
    await starting;
    assert.equal(track.stopped, true);
    assert.equal(voice.camera, null);
    assert.equal(transceivers.length, 0);
    assert.equal(requests.length, 0);
});
