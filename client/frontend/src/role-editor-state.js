// Draft state has no authority. Only acknowledged server revisions become the
// saved baseline; failed requests and server switches cannot silently save it.
export function roleDraft(role = {}) {
    return { id: role.id || 0, name: role.name || "", position: role.position || 0,
        color: role.color || "", icon: role.icon || "", hoist: !!role.hoist, mentionable: !!role.mentionable,
        permissions: [...(role.permissions || [])].sort() };
}

export function roleIsDirty(saved, draft) {
    return JSON.stringify(roleDraft(saved)) !== JSON.stringify(roleDraft(draft));
}

export function roleTemplate(template, grantable) {
    const base = ["view_channel", "read_history", "send_messages", "connect", "speak"];
    const templates = {
        blank: [], member: base, administrator: ["administrator"],
        moderator: [...base, "manage_messages", "move_members", "mute_members", "deafen_members", "disconnect_members", "kick_members"],
    };
    return (templates[template] || []).filter((key) => grantable.includes(key));
}

export function reorderedRoles(roles, roleID, direction) {
    const ordered = [...roles].sort((a, b) => a.position - b.position);
    const index = ordered.findIndex((r) => r.id === roleID);
    const target = index + direction;
    if (index <= 0 || target <= 0 || target >= ordered.length) return null;
    [ordered[index], ordered[target]] = [ordered[target], ordered[index]];
    return ordered.map((r) => r.id);
}
