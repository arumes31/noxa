import test from "node:test";
import assert from "node:assert/strict";
import { resolveParent, directPeer } from "../src/chat-relations.js";

test("reply identity is explicit and cannot be forged by a quoted nickname", () => {
    const original = { id: 1, from: "Alice", ts: 1 };
    const unrelated = { id: 2, from: "Alice", ts: 2 };
    assert.equal(resolveParent([original, unrelated], { text: "↪ Alice: hello", ts: 3 }), null);
    assert.equal(resolveParent([original, unrelated], { replyToID: 1, text: "hello" }), original);
    assert.equal(resolveParent([original], { replyToID: 9 }), null);
});

test("outgoing direct messages require their recipient identity", () => {
    assert.equal(directPeer({}, { self: true, fromUID: "self" }), "");
    assert.equal(directPeer({ to_unique_id: "bob" }, { self: true }), "bob");
    assert.equal(directPeer({ to_unique_id: "self" }, { self: false, fromUID: "alice" }), "alice");
});
