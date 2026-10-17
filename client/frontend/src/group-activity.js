// group-activity.js holds the pure rules of private-group activity: who is
// typing (an expiring set fed by "conversation_typing" relays) and where each
// member's shared read receipt sits in the rendered thread ("Seen by …").
// conversations.js renders what these functions decide; they are unit-tested
// without a DOM (unit/group-activity.test.mjs).
import { t } from "./i18n.js";

// Typing pings arrive at most every 2 s per member (server throttle), so an
// entry lives a little longer than that to avoid flicker between pings.
export const GROUP_TYPING_TTL_MS = 4500;
export const GROUP_TYPING_PING_MS = 2000;

// createTypingStore keeps, per group, the members currently typing.
export function createTypingStore({ ttl = GROUP_TYPING_TTL_MS, now = () => Date.now() } = {}) {
    const groups = new Map(); // groupID -> Map(uid -> {nick, expires})
    const prune = () => {
        const time = now();
        for (const [group, typers] of groups) {
            for (const [uid, entry] of typers) if (entry.expires <= time) typers.delete(uid);
            if (!typers.size) groups.delete(group);
        }
    };
    return {
        note(group, uid, nick) {
            if (!group || !uid) return;
            if (!groups.has(group)) groups.set(group, new Map());
            groups.get(group).set(uid, { nick: nick || uid, expires: now() + ttl });
        },
        clear(group, uid) {
            const typers = groups.get(group);
            const hit = !!typers?.delete(uid);
            if (typers && !typers.size) groups.delete(group);
            return hit;
        },
        drop(group) { groups.delete(group); },
        typers(group) {
            prune();
            return [...(groups.get(group)?.entries() || [])].map(([uid, entry]) => ({ uid, nick: entry.nick }));
        },
        names(group) { return this.typers(group).map(entry => entry.nick); },
        reset() { groups.clear(); },
        // live reports whether anything is still pending, so the caller can
        // stop its sweep timer when nobody types anywhere.
        live() { prune(); return groups.size > 0; },
    };
}

export function typingLabel(names) {
    if (!names.length) return "";
    if (names.length === 1) return t("chat.typingOne", { name: names[0] });
    if (names.length === 2) return t("chat.typingTwo", { first: names[0], second: names[1] });
    return t("chat.typingMany", { count: names.length });
}

// seenMarkers places every other accepted member's shared read position on
// the newest rendered message it covers: Map(messageID -> [uid, ...]).
// messages are {id, from} in any order. The viewer is never listed, nor is
// a member under their own message (they wrote it). Members with no shared
// position (receipts off, older client) or one below the loaded page are not
// placed at all.
export function seenMarkers(messages, members, selfUID) {
    const ordered = [...messages].filter(message => Number(message?.id) > 0).sort((a, b) => a.id - b.id);
    const markers = new Map();
    for (const member of members || []) {
        const seen = Number(member?.seen_message_id) || 0;
        if (!seen || member.pending || member.unique_id === selfUID) continue;
        let target = null;
        for (const message of ordered) {
            if (message.id > seen) break;
            target = message;
        }
        if (!target || target.from === member.unique_id) continue;
        if (!markers.has(target.id)) markers.set(target.id, []);
        markers.get(target.id).push(member.unique_id);
    }
    return markers;
}

// seenLabel is the text of one marker: up to three names, then a count.
export function seenLabel(names) {
    if (!names.length) return "";
    if (names.length <= 3) return t("dm.seenBy", { names: names.join(", ") });
    return t("dm.seenByMore", { names: names.slice(0, 2).join(", "), count: names.length - 2 });
}

// applySeen updates one member's position after a "conversation_read" relay.
// Positions only move forward, matching the server.
export function applySeen(members, uid, messageID) {
    const member = (members || []).find(item => item.unique_id === uid && !item.pending);
    const id = Number(messageID) || 0;
    if (!member || id <= (Number(member.seen_message_id) || 0)) return false;
    member.seen_message_id = id;
    return true;
}
