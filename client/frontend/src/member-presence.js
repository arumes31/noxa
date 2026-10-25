// Session controls keep their client IDs; the online summary counts accounts.
export function onlineMemberCount(clients) {
    return new Set(clients.map(c => c.user_id > 0 ? `user:${c.user_id}` :
        c.unique_id ? `identity:${c.unique_id}` : `session:${c.client_id}`)).size;
}

export function visibleInChannelTree(client) {
    return !(client.on_mobile && !client.channel_id);
}

// The server never discloses user_id, so an account is identified by its
// unique_id; anonymous sessions without one stay distinct per connection.
export function accountKey(client) {
    return client.user_id > 0 ? `user:${client.user_id}` :
        client.unique_id ? `identity:${client.unique_id}` : `session:${client.client_id}`;
}

export function deviceKind(client) {
    return client?.on_mobile ? "mobile" : "desktop";
}

// One entry per account in the given (already filtered and sorted) list. The
// first session keeps the row; the desktop session wins over a phone and the
// viewer's own session always leads so self-only controls stay reachable.
export function groupSessionsByAccount(clients, selfID) {
    const groups = new Map();
    for (const client of clients) {
        const key = accountKey(client);
        if (!groups.has(key)) groups.set(key, { key, primary: client, sessions: [client] });
        else {
            const group = groups.get(key);
            group.sessions.push(client);
            if (preferredSession(client, group.primary, selfID)) group.primary = client;
        }
    }
    return [...groups.values()].map(group => ({ ...group, others: group.sessions.filter(client => client !== group.primary) }));
}

function preferredSession(candidate, current, selfID) {
    if (current.client_id === selfID) return false;
    if (candidate.client_id === selfID) return true;
    if (!!candidate.on_mobile !== !!current.on_mobile) return !candidate.on_mobile;
    return !!candidate.is_speaking && !current.is_speaking;
}
