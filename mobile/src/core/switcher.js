// switcher.js — ranking for the quick switcher (Ctrl/Cmd+K): channels,
// people, conversations and settings pages matched against a typed query.

/** Lower case without accents, so "Kai" matches "kaï" and "Über" "uber". */
export function fold(text) {
    return String(text ?? "").normalize("NFD").replace(/\p{M}/gu, "").toLowerCase();
}

/**
 * How well query matches text; -1 when it does not. Exact beats prefix,
 * a word start beats any substring, and a substring beats a subsequence
 * ("pb1" in "Public1"). Earlier and shorter matches win ties.
 */
export function score(query, text) {
    const q = fold(query).trim();
    const s = fold(text);
    if (!q) return 0;
    if (s === q) return 1000;
    if (s.startsWith(q)) return 800 - s.length * 0.1;
    const index = s.indexOf(q);
    if (index > 0) {
        const wordStart = /[^\p{L}\p{N}]/u.test(s[index - 1]);
        return (wordStart ? 600 : 400) - index - s.length * 0.05;
    }
    let qi = 0;
    for (const ch of s) if (ch === q[qi]) qi++;
    return qi === q.length ? 200 - s.length * 0.1 : -1;
}

// Equal matches list in this order: where people usually want to go first.
export const KIND_ORDER = ["dm", "text", "group", "person", "voice", "page"];

/**
 * Ranks items {label, kind, keywords?, typed?, boost?} for a query. With an
 * empty query, typed items (settings pages) stay out and the rest list by
 * boost (unread), then kind, then their given order. Returns at most limit.
 */
export function rank(query, items, limit = 12) {
    const q = String(query ?? "").trim();
    const kind = item => {
        const index = KIND_ORDER.indexOf(item.kind);
        return index < 0 ? KIND_ORDER.length : index;
    };
    const scored = [];
    items.forEach((item, index) => {
        if (!q) {
            if (!item.typed) scored.push({ item, index, score: item.boost || 0 });
            return;
        }
        const best = Math.max(score(q, item.label), ...(item.keywords || []).map(word => score(q, word) - 50));
        if (best >= 0) scored.push({ item, index, score: best + (item.boost || 0) });
    });
    scored.sort((a, b) => b.score - a.score || kind(a.item) - kind(b.item)
        || (q ? a.item.label.length - b.item.label.length || a.item.label.localeCompare(b.item.label) : 0)
        || a.index - b.index);
    return scored.slice(0, limit).map(entry => entry.item);
}
