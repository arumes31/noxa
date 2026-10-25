import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { parseFileRef, transformCustomEmoji } from "../src/chat-parsers.js";

describe("parseFileRef", () => {
    it("matches the Go parser for legacy, partial, encrypted, and empty captures", () => {
        assert.deepEqual(parseFileRef("photo.png"), {
            storage: "photo.png", key: "", name: "photo.png", valid: true, legacy: true, meta: null,
        });
        assert.deepEqual(parseFileRef("legacy#name"), {
            storage: "legacy#name", key: "", name: "legacy#name", valid: true, legacy: true, meta: null,
        });
        assert.deepEqual(parseFileRef("blob#key#photo.png"), {
            storage: "blob", key: "key", name: "photo.png", valid: true, legacy: false, meta: null,
        });
        assert.deepEqual(parseFileRef("#key#"), {
            storage: "", key: "key", name: "", valid: true, legacy: false, meta: null,
        });
        assert.deepEqual(parseFileRef("blob#key#name#with#hash"), {
            storage: "blob", key: "key", name: "name#with#hash", valid: true, legacy: false, meta: null,
        });
        assert.deepEqual(parseFileRef(null), {
            storage: "", key: "", name: "", valid: false, legacy: false, meta: null,
        });
    });
});

describe("transformCustomEmoji", () => {
    const url = "data:image/png;base64,aGVsbG8=";

    it("replaces every text token while preserving markup and attribute values", () => {
        const output = transformCustomEmoji(
            '<a data-note=":wave:">:wave:</a> :wave: <code>:wave:</code>',
            [{ name: "wave" }], new Map([["wave", url]]),
        );
        assert.equal((output.match(/class="md-emoji"/g) || []).length, 2);
        assert.match(output, /data-note=":wave:"/);
        assert.match(output, /<code>:wave:<\/code>/);
    });

    it("rejects unsafe URLs and escapes hostile names without producing markup", () => {
        assert.equal(
            transformCustomEmoji(":wave:", [{ name: "wave" }], new Map([["wave", "javascript:alert(1)"]])),
            ":wave:",
        );
        const output = transformCustomEmoji(
            ':x" onerror="boom:', [{ name: 'x" onerror="boom' }], { 'x" onerror="boom': url },
        );
        assert.match(output, /alt=":x&quot; onerror=&quot;boom:"/);
        assert.doesNotMatch(output, / onerror="/);
    });

    it("handles absent/invalid inputs without changing the source", () => {
        assert.equal(transformCustomEmoji(":wave:", null, new Map()), ":wave:");
        assert.equal(transformCustomEmoji(null, [{ name: "wave" }], new Map([["wave", url]])), "");
        assert.equal(transformCustomEmoji(":wave:", [{}, ""], new Map()), ":wave:");
    });

    it("marks referenced emoji that are not loaded yet and reports each once", () => {
        const missing = [];
        const output = transformCustomEmoji(
            ":wave: :hello: :hello: :fresh: :bad: <code>:late:</code>",
            [{ name: "wave" }, { name: "hello" }, { name: "fresh" }, { name: "bad" }, { name: "late" }, { name: "unused" }],
            new Map([["wave", url], ["hello", ""], ["bad", "javascript:alert(1)"]]),
            name => missing.push(name),
        );
        // Loading ("") and never-requested (undefined) names are reported and
        // marked; a rejected URL, a code-only token and an unreferenced emoji are not.
        assert.deepEqual(missing, ["hello", "fresh"]);
        assert.equal((output.match(/class="md-emoji"/g) || []).length, 1);
        assert.equal((output.match(/<span class="md-emoji-pending" data-emoji="hello">:hello:<\/span>/g) || []).length, 2);
        assert.match(output, /<span class="md-emoji-pending" data-emoji="fresh">:fresh:<\/span>/);
        assert.match(output, / :bad: /);
        assert.match(output, /<code>:late:<\/code>/);
        assert.doesNotMatch(output, /data-emoji="(?:bad|late|unused)"/);
        // The callback is optional, and the placeholder escapes hostile names.
        const hostile = transformCustomEmoji(':x" onerror="boom:', [{ name: 'x" onerror="boom' }], new Map());
        assert.match(hostile, /data-emoji="x&quot; onerror=&quot;boom"/);
        assert.doesNotMatch(hostile, / onerror="/);
    });
});
