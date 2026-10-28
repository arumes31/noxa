// 1c-iii-a item 2: peer pins advance only through the signed reset chain
// verified from the cached pin (mirrors internal/e2ee/olmdir VerifyResetChain).
import test from "node:test";
import assert from "node:assert/strict";
import { generateIdentity, toBase64 } from "../src/core/crypto.js";
import { signReset, verifyResetChain } from "../src/core/olm-reset.js";

const server = "tls-sha256:" + "ab".repeat(32);
const raw = b => toBase64(b).replace(/=+$/, "");
const keys = () => { const i = generateIdentity(); return { identity: raw(i.xPublic), signing: raw(i.edPublic) }; };

function chain(noxa, n) {
    const pins = [{ epoch: 1, ...keys() }], resets = [];
    for (let i = 0; i < n; i++) {
        const cur = pins.at(-1), next = { epoch: cur.epoch + 1, ...keys() };
        resets.push(signReset({ version: "noxa-olm-reset-v1", server, principal: "dave", device: "dave-phone", transition_id: "t" + i,
            old_epoch: cur.epoch, old_identity: cur.identity, old_signing: cur.signing, new_epoch: next.epoch, new_identity: next.identity, new_signing: next.signing,
            generation: 2, publication_digest: "cd".repeat(32), reason: "lost" }, noxa.edSecret));
        pins.push(next);
    }
    return { pins, resets };
}
const pager = resets => async after => resets.filter(r => r.old_epoch >= after).slice(0, 32);
const ctx = (noxa, resets) => ({ server, principal: "dave", device: "dave-phone", noxaSigning: noxa.edPublic, fetch: pager(resets) });

test("a multi-page signed chain promotes the cached pin", async () => {
    const noxa = generateIdentity(), { pins, resets } = chain(noxa, 40);
    const { pin, passed } = await verifyResetChain(pins[0], pins[40], ctx(noxa, resets));
    assert.deepEqual(pin, pins[40]);
    assert.equal(passed.length, 40);
    assert.deepEqual(passed[0], pins[0]);
});

test("gaps, other signers, independent pins and wrong endpoints never promote", async () => {
    const noxa = generateIdentity(), other = generateIdentity(), { pins, resets } = chain(noxa, 6);
    const refuse = (from, to, c) => assert.rejects(verifyResetChain(from, to, c));
    await refuse(pins[0], pins[5], ctx(other, resets));
    await refuse(pins[0], pins[5], ctx(noxa, [...resets.slice(0, 2), ...resets.slice(3)]));
    await refuse(pins[0], { epoch: 6, identity: pins[1].identity }, ctx(noxa, resets));
    await refuse(pins[0], { ...pins[6], signing: pins[1].signing }, ctx(noxa, resets));
    await refuse(pins[5], pins[5], ctx(noxa, resets));
    await refuse(pins[5], pins[2], ctx(noxa, resets));
    await refuse({ ...pins[0], identity: keys().identity }, pins[3], ctx(noxa, resets));
    await refuse(pins[0], pins[3], ctx(noxa, [signReset(resets[0], other.edSecret), ...resets.slice(1)]));
    await refuse(pins[0], pins[3], ctx(noxa, [resets[0], signReset({ ...resets[1], device: "eve-phone" }, noxa.edSecret), ...resets.slice(2)]));
    await refuse(pins[0], pins[3], { ...ctx(noxa, resets), fetch: async () => ({ not: "a list" }) });
});
