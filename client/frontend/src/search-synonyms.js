// search-synonyms.js — concept synonyms for the quick switcher / command
// palette. A person looking for a direct message types "DM", "Nachricht",
// "conversation" or "Unterhaltung"; a channel is a "Kanal", settings are
// "Einstellungen". Every palette item carries the concepts it belongs to
// and a query word that names a concept matches all of them, so the words
// never have to appear in the item's label.
//
// Pure module: no DOM, no state. Used by chat-ui.js (quick switcher) and
// unified-search.js.

export const SEARCH_CONCEPTS = Object.freeze({
    dm: ["dm", "dms", "direktnachricht", "direktnachrichten", "direct message", "direct messages", "nachricht", "nachrichten",
        "message", "messages", "pm", "private message", "privatnachricht", "chat", "chats", "unterhaltung", "unterhaltungen",
        "conversation", "conversations", "gespräch", "gespräche", "anschreiben", "schreiben", "write"],
    group: ["gruppe", "gruppen", "group", "groups", "private gruppe", "private group", "gruppenchat", "group chat"],
    channel: ["kanal", "kanäle", "channel", "channels", "raum", "räume", "room", "rooms", "textkanal", "text channel", "text"],
    voice: ["sprach", "sprache", "sprachkanal", "sprachkanäle", "voice", "voice channel", "stimme", "audio", "talk", "call", "anruf", "beitreten", "join"],
    settings: ["einstellungen", "einstellung", "settings", "setting", "optionen", "options", "preferences", "präferenzen", "konfiguration", "config", "configuration"],
    member: ["mitglied", "mitglieder", "member", "members", "benutzer", "user", "users", "person", "people", "leute", "nutzer",
        "kontakt", "kontakte", "contact", "contacts", "friend", "friends", "freund", "freunde"],
    search: ["suche", "suchen", "search", "find", "finden", "durchsuchen"],
    files: ["datei", "dateien", "file", "files", "medien", "media", "anhang", "anhänge", "attachment", "attachments", "upload", "uploads", "bilder", "images"],
});

// normalizeSearchText lower-cases, folds German umlauts to their ASCII
// spellings (ä → ae) and strips remaining diacritics, so "Kanäle", "kanaele"
// and "KANÄLE" compare equal.
export function normalizeSearchText(text) {
    return String(text ?? "")
        .toLocaleLowerCase()
        .replace(/ä/g, "ae").replace(/ö/g, "oe").replace(/ü/g, "ue").replace(/ß/g, "ss")
        .normalize("NFD").replace(/[̀-ͯ]/g, "")
        .replace(/[#@✉⚙👥🔍]/gu, " ")
        .replace(/\s+/g, " ")
        .trim();
}

const SYNONYMS = Object.entries(SEARCH_CONCEPTS).map(([concept, words]) => [concept, words.map(normalizeSearchText)]);

// conceptsFor returns the concepts a single query word names. A word names a
// concept when it equals a synonym or is a prefix of one, at least three
// characters long ("einst" → settings, "sprach" → voice). "dm" and "pm" are
// exact-only short forms.
export function conceptsFor(word) {
    const term = normalizeSearchText(word);
    if (!term) return [];
    const found = [];
    for (const [concept, words] of SYNONYMS) {
        if (words.some(synonym => synonym === term || (term.length >= 3 && synonym.startsWith(term)))) found.push(concept);
    }
    return found;
}

// parseConceptQuery splits a query into the concepts its words name and the
// text that is left for label matching. Two-word synonyms ("direct message")
// are recognised before single words. Returns { concepts: Set, text }.
export function parseConceptQuery(query) {
    const words = normalizeSearchText(query).split(" ").filter(Boolean);
    const concepts = new Set();
    const rest = [];
    for (let index = 0; index < words.length; index++) {
        const pair = index + 1 < words.length ? `${words[index]} ${words[index + 1]}` : "";
        const pairConcepts = pair ? conceptsFor(pair).filter(concept => SYNONYMS.find(([name]) => name === concept)[1].includes(pair)) : [];
        if (pairConcepts.length) { pairConcepts.forEach(concept => concepts.add(concept)); index++; continue; }
        const single = conceptsFor(words[index]);
        if (single.length) single.forEach(concept => concepts.add(concept));
        else rest.push(words[index]);
    }
    return { concepts, text: rest.join(" ") };
}

// expandSearchQuery lists the concepts a query names, for callers that only
// need to know whether "Direktnachricht" means the same as "DM".
export function expandSearchQuery(query) {
    return [...parseConceptQuery(query).concepts];
}

// Score of an item that only matched through a concept word: between a
// label that starts with the query (100) and one that merely contains it.
export const CONCEPT_MATCH_SCORE = 96;

// conceptScore scores an item ({ label, concepts: [] }) against a query whose
// words may name concepts. It returns -1 when the query names no concept or
// the item does not belong to any of the named concepts. Remaining words are
// matched against the label by `scoreText(text, label)` (the caller's plain
// scorer); a concept-only query scores CONCEPT_MATCH_SCORE.
export function conceptScore(query, item, scoreText) {
    const concepts = Array.isArray(item?.concepts) ? item.concepts : [];
    if (!concepts.length) return -1;
    const parsed = parseConceptQuery(query);
    if (!parsed.concepts.size || !concepts.some(concept => parsed.concepts.has(concept))) return -1;
    if (!parsed.text) return CONCEPT_MATCH_SCORE;
    const score = scoreText(parsed.text, normalizeSearchText(item.label ?? ""));
    return score < 0 ? -1 : score + 5;
}

// synonymMatch tells whether a query names at least one of the given concepts.
export function synonymMatch(query, concepts) {
    const named = parseConceptQuery(query).concepts;
    return (Array.isArray(concepts) ? concepts : [concepts]).some(concept => named.has(concept));
}
