// F4 (PWA): one-time keys are refilled as soon as the server reports few
// claimable keys, never past the Olm account's 100 private keys, and not at
// all when the server does not report the count (older servers).
import test from "node:test";
import assert from "node:assert/strict";
import { RatchetConsumer } from "../src/core/ratchet-consumer.js";
import { generateIdentity, toBase64 } from "../src/core/crypto.js";

const server = "tls-sha256:" + "c".repeat(64);
const raw = b => toBase64(b).replace(/=+$/, "");
const enc = new TextEncoder();
const rk = (kind, id) => JSON.stringify([kind, id]);

function harness({ available, open = 0 }) {
    const records = new Map();
    const put = (kind, id, value) => records.set(rk(kind, id), { revision: 1, data: enc.encode(JSON.stringify(value)) });
    for (let i = 0; i < open; i++) put("obligation", `local:old${i}`, { account_id: "local", expires_at: Math.floor(Date.now() / 1000) + 3600, key_id: `old${i}` });
    const noxa = generateIdentity(), olm = generateIdentity();
    const store = {
        async read(refs = []) { return { records: Object.fromEntries(refs.map(([k, id]) => [rk(k, id), records.get(rk(k, id)) || { revision: 0 }])) }; },
        async accountAuthority() { return { accountID: "local", epoch: 1, blocked: false }; },
        async checkAccountAuthority() {},
        async pendingRefs(kind, after) { return [...records.keys()].map(k => JSON.parse(k)).filter(([k, id]) => k === kind && id > after).sort((x, y) => (x[1] < y[1] ? -1 : 1)); },
        coreKey() { return new Uint8Array(32); },
        async commitPublication(s, id, account, payload, otks) {
            records.set(rk("account", "local"), { revision: 1, data: account });
            records.set(rk("publication", id), { revision: 1, data: enc.encode(JSON.stringify({ payload: [...payload] })) });
            for (const [oid, o] of Object.entries(otks)) put("obligation", oid, o);
        },
    };
    let n = 0;
    const generated = [];
    const core = { async apply(r) {
        assert.ok(r.op === "new_account" || r.op === "prekeys");
        generated.push(r.count);
        const one_time_keys = Array.from({ length: r.count }, () => ({ id: "k" + ++n, key: raw(generateIdentity().xPublic) }));
        return { account: "acct-" + n, bundle: { identity: raw(olm.xPublic), signing: raw(olm.edPublic), one_time_keys } };
    } };
    const published = [];
    const directory = async r => {
        assert.equal(r.action, "publish");
        published.push(r.bundle.generation);
        return { request_id: r.request_id, action: r.action, ...(available === undefined ? {} : { available }) };
    };
    const c = new RatchetConsumer({ store, core, namespace: { server, principal: "alice", device: "a1" }, admission: async () => [], directory,
        deviceKey: async () => noxa.edPublic, signBundle: async b => b, assertCurrent: () => {} });
    return { c, generated, published };
}

test("a busy device refills when the server reports few claimable keys", async () => {
    const { c, generated, published } = harness({ available: 3 });
    await c.ready();
    assert.deepEqual(generated, [20, 10], "the new account, then one refill");
    assert.deepEqual(published, [1, 2], "the refill is published as the next generation");
});

test("no refill while enough keys are claimable or the count is unknown", async () => {
    for (const available of [10, undefined]) {
        const { c, generated, published } = harness({ available });
        await c.ready();
        assert.deepEqual(generated, [20], `available=${available}`);
        assert.deepEqual(published, [1]);
    }
});

test("no refill that would push the account past 100 private keys", async () => {
    const { c, generated, published } = harness({ available: 0, open: 75 });
    await c.ready();
    assert.deepEqual(generated, [20], "75 old + 20 open keys leave no room for 10 more");
    assert.deepEqual(published, [1]);
});
