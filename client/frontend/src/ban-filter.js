// (350) Ban list filtering. Pure: the caller formats dates so the filter
// matches exactly what the table shows.
export function normalizeBanQuery(query) {
    return String(query || "").trim().toLowerCase().split(/\s+/).filter(Boolean);
}

export function banSearchText(ban, { formatTime = unix => new Date(unix * 1000).toISOString().slice(0, 10), permanentLabel = "permanent", typeLabels = ["ip", "unique_id", "nickname"] } = {}) {
    if (!ban || typeof ban !== "object") return "";
    const expiry = ban.expires_at ? formatTime(ban.expires_at) : permanentLabel;
    return [ban.value, ban.nickname, ban.account, ban.account_name, ban.display_name, ban.unique_id, ban.id != null ? `#${ban.id}` : "", ban.banned_by, ban.banned_by_name, ban.reason, expiry, typeLabels[ban.type] || ""]
        .filter(Boolean).map(part => String(part).toLowerCase()).join(" \u0000 ");
}

// Every whitespace-separated term must match somewhere (AND); matching is a
// case-insensitive substring test over account name/id, moderator, reason,
// expiry and ban type.
export function filterBans(bans, query, options = {}) {
    const list = Array.isArray(bans) ? bans : [];
    const terms = normalizeBanQuery(query);
    if (!terms.length) return list;
    return list.filter(ban => { const text = banSearchText(ban, options); return terms.every(term => text.includes(term)); });
}
