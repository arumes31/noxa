// When an installed app update is applied: at once outside voice, after
// leaving voice otherwise.
import test from "node:test";
import assert from "node:assert/strict";

import { updatePolicy } from "../src/core/update-policy.js";

function setup(voice) {
    const state = { voice, reloads: 0 };
    const policy = updatePolicy({ inVoice: () => state.voice, reload: () => { state.reloads++; } });
    return { state, policy };
}

test("outside voice an installed update reloads right away", () => {
    const { state, policy } = setup(false);
    assert.equal(policy.ready(), true);
    assert.equal(state.reloads, 1);
});

test("in voice the update waits and reloads once the user leaves voice", () => {
    const { state, policy } = setup(true);
    assert.equal(policy.ready(), false);
    assert.equal(policy.check(), false, "still in voice");
    assert.equal(state.reloads, 0);
    state.voice = false;
    assert.equal(policy.check(), true);
    assert.equal(state.reloads, 1);
    assert.equal(policy.check(), false, "reloads only once");
    assert.equal(state.reloads, 1);
});

test("voice changes without an installed update never reload", () => {
    const { state, policy } = setup(false);
    assert.equal(policy.check(), false);
    assert.equal(state.reloads, 0);
});
