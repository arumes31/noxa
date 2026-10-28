// Review P2-2: the PWA never signs raw server bytes at login, so a malicious
// server cannot obtain its signature over an approval or other transcript.
import test from "node:test";
import assert from "node:assert/strict";
import nacl from "tweetnacl";
import { generateIdentity, toBase64, utf8 } from "../src/core/crypto.js";
import { LOGIN_CHALLENGE_DOMAIN, signIn } from "../src/core/device.js";

function fakeConn(challenge) {
    const sent = [];
    return { sent, async request(type, payload) {
        sent.push(payload);
        if (sent.length === 1) return { challenge: toBase64(challenge) };
        return { ok: true, unique_id: "u", device_id: "d" };
    } };
}

test("forged transcripts as login challenges are refused", async () => {
    const identity = generateIdentity();
    for (const forged of [utf8("noxa-device-approval-v1\nalice\ndesk\npem\nx"), utf8("noxa-" + "z".repeat(27)), new Uint8Array(31), new Uint8Array(64)]) {
        const conn = fakeConn(forged);
        await assert.rejects(signIn(conn, identity, { version: "1" }), e => e.code === "unsafe_challenge");
        assert.equal(conn.sent.length, 1, "no signature is sent");
    }
});

test("a normal login signs only the domain-separated challenge", async () => {
    const identity = generateIdentity();
    const challenge = nacl.randomBytes(32);
    challenge[0] = 0x41;
    const conn = fakeConn(challenge);
    await signIn(conn, identity, { version: "1" });
    const signature = Uint8Array.from(atob(conn.sent[1].signature), c => c.charCodeAt(0));
    const message = new Uint8Array([...utf8(LOGIN_CHALLENGE_DOMAIN), ...challenge]);
    assert.equal(nacl.sign.detached.verify(message, signature, identity.edPublic), true);
    assert.equal(nacl.sign.detached.verify(challenge, signature, identity.edPublic), false, "never a raw signature");
});
