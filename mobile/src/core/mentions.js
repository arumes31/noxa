// mentions.js — composer mention completion and keyword matching, no DOM.
//
// The server resolves mentions from the plaintext body (internal/server
// chatx.go parseMentions, custom_role_mentions.go): "@nickname" matches an
// online user case-insensitively, "<@&roleID>" a mentionable role, and
// @here/@everyone/@channel everyone in the channel when the sender has
// mention_everyone. Anything else is just text, so offering a choice the
// server then ignores costs nothing.

export const MASS_MENTIONS = ["here", "everyone", "channel"];

/**
 * The mention being typed right before the caret: {start, query} where start
 * is the index of "@", or null. "@" must start the text or follow a space.
 */
export function mentionAt(value, caret = value.length) {
    const m = /(^|[\s(])@([\p{L}\p{N}_.-]*)$/u.exec(value.slice(0, caret));
    if (!m) return null;
    return { start: caret - m[2].length - 1, query: m[2] };
}

/**
 * Splits text at "<@&roleID>" role mention tokens: [{type: "text", text} |
 * {type: "role", id}]. The chat renders the role's name (@Name) instead of
 * the raw token.
 */
export function splitRoleMentions(text) {
    const out = [];
    let last = 0;
    for (const m of String(text).matchAll(/<@&(\d{1,18})>/g)) {
        if (m.index > last) out.push({ type: "text", text: text.slice(last, m.index) });
        out.push({ type: "role", id: Number(m[1]) });
        last = m.index + m[0].length;
    }
    if (last < text.length || !out.length) out.push({ type: "text", text: text.slice(last) });
    return out;
}

/** Roles that appear on visible clients (the snapshot's role chips): [{id, name, color}]. */
export function visibleRoles(clients) {
    const roles = new Map();
    for (const client of clients || []) {
        for (const role of client.roles || []) {
            if (Number.isSafeInteger(role.id) && role.id > 0 && typeof role.name === "string" && role.name) roles.set(role.id, { id: role.id, name: role.name, color: role.color || "" });
        }
    }
    return [...roles.values()];
}

/**
 * Completion choices for query: online users (prefix matches first, then
 * substring), roles and the mass forms. Each is {kind, label, insert, ...}.
 * options: {clients, selfUID, roles: bool, mass: bool, limit}.
 */
export function mentionChoices(query, { clients = [], selfUID = "", roles = true, mass = true, limit = 8 } = {}) {
    const q = query.toLowerCase();
    const rank = name => {
        const lower = name.toLowerCase();
        return lower.startsWith(q) ? 0 : lower.includes(q) ? 1 : -1;
    };
    const users = new Map();
    for (const client of clients) {
        const name = client.nickname || "";
        if (!name || client.unique_id === selfUID || users.has(client.unique_id || name)) continue;
        const r = rank(name);
        if (r < 0) continue;
        users.set(client.unique_id || name, { kind: "user", label: name, insert: `@${name}`, uid: client.unique_id || "", status: client.status || "online", rank: r });
    }
    const out = [...users.values()].sort((a, b) => a.rank - b.rank || a.label.localeCompare(b.label));
    if (roles) {
        for (const role of visibleRoles(clients)) {
            const r = rank(role.name);
            if (r >= 0) out.push({ kind: "role", label: `@${role.name}`, insert: `<@&${role.id}>`, id: role.id, color: role.color, rank: r });
        }
    }
    if (mass) for (const form of MASS_MENTIONS) if (form.startsWith(q)) out.push({ kind: "mass", label: `@${form}`, insert: `@${form}`, rank: 0 });
    return out.slice(0, limit);
}

/** Replaces value[start, caret) with insert and one space. Returns {value, caret}. */
export function insertMention(value, start, caret, insert) {
    const before = value.slice(0, start);
    const after = value.slice(caret);
    const text = insert + (after.startsWith(" ") ? "" : " ");
    return { value: before + text + after, caret: before.length + text.length + (after.startsWith(" ") ? 1 : 0) };
}

/**
 * Tab completion like the desktop: the first Tab completes the "@query"
 * before the caret with the nearest choice, further Tabs (caret unchanged)
 * cycle through the others. choose(query) returns the choices. Returns
 * {value, caret, cycle} or null when there is nothing to complete.
 */
export function tabComplete(value, caret, cycle, choose) {
    if (cycle && caret === cycle.end && cycle.matches.length) {
        const index = (cycle.index + 1) % cycle.matches.length;
        const pick = cycle.matches[index];
        const next = value.slice(0, cycle.start) + pick + " " + value.slice(cycle.end);
        const end = cycle.start + pick.length + 1;
        return { value: next, caret: end, cycle: { ...cycle, index, end } };
    }
    const at = mentionAt(value, caret);
    if (!at) return null;
    const matches = choose(at.query).map(choice => choice.insert);
    if (!matches.length) return null;
    const next = value.slice(0, at.start) + matches[0] + " " + value.slice(caret);
    const end = at.start + matches[0].length + 1;
    return { value: next, caret: end, cycle: { start: at.start, end, index: 0, matches } };
}

// --- Keywords --------------------------------------------------------------------

/** Keywords from a free-form list (commas or new lines), trimmed and unique. */
export function parseKeywords(input) {
    const seen = new Set();
    const out = [];
    for (const raw of String(input || "").split(/[,\n]/)) {
        const word = raw.trim();
        if (!word || seen.has(word.toLowerCase())) continue;
        seen.add(word.toLowerCase());
        out.push(word.slice(0, 64));
    }
    return out.slice(0, 50);
}

const escape = s => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
const patterns = new Map();

function keywordPattern(word) {
    if (!patterns.has(word)) {
        if (patterns.size > 200) patterns.clear();
        // Whole word: no letter, digit or _ directly before or after.
        patterns.set(word, new RegExp(`(^|[^\\p{L}\\p{N}_])${escape(word)}(?=$|[^\\p{L}\\p{N}_])`, "iu"));
    }
    return patterns.get(word);
}

/** The first keyword text contains as a whole word (case-insensitive), or "". */
export function keywordHit(text, keywords) {
    if (typeof text !== "string" || !text || !Array.isArray(keywords)) return "";
    for (const raw of keywords) {
        const word = typeof raw === "string" ? raw.trim() : "";
        if (word && keywordPattern(word).test(text)) return word;
    }
    return "";
}

/** Whether a channel message counts as a mention: the server said so, or a keyword matches. */
export function isMention(message, keywords) {
    if (!message || message.own) return false;
    return !!message.mentioned || !!keywordHit(message.text, keywords);
}
