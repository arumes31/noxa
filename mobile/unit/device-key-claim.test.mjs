// A device is confirmed only with its own signature over its signing and
// encryption key (key claim): the fingerprint the user compares covers the
// signing key alone, so a listing that pairs the real signing key with another
// encryption key gets no approval, and nothing is sealed to that key.
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import nacl from "tweetnacl";
import { deviceKeyClaimTranscript, fromBase64, generateIdentity, keyClaimRef, keyFingerprint, publicKeyPEM, sign, toBase64 } from "../src/core/crypto.js";
import { LOGIN_CHALLENGE_SCHEME, signIn } from "../src/core/device.js";
import { approvalTranscript, endorseDevice, hasKeyClaim } from "../src/core/devices.js";
import { Msg } from "../src/core/frame.js";
import { verifySignature } from "../src/core/olm-keys.js";

const UID = "uid-1";
const me = generateIdentity(), other = generateIdentity(), relay = generateIdentity();
const pem = publicKeyPEM(other.edPublic);
const claim = (identity, ref, x) => toBase64(sign(deviceKeyClaimTranscript(ref, pem, toBase64(x)), identity.edSecret));
const listed = (x, keyClaim) => ({ device_id: "dev-2", name: "Tablet", kind: "web", ed25519_public_key: pem, fingerprint: keyFingerprint(pem),
    x25519_public_key: toBase64(x), ...(keyClaim ? { key_claim: keyClaim } : {}) });

function session() {
    const requests = [];
    return { requests, uniqueID: UID, deviceID: "dev-1", identity: me,
        conn: { request: async (_type, request) => { requests.push(request); return { action: request.action, device: { confirmed: true } }; } } };
}

test("a device with its own key claim is confirmed over exactly the claimed keys", async () => {
    const s = session();
    await endorseDevice(s, listed(other.xPublic, claim(other, keyClaimRef(UID), other.xPublic)));
    assert.equal(s.requests.length, 1);
    assert.ok(verifySignature(fromBase64(s.requests[0].signature), approvalTranscript(UID, "dev-1", pem, toBase64(other.xPublic)), me.edPublic));
});

test("a listing whose encryption key the device did not sign is refused and nothing is signed", async () => {
    const real = claim(other, keyClaimRef(UID), other.xPublic);
    const cases = {
        "another encryption key with the real claim": listed(relay.xPublic, real),
        "another encryption key claimed by another key": listed(relay.xPublic, claim(relay, keyClaimRef(UID), relay.xPublic)),
        "no claim (a device from before key claims, or a server that drops it)": listed(other.xPublic),
        "a claim made for another account": listed(other.xPublic, claim(other, keyClaimRef("someone-else"), other.xPublic)),
        "a link claim": listed(other.xPublic, claim(other, "link:abc", other.xPublic)),
        "a malformed claim": listed(other.xPublic, "not base64"),
    };
    for (const [name, target] of Object.entries(cases)) {
        const s = session();
        assert.equal(target.fingerprint, keyFingerprint(pem), "the fingerprint comparison alone would pass");
        await assert.rejects(endorseDevice(s, target), /has not proven its encryption key/, name);
        assert.equal(s.requests.length, 0, name);
        assert.equal(hasKeyClaim(UID, target), false, name);
    }
});

test("signing in as a linked device publishes its key claim", async () => {
    const identity = generateIdentity();
    const challenge = nacl.randomBytes(32);
    challenge[0] = 0x41;
    const run = async (options, response = { ok: true, unique_id: UID, device_id: "dev-9" }) => {
        const sent = [];
        const conn = { async request(type, payload) {
            sent.push({ type, payload });
            if (type === Msg.Authenticate) return { challenge: toBase64(challenge), scheme: LOGIN_CHALLENGE_SCHEME };
            if (type === Msg.AuthSignature) return response;
            return { action: payload.action };
        } };
        await signIn(conn, identity, { version: "1", serverBinding: "origin:https://chat.example", ...options });
        return sent.filter(m => m.type === Msg.DeviceRequest).map(m => m.payload);
    };
    const published = await run({ keyClaim: true });
    assert.equal(published.length, 1);
    assert.equal(published[0].action, "key_claim");
    const device = { ed25519_public_key: publicKeyPEM(identity.edPublic), x25519_public_key: toBase64(identity.xPublic), key_claim: published[0].signature };
    assert.equal(hasKeyClaim(UID, device), true);
    assert.equal(hasKeyClaim("someone-else", device), false);
    // Short worker sessions and sessions without a device publish nothing.
    assert.deepEqual(await run({}), []);
    assert.deepEqual(await run({ keyClaim: true }, { ok: true, unique_id: UID }), []);
});

test("the key claim matches the Go implementation", () => {
    const fixture = JSON.parse(readFileSync(new URL("./fixtures/interop.json", import.meta.url), "utf8"));
    const signing = nacl.sign.keyPair.fromSeed(fromBase64(fixture.ed25519_seed));
    const transcript = deviceKeyClaimTranscript(keyClaimRef(fixture.approval_principal), fixture.public_key_pem, fixture.x25519_public);
    assert.equal(new TextDecoder().decode(transcript), `noxa-device-key-claim-v1\ndevice:${fixture.approval_principal}\n${fixture.public_key_pem}\n${fixture.x25519_public}`);
    assert.equal(toBase64(sign(transcript, signing.secretKey)), fixture.device_key_claim_signature);
    assert.equal(hasKeyClaim(fixture.approval_principal, { ed25519_public_key: fixture.public_key_pem, x25519_public_key: fixture.x25519_public, key_claim: fixture.device_key_claim_signature }), true);
});
