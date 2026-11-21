// Review P2-2: the PWA never signs raw server bytes at login, so a malicious
// server cannot obtain its signature over an approval or other transcript.
import test from "node:test";
import assert from "node:assert/strict";
import nacl from "tweetnacl";
import { readFileSync } from "node:fs";
import { fromBase64, generateIdentity, sign, toBase64, utf8 } from "../src/core/crypto.js";
import { LOGIN_CHALLENGE_DOMAIN, LOGIN_CHALLENGE_SCHEME, loginChallengeMessage, pageServerBinding, signIn } from "../src/core/device.js";
import { classifySignIn } from "../src/core/login-errors.js";

// Node has no page: the tests name the server like a page at this origin would.
const serverBinding = "origin:https://chat.example";

function fakeConn(challenge, scheme = LOGIN_CHALLENGE_SCHEME) {
    const sent = [];
    return { sent, async request(type, payload) {
        sent.push(payload);
        if (sent.length === 1) return { challenge: toBase64(challenge), ...(scheme ? { scheme } : {}) };
        return { ok: true, unique_id: "u", device_id: "d" };
    } };
}

test("forged transcripts as login challenges are refused", async () => {
    const identity = generateIdentity();
    for (const forged of [utf8("noxa-device-approval-v1\nalice\ndesk\npem\nx"), utf8("noxa-" + "z".repeat(27)), new Uint8Array(31), new Uint8Array(64)]) {
        const conn = fakeConn(forged);
        await assert.rejects(signIn(conn, identity, { version: "1", serverBinding }), e => e.code === "unsafe_challenge");
        assert.equal(conn.sent.length, 1, "no signature is sent");
    }
});

test("a normal login signs only the domain-separated challenge", async () => {
    const identity = generateIdentity();
    const challenge = nacl.randomBytes(32);
    challenge[0] = 0x41;
    const conn = fakeConn(challenge);
    await signIn(conn, identity, { version: "1", serverBinding });
    const signature = Uint8Array.from(atob(conn.sent[1].signature), c => c.charCodeAt(0));
    const message = new Uint8Array([...utf8(`${LOGIN_CHALLENGE_DOMAIN}${serverBinding}\n`), ...challenge]);
    assert.equal(nacl.sign.detached.verify(message, signature, identity.edPublic), true);
    assert.equal(nacl.sign.detached.verify(challenge, signature, identity.edPublic), false, "never a raw signature");
    assert.ok(conn.sent[0].client_features.includes("login-bound-v2"), "the login announces the bound proof");
});

test("a login proof names the page's own origin and verifies for no other server", async () => {
    const identity = generateIdentity();
    const challenge = nacl.randomBytes(32);
    challenge[0] = 0x41;
    assert.equal(pageServerBinding({ origin: "https://chat.example" }), serverBinding);
    assert.equal(pageServerBinding({ origin: "null" }), "");
    assert.equal(pageServerBinding(undefined), "");
    const conn = fakeConn(challenge);
    await signIn(conn, identity, { version: "1", serverBinding });
    const signature = Uint8Array.from(atob(conn.sent[1].signature), c => c.charCodeAt(0));
    for (const other of ["origin:https://other.example", `tls-sha256:${"ab".repeat(32)}`]) {
        assert.equal(nacl.sign.detached.verify(loginChallengeMessage(other, challenge), signature, identity.edPublic), false, other);
    }
    const unbound = new Uint8Array([...utf8("noxa-auth-challenge-v1\n"), ...challenge]);
    assert.equal(nacl.sign.detached.verify(unbound, signature, identity.edPublic), false, "not the earlier unbound form");
    // Without a known origin nothing is signed (and nothing is sent).
    for (const missing of ["", "a\nb", undefined]) {
        const none = fakeConn(challenge);
        await assert.rejects(signIn(none, identity, { version: "1", ...(missing === undefined ? {} : { serverBinding: missing }) }), e => e.code === "no_server_binding");
        assert.equal(none.sent.length, 1, "no signature is sent");
    }
});

test("the bound login message and signature match the Go implementation", () => {
    const fixture = JSON.parse(readFileSync(new URL("./fixtures/interop.json", import.meta.url), "utf8"));
    const signing = nacl.sign.keyPair.fromSeed(fromBase64(fixture.ed25519_seed));
    const message = loginChallengeMessage(fixture.login_binding, fromBase64(fixture.challenge));
    assert.equal(new TextDecoder().decode(message), `noxa-auth-challenge-v2\n${fixture.login_binding}\n${new TextDecoder().decode(fromBase64(fixture.challenge))}`);
    assert.equal(toBase64(sign(message, signing.secretKey)), fixture.login_signature);
});

test("an older server (no advertised scheme) gets no signature and a 'server needs an update' error", async () => {
    const identity = generateIdentity();
    const challenge = nacl.randomBytes(32);
    challenge[0] = 0x41;
    const conn = fakeConn(challenge, "");
    await assert.rejects(signIn(conn, identity, { version: "1", serverBinding }), e => e.kind === "serverOutdated" && classifySignIn(e).kind === "serverOutdated");
    assert.equal(conn.sent.length, 1, "no signature is sent");
    // Neither does a server that still offers the unbound scheme.
    const unbound = fakeConn(challenge, "noxa-auth-challenge-v1");
    await assert.rejects(signIn(unbound, identity, { version: "1", serverBinding }), e => e.kind === "serverOutdated");
    assert.equal(unbound.sent.length, 1, "no signature is sent");
});

test("the app login announces resume and presents its token; worker logins do neither", async () => {
    const identity = generateIdentity();
    const challenge = nacl.randomBytes(32);
    challenge[0] = 0x41;
    const app = fakeConn(challenge);
    await signIn(app, identity, { version: "1", goodbye: true, resumeToken: "tok", serverBinding });
    assert.equal(app.sent[0].resume_token, "tok");
    assert.ok(app.sent[0].client_features.includes("resume-v1"));
    const worker = fakeConn(challenge);
    await signIn(worker, identity, { version: "1", serverBinding });
    assert.equal(worker.sent[0].resume_token, undefined);
    assert.ok(!worker.sent[0].client_features.includes("resume-v1"));
});
