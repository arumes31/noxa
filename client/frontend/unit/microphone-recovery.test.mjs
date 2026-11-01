import assert from "node:assert/strict";
import { test } from "node:test";
import { watchMicrophone, watchAudioOutput, createMicrophoneSenderOwner } from "../src/microphone-recovery.js";

function fixture() {
    const track = new EventTarget(); track.readyState = "live";
    track.getSettings = () => ({ deviceId: "headset" }); track.label = "USB headset";
    const devices = new EventTarget();
    devices.enumerateDevices = async () => [{ kind: "audioinput", deviceId: "headset" }];
    return { track, devices };
}
const settle = () => new Promise(resolve => setImmediate(resolve));

test("ended microphone reports once and disposing detaches listeners", () => {
    const { track, devices } = fixture(); let losses = 0;
    const stop = watchMicrophone(track, () => losses++, devices);
    track.dispatchEvent(new Event("ended")); track.dispatchEvent(new Event("ended"));
    assert.equal(losses, 1); stop();
    track.dispatchEvent(new Event("ended")); assert.equal(losses, 1);
});
test("device inventory loss does not request a replacement microphone", async () => {
    const { track, devices } = fixture(); let losses = 0;
    devices.getUserMedia = () => assert.fail("recovery needs an explicit choice");
    const stop = watchMicrophone(track, () => losses++, devices);
    devices.dispatchEvent(new Event("devicechange")); await settle(); assert.equal(losses, 0);
    devices.enumerateDevices = async () => [{ kind: "audioinput", deviceId: "laptop" }];
    devices.dispatchEvent(new Event("devicechange")); await settle(); assert.equal(losses, 1); stop();
});
test("late enumeration from an old session cannot invalidate a new capture", async () => {
    const { track, devices } = fixture(); let finish;
    devices.enumerateDevices = () => new Promise(resolve => { finish = resolve; });
    const stop = watchMicrophone(track, () => assert.fail("disposed capture"), devices);
    devices.dispatchEvent(new Event("devicechange")); stop(); finish([]); await settle();
});
test("failed discovery and empty device identifiers are not device loss", async () => {
    const { track, devices } = fixture();
    devices.enumerateDevices = async () => { throw new Error("discovery unavailable"); };
    const stop = watchMicrophone(track, () => assert.fail("unproven device loss"), devices);
    devices.dispatchEvent(new Event("devicechange")); await settle(); stop();
    track.getSettings = () => ({}); devices.enumerateDevices = async () => [];
    const dispose = watchMicrophone(track, () => assert.fail("unknown device"), devices);
    devices.dispatchEvent(new Event("devicechange")); await settle(); dispose();
});

test("only the latest device inventory can report an output loss", async () => {
    const { devices } = fixture(); let selected = "headset", losses = 0;
    const requests = []; devices.enumerateDevices = () => new Promise(resolve => requests.push(resolve));
    const stop = watchAudioOutput(() => selected, () => losses++, devices);
    devices.dispatchEvent(new Event("devicechange")); devices.dispatchEvent(new Event("devicechange"));
    requests[1]([{ kind: "audiooutput", deviceId: selected }]); await settle();
    requests[0]([]); await settle(); assert.equal(losses, 0);
    devices.enumerateDevices = async () => [];
    devices.dispatchEvent(new Event("devicechange")); await settle();
    devices.dispatchEvent(new Event("devicechange")); await settle(); assert.equal(losses, 1);
    selected = "default"; devices.dispatchEvent(new Event("devicechange")); await settle(); assert.equal(losses, 1);
    stop(); selected = "new-output"; devices.dispatchEvent(new Event("devicechange")); await settle(); assert.equal(losses, 1);
});

test("a default microphone alias cannot silently move capture to a different physical device", async () => {
    const { track, devices } = fixture(); let losses = 0;
    track.getSettings = () => ({ deviceId: "default", groupId: "headset" });
    devices.enumerateDevices = async () => [{ kind: "audioinput", deviceId: "default", groupId: "headset" }];
    const stop = watchMicrophone(track, () => losses++, devices);
    devices.dispatchEvent(new Event("devicechange")); await settle(); assert.equal(losses, 0);
    devices.enumerateDevices = async () => [{ kind: "audioinput", deviceId: "default", groupId: "laptop" }];
    devices.dispatchEvent(new Event("devicechange")); await settle(); assert.equal(losses, 1);
    stop();
});

function senderFixture(track = { id: "first" }) {
    const calls = [], transceiver = { direction: "sendrecv", sender: { track, async replaceTrack(next) { calls.push(next); this.track = next; } } };
    const pc = { addTransceiver() { assert.fail("must reuse the registered microphone, not another transceiver"); } };
    const owner = createMicrophoneSenderOwner(); owner.remember(pc, transceiver);
    return { owner, pc, transceiver, calls, track };
}
const microphoneOptions = { direction: "sendrecv" };
test("repeated microphone retries reuse the same sender and leave other media untouched", async () => {
    const f = senderFixture(), sender = f.transceiver.sender;
    for (let i = 0; i < 3; i++) {
        const previous = sender.track, next = { id: `retry-${i}` };
        await f.owner.detach(f.pc, previous);
        assert.equal(await f.owner.attach(f.pc, next, microphoneOptions, () => true), f.transceiver);
        assert.equal(f.transceiver.sender, sender); assert.equal(sender.track, next);
    }
    assert.equal(f.calls.length, 6);
});
test("a delayed microphone detach finishes before replacement and cannot clear the new source", async () => {
    const f = senderFixture(); let finish;
    f.transceiver.sender.replaceTrack = next => new Promise(resolve => {
        if (next === null) finish = () => { f.transceiver.sender.track = null; resolve(); };
        else { f.transceiver.sender.track = next; resolve(); }
    });
    const detached = f.owner.detach(f.pc, f.track), next = { id: "new" };
    const attached = f.owner.attach(f.pc, next, microphoneOptions, () => true);
    await settle(); assert.equal(f.transceiver.sender.track, f.track);
    finish(); await detached; await attached;
    assert.equal(await f.owner.detach(f.pc, f.track), false);
    assert.equal(f.transceiver.sender.track, next);
});
test("a stale session cannot attach to the retained microphone slot", async () => {
    const f = senderFixture();
    await assert.rejects(f.owner.attach(f.pc, {}, microphoneOptions, () => false), { name: "AbortError" });
    assert.equal(f.calls.length, 0); assert.equal(f.transceiver.sender.track, f.track);
});
test("session replacement during replaceTrack removes only that stale source", async () => {
    const f = senderFixture(); let current = true;
    f.transceiver.sender.replaceTrack = async next => { f.transceiver.sender.track = next; current = false; };
    await assert.rejects(f.owner.attach(f.pc, {}, microphoneOptions, () => current), { name: "AbortError" });
    assert.equal(f.transceiver.sender.track, null);
    const next = { id: "next" }; current = true;
    f.transceiver.sender.replaceTrack = async t => { f.transceiver.sender.track = t; };
    await f.owner.attach(f.pc, next, microphoneOptions, () => current);
    assert.equal(f.transceiver.sender.track, next);
});
test("initial receive-only microphone is promoted without adding a new transceiver", async () => {
    const f = senderFixture(null); f.transceiver.direction = "recvonly";
    const next = { id: "first-capture" };
    await f.owner.attach(f.pc, next, microphoneOptions, () => true);
    assert.equal(f.transceiver.direction, "sendrecv"); assert.equal(f.transceiver.sender.track, next);
});
test("sender ownership does not leak across peer replacement or failed operations", async () => {
    const f = senderFixture(), next = { id: "new-peer" };
    const transceiver = { sender: { track: next }, direction: "sendrecv" };
    let added = 0;
    const pc = { addTransceiver(track, options) { added++; assert.equal(track, next); assert.equal(options, microphoneOptions); return transceiver; } };
    await f.owner.attach(pc, next, microphoneOptions, () => true);
    assert.equal(added, 1); assert.equal(f.transceiver.sender.track, f.track);
    f.transceiver.sender.replaceTrack = async () => { throw Error("closed"); };
    await assert.rejects(f.owner.detach(f.pc, f.track), /closed/);
    f.transceiver.sender.replaceTrack = async t => { f.transceiver.sender.track = t; };
    await f.owner.attach(f.pc, next, microphoneOptions, () => true);
    assert.equal(f.transceiver.sender.track, next);
});

test("profile updates share the microphone owner queue and cannot claim another sender", async () => {
    const f = senderFixture(), next = { id: "processed-profile" };
    await f.owner.replace(f.pc, f.transceiver.sender, next, () => true);
    assert.equal(await f.owner.detach(f.pc, next), true);
    assert.equal(f.transceiver.sender.track, null);
    await assert.rejects(f.owner.replace(f.pc, { track: null }, next, () => true), /sender changed/);
    assert.equal(f.transceiver.sender.track, null);
});
