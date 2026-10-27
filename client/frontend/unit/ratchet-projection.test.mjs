import test from "node:test";
import assert from "node:assert/strict";
import { groupRatchetRows, legacyID, mergeGroupMessages, quarantineRows, ratchetDMMessage, ratchetIsIncoming, sameDMMessage } from "../src/ratchet-projection.js";

const key = (sender, id) => JSON.stringify(["noxa-olm-v1", sender, id]);
const record = (sender, id, extra = {}) => ({ protocol: "noxa-olm-v1", scope: "group", conversation: "g", sender, logical_id: id, key: key(sender, id), purpose: "chat",
    body: { text: `${sender}:${id}` }, sent_at: 10, self: false, copied: false, transport: { message_id: "5d0c7f2a-0000-4000-8000-000000000001" }, ...extra });

test("opaque IDs are never coerced into legacy numeric IDs", () => {
    assert.equal(legacyID({ id: 7 }), 7);
    assert.equal(legacyID({ id: "7" }), 0);
    assert.equal(legacyID({ id: "olm:" + key("a", "1") }), 0);
    assert.equal(legacyID({ id: 1.5 }), 0);
    const m = ratchetDMMessage({ ...record("alice", "m1"), scope: "direct", peer: "alice" });
    assert.equal(m.id, 0, "a DM record has no legacy numeric id");
    assert.equal(m.clientMsgID, "m1");
    assert.equal(m.ratchetKey, key("alice", "m1"));
});

test("DM identity is protocol + sender + logical ID", () => {
    const a = ratchetDMMessage({ ...record("alice", "m1"), scope: "direct" });
    const b = ratchetDMMessage({ ...record("bob", "m1"), scope: "direct" });
    assert.equal(sameDMMessage(a, b), false, "another sender reusing the logical string");
    assert.equal(sameDMMessage(a, { ...a, protocol: "" }), false, "a legacy message with the same client id");
    assert.equal(sameDMMessage(a, ratchetDMMessage({ ...record("alice", "m1"), scope: "direct", status: "accepted" })), true);
});

test("only durable new receives are incoming", () => {
    assert.equal(ratchetIsIncoming({ ...record("bob", "x"), is_new: true }), true);
    assert.equal(ratchetIsIncoming({ ...record("bob", "x"), is_new: false }), false, "replay");
    assert.equal(ratchetIsIncoming({ ...record("me", "x"), is_new: true, self: true, copied: true }), false, "own linked copy");
    assert.equal(ratchetIsIncoming({ ...record("bob", "x"), is_new: true, purpose: "edit" }), false);
});

test("group rows apply edits/deletes only for the original sender", () => {
    const rows = groupRatchetRows([
        record("alice", "g1", { sent_at: 3 }),
        record("bob", "g1", { sent_at: 4 }),
        record("bob", "e", { purpose: "edit", reference: "g1", reference_key: key("alice", "g1"), body: { text: "forged" } }),
        record("alice", "d", { purpose: "delete", reference: "g1", reference_key: key("alice", "g1"), body: {} }),
        record("bob", "e2", { purpose: "edit", reference: "g1", reference_key: key("bob", "g1"), body: { text: "bob edited" } }),
    ]);
    assert.deepEqual(rows.map(r => [r.id, r.from_unique_id, r.body, r.deleted, r.edited]), [
        ["olm:" + key("alice", "g1"), "alice", "", true, false],
        ["olm:" + key("bob", "g1"), "bob", "bob edited", false, true],
    ]);
});

test("mixed legacy numeric and opaque rows: legacy keeps id order, new rows by time", () => {
    const olm = id => "olm:" + key("alice", id);
    const merged = mergeGroupMessages([{ id: 9, created_at: 50 }, { id: 7, created_at: 40 }, { id: 12, created_at: 70 }],
        groupRatchetRows([record("alice", "late", { sent_at: 90 }), record("alice", "mid", { sent_at: 55 })]));
    assert.deepEqual(merged.map(m => m.id), [7, 9, olm("mid"), 12, olm("late")]);
    assert.deepEqual(merged.map(legacyID), [7, 9, 0, 12, 0], "numeric navigation sees legacy IDs only");
});

test("quarantine placeholders are locked rows scoped to their conversation", () => {
    const q = [{ id: "d1", sender: "bob", code: "lost_key", scope: "direct", peer: "bob", seen_at: 2 }, { id: "d2", sender: "carol", code: "invalid", scope: "group", conversation: "g", seen_at: 3 }];
    assert.deepEqual(quarantineRows(q, { scope: "direct", peer: "bob" }).map(r => [r.id, r.code]), [["quarantine:d1", "lost_key"]]);
    assert.deepEqual(quarantineRows(q, { scope: "group", conversation: "g" }).map(r => r.id), ["quarantine:d2"]);
    assert.deepEqual(quarantineRows(q, { scope: "direct", peer: "carol" }), []);
});

test("own group changes apply only once accepted", () => {
    const msg = record("me", "L1", { self: true, status: "accepted" });
    const del = record("me", "d", { purpose: "delete", reference: "L1", reference_key: key("me", "L1"), self: true, status: "aborted", body: {} });
    assert.deepEqual(groupRatchetRows([msg, del]).map(r => [r.deleted, r.body]), [[false, "me:L1"]]);
    assert.deepEqual(groupRatchetRows([msg, { ...del, status: "accepted" }]).map(r => [r.deleted, r.body]), [[true, ""]]);
    assert.deepEqual(groupRatchetRows([msg, { ...del, status: "" }]).map(r => r.deleted), [true], "a linked copy carries no status");
});
