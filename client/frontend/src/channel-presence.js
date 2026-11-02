// Compare only membership the server already disclosed to this client.
export function captureChannelPresence(state) {
    return {
        scope: JSON.stringify([state.activeTabID, state.serverGeneration, state.sessionGeneration, state.myClientID]),
        selfID: state.myClientID, channelID: Number(state.myChannelID) || 0, replaying: !!state.replayingTabID,
        clients: new Map(state.clients.map(client => [client.client_id, { ...client, channel_id: Number(client.channel_id) || 0 }])),
    };
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
        const moved = !!next?.channel_id;
        changes.push({ client, message: moved ? 'runtime.movedOut' : 'runtime.left',
            soundEvent: 'user_leave',
            // A filtered snapshot can omit a private destination. Its absence does
            // not prove disconnection; all observers of this departure hear
            // the same neutral cue. Explicit events retain precise wording.
            speechEvent: 'user_leave' });
    }
    for (const [id, client] of after.clients) {
        if (id === after.selfID || client.channel_id !== after.channelID || before.clients.get(id)?.channel_id === after.channelID) continue;
        changes.push({ client, message: 'runtime.joined', soundEvent: 'user_join', speechEvent: 'user_join' });
    }
    return changes;
}
