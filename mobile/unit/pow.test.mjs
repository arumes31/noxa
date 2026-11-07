import assert from "node:assert/strict";
import test from "node:test";
import { MAX_POW_DIFFICULTY, POW_ALGORITHM, challengePrefix, digestZeroBits, powFirstWord, searchNonces, solvePow } from "../src/core/pow.js";
import { noteShutdownJitter, takeShutdownJitterMs } from "../src/core/reconnect-hint.js";
import { classifySignIn, signInError } from "../src/core/login-errors.js";

const prefix = new TextEncoder().encode("0123456789abcdef");
const b64 = bytes => Buffer.from(bytes).toString("base64");

test("the single-block SHA-256 matches the Go vector (internal/netproto/pow_test.go)", () => {
    // SHA-256("noxa-pow-v1" || prefix || 0x0000010203040506) = 0d1e5939...
    assert.equal(powFirstWord(prefix, 0x0000010203040506), 0x0d1e5939);
    assert.equal(Math.clz32(powFirstWord(prefix, 0x0000010203040506)), 4);
});

test("found nonces really have the requested zero bits (checked with WebCrypto)", async () => {
    for (const difficulty of [4, 10, 14]) {
        const nonce = searchNonces(prefix, difficulty, 0, 1 << 20);
        assert.ok(nonce >= 0, `no solution for ${difficulty}`);
        assert.ok(await digestZeroBits(prefix, nonce) >= difficulty);
        assert.equal(searchNonces(prefix, difficulty, 0, nonce), -1, "the first solution is returned");
    }
});

test("solvePow solves valid challenges and refuses malformed or oversized ones", async () => {
    const nonce = await solvePow({ algorithm: POW_ALGORITHM, prefix: b64(prefix), difficulty: 10 }, { worker: undefined });
    assert.ok(await digestZeroBits(prefix, nonce) >= 10);
    for (const bad of [
        { algorithm: "md5", prefix: b64(prefix), difficulty: 4 },
        { algorithm: POW_ALGORITHM, prefix: b64(prefix.slice(0, 8)), difficulty: 4 },
        { algorithm: POW_ALGORITHM, prefix: b64(prefix), difficulty: MAX_POW_DIFFICULTY + 1 },
        { algorithm: POW_ALGORITHM, prefix: "%%%", difficulty: 4 },
    ]) {
        assert.equal(challengePrefix(bad), null);
        await assert.rejects(solvePow(bad, { worker: undefined }), /unsupported proof of work/);
    }
});

test("a restarting server's delay and the shutdown jitter reach the reconnect logic", () => {
    assert.deepEqual(classifySignIn("server is restarting; try again in 42 seconds"), { kind: "restarting", retryAfter: 42 });
    const error = signInError("server is restarting; try again in 7 seconds");
    assert.equal(error.kind, "restarting");
    assert.equal(error.retryAfter, 7);
    assert.equal(classifySignIn("server is restarting; try again in soon").kind, "unknown");
    assert.equal(classifySignIn("the server is under heavy load; update noXa to connect").kind, "version");
    noteShutdownJitter(20);
    assert.equal(takeShutdownJitterMs(() => 0.5), 10000);
    assert.equal(takeShutdownJitterMs(() => 0.5), 0, "used once");
    noteShutdownJitter(999);
    assert.equal(takeShutdownJitterMs(() => 1), 60000, "bounded to a minute");
});
