import test from "node:test";
import assert from "node:assert/strict";
import { ALL_SCOPE_LIMIT, formatFileSize, groupUnifiedResults, normalizeScope, scoreText, searchChannels, searchFiles, searchMembers, suggestScope, UNIFIED_SCOPES } from "../src/unified-search.js";

const channels = [
    { ChannelID: 1, Name: "General", TextOnly: false, Topic: "Everything release related" },
    { ChannelID: 2, Name: "release-notes", TextOnly: true },
    { ChannelID: 3, Name: "Dev Lounge", TextOnly: false },
];
const clients = [
    { unique_id: "me", nickname: "Me" },
    { unique_id: "rel-1", nickname: "Reliable Rita", channel_id: 3 },
    { unique_id: "bad", nickname: "Release Bot" },
    { unique_id: "u-release-2", nickname: "Tom" },
];
const files = [
    { name: "release-plan.pdf", folder: "docs", size: 2048, uploader: "Rita" },
    { name: "photo.png", folder: "release", size: 1024 * 1024 * 3, uploader: "Tom" },
    { name: "notes.txt", folder: "", size: 12, uploader: "Release Bot" },
    { name: "unrelated.zip", folder: "", size: 1 },
];

test("scopes normalise and scoreText ranks prefix, word start, substring, subsequence", () => {
    assert.deepEqual(UNIFIED_SCOPES, ["all", "messages", "files", "members", "channels"]);
    assert.equal(normalizeScope("files"), "files");
    assert.equal(normalizeScope("bogus"), "all");
    assert.equal(normalizeScope(undefined), "all");
    assert.equal(scoreText("rel", "release"), 100);
    assert.equal(scoreText("rel", "the release"), 90);
    assert.equal(scoreText("rel", "unreleased"), 80);
    assert.ok(scoreText("rls", "release") > 0 && scoreText("rls", "release") < 80);
    assert.equal(scoreText("zzz", "release"), -1);
    assert.equal(scoreText("", "anything"), 0);
    assert.equal(scoreText("Kanäle", "kanaele"), 100);
});

test("members match by name or ID, skip self and blocked, and carry the voice channel", () => {
    const hits = searchMembers(clients, "rel", { me: "me", blocked: ["bad"], channels });
    assert.deepEqual(hits.map(hit => hit.uid), ["rel-1", "u-release-2"]);
    assert.equal(hits[0].channel, "Dev Lounge");
    assert.equal(hits[0].kind, "member");
    assert.equal(searchMembers(clients, "me", { me: "me" }).length, 0);
});

test("channels match by name first and by topic with a lower weight", () => {
    const hits = searchChannels(channels, "release");
    assert.deepEqual(hits.map(hit => hit.id), [2, 1]);
    assert.equal(hits[0].textOnly, true);
    assert.equal(hits[1].topic, "Everything release related");
    assert.deepEqual(searchChannels(channels, "lounge").map(hit => hit.name), ["Dev Lounge"]);
});

test("files match by name, folder and uploader in that order", () => {
    const hits = searchFiles(files, "release", { channelID: 4 });
    assert.deepEqual(hits.map(hit => hit.name), ["release-plan.pdf", "photo.png", "notes.txt"]);
    assert.ok(hits.every(hit => hit.channelID === 4 && hit.kind === "file"));
    assert.equal(searchFiles(files, "zip").length, 1);
});

test("the All scope groups every non-empty kind, truncated with totals; a specific scope lists everything", () => {
    const messages = Array.from({ length: 8 }, (_, index) => ({ id: index + 1, body: "release" }));
    const grouped = groupUnifiedResults({ messages, files: searchFiles(files, "release"), members: searchMembers(clients, "release", { me: "me" }), channels: [] });
    assert.equal(grouped.scope, "all");
    assert.deepEqual(grouped.groups.map(group => group.kind), ["messages", "files", "members"]);
    assert.equal(grouped.groups[0].items.length, ALL_SCOPE_LIMIT);
    assert.equal(grouped.groups[0].total, 8);
    assert.equal(grouped.groups[0].truncated, true);
    assert.equal(grouped.groups[1].truncated, false);
    assert.deepEqual(grouped.counts, { messages: 8, files: 3, members: 2, channels: 0, all: 13 });
    assert.equal(grouped.total, 13);

    const onlyMessages = groupUnifiedResults({ messages, files: [{ name: "x" }] }, { scope: "messages" });
    assert.deepEqual(onlyMessages.groups.map(group => group.kind), ["messages"]);
    assert.equal(onlyMessages.groups[0].items.length, 8);
    assert.equal(onlyMessages.groups[0].truncated, false);
    assert.equal(onlyMessages.total, 8);

    const emptyScope = groupUnifiedResults({ messages }, { scope: "channels" });
    assert.deepEqual(emptyScope.groups, [{ kind: "channels", items: [], total: 0, truncated: false }], "an empty specific scope still reports its group");
    assert.equal(emptyScope.total, 0);
});

test("suggestScope reads concept words, so 'Dateien bericht' lands in files and 'Mitglied alex' in members", () => {
    assert.equal(suggestScope("Dateien bericht"), "files");
    assert.equal(suggestScope("member alex"), "members");
    assert.equal(suggestScope("DM alex"), "members");
    assert.equal(suggestScope("Kanal dev"), "channels");
    assert.equal(suggestScope("alex"), "");
});

test("file sizes format compactly", () => {
    assert.equal(formatFileSize(12), "12 B");
    assert.equal(formatFileSize(2048), "2.0 KB");
    assert.equal(formatFileSize(300 * 1024), "300 KB");
    assert.equal(formatFileSize(3 * 1024 * 1024), "3.0 MB");
    assert.equal(formatFileSize(2.5 * 1024 ** 3), "2.50 GB");
});
