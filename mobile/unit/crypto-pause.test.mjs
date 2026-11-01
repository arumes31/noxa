import assert from "node:assert/strict";
import { test } from "node:test";
import { pauseCrypto } from "../tests/crypto-pause.mjs";

test("crypto pause releases concurrent and captured late calls with one gate", { timeout: 2000 }, async () => {
    const calls = [], subtle = { async encrypt(value) { calls.push(value); return value; } };
    let keyReady, paused = 0;
    const key = new Promise(resolve => { keyReady = resolve; });
    const release = pauseCrypto(subtle, "encrypt", () => paused++);
    const captured = subtle.encrypt;
    const late = (async () => captured(await key))();
    const first = subtle.encrypt("first"), second = subtle.encrypt("second");
    assert.equal(paused, 2); assert.deepEqual(calls, []);
    release(); release(); keyReady("late");
    assert.deepEqual(await Promise.all([first, second, late, captured("after restoration")]), ["first", "second", "late", "after restoration"]);
    assert.equal(await subtle.encrypt("native"), "native");
    assert.deepEqual(calls.sort(), ["after restoration", "first", "late", "native", "second"]);
});

test("releasing a crypto pause cannot replace a newer method owner", async () => {
    const subtle = { async encrypt(value) { return value; } };
    const release = pauseCrypto(subtle, "encrypt"), captured = subtle.encrypt;
    const replacement = async () => "replacement";
    subtle.encrypt = replacement; release();
    assert.equal(subtle.encrypt, replacement);
    assert.equal(await captured("held"), "held");
});
