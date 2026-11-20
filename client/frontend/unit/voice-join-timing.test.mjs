import test from "node:test";
import assert from "node:assert/strict";
import { beginVoiceJoin, markVoiceJoin, markVoiceJoinKeys, slowVoiceJoinSummary, voiceJoinTiming } from "../src/voice-join-timing.js";

test("marks keep their first time and key marks follow the engine status", () => {
    beginVoiceJoin(1000);
    markVoiceJoin("microphone", 1120);
    markVoiceJoin("microphone", 1900);
    markVoiceJoinKeys({ state: "keying", epoch: 0, recipients: 2, delivered: 0, participants: [{ received: false }, { received: false }] }, 1200);
    markVoiceJoinKeys({ state: "encrypted", epoch: 1, recipients: 2, delivered: 1, participants: [{ received: true }, { received: false }] }, 1400);
    markVoiceJoinKeys({ state: "encrypted", epoch: 1, recipients: 2, delivered: 2, participants: [{ received: true }, { received: true }] }, 1700);
    assert.deepEqual(voiceJoinTiming(), { microphone: 120, own_key_active: 400, own_key_first_delivered: 400, first_key_received: 400, own_key_all_delivered: 700, all_keys_received: 700 });
});

test("only a finished slow join is summarized, once", () => {
    beginVoiceJoin(0);
    markVoiceJoin("microphone", 200);
    assert.equal(slowVoiceJoinSummary(), "", "not connected yet");
    markVoiceJoin("transport_connected", 3500);
    assert.equal(slowVoiceJoinSummary(), "microphone 200 · transport_connected 3500");
    assert.equal(slowVoiceJoinSummary(), "", "reported once");
    beginVoiceJoin(0);
    markVoiceJoin("transport_connected", 900);
    assert.equal(slowVoiceJoinSummary(), "", "a fast join is not reported");
});
