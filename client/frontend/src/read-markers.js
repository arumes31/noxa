// read-markers.js — conflict-free merge of read positions across the
// account's devices (458). A read position only ever moves forward: the
// merge of a local and a remote marker is the larger message id, so a late or
// stale marker from any device can never regress what another device read.
// Pure; no DOM, no state.

// readPosition normalizes one marker value: a positive finite number, else 0.
export function readPosition(value) {
    const id = typeof value === "string" ? Number(value.trim()) : Number(value);
    return Number.isFinite(id) && id > 0 ? Math.floor(id) : 0;
}

// mergeReadMarker returns the furthest of two read positions. Unknown or
// invalid positions never win, so merging with garbage keeps the local value.
export function mergeReadMarker(local, remote) {
    return Math.max(readPosition(local), readPosition(remote));
}

// mergeReadMarkers merges two {conversationKey -> messageID} maps. The result
// holds every key of either side at its furthest position. `changed` reports
// whether the merge moved anything past `local`, so callers persist only when
// a remote marker was actually ahead.
export function mergeReadMarkers(local, remote) {
    const merged = {};
    let changed = false;
    for (const [key, value] of Object.entries(local || {})) {
        const id = readPosition(value);
        if (id) merged[key] = id;
    }
    for (const [key, value] of Object.entries(remote || {})) {
        const id = readPosition(value);
        if (!id) continue;
        if (id > (merged[key] || 0)) { merged[key] = id; changed = true; }
    }
    return { merged, changed };
}

// remoteReadMarker extracts the read marker from a "device_event" payload
// ({event: "read", read: {scope, id, message_id}}) as sent to the account's
// other sessions. It returns null for anything else.
export function remoteReadMarker(event) {
    if (!event || event.event !== "read" || !event.read || typeof event.read !== "object") return null;
    const scope = String(event.read.scope || "");
    const id = String(event.read.id || "");
    if (!["dm", "conversation", "channel"].includes(scope) || !id) return null;
    return { scope, id, messageID: readPosition(event.read.message_id) };
}
