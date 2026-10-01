// Cosmetics come from the current filtered snapshot, never a management query.
export function memberRoles(state, uid) {
    const roles = state.clients.find((c) => c.unique_id === uid)?.roles;
    return Array.isArray(roles) ? [...roles].sort((a, b) => b.position - a.position) : [];
}

export function roleColor(color) {
    return /^#[0-9a-f]{6}$/i.test(color || "") ? color : "";
}

export function hoistedRoles(state) {
    const groups = new Map();
    for (const client of state.clients) {
        const role = [...(client.roles || [])].sort((a, b) => b.position - a.position).find((r) => r.hoist);
        if (!role) continue;
        if (!groups.has(role.id)) groups.set(role.id, { group: role, members: [] });
        groups.get(role.id).members.push(client);
    }
    return [...groups.values()].sort((a, b) => b.group.position - a.group.position);
}

export function roleChip(role) {
    const chip = document.createElement("span");
    chip.className = "role-chip group-badge";
    chip.textContent = [role.icon, role.name].filter(Boolean).join(" ");
    chip.title = role.name;
    const color = roleColor(role.color);
    if (color) { chip.style.color = color; chip.style.borderColor = color; }
    return chip;
}
