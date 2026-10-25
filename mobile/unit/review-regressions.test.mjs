import assert from "node:assert/strict";
import { test } from "node:test";
import { ActivityLog } from "../src/core/activity.js";
import { ChatSession } from "../src/core/chat-session.js";
import { openExport, sealExport, EXPORT_FORMAT } from "../src/core/chat-export.js";

function deferred() {
    let resolve, reject;
    const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
    return { promise, resolve, reject };
}

test("activity preserves existing history when an event arrives before storage loads", async () => {
    const load = deferred();
    const saves = [];
    const log = new ActivityLog({ load: () => load.promise, save: entries => saves.push(structuredClone(entries)) });
    const fresh = log.add({ kind: "dm", title: "New" });
    await Promise.resolve();
    assert.equal(saves.length, 0, "never overwrite storage before reading it");
    load.resolve([{ id: "old", kind: "mention", title: "Old", read: false }]);
    await log.flush();
    assert.deepEqual(saves.at(-1).map(e => e.id), [fresh.id, "old"]);
});

test("clearing activity during startup does not resurrect the stored log", async () => {
    const load = deferred();
    let saved;
    const log = new ActivityLog({ load: () => load.promise, save: entries => { saved = entries; } });
    log.clear();
    load.resolve([{ id: "old", kind: "dm", title: "Private" }]);
    await log.flush();
    assert.deepEqual(log.entries, []);
    assert.deepEqual(saved, []);
});

test("activity persists early events even if the initial read fails", async () => {
    const load = deferred();
    let saved;
    const log = new ActivityLog({ load: () => load.promise, save: entries => { saved = entries; } });
    log.add({ kind: "call", title: "Call" });
    load.reject(new Error("unreadable"));
    await log.flush();
    assert.equal(saved[0].title, "Call");
});

function chat(request) {
    return new ChatSession({ on: () => () => {}, request }, {}, { client_id: 1, unique_id: "me" });
}

test("channel subscriptions include channels beyond the first 64", async () => {
    const requests = [];
    const subscribed = new Set();
    const session = chat(async (_type, body) => {
        requests.push(body);
        body.channel_ids.forEach(id => subscribed.add(id));
        return { channel_ids: [...subscribed] };
    });
    const ids = Array.from({ length: 130 }, (_, i) => i + 1);
    await session.subscribe([...ids, 1, -1, 0, 1.5]);
    assert.deepEqual(requests.map(r => r.channel_ids.length), [64, 64, 2]);
    assert.deepEqual([...session.subscriptions], ids);
});

test("an unsubscribe received during subscription runs afterward, while duplicates coalesce", async () => {
    const first = deferred();
    const requests = [];
    const session = chat(async (_type, body) => {
        requests.push(body);
        if (requests.length === 1) await first.promise;
        return { channel_ids: body.subscribe ? body.channel_ids : [] };
    });
    const subscribing = session.subscribe([1]);
    const duplicate = session.subscribe([1]);
    const unsubscribing = session.subscribe([1], false);
    await Promise.resolve();
    assert.equal(requests.length, 1);
    first.resolve();
    await Promise.all([subscribing, duplicate, unsubscribing]);
    assert.deepEqual(requests.map(r => r.subscribe), [true, false]);
    assert.equal(session.subscriptions.size, 0);
});

test("a failed subscription batch does not block retrying the channels", async () => {
    let calls = 0;
    const session = chat(async (_type, body) => {
        if (++calls === 1) throw new Error("disconnected");
        return { channel_ids: body.channel_ids };
    });
    await assert.rejects(session.subscribe([5]), /disconnected/);
    await session.subscribe([5]);
    assert.deepEqual([...session.subscriptions], [5]);
});

test("encrypted chat imports reject excessive or invalid work factors before WebCrypto", async () => {
    const subtle = { importKey: () => assert.fail("must validate before deriving a key") };
    for (const iter of [0, -1, 999, 1.5, "600000", 10000001, Infinity, 4294967295]) {
        await assert.rejects(sealExport("chat", "secret", { iterations: iter, subtle }), /unsupported export/);
        await assert.rejects(openExport({ format: EXPORT_FORMAT, v: 1, kdf: "pbkdf2-sha256", cipher: "aes-256-gcm", iter }, "secret", { subtle }), /not a noXa/);
    }
});

test("account cleanup removes private settings but keeps appearance", async () => {
    const settings = await import("../src/core/settings.js");
    settings.set({ theme: "light", fontScale: 1.2, contacts: [{ uid: "a", note: "secret" }], notes: { a: "secret" }, presenceMessage: "private", chatTabs: { order: ["dm:a"], pinned: [] } });
    settings.clearAccountSettings();
    assert.deepEqual(settings.get("contacts"), []);
    assert.deepEqual(settings.get("notes"), {});
    assert.deepEqual(settings.get("chatTabs"), { order: [], pinned: [] });
    assert.equal(settings.get("presenceMessage"), "");
    assert.equal(settings.get("theme"), "light");
    assert.equal(settings.get("fontScale"), 1.2);
    settings.reset();
});
