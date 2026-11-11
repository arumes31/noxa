// Compare only membership the server already disclosed to this client.
export function captureChannelPresence(state) {
    return {
        scope: JSON.stringify([state.activeTabID, state.serverGeneration, state.sessionGeneration, state.myClientID]),
        selfID: state.myClientID, channelID: Number(state.myChannelID) || 0, replaying: !!state.replayingTabID,
        clients: new Map(state.clients.map(client => [client.client_id, { ...client, channel_id: Number(client.channel_id) || 0 }])),
        // Snapshot hints: why recent sessions ended, and who came back after a drop.
        departures: state.presenceDepartures || {},
        reconnected: state.presenceReconnected || new Set(),
    };
}

/** The cue for a member leaving: a lost connection, a silent replacement, or a plain leave. */
export function departureCue(reason, moved = false) {
    if (reason === 'replaced') return null; // the same device is already back on a new session
    if (reason === 'connection_lost') {
        return { kind: 'leave', message: 'runtime.connectionLostUser', soundEvent: 'user_leave', speechEvent: 'user_connection_lost' };
    }
    return { kind: 'leave', message: moved ? 'runtime.movedOut' : 'runtime.left', soundEvent: 'user_leave', speechEvent: 'user_leave' };
}

/** The cue for a member arriving: a reconnect after a lost connection, or a join. */
export function arrivalCue(reconnected) {
    return reconnected
        ? { kind: 'join', message: 'runtime.reconnectedUser', soundEvent: 'user_join', speechEvent: 'user_reconnected' }
        : { kind: 'join', message: 'runtime.joined', soundEvent: 'user_join', speechEvent: 'user_join' };
}

export function channelPresenceChanges(before, after) {
    if (!before || before.scope !== after.scope || before.replaying || after.replaying
        || !after.selfID || !after.channelID || before.channelID !== after.channelID
        || after.clients.get(after.selfID)?.channel_id !== after.channelID) return [];
    const changes = [];
    for (const [id, client] of before.clients) {
        if (id === after.selfID || client.channel_id !== after.channelID) continue;
        const next = after.clients.get(id);
        if (next?.channel_id === after.channelID) continue;
        // A filtered snapshot can omit a private destination, so absence alone
        // does not prove disconnection: only the server's reason does.
        const cue = departureCue(after.departures[id], !!next?.channel_id);
        if (cue) changes.push({ client, ...cue });
    }
    for (const [id, client] of after.clients) {
        if (id === after.selfID || client.channel_id !== after.channelID || before.clients.get(id)?.channel_id === after.channelID) continue;
        changes.push({ client, ...arrivalCue(after.reconnected.has(id)) });
    }
    return changes;
}

// After the own connection comes back the roster refills; those arrivals are
// not news, so only "reconnected to the server" plays.
export const RECONNECT_SETTLE_MS = 8000;
// Members seen before the drop stay quiet when they reappear within this time.
export const RECONNECT_KNOWN_MS = 60000;

/** Remembers the members of the own channel when the connection drops. */
export function notePresenceDrop(state) {
    const channelID = Number(state.myChannelID) || 0;
    state.presenceBeforeDrop = new Set(state.clients
        .filter(client => channelID && Number(client.channel_id) === channelID && client.unique_id)
        .map(client => client.unique_id));
    state.presenceKnownUntil = 0;
    state.presenceQuietUntil = 0;
}

/** Starts the quiet period after the own connection is restored. */
export function notePresenceRestored(state, now = Date.now()) {
    state.presenceQuietUntil = now + RECONNECT_SETTLE_MS;
    state.presenceKnownUntil = now + RECONNECT_KNOWN_MS;
}

/**
 * Reports whether a presence cue belongs to rebuilding the roster after the
 * own reconnect: inside the settle window, or a member who was already there.
 */
export function presenceCueSuppressed(state, change, now = Date.now()) {
    if (now < (state.presenceQuietUntil || 0)) return true;
    const uid = change.client?.unique_id;
    if (change.kind === 'join' && uid && now < (state.presenceKnownUntil || 0) && state.presenceBeforeDrop?.has(uid)) {
        state.presenceBeforeDrop.delete(uid);
        return true;
    }
    return false;
}
