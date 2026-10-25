// member-picker-model.js — the pure part of the member picker: merging the
// client's known members (online, contacts, open conversations, private-group
// members) into candidates, ranking them for a typed query and listing the
// facts that tell same-named members apart. No DOM; unit-tested directly.
import { normalizeSearchText } from "./search-synonyms.js";

// shortAccountID abbreviates a long account identifier for display.
export function shortAccountID(uid) {
    const id = String(uid || "");
    return id.length > 14 ? `${id.slice(0, 6)}…${id.slice(-4)}` : id;
}

// looksLikeAccountID is the heuristic behind the "paste ID" fallback: one
// token without spaces, long enough not to be a nickname typo.
export function looksLikeAccountID(text) {
    const value = String(text || "").trim();
    return value.length >= 8 && !/\s/.test(value);
}

// buildMemberCandidates merges the known sources into unique candidates.
//   clients:       state.clients (online members: unique_id, nickname, status, channel_id)
//   contacts:      state.settings.contacts ({unique_id, label, nick_history})
//   conversations: open direct conversations ({uid, name})
//   groups:        private groups ({id, name, members: [{unique_id, pending}]})
//   blocked:       blocked account IDs; me: own account ID (both excluded)
//   channels:      state.channels (ChannelID, Name) to name a voice channel
//   server:        the server's display name
export function buildMemberCandidates({ clients = [], contacts = [], conversations = [], groups = [], blocked = [], me = "", channels = [], server = "" } = {}) {
    const banned = new Set(blocked);
    const byID = new Map();
    const candidate = uid => {
        if (!byID.has(uid)) byID.set(uid, { uid, name: "", handle: shortAccountID(uid), server, online: false, presence: "offline", channel: "", friend: false, conversation: false, groups: [] });
        return byID.get(uid);
    };
    for (const client of clients) {
        const uid = client?.unique_id;
        if (!uid || uid === me || banned.has(uid)) continue;
        const entry = candidate(uid);
        entry.name = entry.name || client.nickname || "";
        entry.online = true;
        entry.presence = ["away", "busy"].includes(client.status) ? client.status : client.status === "invisible" ? "offline" : "online";
        const channelID = Number(client.channel_id);
        if (channelID > 0) entry.channel = channels.find(channel => channel.ChannelID === channelID)?.Name || "";
    }
    for (const contact of contacts) {
        const uid = contact?.unique_id;
        if (!uid || uid === me || banned.has(uid)) continue;
        const entry = candidate(uid);
        entry.friend = true;
        const history = Array.isArray(contact.nick_history) ? contact.nick_history : [];
        entry.name = entry.name || contact.label || history[history.length - 1] || "";
    }
    for (const conversation of conversations) {
        const uid = conversation?.uid;
        if (!uid || uid === me || banned.has(uid)) continue;
        const entry = candidate(uid);
        entry.conversation = true;
        entry.name = entry.name || conversation.name || "";
    }
    for (const group of groups) {
        for (const member of group?.members || []) {
            const uid = member?.unique_id;
            if (!uid || uid === me || banned.has(uid) || member.pending) continue;
            const entry = candidate(uid);
            if (group.name && !entry.groups.includes(group.name)) entry.groups.push(group.name);
        }
    }
    for (const entry of byID.values()) if (!entry.name) entry.name = entry.handle;
    return [...byID.values()];
}

function scoreText(query, text) {
    const haystack = normalizeSearchText(text);
    if (!query) return 0;
    if (!haystack) return -1;
    const index = haystack.indexOf(query);
    if (index === 0) return 100;
    if (index > 0) return haystack[index - 1] === " " ? 90 : 80;
    let position = 0;
    for (const character of haystack) if (character === query[position]) position++;
    return position === query.length ? 50 - haystack.length * 0.05 : -1;
}

// rankMemberCandidates filters and orders candidates for a query. Without a
// query it lists existing conversations, then friends, then online members.
// Ties break towards an existing conversation, a friend, an online member,
// then the name.
export function rankMemberCandidates(candidates, query = "", { limit = 8, exclude = [] } = {}) {
    const excluded = new Set(exclude);
    const q = normalizeSearchText(query);
    const scored = [];
    for (const entry of candidates) {
        if (excluded.has(entry.uid)) continue;
        let score = 0;
        if (q) {
            score = Math.max(scoreText(q, entry.name), scoreText(q, entry.uid) - 20, ...entry.groups.map(name => scoreText(q, name) - 60), entry.channel ? scoreText(q, entry.channel) - 60 : -1);
            if (score < 0) continue;
        }
        scored.push({ entry, score });
    }
    const weight = ({ entry }) => (entry.conversation ? 4 : 0) + (entry.friend ? 2 : 0) + (entry.online ? 1 : 0);
    scored.sort((a, b) => b.score - a.score || weight(b) - weight(a) || a.entry.name.localeCompare(b.entry.name));
    return scored.slice(0, limit).map(item => item.entry);
}

// candidateContext lists the facts that distinguish same-named members, as
// {kind, value} pairs the UI translates: conversation, friend, channel, group,
// server and handle. Only facts the candidate has are listed.
export function candidateContext(entry, { sameName = false } = {}) {
    const facts = [];
    if (entry.conversation) facts.push({ kind: "conversation" });
    if (entry.friend) facts.push({ kind: "friend" });
    if (entry.channel) facts.push({ kind: "channel", value: entry.channel });
    else if (entry.groups.length) facts.push({ kind: "group", value: entry.groups[0] });
    if (entry.server) facts.push({ kind: "server", value: entry.server });
    if (sameName || facts.length < 2) facts.push({ kind: "handle", value: entry.handle });
    return facts;
}

// duplicateNames returns the lower-cased names that occur more than once.
export function duplicateNames(candidates) {
    const seen = new Map();
    for (const entry of candidates) {
        const key = normalizeSearchText(entry.name);
        seen.set(key, (seen.get(key) || 0) + 1);
    }
    return new Set([...seen].filter(([, count]) => count > 1).map(([key]) => key));
}

