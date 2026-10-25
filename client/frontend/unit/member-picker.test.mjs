import test from "node:test";
import assert from "node:assert/strict";
import { buildMemberCandidates, candidateContext, duplicateNames, looksLikeAccountID, rankMemberCandidates, shortAccountID } from "../src/member-picker-model.js";

const channels = [{ ChannelID: 7, Name: "Lounge" }, { ChannelID: 9, Name: "Dev" }];
const clients = [
    { client_id: 1, unique_id: "me-0000000000000000", nickname: "Me", status: "online" },
    { client_id: 2, unique_id: "alex-a-11111111111111", nickname: "Alex", status: "online", channel_id: 7 },
    { client_id: 3, unique_id: "alex-b-22222222222222", nickname: "Alex", status: "away", channel_id: 0 },
    { client_id: 4, unique_id: "blocked-333333333333", nickname: "Troll", status: "online" },
    { client_id: 5, unique_id: "ghost", nickname: "Ghost", status: "invisible" },
];
const contacts = [
    { unique_id: "alex-b-22222222222222", label: "" },
    { unique_id: "offline-friend-44444", label: "Dana", nick_history: ["Dan", "Dana"] },
    { unique_id: "old-nick-555555555555", nick_history: ["Olaf"] },
];
const conversations = [{ uid: "chatty-6666666666666", name: "Chatty" }, { uid: "alex-a-11111111111111", name: "Alex" }];
const groups = [{ id: "g1", name: "Raid night", members: [{ unique_id: "alex-b-22222222222222" }, { unique_id: "pending-77777777777", pending: true }, { unique_id: "me-0000000000000000" }] }];
const build = () => buildMemberCandidates({ clients, contacts, conversations, groups, blocked: ["blocked-333333333333"], me: "me-0000000000000000", channels, server: "Home server" });

test("candidates merge online members, contacts, conversations and groups without self or blocked accounts", () => {
    const list = build();
    const ids = list.map(entry => entry.uid).sort();
    assert.deepEqual(ids, ["alex-a-11111111111111", "alex-b-22222222222222", "chatty-6666666666666", "ghost", "offline-friend-44444", "old-nick-555555555555"]);
    const a = list.find(entry => entry.uid === "alex-a-11111111111111");
    assert.equal(a.online, true); assert.equal(a.presence, "online"); assert.equal(a.channel, "Lounge"); assert.equal(a.conversation, true); assert.equal(a.friend, false);
    const b = list.find(entry => entry.uid === "alex-b-22222222222222");
    assert.equal(b.presence, "away"); assert.equal(b.friend, true); assert.deepEqual(b.groups, ["Raid night"]); assert.equal(b.channel, "");
    const dana = list.find(entry => entry.uid === "offline-friend-44444");
    assert.equal(dana.name, "Dana"); assert.equal(dana.online, false); assert.equal(dana.friend, true);
    assert.equal(list.find(entry => entry.uid === "old-nick-555555555555").name, "Olaf", "falls back to the newest nickname seen");
    assert.equal(list.find(entry => entry.uid === "ghost").presence, "offline", "invisible members read as offline");
    assert.ok(list.every(entry => entry.server === "Home server"));
});

test("a member known only by ID is named by the short ID", () => {
    const [entry] = buildMemberCandidates({ conversations: [{ uid: "abcdefghijklmnopqrstuvwxyz" }] });
    assert.equal(entry.name, "abcdef…wxyz");
    assert.equal(shortAccountID("short"), "short");
});

test("ranking prefers name prefixes, then word starts, then IDs and context, and breaks ties by relationship", () => {
    const list = build();
    const alex = rankMemberCandidates(list, "ale");
    assert.deepEqual(alex.map(entry => entry.uid), ["alex-a-11111111111111", "alex-b-22222222222222"], "existing conversation before friend");
    assert.deepEqual(rankMemberCandidates(list, "raid").map(entry => entry.uid), ["alex-b-22222222222222"], "group names match with a lower weight");
    assert.deepEqual(rankMemberCandidates(list, "lounge").map(entry => entry.uid), ["alex-a-11111111111111"], "voice channel matches");
    assert.deepEqual(rankMemberCandidates(list, "chatty-6666").map(entry => entry.uid), ["chatty-6666666666666"], "a pasted ID prefix matches");
    assert.deepEqual(rankMemberCandidates(list, "nobody"), []);
});

test("without a query the list leads with conversations, then friends, then online members, within the limit", () => {
    const list = build();
    const order = rankMemberCandidates(list, "").map(entry => entry.uid);
    assert.deepEqual(order.slice(0, 2).sort(), ["alex-a-11111111111111", "chatty-6666666666666"]);
    assert.equal(order.indexOf("ghost"), order.length - 1);
    assert.equal(rankMemberCandidates(list, "", { limit: 2 }).length, 2);
    assert.ok(!rankMemberCandidates(list, "", { exclude: ["ghost"] }).some(entry => entry.uid === "ghost"));
});

test("context facts distinguish same-named members: conversation, friend, channel or group, server and short ID", () => {
    const list = build();
    const duplicates = duplicateNames(list);
    assert.ok(duplicates.has("alex"));
    assert.ok(!duplicates.has("dana"));
    const a = candidateContext(list.find(entry => entry.uid === "alex-a-11111111111111"), { sameName: true });
    assert.deepEqual(a, [{ kind: "conversation" }, { kind: "channel", value: "Lounge" }, { kind: "server", value: "Home server" }, { kind: "handle", value: "alex-a…1111" }]);
    const b = candidateContext(list.find(entry => entry.uid === "alex-b-22222222222222"), { sameName: true });
    assert.deepEqual(b.map(fact => fact.kind), ["friend", "group", "server", "handle"]);
    const lone = candidateContext({ uid: "x", name: "X", handle: "x", server: "", channel: "", groups: [], friend: false, conversation: false });
    assert.deepEqual(lone, [{ kind: "handle", value: "x" }], "the ID is always shown when little else is known");
});

test("looksLikeAccountID accepts one long token and rejects names", () => {
    assert.equal(looksLikeAccountID("alex-a-11111111111111"), true);
    assert.equal(looksLikeAccountID("Alex"), false);
    assert.equal(looksLikeAccountID("alex smith jr"), false);
    assert.equal(looksLikeAccountID(""), false);
});
