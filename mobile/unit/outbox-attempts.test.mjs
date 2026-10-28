// 1c-iii-a item 4: outbox draft/attempt split. Real outbox.js + db.js over the
// fake IndexedDB: replaced drafts never overwrite a committed attempt, expiry
// never drops one, pending() failures never lose or replay entries, and a
// flush resumed after logout neither sends nor writes back.
import test from "node:test";
import assert from "node:assert/strict";
import { installFakeIndexedDB } from "./fake-idb.mjs";

installFakeIndexedDB();
const { enqueue, flush, pending, remove, waitingCount, OUTBOX_TTL } = await import("../src/core/outbox.js");
const { openDB, run, Stores } = await import("../src/core/db.js");

const raw = async (store, op) => { const db = await openDB(); try { return await run(db, store, "readwrite", op); } finally { db.close(); } };
const setFence = generation => raw(Stores.device, s => s.put({ generation, active: true }, "identity-fence"));
async function clear() { for (const e of await pending()) await remove(e.id); }
const session = { conn: { open: true }, send: async () => {}, showQueued() {} };
const uncertain = text => Object.assign(new Error("ratchet: uncertain"), { code: "uncertain", retryable: true, committed: { record: { body: { text } }, receipt: null } });

function ratchetDM(behaviour) {
    const sent = [];
    return { sent, ratchet: {}, showQueued() {}, send: async (peer, text, nick, { id }) => { sent.push([id, text]); return behaviour(id, text); } };
}

test("a replaced draft never overwrites a committed attempt", async () => {
    await setFence(1); await clear();
    await enqueue({ kind: "dm", peer: "bob" }, "draft A", { id: "op-1" });
    await enqueue({ kind: "dm", peer: "bob" }, "draft B", { id: "op-1" }); // editable before commit
    assert.equal((await pending())[0].text, "draft B");
    const dm = ratchetDM(() => { throw uncertain("draft B"); });
    assert.equal(await flush(session, dm), 0);
    let [entry] = await pending();
    assert.deepEqual(entry.committed, { text: "draft B" }, "the attempt part is durable");
    const replaced = await enqueue({ kind: "dm", peer: "bob" }, "draft C", { id: "op-1" });
    assert.equal(replaced.replaced, false);
    [entry] = await pending();
    assert.deepEqual(entry.committed, { text: "draft B" });
    const ok = ratchetDM(() => ({ receipt: { state: "accepted" } }));
    assert.equal(await flush(session, ok), 1);
    assert.deepEqual(ok.sent, [["op-1", "draft B"]], "the committed text resolves, never the replaced draft");
    assert.equal((await pending()).length, 0);
});

test("the consumer's committed original is authoritative for the attempt", async () => {
    await setFence(1); await clear();
    await enqueue({ kind: "conversation", conversation: "g" }, "mine", { id: "op-2" });
    const groups = { ratchet: {}, showQueued() {}, send: async () => { throw uncertain("original elsewhere"); } };
    await flush(session, ratchetDM(() => ({})), groups);
    assert.deepEqual((await pending())[0].committed, { text: "original elsewhere" });
});

test("an expired draft is dropped, an expired committed attempt still resolves", async () => {
    await setFence(1); await clear();
    const realNow = Date.now;
    try {
        await enqueue({ kind: "dm", peer: "bob" }, "attempted", { id: "attempted" });
        await enqueue({ kind: "dm", peer: "bob" }, "never attempted", { id: "never" });
        // The first entry's attempt stays uncertain (retryable stops the flush
        // before the second is tried).
        await flush(session, ratchetDM(() => { throw uncertain("attempted"); }));
        Date.now = () => realNow() + OUTBOX_TTL + 1000;
        const list = await pending();
        const attempted = list.find(e => e.id === "attempted"), never = list.find(e => e.id === "never");
        assert.equal(!!attempted.expired, false, "a committed attempt never expires");
        assert.equal(attempted.committed.text, "attempted");
        assert.equal(never.expired, true, "a never-attempted draft expires");
        assert.equal(never.text, "");
        const ok = ratchetDM(() => ({ receipt: { state: "accepted" } }));
        await flush(session, ok);
        assert.deepEqual(ok.sent, [["attempted", "attempted"]], "the committed attempt resolves after the TTL; the expired draft is not sent");
        assert.deepEqual((await pending()).map(e => e.id), ["never"]);
    } finally { Date.now = realNow; }
});

test("pending() read failures never lose, send or hide entries", async () => {
    await setFence(1); await clear();
    await enqueue({ kind: "dm", peer: "bob" }, "readable", { id: "good" });
    await raw(Stores.outbox, s => s.put({ iv: [1], sealed: [2] }, "corrupt"));
    const list = await pending();
    assert.deepEqual(list.find(e => e.id === "corrupt")?.unreadable, true);
    assert.equal(await waitingCount(), 2, "an unreadable entry still counts as waiting (not lost)");
    const dm = ratchetDM(() => ({ receipt: { state: "accepted" } }));
    assert.equal(await flush(session, dm), 1);
    assert.deepEqual(dm.sent, [["good", "readable"]]);
    assert.deepEqual((await pending()).map(e => e.id), ["corrupt"], "the unreadable entry is kept, never deleted");
    await remove("corrupt");
    // A failure listing the store rejects the flush; nothing is changed.
    await enqueue({ kind: "dm", peer: "bob" }, "kept", { id: "kept" });
    const realOpen = indexedDB.open;
    indexedDB.open = () => { const r = {}; setTimeout(() => { r.error = new Error("storage unavailable"); r.onerror?.(); }); return r; };
    try { await assert.rejects(flush(session, ratchetDM(() => ({})))); }
    finally { indexedDB.open = realOpen; }
    assert.deepEqual((await pending()).map(e => [e.id, e.text]), [["kept", "kept"]]);
});

test("logout during a flush: nothing is sent or written back afterwards", async () => {
    await setFence(1); await clear();
    await enqueue({ kind: "dm", peer: "bob" }, "first", { id: "l-1" });
    await enqueue({ kind: "dm", peer: "bob" }, "second", { id: "l-2" });
    let release, entered;
    const gate = new Promise(r => { release = r; }), inSend = new Promise(r => { entered = r; });
    const dm = ratchetDM(async () => { entered(); await gate; throw Object.assign(new Error("refused"), { code: "invalid" }); });
    const running = flush(session, dm);
    await inSend;
    // Sign-out: the identity generation moves on and the outbox is wiped.
    await setFence(2);
    await raw(Stores.outbox, s => s.clear());
    release();
    assert.equal(await running, 0);
    assert.deepEqual(dm.sent.map(s => s[0]), ["l-1"], "no further entry is sent after logout");
    assert.deepEqual(await pending(), [], "the refused entry is not written back into the wiped outbox");
});

// Adopted review repros (1c-iii-a2 P2-3, P3-1, P3-2).
test("an explicit Retry of a committed entry that failed terminally resolves the same operation", async () => {
    await setFence(1); await clear();
    await enqueue({ kind: "dm", peer: "bob" }, "hello", { id: "op-f" });
    await flush(session, ratchetDM(() => { throw Object.assign(new Error("account_changed"), { code: "account_changed" }); }));
    assert.equal((await pending())[0].failed, true);
    const r = await enqueue({ kind: "dm", peer: "bob" }, "edited meanwhile", { id: "op-f" }); // UI Retry
    assert.equal(r.replaced, false);
    assert.equal(!!r.failed, false, "the terminal failure is cleared");
    assert.deepEqual(r.committed, { text: "hello" }, "the attempt is kept");
    const dm = ratchetDM(() => ({ receipt: { state: "accepted" } }));
    await flush(session, dm);
    assert.deepEqual(dm.sent, [["op-f", "hello"]], "the retry resends the SAME operation with its committed text");
    assert.equal((await pending()).length, 0);
});

test("enqueue never overwrites an unreadable (possibly committed) entry", async () => {
    await setFence(1); await clear();
    await enqueue({ kind: "dm", peer: "bob" }, "original", { id: "op-u" });
    await flush(session, ratchetDM(() => { throw Object.assign(new Error("uncertain"), { code: "uncertain", retryable: true }); }));
    await raw(Stores.outbox, s => s.put({ broken: true }, "op-u"));
    await assert.rejects(enqueue({ kind: "dm", peer: "bob" }, "changed draft", { id: "op-u" }), e => e.code === "unreadable");
    const dm = ratchetDM(() => ({ receipt: { state: "accepted" } }));
    await flush(session, dm);
    assert.deepEqual(dm.sent, [], "nothing is sent under the committed operation's ID");
    assert.equal((await pending())[0].unreadable, true);
    await remove("op-u");
});

test("a draft replaced while its send is committing is committed as it stands, never dropped", async () => {
    await setFence(1); await clear();
    await enqueue({ kind: "dm", peer: "bob" }, "A", { id: "op-r" });
    let enq;
    const dm = ratchetDM(async () => { enq = await enqueue({ kind: "dm", peer: "bob" }, "B", { id: "op-r" }); throw Object.assign(new Error("uncertain"), { code: "uncertain", retryable: true }); });
    await flush(session, dm);
    assert.equal(enq.replaced, false, "the replace during the send sees the committed attempt");
    const [after] = await pending();
    assert.deepEqual(after.committed, { text: "A" }, "the committed part survives the concurrent enqueue");
});

test("pending()'s expiry rewrite during a flush is fenced against logout", async () => {
    await setFence(1); await clear();
    const realNow = Date.now;
    try {
        await enqueue({ kind: "dm", peer: "bob" }, "old draft", { id: "op-e" });
        Date.now = () => realNow() + 15 * 24 * 3600 * 1000;
        await setFence(2); // logged out
        await raw(Stores.outbox, s => s.clear());
        await raw(Stores.outbox, s => s.put({ broken: true }, "noise")); // keep the store non-empty
        await pending(Date.now(), { fence: { generation: 1, active: true } });
        assert.deepEqual((await raw(Stores.outbox, s => s.getAllKeys())), ["noise"], "nothing written back for the old generation");
    } finally { Date.now = realNow; await raw(Stores.outbox, s => s.clear()); await setFence(1); }
});

test("fix-review P3-7: a draft another tab marked failed is not committed; one bad entry never stops the flush", async () => {
    await setFence(1); await clear();
    await enqueue({ kind: "dm", peer: "bob" }, "first", { id: "p7-a" });
    await enqueue({ kind: "dm", peer: "bob" }, "second", { id: "p7-b" });
    // Another tab marks p7-a failed (never attempted) while this flush runs.
    const { putSealed } = await import("../src/core/db.js");
    const [a] = await pending();
    await putSealed(Stores.outbox, "p7-a", { ...a, failed: true, error: "elsewhere" });
    const dm = ratchetDM(() => ({ receipt: { state: "accepted" } }));
    await flush(session, dm);
    const left = await pending();
    assert.deepEqual(left.map(e => [e.id, !!e.committed]), [["p7-a", false]], "the failed draft stays uncommitted and editable");
    assert.deepEqual(dm.sent, [["p7-b", "second"]]);
});
