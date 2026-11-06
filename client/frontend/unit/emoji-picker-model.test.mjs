import assert from "node:assert/strict";
import test from "node:test";
import { MAX_FAVORITES, MAX_RECENT, emojiIndex, recordEmojiUse, searchEmoji, storedEmoji, toggleFavorite } from "../src/emoji-picker-model.js";

const builtIn = { smile: "😄", heart_eyes: "😍", heart: "❤️", thumbsup: "👍", "+1": "👍", sweat_smile: "😅" };

test("search matches shortcodes, German keywords and custom emoji", () => {
    const index = emojiIndex(builtIn, ["party_parrot"]);
    assert.deepEqual(searchEmoji(index, "smile").map(e => e.emoji), ["😄", "😅"]);
    assert.deepEqual(searchEmoji(index, ":heart:").map(e => e.emoji), ["❤️", "😍"], "the exact shortcode ranks first");
    assert.deepEqual(searchEmoji(index, "Herz").map(e => e.emoji), ["❤️", "😍"], "an exact keyword beats herzaugen");
    assert.deepEqual(searchEmoji(index, "daumen hoch").map(e => e.emoji), ["👍"]);
    assert.deepEqual(searchEmoji(index, "parrot").map(e => e.emoji), [":party_parrot:"]);
    assert.deepEqual(searchEmoji(index, "sweat smile").map(e => e.emoji), ["😅"]);
    assert.deepEqual(searchEmoji(index, "   "), []);
    assert.equal(emojiIndex(builtIn).filter(e => e.emoji === "👍").length, 1, "aliases share one entry");
});

test("recents move to the front, stay unique and bounded", () => {
    let recents = [];
    for (const e of ["a", "b", "a", "c"]) recents = recordEmojiUse(recents, e);
    assert.deepEqual(recents, ["c", "a", "b"]);
    for (let i = 0; i < 40; i++) recents = recordEmojiUse(recents, `e${i}`);
    assert.equal(recents.length, MAX_RECENT);
    assert.equal(recents[0], "e39");
});

test("favorites toggle and stored lists are sanitised", () => {
    assert.deepEqual(toggleFavorite(["😄"], "❤️"), ["😄", "❤️"]);
    assert.deepEqual(toggleFavorite(["😄", "❤️"], "😄"), ["❤️"]);
    const many = Array.from({ length: 80 }, (_, i) => `x${i}`);
    assert.equal(toggleFavorite(many, "new").length, MAX_FAVORITES);
    assert.deepEqual(storedEmoji(["😄", 5, "", "😄", "x".repeat(65), ":ok:"], 10), ["😄", ":ok:"]);
    assert.deepEqual(storedEmoji(null, 10), []);
});
