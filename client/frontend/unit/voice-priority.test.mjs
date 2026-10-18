import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { voiceSendEncoding, applyVoicePriority, applyVideoNetworkPriority, VOICE_PRIORITY, VIDEO_NETWORK_PRIORITY } from "../src/voice-priority.js";

test("microphone encodings are high for allocation and network class", () => {
    assert.deepEqual(voiceSendEncoding(), { priority: "high", networkPriority: "high" });
    assert.notEqual(voiceSendEncoding(), voiceSendEncoding(), "each transceiver gets a fresh object");
    const parameters = { encodings: [{ maxBitrate: 32000, priority: "low" }] };
    assert.equal(applyVoicePriority(parameters), parameters);
    assert.deepEqual(parameters.encodings[0], { maxBitrate: 32000, priority: "high", networkPriority: "high" });
    assert.deepEqual(applyVoicePriority({}), {});
    assert.equal(applyVoicePriority(undefined), undefined);
});

test("video keeps its allocation weight but never shares the voice network class", () => {
    const screen = applyVideoNetworkPriority({ priority: "high", maxBitrate: 8_000_000 });
    assert.deepEqual(screen, { priority: "high", maxBitrate: 8_000_000, networkPriority: "low" });
    assert.notEqual(VIDEO_NETWORK_PRIORITY, VOICE_PRIORITY);
    assert.equal(applyVideoNetworkPriority(undefined), undefined);
});

test("every microphone transceiver and channel-audio update applies voice priority", () => {
    const main = readFileSync(new URL("../src/main.js", import.meta.url), "utf8");
    const micTransceivers = main.match(/addTransceiver\(sent,[\s\S]*?\}\)/g) || [];
    assert.equal(micTransceivers.length, 2, "both microphone publish paths are covered");
    for (const call of micTransceivers) assert.match(call, /sendEncodings: \[voiceSendEncoding\(\)\]/);
    const channelAudio = main.slice(main.indexOf("async function applyChannelAudio"), main.indexOf("// (25) A move into or out of a music channel"));
    assert.match(channelAudio, /applyVoicePriority\(p\)/);
    const publication = readFileSync(new URL("../src/video-publication.js", import.meta.url), "utf8");
    assert.equal((publication.match(/applyVideoNetworkPriority\(/g) || []).length, 2, "screen/camera caps and initial screen encoding");
});
