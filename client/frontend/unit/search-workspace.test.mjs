import test from "node:test";
import assert from "node:assert/strict";
import { filterSearchResults, searchTimestamp, searchSnippet } from "../src/search-workspace.js";

const now = Date.parse("2026-10-21T12:00:00Z");
const messages = [
    { id: 1, from_unique_id: "a", from_nickname: "Same name", body: "weekend weekend [file:blob#key#notes.txt]", sent_at: now / 1000 - 3600 },
    { id: 2, from_unique_id: "b", from_nickname: "Same name", body: "weekend", sent_at: now / 1000 - 2 * 86400 },
    { id: 3, from_unique_id: "a", body: "weekend", sent_at: "2026-09-01T10:00:00Z" },
];
test("search filters combine stable author identity, date and attachments", () => {
    assert.deepEqual(filterSearchResults(messages, { author: "a", days: 7, attachments: true, now }).map(m => m.id), [1]);
    assert.deepEqual(filterSearchResults(messages, { author: "b", now }).map(m => m.id), [2]);
    assert.equal(filterSearchResults(messages, { author: "a", days: 1, attachments: true, now: now + 2 * 86400000 }).length, 0);
});
test("search ordering supports newest, oldest and relevance without mutating results", () => {
    assert.deepEqual(filterSearchResults(messages, { sort: "oldest" }).map(m => m.id), [3, 2, 1]);
    assert.deepEqual(filterSearchResults([...messages].reverse(), { sort: "relevance", query: "weekend" }).map(m => m.id), [1, 2, 3]);
    assert.deepEqual(messages.map(m => m.id), [1, 2, 3]);
    assert.equal(searchTimestamp(messages[0]), now - 3600000);
    assert.equal(searchTimestamp({ sent_at: "invalid" }), 0);
});

test("search snippets show filenames without exposing attachment storage keys", () => {
    const text = searchSnippet({ body: "Read [file:storage.vcx#VERY_SECRET_KEY#notes.txt] and [file:legacy.pdf]" });
    assert.match(text, /notes\.txt/);
    assert.match(text, /legacy\.pdf/);
    assert.ok(!text.includes("VERY_SECRET_KEY"));
    assert.ok(!text.includes("storage.vcx"));
    assert.equal(searchSnippet({ body: "plain message" }), "plain message");
});
