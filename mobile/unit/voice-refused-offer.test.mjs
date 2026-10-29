import test from "node:test";
import assert from "node:assert/strict";
import { Voice } from "../src/core/voice.js";
import { Msg } from "../src/core/frame.js";

// Review 2b P3-2: a server offer the client must refuse (no encrypted media
// session) is rolled back, never answered, and ends voice with the localized
// reason instead of leaving a silent, stuck session.
test("a refused server offer is rolled back, never answered, and reported", async () => {
    const handlers = {};
    const sent = [];
    const session = Object.assign(new EventTarget(), {
        relayServers: async () => [], clients: () => [], channels: () => [], self: () => null,
        conn: { open: true, on: (type, fn) => { handlers[type] = fn; return () => {}; }, send: (type, data) => sent.push([type, data]), request: async (_type, r) => ({ channel_id: r?.channel_id ?? 0 }) },
    });
    const voice = new Voice(session);
    const descriptions = [];
    voice.pc = { remoteDescription: null, setRemoteDescription: async d => { descriptions.push(d.type); }, createAnswer: async () => { throw new Error("must not answer"); },
        setLocalDescription: async () => { throw new Error("must not answer"); }, close() {}, getTransceivers: () => [], getSenders: () => [] };
    const errors = [];
    voice.addEventListener("media-error", event => errors.push(event.detail));
    handlers[Msg.WebRTCOffer]({ sdp: "v=0" });
    for (let i = 0; i < 50 && !errors.length; i++) await new Promise(resolve => setTimeout(resolve, 5));
    assert.deepEqual(descriptions.slice(0, 2), ["offer", "rollback"]);
    assert.equal(errors.length, 1);
    assert.equal(errors[0].reason, "closed");
    assert.equal(sent.filter(([type]) => type === Msg.WebRTCAnswer).length, 0, "no answer was sent");
    voice.close();
});
