import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { describe, it } from "node:test";
import nacl from "tweetnacl";

import {
    fromBase64, keyFingerprint, linkClaimTranscript, linkProof, linkSAS, normalizeLinkCode, openDM, openScope,
    openSealed, publicKeyPEM, sealDM, sealScope, sign, toBase64, toBase64URL, fromBase64URL, uniqueIDFromPublicKey, utf8,
} from "../src/core/crypto.js";
import { encodeFrame, FrameReader, Msg } from "../src/core/frame.js";

const fixture = JSON.parse(await readFile(new URL("./fixtures/interop.json", import.meta.url), "utf8"));

describe("Go interop", () => {
    const signing = nacl.sign.keyPair.fromSeed(fromBase64(fixture.ed25519_seed));

    it("encodes identity keys, unique IDs and fingerprints like Go", () => {
        const pem = publicKeyPEM(signing.publicKey);
        assert.equal(pem, fixture.public_key_pem);
        assert.equal(uniqueIDFromPublicKey(signing.publicKey), fixture.unique_id);
        assert.equal(keyFingerprint(pem), fixture.fingerprint);
    });

    it("signs challenges the server verifies", () => {
        assert.equal(toBase64(sign(fromBase64(fixture.challenge), signing.secretKey)), fixture.signature);
    });

    it("derives link proofs and comparison codes like Go", () => {
        const transcript = linkClaimTranscript(fixture.link_id, fixture.public_key_pem, fixture.x25519_public);
        assert.equal(toBase64(linkProof(fromBase64(fixture.link_secret), transcript)), fixture.link_proof);
        assert.equal(linkSAS(fromBase64(fixture.link_secret), transcript), fixture.link_sas);
        assert.equal(linkSAS(utf8(normalizeLinkCode("k7qm-4xpa")), transcript), fixture.code_sas);
    });

    it("opens sealed scope keys and channel bodies from Go", () => {
        const key = openSealed(fromBase64(fixture.sealed_scope_key), fromBase64(fixture.x25519_public), fromBase64(fixture.x25519_secret));
        assert.deepEqual(key, fromBase64(fixture.scope_key));
        assert.equal(openScope(fixture.scope_blob, key), fixture.scope_text);
        assert.equal(openScope(sealScope("round trip", key), key), "round trip");
        assert.equal(openScope(fixture.scope_blob, new Uint8Array(32)), null);
    });

    it("opens and seals direct messages compatible with NaCl box", () => {
        const peer = fromBase64(fixture.peer_x25519_public);
        const mine = fromBase64(fixture.x25519_secret);
        assert.equal(openDM(fixture.dm_blob_from_peer, peer, mine), fixture.dm_text);
        const reply = sealDM("hi back", peer, mine);
        assert.equal(openDM(reply, fromBase64(fixture.x25519_public), fromBase64(fixture.peer_x25519_secret)), "hi back");
    });

    it("round-trips base64url", () => {
        const bytes = new Uint8Array([251, 255, 0, 1, 2]);
        assert.equal(toBase64URL(bytes), "-_8AAQI");
        assert.deepEqual(fromBase64URL("-_8AAQI"), bytes);
    });
});

describe("frames", () => {
    it("encodes the Go wire format", () => {
        const frame = encodeFrame(Msg.Ping, {});
        assert.deepEqual(Array.from(frame), [0, 0, 0, 4, 0, 8, 123, 125]);
    });

    it("reassembles frames split and merged across messages", () => {
        const a = encodeFrame(Msg.Pong, { n: 1 });
        const b = encodeFrame(Msg.Event, { type: "x", data: { ok: true } });
        const stream = new Uint8Array(a.length + b.length);
        stream.set(a);
        stream.set(b, a.length);
        const reader = new FrameReader();
        assert.deepEqual(reader.push(stream.subarray(0, 3)), []);
        assert.deepEqual(reader.push(stream.subarray(3, a.length + 5)), [{ type: Msg.Pong, payload: { n: 1 } }]);
        assert.deepEqual(reader.push(stream.subarray(a.length + 5)), [{ type: Msg.Event, payload: { type: "x", data: { ok: true } } }]);
    });

    it("rejects oversized declarations", () => {
        const reader = new FrameReader();
        assert.throws(() => reader.push(new Uint8Array([0x7f, 0, 0, 0, 0, 1])), /invalid frame length/);
    });
});
