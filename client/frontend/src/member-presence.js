// Session controls keep their client IDs; the online summary counts accounts.
export function onlineMemberCount(clients) {
    return new Set(clients.map(c => c.user_id > 0 ? `user:${c.user_id}` :
        c.unique_id ? `identity:${c.unique_id}` : `session:${c.client_id}`)).size;
}

export function visibleInChannelTree(client) {
    return !(client.on_mobile && !client.channel_id);
}
