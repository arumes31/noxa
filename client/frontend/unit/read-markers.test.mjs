import test from "node:test";
import assert from "node:assert/strict";
import { mergeReadMarker, mergeReadMarkers, readPosition, remoteReadMarker } from "../src/read-markers.js";

test("readPosition accepts positive ids in any representation and rejects the rest", () => {
    assert.equal(readPosition(42), 42);
    assert.equal(readPosition("42"), 42);
    assert.equal(readPosition(" 7 "), 7);
    assert.equal(readPosition(3.9), 3);
    for (const value of [0, -1, "", "abc", null, undefined, NaN, Infinity, {}, []]) assert.equal(readPosition(value), 0, String(value));
});

test("mergeReadMarker never regresses and ignores garbage", () => {
    assert.equal(mergeReadMarker(10, 20), 20);
    assert.equal(mergeReadMarker(20, 10), 20);
    assert.equal(mergeReadMarker(20, "15"), 20);
    assert.equal(mergeReadMarker(20, undefined), 20);
    assert.equal(mergeReadMarker(undefined, "33"), 33);
    assert.equal(mergeReadMarker("x", null), 0);
});

test("mergeReadMarkers is commutative, associative and idempotent (conflict-free)", () => {
    const a = { 1: 10, 2: 50, 3: 7 };
    const b = { 1: 20, 2: 40, 4: 3 };
    const c = { 2: 60, 5: 1 };
    const ab = mergeReadMarkers(a, b).merged, ba = mergeReadMarkers(b, a).merged;
    assert.deepEqual(ab, ba);
    assert.deepEqual(ab, { 1: 20, 2: 50, 3: 7, 4: 3 });
    const abc = mergeReadMarkers(ab, c).merged, bca = mergeReadMarkers(mergeReadMarkers(b, c).merged, a).merged;
    assert.deepEqual(abc, bca);
    assert.deepEqual(mergeReadMarkers(abc, abc), { merged: abc, changed: false });
});

test("mergeReadMarkers reports whether the remote side moved anything forward", () => {
    assert.equal(mergeReadMarkers({ 1: 10 }, { 1: 5 }).changed, false);
    assert.equal(mergeReadMarkers({ 1: 10 }, { 1: 10 }).changed, false);
    assert.equal(mergeReadMarkers({ 1: 10 }, { 1: 11 }).changed, true);
    assert.equal(mergeReadMarkers({ 1: 10 }, { 2: 1 }).changed, true);
    assert.equal(mergeReadMarkers({ 1: 10 }, { 2: "junk", 3: 0 }).changed, false);
    assert.deepEqual(mergeReadMarkers(null, undefined), { merged: {}, changed: false });
    assert.deepEqual(mergeReadMarkers({ 1: "0", 2: "abc" }, {}).merged, {});
});

test("remoteReadMarker accepts only read device events with a known scope", () => {
    assert.deepEqual(remoteReadMarker({ event: "read", read: { scope: "channel", id: "12", message_id: "900" } }), { scope: "channel", id: "12", messageID: 900 });
    assert.deepEqual(remoteReadMarker({ event: "read", read: { scope: "dm", id: "peer-uid" } }), { scope: "dm", id: "peer-uid", messageID: 0 });
    assert.deepEqual(remoteReadMarker({ event: "read", read: { scope: "conversation", id: "g1", message_id: 4 } }), { scope: "conversation", id: "g1", messageID: 4 });
    for (const event of [null, {}, { event: "linked" }, { event: "read" }, { event: "read", read: "x" }, { event: "read", read: { scope: "channel" } }, { event: "read", read: { scope: "other", id: "1" } }]) {
        assert.equal(remoteReadMarker(event), null, JSON.stringify(event));
    }
});
