// Shared voice hierarchy for the channel tree and its linked text chats.
export function voiceChannelTree(channels = []) {
    const channelByID = new Map(channels.map(channel => [channel.ChannelID, channel]));
    const byParent = new Map();
    for (const channel of channels) {
        if (channel.TextOnly) continue;
        let parent = channel.ParentID || 0, guard = 0;
        while (parent && channelByID.get(parent)?.TextOnly && guard++ < 1000) {
            parent = channelByID.get(parent).ParentID || 0;
        }
        if (!byParent.has(parent)) byParent.set(parent, []);
        byParent.get(parent).push(channel);
    }
    // Stable sorting preserves snapshot order for equally ranked siblings.
    for (const siblings of byParent.values()) {
        siblings.sort((a, b) => Number(b.SystemKind === "home") - Number(a.SystemKind === "home") || (a.OrderIndex || 0) - (b.OrderIndex || 0));
    }
    return byParent;
}

export function orderedVoiceChannels(channels = []) {
    const tree = voiceChannelTree(channels), ordered = [], visited = new Set();
    const pending = [...(tree.get(0) || [])].reverse();
    while (pending.length) {
        const channel = pending.pop();
        if (visited.has(channel.ChannelID)) continue;
        visited.add(channel.ChannelID);
        ordered.push(channel);
        pending.push(...[...(tree.get(channel.ChannelID) || [])].reverse());
    }
    return ordered;
}
