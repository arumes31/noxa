import assert from "node:assert/strict";
import { test } from "node:test";

import { ChatSession } from "../src/core/chat-session.js";
import { buildInbox } from "../src/core/inbox.js";
import { newerPollState, pollClosed, pollPercentages, pollRemaining, togglePollChoice, validatePoll } from "../src/core/poll-state.js";
import { referenceHref, referenceTag, resolveAll, resolveReference } from "../src/core/saved-resolve.js";
import { channelEntry, dayBounds, dmEntry, excerpt, groupByConversation, groupEntry, highlight, matches, searchEntries } from "../src/core/search.js";

const KEY = Buffer.alloc(32, 9).toString("base64");

function entries() {
    return [
        channelEntry(4, { id: 10, from: "Ann", fromUniqueID: "ann", text: "Deploy tonight?", sentAt: 100 }),
        channelEntry(4, { id: 11, from: "Bob", fromUniqueID: "bob", text: "deploy is done", sentAt: 300 }),
        channelEntry(4, { id: 12, from: "Bob", fromUniqueID: "bob", text: "", deleted: true, sentAt: 310 }),
        dmEntry("ann", { id: "c1", self: false, from: "ann", fromNickname: "Ann", body: `logs [file:abc#${KEY}#nx1;s=10#deploy.log]`, sentAt: 200 }),
        dmEntry("ann", { id: "c2", self: true, from: "me", body: "thanks", sentAt: 250 }, { selfName: "Me", selfUID: "me" }),
        groupEntry("g1", { id: 5, from_unique_id: "bob", text: "Deploy party", created_at: 400 }, uid => uid === "bob" ? "Bob" : "?"),
    ];
}

test("search matches every word in text or sender, case-insensitively", () => {
    const list = entries();
    assert.deepEqual(searchEntries(list, { query: "DEPLOY" }).map(e => e.id), [5, 11, "c1", 10]);
    assert.deepEqual(searchEntries(list, { query: "deploy bob" }).map(e => e.id), [5, 11]);
    assert.equal(matches(list[2], { query: "" , from: ["bob"] }), false, "deleted messages never match");
    assert.equal(matches(list[0], {}), false, "an empty filter matches nothing");
});

test("search filters by people, conversation, dates and attachments and sorts both ways", () => {
    const list = entries();
    assert.deepEqual(searchEntries(list, { query: "deploy", from: ["bob"] }).map(e => e.id), [5, 11]);
    assert.deepEqual(searchEntries(list, { from: ["me"] }).map(e => e.id), ["c2"], "own direct messages carry the own ID");
    assert.deepEqual(searchEntries(list, { query: "deploy", conversations: ["channel:4"] }, "oldest").map(e => e.id), [10, 11]);
    assert.deepEqual(searchEntries(list, { query: "deploy", after: 150, before: 350 }).map(e => e.id), [11, "c1"]);
    assert.deepEqual(searchEntries(list, { attachments: true }).map(e => e.id), ["c1"]);
    assert.deepEqual(searchEntries(list, { query: "deploy" }, "oldest").map(e => e.id), [10, "c1", 11, 5]);
});

test("results group by conversation in result order", () => {
    const groups = groupByConversation(searchEntries(entries(), { query: "deploy" }));
    assert.deepEqual(groups.map(g => [g.conversation, g.results.length]), [["conversation:g1", 1], ["channel:4", 2], ["dm:ann", 1]]);
});

test("excerpts cut around the first word, show attachment names and highlight hits", () => {
    const long = `${"a".repeat(100)} needle ${"b".repeat(100)}`;
    const cut = excerpt(long, "needle", 10);
    assert.ok(cut.startsWith("…") && cut.endsWith("…") && cut.includes("needle"));
    assert.equal(excerpt(`see [file:abc#${KEY}#nx1;s=10#deploy.log]`), "see 📎 deploy.log");
    assert.deepEqual(highlight("Deploy and deploy", "deploy"), [{ text: "Deploy", hit: true }, { text: " and ", hit: false }, { text: "deploy", hit: true }]);
    assert.equal(dayBounds("bad"), 0);
    assert.ok(dayBounds("2026-06-10", true) - dayBounds("2026-06-10") === 86399);
});

test("the inbox lists unread conversations newest first with their unread mentions", () => {
    const unread = new Map([["channel:4", { count: 3, mentions: 1 }], ["dm:ann", { count: 1, mentions: 0 }], ["channel:9", { count: 2, mentions: 0 }]]);
    const conversations = [
        { tag: "channel:4", kind: "channel", title: "# dev", href: "#/chat/4", messages: [
            { id: 1, from: "Bob", text: "@me old", at: 10, mentioned: true },
            { id: 2, from: "Bob", text: "@me look", at: 50, mentioned: true },
            { id: 3, from: "Ann", text: "hi", at: 60 }] },
        { tag: "dm:ann", kind: "dm", title: "Ann", href: "#/dm/ann", messages: [{ id: "x", from: "Ann", text: "ping", at: 90 }] },
        { tag: "dm:bob", kind: "dm", title: "Bob", href: "#/dm/bob", messages: [{ id: "y", from: "Bob", text: "read", at: 95 }] },
        { tag: "conversation:g", kind: "group", title: "Team", href: "#/group/g", messages: [], serverUnread: 4 },
    ];
    const inbox = buildInbox({ unread, conversations });
    assert.deepEqual(inbox.items.map(i => [i.tag, i.count]), [["dm:ann", 1], ["channel:4", 3], ["conversation:g", 4]],
        "unknown conversations (channel:9) and read ones (dm:bob) are left out; groups use the server's count");
    assert.deepEqual(inbox.mentions.map(m => m.message.id), [2], "only the newest unread mention");
    assert.equal(inbox.total, 8);
    assert.equal(buildInbox({ unread: new Map(), conversations }).items.length, 1, "only the group's server count remains");
});

test("saved references resolve through their conversation's source", async () => {
    const calls = [];
    const sources = {
        channel: async (channelID, id) => { calls.push(["channel", channelID, id]); return id === 7 ? { from: "Ann", text: "kept", at: 1 } : null; },
        thread: async (channelID, threadID, id) => { calls.push(["thread", channelID, threadID, id]); return { from: "Bob", text: "reply", at: 2 }; },
        group: async () => ({ from: "Cy", text: null, at: 3 }),
        dm: async () => { throw new Error("store unavailable"); },
    };
    const refs = [
        { kind: "channel", channelID: 4, messageID: 7 },
        { kind: "channel", channelID: 4, messageID: 8 },
        { kind: "thread", channelID: 4, threadID: 2, messageID: 9 },
        { kind: "group", groupID: "g", messageID: 1 },
        { kind: "dm", peer: "ann", clientMsgID: "c" },
    ];
    const results = await resolveAll(refs, sources);
    assert.deepEqual(results.map(r => r.message?.text ?? null), ["kept", null, "reply", null, null], "missing, locked and failing lookups are unavailable");
    assert.deepEqual(results.map(r => r.href), ["#/chat/4", "#/chat/4", "#/thread/4/2", "#/group/g", "#/dm/ann"]);
    assert.deepEqual(calls[2], ["thread", 4, 2, 9]);
    assert.equal(referenceTag({ kind: "dm", peer: "x" }), "dm:x");
    assert.equal(referenceHref({ kind: "group", groupID: "a/b" }), "#/group/a%2Fb");
    const deleted = await resolveReference({ kind: "channel", channelID: 1, messageID: 1 }, { channel: () => ({ text: "", deleted: true }) });
    assert.equal(deleted.message, null);
});

test("poll bodies parse and percentages, ballots and validation follow the server", () => {
    const body = `[noxa-poll:v1]${JSON.stringify({ question: "Lunch?", options: ["Pizza", "Sushi"], multiple: true, closes_at: 2000 })}`;
    assert.deepEqual(ChatSession.parsePoll(body), { question: "Lunch?", options: ["Pizza", "Sushi"], multiple: true, closesAt: 2000 });
    assert.equal(ChatSession.parsePoll("[noxa-poll:v1]{broken"), null);
    assert.equal(ChatSession.parsePoll("hello"), null);

    assert.deepEqual(pollPercentages([1, 2, 0], 3), [33, 67, 0]);
    assert.deepEqual(pollPercentages([2, 2], 2), [100, 100], "multiple choice can exceed 100 in total");
    assert.deepEqual(pollPercentages([0, 0], 0), [0, 0]);

    assert.deepEqual(togglePollChoice([], 1, false), [1]);
    assert.deepEqual(togglePollChoice([1], 0, false), [0]);
    assert.deepEqual(togglePollChoice([2], 0, true), [0, 2]);
    assert.deepEqual(togglePollChoice([0, 2], 2, true), [0]);

    const now = 1000;
    const ok = { question: "Q", options: ["a", "b"], closesAt: now + 3600 };
    assert.equal(validatePoll(ok, now), "");
    assert.equal(validatePoll({ ...ok, question: " " }, now), "poll.error.question");
    assert.equal(validatePoll({ ...ok, options: ["a"] }, now), "poll.error.fewOptions");
    assert.equal(validatePoll({ ...ok, options: Array.from({ length: 11 }, (_, i) => `o${i}`) }, now), "poll.error.manyOptions");
    assert.equal(validatePoll({ ...ok, options: ["A", " a "] }, now), "poll.error.duplicate");
    assert.equal(validatePoll({ ...ok, options: ["a", ""] }, now), "poll.error.emptyOption");
    assert.equal(validatePoll({ ...ok, closesAt: now + 8 * 86400 }, now), "poll.error.duration");

    assert.deepEqual(pollRemaining(now + 90061, now), { total: 90061, days: 1, hours: 1, minutes: 1, seconds: 1 });
    assert.equal(pollClosed({ closed: false }, now + 1, now), false);
    assert.equal(pollClosed({ closed: false }, now, now), true);
    assert.equal(newerPollState({ version: 3 }, { version: 2 }).version, 3);
    assert.equal(newerPollState({ version: 3 }, { version: 4 }).version, 4);
});
