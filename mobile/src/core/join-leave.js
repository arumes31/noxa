// join-leave.js — compact "joined / left" system lines for a channel chat
// (the desktop's showJoinLeave). Channel chat has no such messages on the
// wire, so they are derived from presence events while the app runs:
// in the server chat (scope 0) connects and disconnects, in a channel the
// voice members moving in and out. Kept in memory only.

const LIMIT = 200;
const logs = new WeakMap(); // session -> Map(scope -> [line])

/**
 * The lines one presence event adds, as [{scope, kind: "joined"|"left",
 * name, uid, at}]. Must run before the session applies the event to its
 * snapshot (the "server-event" emission does), so a leaving client's channel
 * is still known.
 */
export function presenceLines(clients, type, data, at = Date.now() / 1000) {
    if (!data?.client_id) return [];
    const known = clients.find(c => c.client_id === data.client_id);
    const name = data.nickname || known?.nickname || "";
    if (!name) return [];
    const uid = data.unique_id || known?.unique_id || "";
    const line = (scope, kind) => ({ scope, kind, name, uid, at });
    const previous = known ? known.channel_id || 0 : data.from_channel_id || 0;
    switch (type) {
        case "user_joined": {
            const out = [line(0, "joined")];
            if (data.channel_id) out.push(line(data.channel_id, "joined"));
            return out;
        }
        case "user_left": {
            const out = [line(0, "left")];
            if (previous) out.push(line(previous, "left"));
            return out;
        }
        case "user_moved": {
            const to = data.channel_id || 0;
            const from = data.from_channel_id || previous;
            if (from === to) return [];
            const out = [];
            if (from) out.push(line(from, "left"));
            if (to) out.push(line(to, "joined"));
            return out;
        }
        default:
            return [];
    }
}

/** Starts recording a session's presence lines (once); returns the scope map. */
export function joinLeaveLog(session) {
    if (logs.has(session)) return logs.get(session);
    const map = new Map();
    logs.set(session, map);
    session.addEventListener("server-event", event => {
        const { type, data } = event.detail;
        const lines = presenceLines(session.clients(), type, data);
        for (const entry of lines) {
            if (entry.uid && entry.uid === session.uniqueID) continue;
            const list = map.get(entry.scope) || [];
            list.push({ ...entry, system: true, key: `sys:${entry.at}:${list.length}:${entry.kind}:${entry.uid}` });
            if (list.length > LIMIT) list.splice(0, list.length - LIMIT);
            map.set(entry.scope, list);
        }
        if (lines.length) session.dispatchEvent(new CustomEvent("join-leave", { detail: { scopes: lines.map(l => l.scope) } }));
    });
    return map;
}

/**
 * Merges system lines into messages by time and folds runs of more than max
 * consecutive system lines into one {system, group: [...]} item.
 */
export function mergeSystemLines(messages, lines, { timeOf = m => m.sentAt, max = 3 } = {}) {
    const merged = [...messages];
    for (const line of lines) {
        let index = merged.length;
        while (index > 0 && !merged[index - 1].system && (timeOf(merged[index - 1]) || 0) > line.at) index--;
        merged.splice(index, 0, line);
    }
    const out = [];
    let run = [];
    const flush = () => {
        if (run.length > max) out.push({ system: true, group: run, at: run[0].at, key: `grp:${run[0].key}` });
        else out.push(...run);
        run = [];
    };
    for (const item of merged) {
        if (item.system) run.push(item);
        else { flush(); out.push(item); }
    }
    flush();
    return out;
}
