// search.js — message search across the phone's conversations. Channel,
// direct and group messages have different shapes; entries() turns each
// into one searchable record {conversation, id, from, fromUID, text, at},
// and filter/sort/group work on those records only. Nothing here touches
// the network: the screens load older pages and search again.
import { parseFileTokens } from "./files.js";

/** A channel or global chat message (core/chat-session.js). */
export function channelEntry(scope, message) {
    return { conversation: `channel:${scope}`, id: message.id, from: message.from || "", fromUID: message.fromUniqueID || "",
        text: message.deleted ? "" : message.text, at: message.sentAt || 0 };
}

/** A direct message (core/direct.js); own messages carry the own name and ID. */
export function dmEntry(peer, message, { selfName = "", selfUID = "", peerName = "" } = {}) {
    return { conversation: `dm:${peer}`, id: message.id, from: message.self ? selfName : (message.fromNickname || peerName),
        fromUID: message.self ? selfUID : (message.from || peer), text: message.deleted ? "" : message.body, at: message.sentAt || 0 };
}

/** A private group message (core/groups.js); nameOf(uid) gives the sender's name. */
export function groupEntry(id, message, nameOf = () => "") {
    return { conversation: `conversation:${id}`, id: message.id, from: nameOf(message.from_unique_id), fromUID: message.from_unique_id || "",
        text: message.deleted ? "" : message.text, at: message.created_at || 0 };
}

export function hasAttachment(text) {
    return typeof text === "string" && parseFileTokens(text).length > 0;
}

/**
 * Whether an entry matches: query words (all, case-insensitive) in the text
 * or the sender's name, senders (UIDs) and conversations as sets, after and
 * before as Unix seconds (inclusive day bounds are the caller's job) and
 * attachments only.
 */
export function matches(entry, { query = "", from = [], conversations = [], after = 0, before = 0, attachments = false } = {}) {
    if (typeof entry.text !== "string" || !entry.text) return false;
    if (from.length && !from.includes(entry.fromUID)) return false;
    if (conversations.length && !conversations.includes(entry.conversation)) return false;
    if (after && entry.at < after) return false;
    if (before && entry.at > before) return false;
    if (attachments && !hasAttachment(entry.text)) return false;
    const words = query.toLowerCase().split(/\s+/).filter(Boolean);
    if (!words.length) return from.length > 0 || conversations.length > 0 || attachments || !!after || !!before;
    const haystack = `${entry.text}\n${entry.from}`.toLowerCase();
    return words.every(word => haystack.includes(word));
}

/** Filters and sorts entries; sort is "newest" (default) or "oldest". */
export function searchEntries(entries, filter = {}, sort = "newest") {
    const found = entries.filter(entry => matches(entry, filter));
    const direction = sort === "oldest" ? 1 : -1;
    return found.sort((a, b) => direction * ((a.at - b.at) || String(a.id).localeCompare(String(b.id), undefined, { numeric: true })));
}

/**
 * Groups results by conversation, keeping their order; groups follow the
 * position of their first result. Returns [{conversation, results}].
 */
export function groupByConversation(results) {
    const groups = new Map();
    for (const result of results) {
        if (!groups.has(result.conversation)) groups.set(result.conversation, []);
        groups.get(result.conversation).push(result);
    }
    return [...groups].map(([conversation, list]) => ({ conversation, results: list }));
}

/**
 * A one-line excerpt around the first query word: about radius characters
 * either side, with ellipses where text was cut. Attachment tokens show as
 * their file names.
 */
export function excerpt(text, query = "", radius = 48) {
    if (typeof text !== "string") return "";
    let plain = text;
    for (const token of parseFileTokens(text)) plain = plain.replace(token.token, `📎 ${token.name}`);
    plain = plain.replace(/\s+/g, " ").trim();
    const word = query.toLowerCase().split(/\s+/).find(Boolean) || "";
    const at = word ? plain.toLowerCase().indexOf(word) : -1;
    if (at < 0) return plain.length > radius * 2 ? `${plain.slice(0, radius * 2).trimEnd()}…` : plain;
    const start = Math.max(0, at - radius);
    const end = Math.min(plain.length, at + word.length + radius);
    return `${start > 0 ? "…" : ""}${plain.slice(start, end).trim()}${end < plain.length ? "…" : ""}`;
}

/** Splits text into [{text, hit}] runs for highlighting the query words. */
export function highlight(text, query = "") {
    const words = [...new Set(query.toLowerCase().split(/\s+/).filter(Boolean))];
    if (!words.length || !text) return [{ text, hit: false }];
    const lower = text.toLowerCase();
    const runs = [];
    let index = 0;
    while (index < text.length) {
        let next = -1, length = 0;
        for (const word of words) {
            const at = lower.indexOf(word, index);
            if (at >= 0 && (next < 0 || at < next || (at === next && word.length > length))) { next = at; length = word.length; }
        }
        if (next < 0) { runs.push({ text: text.slice(index), hit: false }); break; }
        if (next > index) runs.push({ text: text.slice(index, next), hit: false });
        runs.push({ text: text.slice(next, next + length), hit: true });
        index = next + length;
    }
    return runs;
}

/** Start and end (Unix seconds) of a YYYY-MM-DD date input, local time. */
export function dayBounds(value, end = false) {
    if (!/^\d{4}-\d{2}-\d{2}$/.test(value || "")) return 0;
    const [y, m, d] = value.split("-").map(Number);
    const date = end ? new Date(y, m - 1, d, 23, 59, 59) : new Date(y, m - 1, d);
    return Math.floor(date.getTime() / 1000);
}
