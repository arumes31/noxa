import assert from "node:assert/strict";
import { test } from "node:test";

import { parseFileTokens } from "../src/core/files.js";
import { parseMarkdown } from "../src/core/markdown.js";
import {
    dayKey, daysBetween, displayURL, insertText, isBlocked, mediaKind, mimeOf, previewLink, previewRequest, relativeTime, safeURL, searchEmoji, splitFileTokens,
} from "../src/core/message-format.js";
import { shareText } from "../src/core/share-store.js";

const MASTER = Buffer.alloc(32, 7).toString("base64");

test("file tokens split the body, markdown keeps the text in between", () => {
    const body = `look **here** [file:0123456789abcdef0123456789abcdef.vcx#${MASTER}#nx1;s=5#cat.png] and \`code\``;
    const segments = splitFileTokens(body, parseFileTokens(body));
    assert.deepEqual(segments.map(s => s.type), ["text", "file", "text"]);
    assert.equal(segments[0].text, "look **here**");
    assert.equal(segments[1].ref.name, "cat.png");
    assert.equal(segments[1].ref.size, 5);
    assert.equal(segments[2].text, "and `code`");
    assert.equal(parseMarkdown(segments[0].text)[1].type, "strong");
});

test("a body without tokens stays one untouched text segment", () => {
    assert.deepEqual(splitFileTokens("  hi\nthere ", []), [{ type: "text", text: "  hi\nthere " }]);
    assert.deepEqual(splitFileTokens("", []), []);
});

test("only http(s) URLs become link targets", () => {
    assert.equal(safeURL("https://example.com/a?b=1"), "https://example.com/a?b=1");
    assert.equal(safeURL("http://example.com"), "http://example.com/");
    assert.equal(safeURL("javascript:alert(1)"), null);
    assert.equal(safeURL("data:text/html,x"), null);
    assert.equal(safeURL("not a url"), null);
});

test("long URLs are shortened to 60 characters with an ellipsis", () => {
    const short = "https://example.com/x";
    assert.equal(displayURL(short), short);
    const long = `https://example.com/${"a".repeat(80)}/end`;
    const shown = displayURL(long);
    assert.equal(shown.length, 60);
    assert.ok(shown.includes("…"));
    assert.ok(shown.startsWith("https://example.com/"));
    assert.ok(shown.endsWith("/end"));
});

test("the first previewable link is chosen", () => {
    assert.equal(previewLink(parseMarkdown("see http://plain.example and https://youtu.be/abc")), "https://youtu.be/abc");
    assert.equal(previewLink(parseMarkdown("`https://in.code` nothing")), null);
});

test("preview requests carry the ID the server expects per provider", () => {
    assert.deepEqual(previewRequest("https://youtu.be/dQw4w9WgXcQ"), { provider: "youtube", resource: "dQw4w9WgXcQ" });
    assert.deepEqual(previewRequest("https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=3"), { provider: "youtube", resource: "dQw4w9WgXcQ" });
    assert.deepEqual(previewRequest("https://x.com/someone/status/1234567"), { provider: "x", resource: "1234567" });
    assert.deepEqual(previewRequest("https://www.twitch.tv/somechannel"), { provider: "twitch", resource: "channel:somechannel" });
    assert.deepEqual(previewRequest("https://clips.twitch.tv/FunnyClip"), { provider: "twitch", resource: "clip:FunnyClip" });
    assert.deepEqual(previewRequest("https://vimeo.com/76979871"), { provider: "vimeo", resource: "76979871" });
    assert.deepEqual(previewRequest("https://dai.ly/x8abc12"), { provider: "dailymotion", resource: "x8abc12" });
    assert.deepEqual(previewRequest("https://example.com/page"), { provider: "web", resource: "https://example.com/page" });
    assert.equal(previewRequest("http://example.com/page"), null);
    assert.equal(previewRequest("https://user:pw@example.com/"), null);
});

test("media kinds and MIME types follow the file name", () => {
    assert.equal(mediaKind("a.PNG"), "image");
    assert.equal(mediaKind("clip.webm"), "video");
    assert.equal(mediaKind("notes.txt"), "");
    assert.equal(mimeOf("x.jpg"), "image/jpeg");
    assert.equal(mimeOf("x.mp4"), "video/mp4");
    assert.equal(mimeOf("x.bin"), "application/octet-stream");
});

test("day keys and separators use local days", () => {
    const noon = new Date(2026, 9, 24, 12, 0, 0).getTime() / 1000;
    assert.equal(dayKey(noon), "2026-10-24");
    assert.equal(daysBetween("2026-10-23", "2026-10-24"), 1);
    assert.equal(daysBetween("2026-02-28", "2026-03-01"), 1);
    assert.equal(daysBetween("2026-10-24", "2026-10-24"), 0);
});

test("relative times are short and localized", () => {
    const now = 1_800_000_000;
    assert.match(relativeTime(now - 5, now, "en"), /now/);
    assert.match(relativeTime(now - 120, now, "en"), /2 min/);
    assert.match(relativeTime(now - 7200, now, "en"), /2 hr/);
    assert.match(relativeTime(now - 7200, now, "de"), /2 Std/);
});

test("blocked senders are matched by unique ID", () => {
    assert.equal(isBlocked("u1", ["u1", "u2"]), true);
    assert.equal(isBlocked("u3", ["u1"]), false);
    assert.equal(isBlocked("", [""]), false);
    assert.equal(isBlocked("u1", undefined), false);
});

test("emoji search finds shortcodes and custom server emoji", () => {
    const found = searchEmoji("thumbs", ["thumbsparty", "cat"]);
    assert.deepEqual(found.builtin.map(e => e.char), ["👍", "👎"]);
    assert.deepEqual(found.custom, ["thumbsparty"]);
    assert.deepEqual(searchEmoji(":cat:", ["cat"]).custom, ["cat"]);
    assert.deepEqual(searchEmoji("  ", ["cat"]), { builtin: [], custom: [] });
});

test("inserting at the caret replaces the selection", () => {
    assert.deepEqual(insertText("hello world", 6, 11, "😀"), { value: "hello 😀", caret: 8 });
    assert.deepEqual(insertText("ab", 1, 1, ":party:"), { value: "a:party:b", caret: 8 });
});

test("shared title, text and url join without repeats", () => {
    assert.equal(shareText({ title: "Page", text: "Look https://x.example", url: "https://x.example" }), "Page\nLook https://x.example");
    assert.equal(shareText({ url: "https://y.example" }), "https://y.example");
    assert.equal(shareText({}), "");
});
