// tool-sources.js — the phone's conversations as one list for search, the
// inbox and saved messages: channels and server chat (ChatSession), direct
// messages (DirectMessages) and private groups (Groups), each with its
// title, route and messages as core/search.js entries.
import { app } from "../app.js";
import { channelEntry, dmEntry, groupEntry } from "../core/search.js";
import { t } from "../i18n.js";
import { dmHref, scopeName } from "./common.js";

export function personName(session, uid) {
    if (uid === session.uniqueID) return session.nickname;
    return session.clients().find(c => c.unique_id === uid)?.nickname || app.dm?.conversations.get(uid)?.nickname || t("dm.someone");
}

function peerName(session, peer) {
    return app.dm?.conversations.get(peer)?.nickname || session.clients().find(c => c.unique_id === peer)?.nickname || t("dm.someone");
}

/**
 * [{tag, kind, title, href, entries, mentioned: Set(ids), serverUnread, id}]
 * for every conversation the phone knows. Entries are oldest first.
 */
export function conversations(session = app.session) {
    const out = [];
    for (const scope of session.chatScopes()) {
        const messages = session.messages.get(scope) || [];
        out.push({ tag: `channel:${scope}`, kind: "channel", id: scope, title: scopeName(session, scope), href: `#/chat/${scope}`,
            entries: messages.filter(m => m.id).map(m => ({ ...channelEntry(scope, m), mentioned: !!m.mentioned })) });
    }
    for (const conversation of app.dm?.conversations.values() || []) {
        const peer = conversation.peer;
        const options = { selfName: session.nickname, selfUID: session.uniqueID, peerName: peerName(session, peer) };
        out.push({ tag: `dm:${peer}`, kind: "dm", id: peer, title: peerName(session, peer), href: dmHref(peer),
            entries: (conversation.messages || []).filter(m => !m.pending && !m.queued && !m.failed).map(m => dmEntry(peer, m, options)) });
    }
    for (const group of app.groups?.list.values() || []) {
        if (group.members?.find(m => m.unique_id === session.uniqueID)?.pending) continue;
        out.push({ tag: `conversation:${group.id}`, kind: "group", id: group.id, title: group.name || t("group.title"), href: `#/group/${encodeURIComponent(group.id)}`,
            entries: (app.groups.messages.get(group.id) || []).map(m => groupEntry(group.id, m, uid => personName(session, uid))), serverUnread: group.unread_count || 0 });
    }
    return out;
}

/** The title of a conversation tag. */
export function conversationTitle(session, tag) {
    return conversations(session).find(c => c.tag === tag)?.title || t("dm.someone");
}

/** People seen in the conversations, by name: [{uid, name}]. */
export function people(session, list = conversations(session)) {
    const byUID = new Map();
    for (const client of session.clients()) if (client.unique_id) byUID.set(client.unique_id, client.nickname);
    for (const conversation of list) for (const entry of conversation.entries) if (entry.fromUID && entry.from) byUID.set(entry.fromUID, entry.from);
    return [...byUID].map(([uid, name]) => ({ uid, name })).sort((a, b) => a.name.localeCompare(b.name));
}
