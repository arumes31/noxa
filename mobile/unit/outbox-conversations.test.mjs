import test from "node:test";
import assert from "node:assert/strict";

import { installFakeIndexedDB } from "./fake-idb.mjs";

const reset = installFakeIndexedDB();
const { enqueue, flush, pending, remove, showPending, waitingCount } = await import("../src/core/outbox.js");

async function clear() {
    for (const entry of await pending()) await remove(entry.id);
}

function fakes({ groupError = null } = {}) {
    const calls = [];
    const session = { send: async (scope, text, options) => { calls.push(["channel", scope, text, options.clientMsgID]); }, showQueued: (...args) => calls.push(["show-channel", ...args]) };
    const dm = { send: async (peer, text, nickname, options) => { calls.push(["dm", peer, text, options.id]); }, showQueued: (...args) => calls.push(["show-dm", ...args]) };
    const groups = {
        send: async (id, text, options) => {
            calls.push(["conversation", id, text, options.reference]);
            if (groupError) throw groupError;
        },
        showQueued: (id, state) => calls.push(["show-conversation", id, state]),
    };
    return { calls, session, dm, groups };
}

test("a group message waits in the outbox and is sent with its reference as the client message ID", async () => {
    await clear();
    await enqueue({ kind: "channel", scope: 4 }, "to the channel", { id: "c-1", sentAt: 1 });
    await enqueue({ kind: "conversation", conversation: "g-1" }, "to the group", { id: "ref-1", sentAt: 2 });
    await enqueue({ kind: "dm", peer: "uid-ivy", nickname: "Ivy" }, "to Ivy", { id: "d-1", sentAt: 3 });
    assert.equal(await waitingCount(), 3);
    const { calls, session, dm, groups } = fakes();
    assert.equal(await flush(session, dm, groups), 3);
    assert.deepEqual(calls, [
        ["channel", 4, "to the channel", "c-1"],
        ["conversation", "g-1", "to the group", "ref-1"],
        ["dm", "uid-ivy", "to Ivy", "d-1"],
    ]);
    assert.equal(await waitingCount(), 0);
});

test("without groups a conversation entry keeps waiting while the others are sent", async () => {
    await clear();
    await enqueue({ kind: "conversation", conversation: "g-1" }, "later", { id: "ref-2", sentAt: 1 });
    await enqueue({ kind: "channel", scope: 0 }, "now", { id: "c-2", sentAt: 2 });
    const { calls, session, dm } = fakes();
    assert.equal(await flush(session, dm), 1);
    assert.deepEqual(calls.map(call => call[0]), ["channel"]);
    assert.deepEqual((await pending()).map(entry => entry.id), ["ref-2"]);
});

test("a lost connection stops the flush; a refusal keeps the group message as failed and shows it", async () => {
    await clear();
    await enqueue({ kind: "conversation", conversation: "g-1" }, "first", { id: "ref-3", sentAt: 1 });
    await enqueue({ kind: "conversation", conversation: "g-1" }, "second", { id: "ref-4", sentAt: 2 });
    let run = fakes({ groupError: Object.assign(new Error("connection lost"), { retryable: true }) });
    assert.equal(await flush(run.session, run.dm, run.groups), 0);
    assert.equal(run.calls.length, 1, "stops at the first lost connection");
    assert.equal(await waitingCount(), 2);

    run = fakes({ groupError: Object.assign(new Error("conversation access denied"), { retryable: false }) });
    await flush(run.session, run.dm, run.groups);
    const entries = await pending();
    assert.deepEqual(entries.map(entry => [entry.id, !!entry.failed]), [["ref-3", true], ["ref-4", true]]);
    assert.equal(await waitingCount(), 0);

    // Failed entries are shown in their group as not sent.
    run = fakes();
    await showPending(run.session, run.dm, run.groups);
    assert.deepEqual(run.calls.map(call => [call[0], call[1], call[2].reference, call[2].expired]),
        [["show-conversation", "g-1", "ref-3", true], ["show-conversation", "g-1", "ref-4", true]]);
});

test("retrying replaces a failed entry with the same ID, which then sends", async () => {
    await clear();
    await enqueue({ kind: "conversation", conversation: "g-2" }, "again", { id: "ref-5", sentAt: 5 });
    const failing = fakes({ groupError: Object.assign(new Error("refused"), { retryable: false }) });
    await flush(failing.session, failing.dm, failing.groups);
    assert.equal((await pending())[0].failed, true);
    await enqueue({ kind: "conversation", conversation: "g-2" }, "again", { id: "ref-5", sentAt: 5 });
    assert.equal((await pending()).length, 1);
    const run = fakes();
    assert.equal(await flush(run.session, run.dm, run.groups), 1);
    assert.deepEqual(run.calls, [["conversation", "g-2", "again", "ref-5"]]);
    reset();
});
