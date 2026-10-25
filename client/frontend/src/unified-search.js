// unified-search.js — pure logic for the one search box that covers messages,
// files, members and channels together: client-side matching of what the
// client already holds (members, channels, the channel's file list), scope
// filtering, ranking and grouping. Messages arrive from the existing server
// search; this module only groups them with the rest. No DOM.
import { normalizeSearchText, parseConceptQuery } from "./search-synonyms.js";

export const UNIFIED_SCOPES = Object.freeze(["all", "messages", "files", "members", "channels"]);
export const ALL_SCOPE_LIMIT = 5;

export function normalizeScope(scope) {
    return UNIFIED_SCOPES.includes(scope) ? scope : "all";
}

// scoreText ranks `text` against a normalised query: prefix 100, word start
// 90, substring 80, in-order subsequence 50 (shorter texts first), else -1.
export function scoreText(query, text) {
    const q = normalizeSearchText(query), haystack = normalizeSearchText(text);
    if (!q) return 0;
    if (!haystack) return -1;
    const index = haystack.indexOf(q);
    if (index === 0) return 100;
    if (index > 0) return haystack[index - 1] === " " ? 90 : 80;
    let position = 0;
    for (const character of haystack) if (character === q[position]) position++;
    return position === q.length ? 50 - haystack.length * 0.05 : -1;
}

const byScore = (a, b) => b.score - a.score || String(a.name).localeCompare(String(b.name));

// searchMembers matches state.clients by nickname or account ID. Own and
// blocked accounts are left out; each hit names the member's voice channel.
export function searchMembers(clients = [], query = "", { me = "", blocked = [], channels = [] } = {}) {
    const banned = new Set(blocked);
    const hits = [];
    for (const client of clients) {
        const uid = client?.unique_id;
        if (!uid || uid === me || banned.has(uid)) continue;
        const name = client.nickname || uid;
        const score = Math.max(scoreText(query, name), scoreText(query, uid) - 20);
        if (score < 0) continue;
        const channelID = Number(client.channel_id);
        hits.push({ kind: "member", uid, name, score, channel: channelID > 0 ? channels.find(channel => channel.ChannelID === channelID)?.Name || "" : "" });
    }
    return hits.sort(byScore);
}

// searchChannels matches state.channels by name, then topic (lower weight).
export function searchChannels(channels = [], query = "") {
    const hits = [];
    for (const channel of channels) {
        if (!channel || !Number.isFinite(Number(channel.ChannelID))) continue;
        const name = channel.Name || "";
        const score = Math.max(scoreText(query, name), channel.Topic ? scoreText(query, channel.Topic) - 40 : -1);
        if (score < 0) continue;
        hits.push({ kind: "channel", id: channel.ChannelID, name, score, textOnly: !!channel.TextOnly, topic: channel.Topic || "" });
    }
    return hits.sort(byScore);
}

// searchFiles matches file entries (FileListResponse.entries) by name, folder
// and uploader (lower weight).
export function searchFiles(files = [], query = "", { channelID = 0 } = {}) {
    const hits = [];
    for (const entry of files) {
        if (!entry?.name) continue;
        const score = Math.max(scoreText(query, entry.name), entry.folder ? scoreText(query, entry.folder) - 30 : -1, entry.uploader ? scoreText(query, entry.uploader) - 50 : -1);
        if (score < 0) continue;
        hits.push({ kind: "file", name: entry.name, folder: entry.folder || "", size: Number(entry.size) || 0, uploader: entry.uploader || "", uploadedAt: entry.uploaded_at || "", sha256: entry.sha256 || "", score, channelID: Number(entry.channel_id ?? channelID) || 0 });
    }
    return hits.sort(byScore);
}

// groupUnifiedResults assembles the grouped view. `scope` "all" shows every
// non-empty group truncated to `limit` items with the total; a specific
// scope shows only that group, untruncated. Messages are passed through in
// the order the caller ranked them. The result carries the active scope and
// per-group counts so the UI can render the scope switch with numbers.
export function groupUnifiedResults({ messages = [], files = [], members = [], channels = [] } = {}, { scope = "all", limit = ALL_SCOPE_LIMIT } = {}) {
    const active = normalizeScope(scope);
    const all = { messages, files, members, channels };
    const counts = Object.fromEntries(Object.entries(all).map(([kind, items]) => [kind, items.length]));
    counts.all = Object.values(counts).reduce((sum, count) => sum + count, 0);
    const groups = [];
    for (const kind of UNIFIED_SCOPES.slice(1)) {
        if (active !== "all" && active !== kind) continue;
        const items = all[kind];
        if (active === "all" && !items.length) continue;
        const shown = active === "all" ? items.slice(0, limit) : items;
        groups.push({ kind, items: shown, total: items.length, truncated: shown.length < items.length });
    }
    return { scope: active, counts, groups, total: active === "all" ? counts.all : counts[active] };
}

// suggestScope proposes a scope from the words of the query: "files report"
// → files, "member alex" → members, "channel dev" → channels. Returns "" when
// no concept word names a scope.
export function suggestScope(query) {
    const { concepts } = parseConceptQuery(query);
    if (concepts.has("files")) return "files";
    if (concepts.has("member") || concepts.has("dm")) return "members";
    if (concepts.has("channel") || concepts.has("voice")) return "channels";
    return "";
}

// formatFileSize renders bytes compactly for a result row.
export function formatFileSize(bytes) {
    const value = Number(bytes) || 0;
    if (value < 1024) return `${value} B`;
    if (value < 1024 * 1024) return `${(value / 1024).toFixed(value < 10240 ? 1 : 0)} KB`;
    if (value < 1024 * 1024 * 1024) return `${(value / 1048576).toFixed(1)} MB`;
    return `${(value / 1073741824).toFixed(2)} GB`;
}
