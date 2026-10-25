// Draft state has no authority. Only acknowledged server revisions become the
// saved baseline; failed requests and server switches cannot silently save it.
export function roleDraft(role = {}) {
    return { id: role.id || 0, name: role.name || "", position: role.position || 0,
        color: role.color || "", icon: role.icon || "", icon_image: role.icon_image || "", hoist: !!role.hoist, mentionable: !!role.mentionable,
        deletion_protected: !!role.deletion_protected,
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

// (335) A form captures the server revision and the role it edits when it
// opens. Before saving, the latest snapshot is compared against that baseline:
// a newer revision that touched this very role (or removed it) must not be
// overwritten silently; a newer revision that left the role alone is safe to
// rebase onto.
export function roleFormBaseline(snapshot, roleID = 0) {
    const roles = snapshot?.policy?.roles || [];
    const role = roleID ? roles.find((r) => r.id === roleID) : null;
    return Object.freeze({
        revision: snapshot?.policy?.revision ?? null,
        roleID: roleID || 0,
        exists: !roleID || !!role,
        role: role ? JSON.stringify(roleDraft(role)) : "",
    });
}

export function roleSaveConflict(baseline, latest) {
    if (!baseline || !latest?.policy) return null;
    const current = roleFormBaseline(latest, baseline.roleID);
    if (current.revision === baseline.revision) return null;
    const name = baseline.roleID ? (latest.policy.roles || []).find((r) => r.id === baseline.roleID)?.name || "" : "";
    if (baseline.roleID && baseline.exists && !current.exists) return { kind: "deleted", blocking: true, revision: current.revision, name };
    if (baseline.roleID && current.role !== baseline.role) return { kind: "role", blocking: true, revision: current.revision, name };
    return { kind: "policy", blocking: false, revision: current.revision, name };
}
