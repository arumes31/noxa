import assert from "node:assert/strict";
import test from "node:test";
import { shortcutFormat, toggleFormat } from "../src/composer-format.js";
import { renderMarkdown } from "../src/markdown.js";

test("wraps a selection and keeps it on the same text", () => {
    assert.deepEqual(toggleFormat("say hello now", 4, 9, "**"), { value: "say **hello** now", start: 6, end: 11 });
    assert.deepEqual(toggleFormat("a b", 0, 1, "`"), { value: "`a` b", start: 1, end: 2 });
});

test("toggling again removes the markers, inside or around the selection", () => {
    assert.deepEqual(toggleFormat("say **hello** now", 6, 11, "**"), { value: "say hello now", start: 4, end: 9 });
    assert.deepEqual(toggleFormat("say **hello** now", 4, 13, "**"), { value: "say hello now", start: 4, end: 9 });
});

test("italic does not eat one star of bold text", () => {
    // Selecting the word inside **bold** and pressing italic wraps it again.
    assert.deepEqual(toggleFormat("**bold**", 2, 6, "*"), { value: "***bold***", start: 3, end: 7 });
    assert.deepEqual(toggleFormat("*it*", 1, 3, "*"), { value: "it", start: 0, end: 2 });
});

test("an empty selection or bad input changes nothing", () => {
    assert.equal(toggleFormat("text", 2, 2, "**"), null);
    assert.equal(toggleFormat("text", 3, 1, "**"), null);
    assert.equal(toggleFormat(null, 0, 1, "**"), null);
    assert.equal(toggleFormat("text", 0, 1, ""), null);
});

test("Ctrl/Cmd+B, I and U map to formats; other chords do not", () => {
    assert.equal(shortcutFormat({ ctrlKey: true, key: "b" }).id, "bold");
    assert.equal(shortcutFormat({ metaKey: true, key: "I" }).id, "italic");
    assert.equal(shortcutFormat({ ctrlKey: true, key: "u" }).id, "underline");
    assert.equal(shortcutFormat({ ctrlKey: true, shiftKey: true, key: "b" }), null);
    assert.equal(shortcutFormat({ ctrlKey: true, altKey: true, key: "b" }), null);
    assert.equal(shortcutFormat({ key: "b" }), null);
    assert.equal(shortcutFormat({ ctrlKey: true, key: "k" }), null);
});

test("italic grows to the whole word, which is what the renderer shows", () => {
    assert.deepEqual(toggleFormat("hello world", 0, 3, "*"), { value: "*hello* world", start: 1, end: 6 });
    assert.deepEqual(toggleFormat("say hello", 5, 7, "*"), { value: "say *hello*", start: 5, end: 10 });
});

test("spaces stay outside the markers and lines are wrapped one by one", () => {
    assert.deepEqual(toggleFormat("say hello now", 3, 10, "**"), { value: "say **hello** now", start: 6, end: 11 });
    assert.deepEqual(toggleFormat("one\ntwo", 0, 7, "**"), { value: "**one**\n**two**", start: 0, end: 15 });
    assert.deepEqual(toggleFormat("one\n\ntwo", 0, 8, "~~"), { value: "~~one~~\n\n~~two~~", start: 0, end: 16 });
});

test("text that already contains the marker character is not wrapped", () => {
    assert.equal(toggleFormat("a*b", 0, 3, "**"), null);
    assert.equal(toggleFormat("   ", 0, 3, "**"), null);
    assert.deepEqual(toggleFormat("a*b\nc", 0, 5, "**"), { value: "a*b\n**c**", start: 0, end: 9 });
});

test("formatted selections render as formatting, never as literal markers", () => {
    const cases = [
        [toggleFormat("hello world", 0, 3, "*").value, "<em>hello</em>"],
        [toggleFormat("one\ntwo", 0, 7, "**").value, "<strong>two</strong>"],
        [toggleFormat("say hello now", 3, 10, "__").value, "<u>hello</u>"],
        [toggleFormat("**bold**", 2, 6, "*").value, "<em><strong>bold</strong></em>"],
    ];
    for (const [text, html] of cases) {
        const out = renderMarkdown(text);
        assert.ok(out.includes(html), `${JSON.stringify(text)} rendered ${out}`);
        // No marker character may remain in any text between tags.
        assert.ok(!/(?:^|>)[^<]*[*_~]/.test(out), `literal marker left in ${out}`);
    }
});
