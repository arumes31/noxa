// While the server rules await acceptance the server refuses every send;
// the outbox must not attempt (and so never fail) its entries until the
// rules are accepted, when app.js flushes again.
import test from "node:test";
import assert from "node:assert/strict";
import { installFakeIndexedDB } from "./fake-idb.mjs";
installFakeIndexedDB();
const { enqueue, flush, pending } = await import("../src/core/outbox.js");
const { openDB, run, Stores, putSealed } = await import("../src/core/db.js");
await putSealed(Stores.device, "warmup", { ok: true });
const raw = async (store, op) => { const db = await openDB(); try { return await run(db, store, "readwrite", op); } finally { db.close(); } };
await raw(Stores.device, s => s.put({ generation: 1, active: true }, "identity-fence"));

test("entries wait, unattempted and not failed, until the rules are accepted", async () => {
    await raw(Stores.outbox, s => s.clear());
    const sent = [];
    const sess = { conn: { open: true }, pendingRules: { text: "Be kind", hash: "h1" }, showQueued() {}, async send(scope, text) { sent.push(text); } };
    await enqueue({ kind: "channel", scope: 1 }, "hello", { id: "op-ch" });
    assert.equal(await flush(sess, null, null), 0);
    assert.deepEqual(sent, []);
    const [entry] = await pending();
    assert.equal(entry.failed, undefined);
    sess.pendingRules = null;
    assert.equal(await flush(sess, null, null), 1);
    assert.deepEqual(sent, ["hello"]);
});
