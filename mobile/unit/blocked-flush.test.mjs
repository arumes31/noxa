// Review b7 P2-1 (adopted repro): a DM or group entry blocked by secure
// messaging stays queued with its reason and never stops the flush: channel
// messages (and notification replies) queued behind it are still sent.
import test from "node:test";
import assert from "node:assert/strict";
import nacl from "tweetnacl";
import { installFakeIndexedDB } from "./fake-idb.mjs";
installFakeIndexedDB();
const { enqueue, flush, pending } = await import("../src/core/outbox.js");
const { openDB, run, Stores, putSealed } = await import("../src/core/db.js");
await putSealed(Stores.device, "warmup", { ok: true });
const { DirectMessages } = await import("../src/core/direct.js");
const { Groups } = await import("../src/core/groups.js");
const raw = async (store, op) => { const db = await openDB(); try { return await run(db, store, "readwrite", op); } finally { db.close(); } };
const me = nacl.box.keyPair();
const tick = () => new Promise(r => setTimeout(r, 5));

test("review b7 P2-1: blocked DM and group entries do not stop channel entries behind them", async () => {
    await raw(Stores.device, s => s.put({ generation: 1, active: true }, "identity-fence"));
    const channelSends = [];
    const sess = Object.assign(new EventTarget(), {
        conn: { open: true, send() {}, async request() { throw new Error("no transport expected"); } },
        identity: { xSecret: me.secretKey, xPublic: me.publicKey }, uniqueID: "me", deviceID: "me-dev", nickname: "me", showQueued() {},
        async send(scope, text) { channelSends.push(text); },
    });
    await enqueue({ kind: "dm", peer: "bob" }, "dm first", { id: "op-dm" });
    await tick();
    await enqueue({ kind: "channel", scope: 1 }, "channel second", { id: "op-ch" });
    await tick();
    await enqueue({ kind: "conversation", conversation: "g1" }, "group third", { id: "op-g" });
    await tick();
    // A notification reply (service worker) to a channel mention, queued last.
    await enqueue({ kind: "channel", scope: 2 }, "notification reply", { id: "op-n" });
    const dm = new DirectMessages(sess, {});
    await dm.ready;
    dm.secureRequired = () => Object.assign(new Error("this server does not offer secure messaging"), { code: "server_outdated" });
    const groups = new Groups(sess, dm);
    groups.secureRequired = dm.secureRequired;
    await flush(sess, dm, groups);
    assert.deepEqual(channelSends, ["channel second", "notification reply"], "channel entries behind blocked ones are sent");
    const left = Object.fromEntries((await pending()).map(e => [e.id, e]));
    assert.deepEqual(Object.keys(left).sort(), ["op-dm", "op-g"], "blocked entries stay queued");
    for (const id of ["op-dm", "op-g"]) {
        assert.equal(left[id].failed, undefined, `${id} never failed for a blocked state`);
        assert.equal(left[id].error, "this server does not offer secure messaging", `${id} keeps the reason`);
    }
});
