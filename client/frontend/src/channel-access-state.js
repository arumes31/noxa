export function setChannelOverride(overrides, subject, capability, effect) {
    const next = overrides.filter((o) => !(Number(o.role_id || 0) === Number(subject.role_id || 0) &&
        Number(o.user_id || 0) === Number(subject.user_id || 0) && o.capability === capability));
    if (effect !== "inherit") next.push({ ...subject, capability, effect });
    return next;
}

export function applyChannelPreset(overrides, everyoneID, preset) {
    const presets = {
        public: { view_channel: "allow", read_history: "allow", send_messages: "allow", connect: "allow", speak: "allow" },
        private: { view_channel: "deny" },
        readOnlyPreset: { view_channel: "allow", read_history: "allow", send_messages: "deny" },
        listenOnly: { view_channel: "allow", connect: "allow", speak: "deny", share_camera: "deny", share_screen: "deny" },
    };
    let next = [...overrides];
    for (const [key, effect] of Object.entries(presets[preset] || {})) next = setChannelOverride(next, { role_id: everyoneID }, key, effect);
    return next;
}

export function channelOverrideChanges(before = [], after = []) {
    const key = (entry) => `${entry.role_id || 0}:${entry.user_id || 0}:${entry.capability}`;
    const previous = new Map(before.map((entry) => [key(entry), entry]));
    const next = new Map(after.map((entry) => [key(entry), entry]));
    return [...new Set([...previous.keys(), ...next.keys()])].sort().flatMap((id) => {
        const oldEffect = previous.get(id)?.effect || "inherit";
        const newEffect = next.get(id)?.effect || "inherit";
        return oldEffect === newEffect ? [] : [{ ...(next.get(id) || previous.get(id)), before: oldEffect, after: newEffect }];
    });
}
