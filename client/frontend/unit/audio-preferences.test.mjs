import test from "node:test";
import assert from "node:assert/strict";
import * as audio from "../src/audio.js";

test("audio preference mutations preserve each other and enforce blocks", async () => {
    let persisted = { user_volumes: {}, muted_users: [], blocked_users: [] };
    let failure = "";
    globalThis.window = { __noxa: { state: { settings: structuredClone(persisted) } }, go: { main: { App: {
        SaveSettings: async value => { await new Promise(resolve => setTimeout(resolve, 5)); if (failure) return failure; persisted = structuredClone(value); return ""; },
        GetSettings: async () => structuredClone(persisted),
    } } } };
    const gain = { gain: { value: 1 } }, mute = { gain: { value: 1 } };
    audio.registerUserChain("alice", gain, mute);
    await Promise.all([audio.setUserVolume("alice", 50), audio.setUserVolume("bob", 150)]);
    assert.deepEqual(persisted.user_volumes, { alice: 50, bob: 150 });
    await Promise.all([audio.setUserMuted("alice", true), audio.setUserMuted("bob", true)]);
    assert.deepEqual(new Set(persisted.muted_users), new Set(["alice", "bob"]));
    await audio.setUserMuted("alice", false);
    await audio.setUserBlocked("alice", true);
    assert.equal(gain.gain.value, 0);
    assert.equal(mute.gain.value, 0);
    audio.unregisterUserChain("alice");
    audio.registerUserChain("alice", gain, mute);
    assert.equal(mute.gain.value, 0, "new streams honor the block");
    await audio.setUserBlocked("alice", false);
    assert.equal(gain.gain.value, .5);
    failure = "disk unavailable";
    await assert.rejects(audio.setUserMuted("alice", true), /disk unavailable/);
    await assert.rejects(audio.setUserBlocked("alice", true), /disk unavailable/);
    assert.equal(audio.isUserMuted("alice"), false);
    assert.equal(gain.gain.value, .5);
    failure = "";
    await audio.setUserMuted("alice", true);
    assert.equal(gain.gain.value, 0, "a failed mutation does not poison the queue");
    audio.unregisterUserChain("alice");
});
