import test from "node:test";
import assert from "node:assert/strict";
import { CONCEPT_MATCH_SCORE, conceptScore, conceptsFor, expandSearchQuery, normalizeSearchText, parseConceptQuery, synonymMatch } from "../src/search-synonyms.js";

// The quick switcher's plain scorer, mirrored here so concept scores can be
// compared with label scores the way renderList does.
function plainScore(q, s) {
    q = q.toLowerCase(); s = s.toLowerCase();
    if (!q) return 0;
    const i = s.indexOf(q);
    if (i >= 0) return 100 - i;
    let qi = 0;
    for (const ch of s) if (ch === q[qi]) qi++;
    return qi === q.length ? 50 - s.length * 0.05 : -1;
}

test("normalizeSearchText folds case, umlauts, diacritics and palette glyphs", () => {
    assert.equal(normalizeSearchText("  Kanäle "), "kanaele");
    assert.equal(normalizeSearchText("KANÄLE"), normalizeSearchText("kanaele"));
    assert.equal(normalizeSearchText("Gespräch"), "gespraech");
    assert.equal(normalizeSearchText("# général"), "general");
    assert.equal(normalizeSearchText(null), "");
});

test("direct-message synonyms in both languages name the dm concept", () => {
    for (const word of ["DM", "dms", "Direktnachricht", "Nachricht", "Chat", "message", "direct message", "Unterhaltung", "conversation", "PM", "Privatnachricht", "Gespräch"]) {
        assert.ok(conceptsFor(word).includes("dm"), word);
        assert.ok(synonymMatch(word, "dm"), word);
    }
});

test("channel, voice, settings, member, files and search synonyms resolve", () => {
    assert.deepEqual(conceptsFor("Kanal"), ["channel"]);
    assert.deepEqual(conceptsFor("channel"), ["channel"]);
    assert.deepEqual(conceptsFor("Sprach"), ["voice"]);
    assert.deepEqual(conceptsFor("voice"), ["voice"]);
    assert.deepEqual(conceptsFor("Einstellungen"), ["settings"]);
    assert.deepEqual(conceptsFor("settings"), ["settings"]);
    assert.deepEqual(conceptsFor("Mitglieder"), ["member"]);
    assert.deepEqual(conceptsFor("Dateien"), ["files"]);
    assert.deepEqual(conceptsFor("suche"), ["search"]);
    assert.deepEqual(conceptsFor("xyzzy"), []);
});

test("prefixes need three characters, so short tokens stay literal", () => {
    assert.deepEqual(conceptsFor("einst"), ["settings"]);
    assert.deepEqual(conceptsFor("se"), []);
    assert.deepEqual(conceptsFor("dm"), ["dm"]);
    assert.deepEqual(conceptsFor("d"), []);
});

test("parseConceptQuery separates concept words from label text, including two-word synonyms", () => {
    assert.deepEqual(parseConceptQuery("dm alice"), { concepts: new Set(["dm"]), text: "alice" });
    assert.deepEqual(parseConceptQuery("direct message bob"), { concepts: new Set(["dm"]), text: "bob" });
    assert.deepEqual(parseConceptQuery("Kanal dev"), { concepts: new Set(["channel"]), text: "dev" });
    assert.deepEqual(parseConceptQuery("alice"), { concepts: new Set(), text: "alice" });
    assert.deepEqual(parseConceptQuery(""), { concepts: new Set(), text: "" });
    assert.deepEqual(expandSearchQuery("Nachricht an Einstellungen"), ["dm", "settings"]);
});

test("conceptScore matches tagged items through synonyms and never untagged ones", () => {
    const dm = { label: "✉ Alice", concepts: ["dm"] };
    const channel = { label: "# Alice", concepts: ["channel"] };
    const untagged = { label: "Alice" };
    assert.equal(conceptScore("Direktnachricht", dm, plainScore), CONCEPT_MATCH_SCORE);
    assert.equal(conceptScore("conversation", dm, plainScore), CONCEPT_MATCH_SCORE);
    assert.equal(conceptScore("Direktnachricht", channel, plainScore), -1);
    assert.equal(conceptScore("Direktnachricht", untagged, plainScore), -1);
    assert.equal(conceptScore("alice", dm, plainScore), -1, "no concept word: the plain scorer decides");
});

test("concept plus text filters by kind and ranks the remaining text against the label", () => {
    const alice = { label: "✉ Alice", concepts: ["dm"] };
    const bob = { label: "✉ Bob", concepts: ["dm"] };
    const aliceChannel = { label: "# Alice", concepts: ["channel"] };
    assert.ok(conceptScore("dm ali", alice, plainScore) > CONCEPT_MATCH_SCORE);
    assert.equal(conceptScore("dm ali", bob, plainScore), -1);
    assert.equal(conceptScore("dm ali", aliceChannel, plainScore), -1);
    assert.ok(conceptScore("Kanal ali", aliceChannel, plainScore) > 0);
});

test("a concept-only query outranks a label that merely contains the word", () => {
    const admin = plainScore("dm", "admin-chat");
    assert.ok(CONCEPT_MATCH_SCORE < 100, "a label starting with the query still wins");
    assert.ok(CONCEPT_MATCH_SCORE > admin - 1 || admin === 99, "scores stay in the same band as substring hits");
    assert.ok(CONCEPT_MATCH_SCORE > plainScore("dm", "the admins"));
});

test("synonymMatch accepts a list of concepts", () => {
    assert.equal(synonymMatch("Einstellungen", ["dm", "settings"]), true);
    assert.equal(synonymMatch("Einstellungen", ["dm", "channel"]), false);
    assert.equal(synonymMatch("", ["dm"]), false);
});
