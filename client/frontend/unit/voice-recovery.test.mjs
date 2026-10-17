import { test } from "node:test";
import assert from "node:assert/strict";
import { createVoiceRecovery } from "../src/voice-recovery.js";

function fixture() {
    const track = { id: "old-browser-id", readyState: "live" };
    const transceiver = { mid: "0", currentDirection: "recvonly", receiver: { track } };
    const pc = { connectionState: "connected", signalingState: "stable", remoteDescription: { sdp: "m=audio 9 UDP/TLS/RTP/SAVPF 111\r\na=mid:0\r\na=sendonly\r\na=msid:noxa-c-new c-new\r\n" }, getTransceivers: () => [transceiver] };
    const state = { pc, activeTabID: "tab", serverGeneration: 1, myChannelID: 9, muted: true, deafened: true };
    const calls = []; let connected = false, clock = 0, packets = 0, timestamp = 0;
    const options = { state, attached: () => connected, repair: () => { calls.push("repair"); connected = true; }, resync: async () => calls.push("resync"), rebuild: async () => calls.push("rebuild"), now: () => clock };
    return { state, pc, transceiver, track, calls, options, healthy: () => { connected = true; }, time: value => { clock = value; }, stats: () => new Map([["a", { id: "a", type: "inbound-rtp", kind: "audio", ssrc: 1, trackIdentifier: track.id, packetsReceived: ++packets, timestamp: ++timestamp }]]) };
}

test("advancing received audio with missing playback recovers automatically and retains mute/deafen", async () => {
    const f = fixture(), recover = createVoiceRecovery(f.options);
    await recover(f.pc, f.stats()); await recover(f.pc, f.stats()); assert.deepEqual(f.calls, []);
    await recover(f.pc, f.stats()); assert.deepEqual(f.calls, ["repair"]);
    await recover(f.pc, f.stats()); assert.deepEqual(f.calls, ["repair"]);
    assert.equal(f.state.muted, true); assert.equal(f.state.deafened, true);
});

test("healthy silence, repeated stats and a disconnected peer cannot trigger recovery", async () => {
    const f = fixture(), recover = createVoiceRecovery(f.options), frozen = f.stats();
    for (let i=0; i<5; i++) await recover(f.pc, frozen);
    f.healthy(); for (let i=0; i<5; i++) await recover(f.pc, f.stats());
    f.pc.connectionState = "disconnected"; for (let i=0; i<5; i++) await recover(f.pc, f.stats());
    assert.deepEqual(f.calls, []);
});

test("an ended receiver still getting new packets rebuilds only the current peer", async () => {
    const f = fixture(), recover = createVoiceRecovery(f.options); f.track.readyState = "ended";
    for (let i=0; i<3; i++) await recover(f.pc, f.stats());
    assert.deepEqual(f.calls, ["repair", "rebuild"]);
    f.state.pc = {}; for (let i=0; i<5; i++) await recover(f.pc, f.stats());
    assert.deepEqual(f.calls, ["repair", "rebuild"]);
});

test("failed repair resynchronizes SDP and keeps a bounded retry budget", async () => {
    const f = fixture(); f.options.repair = () => f.calls.push("repair");
    const recover = createVoiceRecovery(f.options);
    for (let i=0; i<3; i++) await recover(f.pc, f.stats());
    assert.deepEqual(f.calls, ["repair", "resync", "repair"]);
    for (let i=0; i<5; i++) await recover(f.pc, f.stats()); assert.equal(f.calls.length, 3);
    f.time(2000); await recover(f.pc, f.stats()); assert.equal(f.calls.length, 6);
    f.pc.remoteDescription.sdp = f.pc.remoteDescription.sdp.replace("c-new\r\n", "c-new|screenaudio\r\n");
    f.time(90000); for (let i=0; i<5; i++) await recover(f.pc, f.stats()); assert.equal(f.calls.length, 6);
});

test("inactive, nonreceiving and program-audio MIDs cannot recover stale inbound counters", async () => {
    for (const mode of ["inactive", "remote-recvonly", "sendonly", "stopped", "program"]) {
        const f = fixture(), recover = createVoiceRecovery(f.options);
        if (mode === "inactive" || mode === "remote-recvonly") f.pc.remoteDescription.sdp = f.pc.remoteDescription.sdp.replace("a=sendonly", mode === "inactive" ? "a=inactive" : "a=recvonly");
        if (mode === "sendonly") f.transceiver.currentDirection = "sendonly";
        if (mode === "stopped") f.transceiver.stopped = true;
        if (mode === "program") f.pc.remoteDescription.sdp = f.pc.remoteDescription.sdp.replace("c-new\r\n", "c-new|screenaudio\r\n");
        for (let i=0; i<5; i++) await recover(f.pc, f.stats());
        assert.deepEqual(f.calls, [], mode);
    }
});

test("MID resolves the live receiver when inbound trackIdentifier is stale", async () => {
    const f = fixture(), recover = createVoiceRecovery(f.options);
    for (let i=0; i<3; i++) {
        const stats = f.stats(); Object.assign(stats.get("a"), {mid:"0",trackIdentifier:"departed-id"});
        await recover(f.pc, stats);
    }
    assert.deepEqual(f.calls, ["repair"]);
});

test("moving the same peer to another channel clears old recovery evidence", async () => {
    const f=fixture(),recover=createVoiceRecovery(f.options);
    await recover(f.pc,f.stats()); await recover(f.pc,f.stats());
    f.state.myChannelID++;
    await recover(f.pc,f.stats()); await recover(f.pc,f.stats());
    assert.deepEqual(f.calls,[]);
    await recover(f.pc,f.stats()); assert.deepEqual(f.calls,["repair"]);
});

test("a pending recovery never overlaps or repairs a replaced server session", async () => {
    const f=fixture(); let finish;
    f.options.repair=()=>f.calls.push("repair");
    f.options.resync=()=>{f.calls.push("resync");return new Promise(resolve=>{finish=resolve;});};
    const recover=createVoiceRecovery(f.options);
    await recover(f.pc,f.stats()); await recover(f.pc,f.stats());
    const pending=recover(f.pc,f.stats());
    await recover(f.pc,f.stats());
    assert.deepEqual(f.calls,["repair","resync"]);
    f.state.pc={}; f.state.serverGeneration++;
    finish(); await pending;
    assert.deepEqual(f.calls,["repair","resync"]);
});

test("replacement peers retain the same channel recovery cooldown", async () => {
    const f=fixture();
    f.options.repair=()=>f.calls.push("repair");
    const recover=createVoiceRecovery(f.options);
    for(let i=0;i<3;i++) await recover(f.pc,f.stats());
    assert.equal(f.calls.length,3);
    let peer={...f.pc}; f.state.pc=peer; f.time(1000);
    for(let i=0;i<3;i++) await recover(peer,f.stats());
    assert.equal(f.calls.length,3);
    f.time(2000); await recover(peer,f.stats()); assert.equal(f.calls.length,6);
    peer={...f.pc}; f.state.pc=peer; f.time(6000);
    for(let i=0;i<3;i++) await recover(peer,f.stats());
    assert.equal(f.calls.length,6);
    f.time(7000); await recover(peer,f.stats()); assert.equal(f.calls.length,9);
});

// A (listener, speaker) path that the server reports as active but that
// delivers nothing here is the "one person cannot hear one other" failure.
function speakerFixture() {
    const f = fixture();
    f.healthy();
    f.state.myClientID = "me";
    f.state.clients = [
        { client_id: "me", channel_id: 9, is_speaking: false },
        { client_id: "c-new", channel_id: 9, is_speaking: false },
        { client_id: "c-missing", channel_id: 9, is_speaking: false },
    ];
    let packets = 0, timestamp = 0;
    f.frozenStats = (advance = false) => {
        if (advance) packets += 5;
        return new Map([["a", { id: "a", type: "inbound-rtp", kind: "audio", mid: "0", ssrc: 1, trackIdentifier: f.track.id, packetsReceived: packets, timestamp: ++timestamp }]]);
    };
    return f;
}

test("a speaking member with no receiver triggers one bounded resync", async () => {
    const f = speakerFixture(), recover = createVoiceRecovery(f.options);
    f.state.clients[2].is_speaking = true;
    await recover(f.pc, f.frozenStats(true)); assert.deepEqual(f.calls, []);
    await recover(f.pc, f.frozenStats(true)); assert.deepEqual(f.calls, ["repair", "resync", "repair"]);
    for (let i = 0; i < 4; i++) await recover(f.pc, f.frozenStats(true));
    assert.equal(f.calls.length, 3, "retry budget respected");
});

test("a speaking member whose receiver stopped advancing triggers resync", async () => {
    const f = speakerFixture(), recover = createVoiceRecovery(f.options);
    f.state.clients[1].is_speaking = true;
    await recover(f.pc, f.frozenStats()); // baseline
    await recover(f.pc, f.frozenStats()); assert.deepEqual(f.calls, []);
    await recover(f.pc, f.frozenStats()); assert.deepEqual(f.calls, ["repair", "resync", "repair"]);
});

test("advancing, quiet, whispering, foreign or own speech never triggers resync", async () => {
    for (const mode of ["advancing", "quiet", "whisper", "other-channel", "self"]) {
        const f = speakerFixture(), recover = createVoiceRecovery(f.options);
        const speaker = mode === "self" ? f.state.clients[0] : f.state.clients[1];
        speaker.is_speaking = mode !== "quiet";
        if (mode === "whisper") speaker.is_whispering = true;
        if (mode === "other-channel") speaker.channel_id = 10;
        for (let i = 0; i < 6; i++) await recover(f.pc, f.frozenStats(mode === "advancing"));
        assert.deepEqual(f.calls, [], mode);
    }
});

test("speech after silence resets the evidence instead of firing on stale counters", async () => {
    const f = speakerFixture(), recover = createVoiceRecovery(f.options);
    await recover(f.pc, f.frozenStats()); await recover(f.pc, f.frozenStats());
    f.state.clients[1].is_speaking = true;
    await recover(f.pc, f.frozenStats(true));
    await recover(f.pc, f.frozenStats(true));
    f.state.clients[1].is_speaking = false;
    await recover(f.pc, f.frozenStats());
    f.state.clients[1].is_speaking = true;
    await recover(f.pc, f.frozenStats(true));
    assert.deepEqual(f.calls, []);
});
