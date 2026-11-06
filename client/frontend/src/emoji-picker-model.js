// emoji-picker-model.js — search, recents and favorites for the emoji picker
// (607). Search matches the shortcode (English) and a small German keyword
// list for the built-in set; custom server emoji match by their name.
export const MAX_RECENT = 24;
export const MAX_FAVORITES = 48;
// Bounds a stored entry: a unicode emoji or a ":custom_name:" token.
const MAX_ENTRY = 64;

// German keywords for the built-in shortcodes (markdown.js EMOJI).
export const DE_KEYWORDS = Object.freeze({
    smile: ["lächeln", "freude"], smiley: ["lächeln"], grin: ["grinsen"], laughing: ["lachen"],
    sweat_smile: ["erleichtert", "schweiß"], joy: ["tränen", "lachen"], rofl: ["kugeln", "lachen"],
    wink: ["zwinkern"], blush: ["erröten", "verlegen"], yum: ["lecker"], sunglasses: ["sonnenbrille", "cool"],
    heart_eyes: ["verliebt", "herzaugen"], kissing_heart: ["kuss"], thinking: ["nachdenken", "grübeln"],
    neutral_face: ["neutral"], expressionless: ["ausdruckslos"], unamused: ["genervt"], roll_eyes: ["augenrollen"],
    grimacing: ["grimasse"], relieved: ["erleichtert"], pensive: ["nachdenklich"], sleepy: ["müde"],
    sleeping: ["schlafen"], mask: ["maske", "krank"], nerd: ["streber", "nerd"], confused: ["verwirrt"],
    worried: ["besorgt"], cry: ["weinen", "traurig"], sob: ["schluchzen"], scream: ["schreien", "angst"],
    angry: ["wütend", "sauer"], rage: ["zorn"], skull: ["totenkopf"], poop: ["kacke"], clown: ["clown"],
    ghost: ["geist"], alien: ["außerirdisch"], robot: ["roboter"], thumbsup: ["daumen hoch", "gut"],
    "+1": ["daumen hoch", "gut"], thumbsdown: ["daumen runter", "schlecht"], "-1": ["daumen runter"],
    ok_hand: ["okay"], clap: ["klatschen", "applaus"], wave: ["winken", "hallo"], pray: ["beten", "danke", "bitte"],
    muscle: ["muskel", "stark"], heart: ["herz", "liebe"], broken_heart: ["gebrochenes herz"], fire: ["feuer"],
    star: ["stern"], sparkles: ["funkeln"], tada: ["party", "feier", "konfetti"], rocket: ["rakete"],
    100: ["hundert", "perfekt"], check: ["haken", "erledigt"], x: ["kreuz", "falsch"], warning: ["warnung"],
    eyes: ["augen", "schauen"], coffee: ["kaffee"], beer: ["bier"], pizza: ["pizza"], gift: ["geschenk"],
});

/**
 * Builds searchable entries. builtIn maps shortcode -> emoji; custom is a
 * list of server emoji names. Duplicate emoji keep all their shortcodes.
 */
export function emojiIndex(builtIn, custom = []) {
    const byEmoji = new Map();
    for (const [code, emoji] of Object.entries(builtIn)) {
        const entry = byEmoji.get(emoji) || { emoji, codes: [], words: [] };
        entry.codes.push(code);
        entry.words.push(code.replace(/_/g, " "), ...(DE_KEYWORDS[code] || []));
        byEmoji.set(emoji, entry);
    }
    const entries = [...byEmoji.values()];
    for (const name of custom) entries.push({ emoji: `:${name}:`, codes: [name], words: [name.replace(/_/g, " ")], custom: true });
    return entries;
}

/**
 * Entries whose shortcode or keyword contains every word of query, best
 * first: an exact shortcode/keyword, then a prefix, then any substring.
 */
export function searchEmoji(index, query) {
    const phrase = String(query || "").toLocaleLowerCase().replace(/:/g, " ").trim().replace(/\s+/g, " ");
    const terms = phrase.split(" ").filter(Boolean);
    if (!terms.length) return [];
    const scored = [];
    index.forEach((entry, order) => {
        const keys = [...entry.codes, ...entry.words].map(key => key.toLocaleLowerCase());
        const haystack = keys.join(" ");
        if (!terms.every(term => haystack.includes(term))) return;
        const score = keys.some(key => key === phrase || key.replace(/_/g, " ") === phrase) ? 0
            : keys.some(key => key.startsWith(phrase)) ? 1 : 2;
        scored.push({ entry, score, order });
    });
    return scored.sort((a, b) => a.score - b.score || a.order - b.order).map(item => item.entry);
}

function clean(list, max) {
    const out = [];
    for (const item of Array.isArray(list) ? list : []) {
        if (typeof item !== "string" || !item || item.length > MAX_ENTRY || out.includes(item)) continue;
        out.push(item);
        if (out.length >= max) break;
    }
    return out;
}

/** Recents with emoji moved to the front, deduplicated and bounded. */
export function recordEmojiUse(recents, emoji) {
    return clean([emoji, ...(Array.isArray(recents) ? recents : [])], MAX_RECENT);
}

/** Favorites with emoji added (at the end) or removed. */
export function toggleFavorite(favorites, emoji) {
    const list = clean(favorites, MAX_FAVORITES);
    return list.includes(emoji) ? list.filter(item => item !== emoji) : clean([...list, emoji], MAX_FAVORITES);
}

/** A stored list, cleaned for display. */
export function storedEmoji(list, max) {
    return clean(list, max);
}
