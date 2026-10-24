// social.js — contacts, blocked users, watched buddies and private notes
// (all kept on this phone in settings), the server lookups of the profile
// card (client info, avatar upload), and the watcher that notices buddies
// coming online and channels reaching a member count.
import { Msg } from "./frame.js";
import * as settings from "./settings.js";

// --- Local lists -------------------------------------------------------------

export function isBlocked(uid) {
    return (settings.get("blockedUsers") || []).includes(uid);
}

export function setBlocked(uid, blocked) {
    const list = (settings.get("blockedUsers") || []).filter(id => id !== uid);
    settings.set({ blockedUsers: blocked ? [...list, uid] : list });
}

export function contacts() {
    return settings.get("contacts") || [];
}

export function isContact(uid) {
    return contacts().some(c => c.uid === uid);
}

export function addContact({ uid, name = "", note = "" }) {
    const id = String(uid || "").trim();
    if (!id) return;
    const list = contacts().filter(c => c.uid !== id);
    const previous = contacts().find(c => c.uid === id);
    settings.set({ contacts: [...list, { uid: id, name: name || previous?.name || "", note: note || previous?.note || "" }] });
}

export function removeContact(uid) {
    settings.set({ contacts: contacts().filter(c => c.uid !== uid) });
}

export function isBuddy(uid) {
    return (settings.get("buddies") || []).includes(uid);
}

export function setBuddy(uid, on) {
    const list = (settings.get("buddies") || []).filter(id => id !== uid);
    settings.set({ buddies: on ? [...list, uid] : list });
}

export function note(uid) {
    return settings.get("notes")?.[uid] || "";
}

export function setNote(uid, text) {
    const notes = { ...(settings.get("notes") || {}) };
    if (text.trim()) notes[uid] = text.slice(0, 1000);
    else delete notes[uid];
    settings.set({ notes });
}

/** Members to notify about when channel reaches count; 0 removes the watch. */
export function setChannelWatch(channelID, count) {
    const watch = { ...(settings.get("channelWatch") || {}) };
    if (count > 0) watch[String(channelID)] = Math.min(99, Math.floor(count));
    else delete watch[String(channelID)];
    settings.set({ channelWatch: watch });
}

// --- Server lookups ----------------------------------------------------------

/** Connection info of an online client (ClientInfoQuery). */
export function clientInfo(session, clientID) {
    return session.conn.request(Msg.ClientInfoQuery, { client_id: clientID }, Msg.ClientInfoResponse, { accept: r => r.client_id === clientID });
}

/** Uploads this account's avatar (base64 PNG/JPEG/GIF/WebP, ≤ 256 KiB). */
export async function setAvatar(session, base64) {
    await session.conn.request(Msg.AvatarSet, { ack_requested: true, data_base64: base64 }, Msg.AssetMutationSaved,
        { accept: r => r.operation === Msg.AvatarSet });
    session.forgetAvatar(session.uniqueID);
}

/** Role chips of a client: [{name, color}] from the highest down. */
export function rolesOf(client) {
    return (client?.roles || []).filter(r => r?.name).map(r => ({ name: r.name, color: /^#[0-9a-f]{3,8}$/i.test(r.color || "") ? r.color : "" }));
}

// --- Watching presence -------------------------------------------------------

/**
 * Compares successive trees and reports buddies that came online and
 * watched channels that reached their member count. The first tree only
 * sets the baseline, so connecting does not announce everyone already
 * online. update() returns [{type: "buddy_online", uid, name} |
 * {type: "channel_watch", channelID, name, count}].
 */
export class PresenceWatch {
    constructor() {
        this.online = null;
        this.counts = new Map();
    }

    update(session, { buddies = [], channelWatch = {} } = {}) {
        const events = [];
        const online = new Map();
        for (const client of session.clients()) if (client.unique_id && client.unique_id !== session.uniqueID) online.set(client.unique_id, client.nickname);
        if (this.online) {
            for (const uid of buddies) {
                if (online.has(uid) && !this.online.has(uid)) events.push({ type: "buddy_online", uid, name: online.get(uid) });
            }
        }
        const counts = new Map();
        for (const channel of session.channels()) {
            const count = channel.clients.length;
            counts.set(channel.id, count);
            const wanted = Number(channelWatch[String(channel.id)]) || 0;
            const before = this.counts.has(channel.id) ? this.counts.get(channel.id) : (this.online ? 0 : count);
            if (wanted > 0 && count >= wanted && before < wanted) events.push({ type: "channel_watch", channelID: channel.id, name: channel.name, count });
        }
        this.online = online;
        this.counts = counts;
        return events;
    }
}
