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
    markVoiceJoinKeys({ state: "encrypted", epoch: 1, recipients: 1, delivered: 1, participants: [{ received: true }] }, 60_000);
    assert.equal(slowVoiceJoinSummary(), "", "a later participant does not make it slow");
    beginVoiceJoin(0);
    markVoiceJoinKeys({ state: "encrypted", epoch: 1, recipients: 1, delivered: 0, participants: [{ received: false }] }, 31_500);
    assert.equal(voiceJoinTiming().own_key_active, 31_500, "a slow start keeps its key marks until the transport connected");
    beginVoiceJoin(0);
    markVoiceJoinKeys({ state: "encrypted", epoch: 1, recipients: 0, delivered: 0, participants: [] }, 300);
    markVoiceJoin("transport_connected", 900);
    markVoiceJoinKeys({ state: "encrypted", epoch: 1, recipients: 1, delivered: 1, participants: [{ received: true }] }, 10_000);
    assert.equal(slowVoiceJoinSummary(), "", "joined alone: a friend arriving 10 s later is not this join");
    beginVoiceJoin(0);
    markVoiceJoinKeys({ state: "keying", epoch: 1, recipients: 1, delivered: 0, participants: [{ received: false }] }, 300);
    markVoiceJoin("transport_connected", 900);
    markVoiceJoinKeys({ state: "encrypted", epoch: 1, recipients: 1, delivered: 1, participants: [{ received: true }] }, 5000);
    assert.match(slowVoiceJoinSummary(), /all_keys_received 5000/, "a slow key exchange with a peer already there is reported");
});
