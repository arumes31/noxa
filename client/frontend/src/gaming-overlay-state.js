// Briefly show each voice connection, then follow speech. A short silence
// grace period keeps the overlay steady between words and speaker changes.
export function createOverlayVisibility() {
    let connection = '', until = 0;
    return (state, snapshot, now = Date.now()) => {
        if (!snapshot.active) { connection = ''; until = 0; return snapshot; }
        const next = JSON.stringify([state.activeTabID, state.serverGeneration, state.sessionGeneration,
            snapshot.label ? 'private-call' : state.myChannelID]);
        if (next !== connection) { connection = next; until = now + 5000; }
        if (snapshot.speakers?.some(speaker => speaker.speaking && !speaker.muted)) until = Math.max(until, now + 1500);
        return now < until ? snapshot : { active: false };
    };
}

export function overlayVoiceState(state, privateCall = null) {
    if (state.settings?.gaming_overlay === false) return { active: false };
    const visibleSpeakers = speakers => speakers.filter(speaker => !state.settings?.gaming_overlay_speakers_only || speaker.speaking)
        .sort((a, b) => Number(!!b.speaking) - Number(!!a.speaking)).slice(0, 8);
    if (privateCall?.active) return { ...privateCall, title: privateCall.label, speakers: visibleSpeakers(privateCall.speakers) };
    if (!state.myChannelID || !state.pc) return { active: false };
    const channel = state.channels.find(channel => channel.ChannelID === state.myChannelID);
    const speakers = state.clients.filter(client => client.channel_id === state.myChannelID)
        .map(client => {
            const self = !!state.myClientID && client.client_id === state.myClientID;
            const muted = !!(client.input_muted || client.self_muted || client.self_deafened || client.server_muted || (self && (state.muted || state.deafened)))
                || (state.settings?.muted_users || []).includes(client.unique_id);
            return { name: client.nickname || client.unique_id, speaking: !!client.is_speaking && !muted, muted };
        });
    return { active: true, title: channel?.Name || 'noXa', muted: state.muted, deafened: state.deafened, speakers: visibleSpeakers(speakers) };
}
