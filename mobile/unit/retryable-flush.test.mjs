// Review b8 P3-1 (adopted repro): a DM that keeps failing with a retryable
// code other than a lost connection or a rate limit (for example the server's
// Olm backend answering "unavailable") stays queued but never stops the
// flush, so channel entries queued behind it are still sent. A rate limit or
// a closed connection still stops the flush (both cover every entry).
import test from "node:test";
import assert from "node:assert/strict";
import { installFakeIndexedDB } from "./fake-idb.mjs";
installFakeIndexedDB();
const { enqueue, flush, pending } = await import("../src/core/outbox.js");
const { openDB, run, Stores, putSealed } = await import("../src/core/db.js");
await putSealed(Stores.device, "warmup", { ok: true });
const raw = async (store, op) => { const db = await openDB(); try { return await run(db, store, "readwrite", op); } finally { db.close(); } };
const tick = () => new Promise(r => setTimeout(r, 5));
await raw(Stores.device, s => s.put({ generation: 1, active: true }, "identity-fence"));

async function scenario(failure, conn = { open: true }) {
    await raw(Stores.outbox, s => s.clear());
    const channelSends = [];
    const sess = { conn, showQueued() {}, async send(scope, text) { channelSends.push(text); } };
    await enqueue({ kind: "dm", peer: "bob" }, "dm first", { id: "op-dm" });
    await tick();
    await enqueue({ kind: "channel", scope: 1 }, "channel second", { id: "op-ch" });
    let attempts = 0;
    const dm = { ratchet: {}, showQueued() {}, async send() { attempts++; throw failure(); } };
    for (let i = 0; i < 3; i++) await flush(sess, dm, null);
    const left = Object.fromEntries((await pending()).map(e => [e.id, e]));
    return { channelSends, left, attempts };
}

test("b8 P3-1: a persistent retryable DM failure does not starve channel entries", async () => {
    const { channelSends, left } = await scenario(() => Object.assign(new Error("secure send failed: unavailable"), { code: "unavailable", retryable: true }));
    assert.deepEqual(channelSends, ["channel second"], "channel entry behind the failing DM is sent");
    assert.deepEqual(Object.keys(left), ["op-dm"], "the DM stays queued");
    assert.equal(left["op-dm"].failed, undefined, "a retryable failure never marks the entry failed");
});

test("b8 P3-1: a committed DM attempt that keeps failing stays queued with its committed text", async () => {
    const { channelSends, left } = await scenario(() => Object.assign(new Error("secure send failed: uncertain"), {
        code: "uncertain", retryable: true, committed: { record: { body: { text: "dm first" } } } }));
    assert.deepEqual(channelSends, ["channel second"]);
    assert.equal(left["op-dm"].failed, undefined);
});

test("b8 P3-1: a rate limit still stops the flush", async () => {
    const { channelSends, left } = await scenario(() => Object.assign(new Error("secure send failed: rate-limit"), { code: "rate-limit", retryable: true }));
    assert.deepEqual(channelSends, [], "a connection-wide rate limit stops the flush");
    assert.deepEqual(Object.keys(left).sort(), ["op-ch", "op-dm"]);
});

test("b8 P3-1: a lost connection still stops the flush", async () => {
    const conn = { open: true };
    const { channelSends, left } = await scenario(() => { conn.open = false; return Object.assign(new Error("connection closed"), { retryable: true }); }, conn);
    assert.deepEqual(channelSends, [], "nothing more is sent on a closed connection");
    assert.deepEqual(Object.keys(left).sort(), ["op-ch", "op-dm"]);
});
