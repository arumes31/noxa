// access-matrix.js — channel access drafts and role editing state, without
// DOM. A channel policy is {channel_id, parent_id, synced, overrides}; an
// override names one subject (role_id or user_id), a capability and an
// effect "allow" or "deny". No override for a subject and capability means
// "inherit". Drafts have no authority: only a saved server revision counts.

export const EFFECTS = Object.freeze(["inherit", "allow", "deny"]);

/** The capabilities the mobile access screen offers first, in display order. */
export const MAIN_CAPABILITIES = Object.freeze([
    "view_channel", "read_history", "send_messages", "connect", "speak",
    "share_camera", "share_screen", "upload_files", "invite_others",
]);

/** Channel presets, applied to @everyone (channel-access-state.js on desktop). */
export const PRESETS = Object.freeze({
    public: { view_channel: "allow", read_history: "allow", send_messages: "allow", connect: "allow", speak: "allow" },
    private: { view_channel: "deny" },
    readOnly: { view_channel: "allow", read_history: "allow", send_messages: "deny" },
    listenOnly: { view_channel: "allow", connect: "allow", speak: "deny", share_camera: "deny", share_screen: "deny" },
});

const same = (a, b) => Number(a || 0) === Number(b || 0);
const matches = (override, subject, capability) =>
    same(override.role_id, subject.role_id) && same(override.user_id, subject.user_id) && override.capability === capability;

/** The effect a subject has for a capability in a list of overrides. */
export function effectOf(overrides, subject, capability) {
    return (overrides || []).find(o => matches(o, subject, capability))?.effect || "inherit";
}

/** Returns new overrides with the subject's capability set to effect ("inherit" removes it). */
export function setOverride(overrides, subject, capability, effect) {
    if (!EFFECTS.includes(effect)) throw new Error(`unknown effect ${effect}`);
    const next = (overrides || []).filter(o => !matches(o, subject, capability));
    if (effect !== "inherit") {
        const entry = subject.user_id ? { user_id: Number(subject.user_id) } : { role_id: Number(subject.role_id) };
        next.push({ ...entry, capability, effect });
    }
    return next;
}

/** The next effect when a three-way control is tapped: inherit → allow → deny → inherit. */
export function nextEffect(effect) {
    return EFFECTS[(EFFECTS.indexOf(effect) + 1) % EFFECTS.length] || "inherit";
}

/** Applies a preset to @everyone, keeping every other override. */
export function applyPreset(overrides, everyoneID, preset) {
    const rules = PRESETS[preset];
    if (!rules) throw new Error(`unknown preset ${preset}`);
    let next = [...(overrides || [])];
    for (const [capability, effect] of Object.entries(rules)) next = setOverride(next, { role_id: everyoneID }, capability, effect);
    return next;
}

/** The preset @everyone currently matches exactly, or "". */
export function presetOf(overrides, everyoneID) {
    const mine = (overrides || []).filter(o => same(o.role_id, everyoneID) && !o.user_id);
    for (const [name, rules] of Object.entries(PRESETS)) {
        const entries = Object.entries(rules);
        if (mine.length === entries.length && entries.every(([capability, effect]) => effectOf(mine, { role_id: everyoneID }, capability) === effect)) return name;
    }
    return "";
}

/** Override differences between two lists: [{role_id|user_id, capability, before, after}]. */
export function overrideChanges(before = [], after = []) {
    const key = o => `${o.role_id || 0}:${o.user_id || 0}:${o.capability}`;
    const previous = new Map(before.map(o => [key(o), o]));
    const next = new Map(after.map(o => [key(o), o]));
    return [...new Set([...previous.keys(), ...next.keys()])].sort().flatMap(id => {
        const was = previous.get(id)?.effect || "inherit";
        const now = next.get(id)?.effect || "inherit";
        return was === now ? [] : [{ ...(next.get(id) || previous.get(id)), before: was, after: now }];
    });
}

/**
 * Switches a draft between synced and customised. Customising starts from
 * the overrides it inherited (inherited), so nothing changes until edited;
 * syncing drops the channel's own overrides.
 */
export function setSynced(draft, synced, inherited = []) {
    return { ...draft, synced, overrides: synced ? [] : structuredClone(inherited) };
}

/** Capabilities a capability depends on, transitively (CapabilityInfo.requires). */
export function requirements(capabilities, key, seen = new Set()) {
    for (const required of capabilities.find(c => c.key === key)?.requires || []) {
        if (seen.has(required)) continue;
        seen.add(required);
        requirements(capabilities, required, seen);
    }
    return [...seen];
}

/**
 * Sets an effect and keeps prerequisites consistent: allowing a capability
 * also allows what it requires; denying one also denies what requires it.
 */
export function setWithDependencies(overrides, capabilities, subject, key, effect) {
    let next = setOverride(overrides, subject, key, effect);
    if (effect === "allow") {
        for (const required of requirements(capabilities, key)) {
            if (effectOf(next, subject, required) === "deny") next = setOverride(next, subject, required, "allow");
        }
    } else if (effect === "deny") {
        for (const dependent of capabilities.filter(c => requirements(capabilities, c.key).includes(key))) {
            if (effectOf(next, subject, dependent.key) === "allow") next = setOverride(next, subject, dependent.key, "deny");
        }
    }
    return next;
}

/** A capability's label in the UI language (CapabilityInfo carries en and de). */
export function capabilityLabel(info, language = "en") {
    return info?.[language] || info?.en || info?.key || "";
}

/** Capabilities grouped by category and filtered by a search in label or key. */
export function groupCapabilities(capabilities, query = "", language = "en", { channelOnly = false } = {}) {
    const needle = query.trim().toLowerCase();
    const groups = new Map();
    for (const info of capabilities || []) {
        if (channelOnly && !info.channel) continue;
        const label = capabilityLabel(info, language);
        if (needle && !label.toLowerCase().includes(needle) && !info.key.includes(needle) && !(info.en || "").toLowerCase().includes(needle)) continue;
        if (!groups.has(info.group)) groups.set(info.group, []);
        groups.get(info.group).push(info);
    }
    return [...groups].map(([group, items]) => ({ group, items }));
}

/** Whether a role has a capability on a channel with these overrides (display only). */
export function roleAllowed(state, overrides, role, capability, seen = new Set()) {
    if (seen.has(capability)) return false;
    const info = state.capabilities.find(c => c.key === capability);
    if (!info) return false;
    if (role.permissions?.includes("administrator")) return true;
    const everyone = state.policy.roles.find(r => r.id === state.policy.everyone_id);
    let allowed = [everyone, role].some(r => r?.permissions?.includes(capability));
    for (const id of new Set([state.policy.everyone_id, role.id])) {
        const effect = effectOf(overrides, { role_id: id }, capability);
        if (effect !== "inherit") allowed = effect === "allow";
    }
    return allowed && (info.requires || []).every(key => roleAllowed(state, overrides, role, key, new Set([...seen, capability])));
}

// --- Roles ---------------------------------------------------------------------------

/** An editable copy of a role; icon_image is left out so an update keeps the stored one. */
export function roleDraft(role = {}) {
    return {
        id: role.id || 0, name: role.name || "", position: role.position || 0, color: role.color || "", icon: role.icon || "",
        hoist: !!role.hoist, mentionable: !!role.mentionable, deletion_protected: !!role.deletion_protected,
        permissions: [...(role.permissions || [])].sort(),
    };
}

export function roleDirty(saved, draft) {
    return JSON.stringify(roleDraft(saved)) !== JSON.stringify(roleDraft(draft));
}

/** Permissions of a role template, limited to what the actor may grant. */
export const ROLE_TEMPLATES = Object.freeze(["blank", "member", "moderator", "administrator"]);
export function roleTemplate(template, grantable = []) {
    const base = ["view_channel", "read_history", "send_messages", "connect", "speak"];
    const templates = {
        blank: [], member: base, administrator: ["administrator"],
        moderator: [...base, "manage_messages", "move_members", "mute_members", "deafen_members", "disconnect_members", "kick_members"],
    };
    return (templates[template] || []).filter(key => grantable.includes(key));
}

/** Role IDs in new order after moving one up (+1) or down (-1); null when it cannot move. @everyone stays first. */
export function reorderedRoles(roles, roleID, direction) {
    const ordered = [...roles].sort((a, b) => a.position - b.position);
    const index = ordered.findIndex(r => r.id === roleID);
    const target = index + direction;
    if (index <= 0 || target <= 0 || target >= ordered.length) return null;
    [ordered[index], ordered[target]] = [ordered[target], ordered[index]];
    return ordered.map(r => r.id);
}

/** Members per role ID in a policy. */
export function memberCounts(policy) {
    const counts = new Map();
    for (const member of policy?.members || []) for (const id of member.role_ids || []) counts.set(id, (counts.get(id) || 0) + 1);
    return counts;
}

/** A member's role IDs after adding or removing one role. */
export function withRole(roleIDs, roleID, present) {
    const set = new Set(roleIDs || []);
    if (present) set.add(roleID); else set.delete(roleID);
    return [...set].sort((a, b) => a - b);
}
