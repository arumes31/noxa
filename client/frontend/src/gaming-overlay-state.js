// Publish only actual talkers; quiet joins, reconnects and muted members never
// produce an overlay. Private calls take precedence over channel membership.
export function overlayVoiceState(state, privateCall = null) {
    if (state.settings?.gaming_overlay === false) return { active: false };
    const visible = speakers => speakers.filter(speaker => speaker.speaking && !speaker.muted).slice(0, 8);
    if (privateCall?.active) {
        const speakers = visible(privateCall.speakers || []);
        return speakers.length ? { ...privateCall, title: privateCall.label, speakers } : { active: false };
    }
    if (!state.myChannelID || !state.pc) return { active: false };
    const speakers = visible(state.clients.filter(client => client.channel_id === state.myChannelID).map(client => {
        const self = !!state.myClientID && client.client_id === state.myClientID;
        const muted = !!(client.input_muted || client.self_muted || client.self_deafened || client.server_muted || (self && (state.muted || state.deafened)))
            || (state.settings?.muted_users || []).includes(client.unique_id);
        return { id: client.unique_id || String(client.client_id || ''), name: client.nickname || client.unique_id,
            speaking: !!client.is_speaking && !muted, muted };
    }));
    if (!speakers.length) return { active: false };
    const channel = state.channels.find(channel => channel.ChannelID === state.myChannelID);
    return { active: true, title: channel?.Name || 'noXa', speakers };
}
