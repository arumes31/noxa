// Review 2b fix P3-B (repro adopted): a refused server offer for a peer that was already replaced
// (leave+rejoin while the offer was being applied) still emits media-error
// and calls leave(), tearing down the NEW peer.
import test from "node:test";
import assert from "node:assert/strict";
import { Voice } from "../src/core/voice.js";
import { Msg } from "../src/core/frame.js";
test("a stale refused offer never tears down the replacement peer", async () => {
    const handlers = {};
    const session = Object.assign(new EventTarget(), {
        relayServers: async () => [], clients: () => [], channels: () => [], self: () => null,
        conn: { open: true, on: (type, fn) => { handlers[type] = fn; return () => {}; }, send() {}, request: async (_t, r) => ({ channel_id: r?.channel_id ?? 0 }) },
    });
    const voice = new Voice(session);
    let closedB = false;
    const pcB = { remoteDescription: null, close() { closedB = true; }, getTransceivers: () => [], getSenders: () => [] };
    const pcA = { remoteDescription: null, setRemoteDescription: async () => { voice.pc = pcB; /* rejoin happened meanwhile */ }, close() {}, getTransceivers: () => [], getSenders: () => [] };
    voice.pc = pcA;
    const errors = [];
    voice.addEventListener("media-error", e => errors.push(e.detail));
    handlers[Msg.WebRTCOffer]({ sdp: "v=0" });
    for (let i = 0; i < 50 && !errors.length; i++) await new Promise(r => setTimeout(r, 5));
    await new Promise(r => setTimeout(r, 20));
    assert.ok(!closedB && errors.length === 0, "stale offer must not tear down the replacement peer");
});
