import assert from "node:assert/strict";
import test from "node:test";
import { combineUnread, descendantUnread } from "../src/channel-unread.js";

const tree = new Map([
    [1, [{ ChannelID: 2 }, { ChannelID: 3 }]],
    [2, [{ ChannelID: 4 }]],
]);
const unread = new Map([
    [1, { n: 1 }],
    [2, { n: 2, replies: 1 }],
    [3, { n: 0 }],
    [4, { n: 5, mention: true, mentions: 2 }],
]);
const lookup = id => unread.get(id) || null;

test("descendants are summed, the channel itself is not", () => {
    assert.deepEqual(descendantUnread(1, tree, lookup), { n: 7, mentions: 2, replies: 1, mention: true });
    assert.deepEqual(descendantUnread(2, tree, lookup), { n: 5, mentions: 2, replies: 0, mention: true });
    assert.deepEqual(descendantUnread(4, tree, lookup), { n: 0, mentions: 0, replies: 0, mention: false });
});

test("a cyclic or malformed tree terminates", () => {
    const cyclic = new Map([[1, [{ ChannelID: 2 }]], [2, [{ ChannelID: 1 }, null, {}]]]);
    assert.deepEqual(descendantUnread(1, cyclic, lookup), { n: 2, mentions: 0, replies: 1, mention: false });
});

test("own and descendant unread combine into one badge", () => {
    assert.deepEqual(combineUnread({ n: 1 }, descendantUnread(1, tree, lookup)), { n: 8, mentions: 2, replies: 1, mention: true });
    assert.deepEqual(combineUnread(null, { n: 3, mentions: 0, replies: 0, mention: false }), { n: 3, mentions: 0, replies: 0, mention: false });
});
