import assert from "node:assert/strict";
import { test } from "node:test";

import { linksIn, parseMarkdown, plainText, previewProvider } from "../src/core/markdown.js";

test("inline markup nests and keeps text verbatim", () => {
    const nodes = parseMarkdown("say **hi *there*** and __under__ ~~gone~~ `x < y`");
    assert.equal(nodes[0].text, "say ");
    assert.equal(nodes[1].type, "strong");
    assert.deepEqual(plainText(nodes[1].children), "hi there");
    assert.equal(nodes[1].children[1].type, "em");
    assert.equal(nodes.find(n => n.type === "u").children[0].text, "under");
    assert.equal(nodes.find(n => n.type === "del").children[0].text, "gone");
    assert.deepEqual(nodes.at(-1), { type: "inline-code", code: "x < y" });
});

test("code blocks are opaque and keep their language", () => {
    const nodes = parseMarkdown("before\n```js\nlet **x** = 1;\n```\nafter");
    assert.equal(nodes[0].text, "before");
    assert.deepEqual(nodes[1], { type: "code", code: "let **x** = 1;", language: "js" });
    assert.equal(nodes[2].text, "after");
});

test("links are detected without trailing punctuation and listed once", () => {
    const nodes = parseMarkdown("see https://example.com/a?b=1). and https://example.com/a?b=1, (https://en.wikipedia.org/wiki/Foo_(bar))");
    const links = linksIn(nodes);
    assert.deepEqual(links, ["https://example.com/a?b=1", "https://en.wikipedia.org/wiki/Foo_(bar)"]);
    assert.equal(nodes.filter(n => n.type === "link").length, 3);
});

test("shortcodes, custom emoji and mentions become nodes", () => {
    const nodes = parseMarkdown(":fire: :party: @rowan @nobody @everyone", { customEmoji: new Set(["party"]), mentionNames: new Map([["rowan", "uid-1"]]) });
    assert.deepEqual(nodes[0], { type: "emoji", name: "fire", char: "🔥" });
    assert.deepEqual(nodes[2], { type: "custom-emoji", name: "party" });
    assert.deepEqual(nodes[4], { type: "mention", name: "rowan", uid: "uid-1", everyone: false });
    assert.equal(nodes[5].text, " @nobody ");
    assert.deepEqual(nodes[6], { type: "mention", name: "everyone", uid: "", everyone: true });
});

test("tables parse header and rows and newlines become breaks", () => {
    const nodes = parseMarkdown("a\nb\n| h1 | h2 |\n| --- | --- |\n| 1 | **2** |\n| 3 | 4 |\ntail");
    assert.equal(nodes[0].text, "a");
    assert.equal(nodes[1].type, "br");
    const table = nodes.find(n => n.type === "table");
    assert.equal(plainText(table.header[1]), "h2");
    assert.equal(table.rows.length, 2);
    assert.equal(table.rows[0][1][0].type, "strong");
    assert.equal(plainText(nodes), "a\nb\nh1 | h2\n1 | 2\n3 | 4\ntail");
});

test("preview providers are classified by host", () => {
    assert.equal(previewProvider("https://youtu.be/abc"), "youtube");
    assert.equal(previewProvider("https://www.youtube.com/watch?v=1"), "youtube");
    assert.equal(previewProvider("https://x.com/a/status/1"), "x");
    assert.equal(previewProvider("https://clips.twitch.tv/x"), "twitch");
    assert.equal(previewProvider("https://vimeo.com/1"), "vimeo");
    assert.equal(previewProvider("https://example.org/page"), "web");
    assert.equal(previewProvider("http://example.org/page"), null);
    assert.equal(previewProvider("not a url"), null);
});
