// A pending share is written under the identity generation it started in.
import test from "node:test";
import assert from "node:assert/strict";
import { installFakeIndexedDB } from "./fake-idb.mjs";
installFakeIndexedDB();
const { currentFence } = await import("../src/core/db.js");
const { saveIdentity } = await import("../src/core/identity-store.js");
const { generateIdentity } = await import("../src/core/crypto.js");
const { clearShare, getShare, putShare } = await import("../src/core/share-store.js");

test("a share stored before sign-in is kept", async () => {
    await clearShare();
    await putShare({ text: "hello", files: [new File(["abc"], "a.txt")] });
    const share = await getShare();
    assert.equal(share.text, "hello");
    assert.equal(share.files.length, 1);
});

test("a share resumed after an account replacement is not stored for the new account", async () => {
    await clearShare();
    let release;
    const gate = new Promise(resolve => { release = resolve; });
    const slow = { name: "old.txt", type: "text/plain", arrayBuffer: async () => { await gate; return new TextEncoder().encode("old account").buffer; } };
    const writing = putShare({ text: "old", files: [slow] });
    await new Promise(resolve => setTimeout(resolve, 20));
    await saveIdentity(generateIdentity(), {}, { onboarding: true, replaceAccount: true, fence: await currentFence() });
    release();
    await assert.rejects(writing, e => e.code === "fenced");
    assert.equal(await getShare(), null);
});
