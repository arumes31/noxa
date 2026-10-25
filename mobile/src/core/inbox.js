// inbox.js — the unread overview: every conversation with unread messages
// (channels, direct messages, private groups) and the mentions among them,
// newest first. The screen describes each conversation it knows about;
// this module only decides what is unread and in which order.

/**
 * conversations: [{tag, kind, title, href, messages: [{id, from, text, at,
 * mentioned}], serverUnread}] with messages oldest first; unread: the app's
 * Map tag -> {count, mentions}. serverUnread is a count the server keeps
 * (groups) and stands in when this device has not counted anything.
 * Returns {items, mentions, total}: items [{tag, kind, title, href, count,
 * mentions, last}] newest activity first, mentions [{tag, title, href,
 * message}] newest first.
 */
export function buildInbox({ conversations = [], unread = new Map() } = {}) {
    const items = [];
    const mentions = [];
    for (const conversation of conversations) {
        const entry = unread.get(conversation.tag);
        const count = entry?.count || conversation.serverUnread || 0;
        if (!count) continue;
        const messages = conversation.messages || [];
        const last = messages.at(-1) || null;
        items.push({ tag: conversation.tag, kind: conversation.kind, title: conversation.title, href: conversation.href,
            count, mentions: entry?.mentions || 0, last });
        // The newest `mentions` mentioning messages are the unread ones.
        if (entry?.mentions) {
            const mentioning = messages.filter(m => m.mentioned).slice(-entry.mentions);
            for (const message of mentioning) mentions.push({ tag: conversation.tag, title: conversation.title, href: conversation.href, message });
        }
    }
    items.sort((a, b) => (b.last?.at || 0) - (a.last?.at || 0) || a.title.localeCompare(b.title));
    mentions.sort((a, b) => (b.message.at || 0) - (a.message.at || 0));
    return { items, mentions, total: items.reduce((sum, item) => sum + item.count, 0) };
}
