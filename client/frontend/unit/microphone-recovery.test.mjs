import assert from "node:assert/strict";
import { test } from "node:test";
import { watchMicrophone, watchAudioOutput } from "../src/microphone-recovery.js";

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
