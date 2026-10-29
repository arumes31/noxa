import test from "node:test";
import assert from "node:assert/strict";
import { installFakeIndexedDB } from "./fake-idb.mjs";
installFakeIndexedDB();
const { Stores, putSealed, getSealed, currentFence } = await import("../src/core/db.js");
const { saveIdentity } = await import("../src/core/identity-store.js");
const { generateIdentity } = await import("../src/core/crypto.js");
const { DirectMessages } = await import("../src/core/direct.js");
const deferred = () => { let resolve, reject; const promise = new Promise((a, b) => { resolve = a; reject = b; }); return { promise, resolve, reject }; };
const session = () => Object.assign(new EventTarget(), { conn: { open: true, send() {} }, identity: generateIdentity(), uniqueID: "old", deviceID: "old-device", nickname: "Old" });
const settle = () => new Promise(r => setTimeout(r, 100));
async function replace() {
    await saveIdentity(generateIdentity(), {}, { onboarding: true, replaceAccount: true, fence: await currentFence() });
    await putSealed(Stores.dm, "peer", { peer: "peer", messages: [{ id: "new", body: "new account" }] });
}

for (const retire of ["replace", "close"]) test(`late ratchet refusal cannot repaint after ${retire}`, async () => {
    const dm = new DirectMessages(session(), {}, { fence: await currentFence() });
    await dm.ready;
    const entered = deferred(), result = deferred();
    await dm.attachRatchet({ closed: false, project() {}, async history() { return []; }, send() { entered.resolve(); return result.promise; } });
    const sending = dm.send("peer", "old private text", "", { id: "old-op" });
    const rejected = assert.rejects(sending);
    await entered.promise;
    await settle();
    if (retire === "replace") await replace(); else dm.close();
    let events = 0;
    dm.addEventListener("messages", () => events++);
    result.reject(Object.assign(new Error("fenced"), { code: "fenced" }));
    await rejected;
    await settle();
    assert.equal(events, 0);
    if (retire === "replace") assert.deepEqual((await getSealed(Stores.dm, "peer")).messages, [{ id: "new", body: "new account" }]);
    dm.close();
});

test("retired instance cannot delete replacement history or change its pins", async () => {
    const dm = new DirectMessages(session(), {}, { fence: await currentFence() });
    await dm.ready;
    await replace();
    await putSealed(Stores.pins, "peer", { signingKey: "replacement", verified: true });
    await assert.rejects(dm.deleteHistory("peer"), { code: "fenced" });
    await assert.rejects(dm.setVerified("peer", false), { code: "fenced" });
    assert.equal((await getSealed(Stores.dm, "peer")).messages[0].id, "new");
    assert.equal((await getSealed(Stores.pins, "peer")).verified, true);
    dm.close();
});

test("history response after account replacement is not imported or ACKed", async () => {
    const s = session(), entered = deferred(), response = deferred(), actions = [];
    const dm = new DirectMessages(s, {}, { fence: await currentFence() });
    await dm.ready;
    dm.devicesOf = async () => ({ devices: [] });
    s.conn.request = async (_, request) => { actions.push(request.action); entered.resolve(); return actions.length === 1 ? response.promise : { history: [] }; };
    const importing = dm.importHistory();
    const rejected = assert.rejects(importing, { code: "fenced" });
    await entered.promise;
    await replace();
    response.resolve({ history: [{ seq: 0, total: 1, from_device_id: "old-device", data: "irrelevant" }] });
    await rejected;
    assert.deepEqual(actions, ["history_get"]);
    assert.equal(await getSealed(Stores.device, "history-imports"), undefined);
    dm.close();
});
