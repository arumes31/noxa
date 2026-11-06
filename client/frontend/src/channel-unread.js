// channel-unread.js — unread counts rolled up from subchannels (620), so a
// collapsed channel still shows that something below it is waiting.

/**
 * Sums the unread state of every descendant of channelID (not the channel
 * itself). byParent maps a channel ID to its child channels ({ ChannelID });
 * unreadFor(id) returns { n, mention, mentions, replies } or null.
 */
export function descendantUnread(channelID, byParent, unreadFor) {
    const total = { n: 0, mentions: 0, replies: 0, mention: false };
    const seen = new Set([channelID]);
    const stack = [...(byParent.get(channelID) || [])];
    while (stack.length) {
        const child = stack.pop();
        const id = child?.ChannelID;
        if (id == null || seen.has(id)) continue; // a malformed tree must not loop
        seen.add(id);
        const unread = unreadFor(id);
        if (unread && unread.n > 0) {
            total.n += unread.n;
            total.mentions += unread.mentions || Number(!!unread.mention);
            total.replies += unread.replies || 0;
            total.mention ||= !!unread.mention;
        }
        stack.push(...(byParent.get(id) || []));
    }
    return total;
}

/** Combines a channel's own unread state with its descendants'. */
export function combineUnread(own, below) {
    const n = (own?.n || 0) + below.n;
    return {
        n,
        mentions: (own?.mentions || Number(!!own?.mention)) + below.mentions,
        replies: (own?.replies || 0) + below.replies,
        mention: !!own?.mention || below.mention,
    };
}
