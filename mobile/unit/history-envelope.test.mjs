// Go<->JS: the Go-encoded deterministic history envelope (internal/netproto
// TestHistoryEnvelopeVectorAndRefusals) decodes and verifies in JS.
import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { ed25519 } from "@noble/curves/ed25519.js";
import { decodeHistoryChunk } from "../src/core/history-envelope.js";

const seed = new Uint8Array(32).fill(7);
const plain = readFileSync(new URL("./fixtures/history-envelope.txt", import.meta.url), "utf8");
const signingKey = ed25519.getPublicKey(seed);

test("a Go-encoded history envelope decodes and verifies in JS, bound to its position", () => {
    const records = decodeHistoryChunk(plain, { source: "desk", target: "phone", seq: 0, total: 1, signingKey });
    assert.equal(records[0].client_msg_id, "R1");
    for (const ctx of [{ source: "other" }, { target: "x" }, { seq: 1, total: 2 }, { signingKey: ed25519.getPublicKey(new Uint8Array(32).fill(8)) }])
        assert.equal(decodeHistoryChunk(plain, { source: "desk", target: "phone", seq: 0, total: 1, signingKey, ...ctx }), null);
});
