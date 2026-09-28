export function overlayVoiceState(state, privateCall = null) {
    if (state.settings?.gaming_overlay === false) return { active: false };
    const visibleSpeakers = speakers => speakers.filter(speaker => !state.settings?.gaming_overlay_speakers_only || speaker.speaking)
        .sort((a, b) => Number(!!b.speaking) - Number(!!a.speaking)).slice(0, 8);
    if (privateCall?.active) return { ...privateCall, title: privateCall.label, speakers: visibleSpeakers(privateCall.speakers) };
    if (!state.myChannelID || !state.pc) return { active: false };
    const channel = state.channels.find(channel => channel.ChannelID === state.myChannelID);
    const speakers = state.clients.filter(client => client.channel_id === state.myChannelID)
        .map(client => ({ name: client.nickname || client.unique_id, speaking: !!client.is_speaking, muted: !!client.input_muted || (state.settings?.muted_users || []).includes(client.unique_id) }));
    return { active: true, title: channel?.Name || 'noXa', muted: state.muted, deafened: state.deafened, speakers: visibleSpeakers(speakers) };
}
