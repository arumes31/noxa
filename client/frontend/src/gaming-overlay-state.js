// Publish channel (or private-call) members while local audio controls allow
// the overlay. The native overlay owns the timing: with "Only show who's
// speaking" on it fades a member out 3 s after they stop; off, every member
// stays in their quiet look. Private calls take precedence over channels.
//
// To keep each update small, speakers-only mode sends just the members who are
// speaking or spoke within RECENT_MS (longer than the native 3 s hold + fade),
// so quiet members' names and avatars are not sent at all.
export const OVERLAY_ROWS = 8;
export const RECENT_MS = 4500;

export function overlayOnlySpeaking(settings) {
    return settings?.gaming_overlay_only_speaking !== false;
}

// recent: Map(member id -> last time seen speaking), kept by the caller.
export function overlayVoiceState(state, privateCall = null, recent = new Map(), now = Date.now()) {
    if (state.settings?.gaming_overlay === false || state.muted || state.deafened) { recent.clear(); return { active: false }; }
    let members, title;
    if (privateCall?.active) {
        if (privateCall.muted || privateCall.deafened) { recent.clear(); return { active: false }; }
        members = (privateCall.speakers || []).map(speaker => speaker.muted && speaker.speaking ? { ...speaker, speaking: false } : speaker);
        title = privateCall.label;
    } else {
        if (!state.myChannelID || !state.pc) { recent.clear(); return { active: false }; }
        members = state.clients.filter(client => client.channel_id === state.myChannelID).map(client => {
            const muted = !!(client.input_muted || client.self_muted || client.self_deafened || client.server_muted)
                || (state.settings?.muted_users || []).includes(client.unique_id);
            return { id: client.unique_id || String(client.client_id || ''), name: client.nickname || client.unique_id,
                speaking: !!client.is_speaking && !muted, muted };
        });
        title = state.channels.find(channel => channel.ChannelID === state.myChannelID)?.Name || 'noXa';
    }
    const ids = new Set();
    for (const member of members) {
        if (!member.id) continue;
        ids.add(member.id);
        if (member.speaking) recent.set(member.id, now);
    }
    for (const [id, at] of recent) if (!ids.has(id) || now - at >= RECENT_MS) recent.delete(id);
    const onlySpeaking = overlayOnlySpeaking(state.settings);
    const candidates = onlySpeaking ? members.filter(member => member.speaking || recent.has(member.id)) : members;
    const speakers = pickRows(candidates, recent);
    if (!speakers.length) return { active: false };
    return privateCall?.active ? { ...privateCall, title, speakers } : { active: true, title, speakers };
}

// More members than rows: keep speakers first, then the most recent talkers,
// then the rest, and show them in their channel order.
function pickRows(members, recent) {
    if (members.length <= OVERLAY_ROWS) return members;
    const rank = member => member.speaking ? Infinity : recent.get(member.id) ?? -Infinity;
    const keep = new Set([...members].map((member, index) => ({ member, index }))
        .sort((a, b) => rank(b.member) - rank(a.member) || a.index - b.index)
        .slice(0, OVERLAY_ROWS).map(item => item.member));
    return members.filter(member => keep.has(member));
}
