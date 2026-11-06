import assert from "node:assert/strict";
import test from "node:test";
import { shortcutFormat, toggleFormat } from "../src/composer-format.js";

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
