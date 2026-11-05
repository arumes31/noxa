import test from "node:test";
import assert from "node:assert/strict";

import { Calls } from "../src/core/calls.js";

// Media requested for a call (the permission prompt still open) must be
// stopped, not kept live, when the call ends before the prompt resolves.

function deferred() {
    let resolve;
    const promise = new Promise(r => { resolve = r; });
    return { promise, resolve };
}

function fakeStream() {
    const track = { kind: "audio", enabled: true, stopped: false, stop() { this.stopped = true; }, addEventListener() {} };
    return { track, stream: { getTracks: () => [track], getAudioTracks: () => [track], getVideoTracks: () => [track] } };
}

function withMediaDevices(t, devices) {
    const previous = Object.getOwnPropertyDescriptor(globalThis, "navigator");
    Object.defineProperty(globalThis, "navigator", { configurable: true, value: { mediaDevices: devices } });
    t.after(() => {
        if (previous) Object.defineProperty(globalThis, "navigator", previous);
        else delete globalThis.navigator;
    });
}

function setup() {
    let current = null;
    const session = Object.assign(new EventTarget(), {
        uniqueID: "self",
        conn: { request: async () => ({ call: structuredClone(current) }) },
    });
    const calls = new Calls(session, {});
    calls.addEventListener("error", () => {});
    const apply = call => {
        current = call;
        session.dispatchEvent(new CustomEvent("server-event", { detail: { type: "private_call", data: { id: call.id } } }));
        return new Promise(resolve => setTimeout(resolve, 0));
    };
    return { calls, apply };
}

const call = (states, extra = {}) => ({ id: "c1", caller: "self", created_at: 1700000000, revision: extra.revision || 1, ended_at: extra.ended_at || 0,
    participants: Object.entries(states).map(([unique_id, state]) => ({ unique_id, state })) });

test("a microphone granted after hang-up is stopped and not kept", async t => {
    const prompt = deferred();
    withMediaDevices(t, { getUserMedia: () => prompt.promise });
    const { calls, apply } = setup();
    await apply(call({ self: "accepted", kai: "accepted" }));
    await apply(call({ self: "left", kai: "left" }, { revision: 2, ended_at: 1700000010 }));
    const { track, stream } = fakeStream();
    prompt.resolve(stream);
    await new Promise(resolve => setTimeout(resolve, 0));
    assert.equal(track.stopped, true);
    assert.equal(calls.stream, null);
    assert.equal(calls.peers.size, 0);
});

test("a screen share granted after the call ended is stopped", async t => {
    const prompt = deferred();
    withMediaDevices(t, { getDisplayMedia: () => prompt.promise });
    const { calls } = setup();
    const sharing = calls.setScreen(true);
    calls.close();
    const { track, stream } = fakeStream();
    prompt.resolve(stream);
    await sharing;
    assert.equal(track.stopped, true);
    assert.equal(calls.screen, null);
});
