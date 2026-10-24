import test from "node:test";
import assert from "node:assert/strict";

import { applyReceipt, exportConversation } from "../src/core/direct.js";

const own = (id, sentAt, extra = {}) => ({ id, sentAt, self: true, body: id, receipt: "sent", ...extra });
const theirs = (id, sentAt) => ({ id, sentAt, self: false, body: id });

test("a delivery receipt marks only its message, and only forward", () => {
    const messages = [own("a", 1), own("b", 2), theirs("x", 3)];
    assert.equal(applyReceipt(messages, "delivered", "b"), true);
    assert.deepEqual(messages.map(m => m.receipt), ["sent", "delivered", undefined]);
    // Repeated: nothing changes.
    assert.equal(applyReceipt(messages, "delivered", "b"), false);
    // Read cannot go back to delivered.
    messages[1].receipt = "read";
    assert.equal(applyReceipt(messages, "delivered", "b"), false);
    assert.equal(messages[1].receipt, "read");
});

test("a read receipt covers every own message up to the one read", () => {
    const messages = [own("a", 1), own("b", 2, { receipt: "delivered" }), theirs("x", 3), own("c", 4), own("d", 5)];
    assert.equal(applyReceipt(messages, "read", "c"), true);
    assert.deepEqual(messages.map(m => m.receipt), ["read", "read", undefined, "read", "sent"]);
});

test("receipts ignore unknown IDs, the contact's messages and messages not yet sent", () => {
    const messages = [own("a", 1, { pending: true, receipt: undefined }), own("b", 2, { queued: true, receipt: undefined }), theirs("x", 3), own("c", 4)];
    assert.equal(applyReceipt(messages, "read", "missing"), false);
    assert.equal(applyReceipt(messages, "read", "x"), false);
    assert.equal(applyReceipt(messages, "bogus", "c"), false);
    assert.equal(applyReceipt(messages, "read", "c"), true);
    assert.deepEqual(messages.map(m => m.receipt), [undefined, undefined, undefined, "read"]);
});

test("a conversation exports as one line per sent message", () => {
    const text = exportConversation({ messages: [
        theirs("hello", 0),
        { ...own("hi back", 60), edited: true },
        { ...own("gone", 120), deleted: true },
        { ...own("waiting", 180), queued: true },
    ] }, { selfName: "Me", peerName: "Ivy", format: date => String(date.getTime() / 1000) });
    assert.equal(text, "[0] Ivy: hello\n[60] Me: hi back (edited)\n[120] Me: (deleted)\n");
});
