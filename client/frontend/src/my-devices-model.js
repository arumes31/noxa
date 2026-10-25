// my-devices-model.js — pure model behind the "My devices" section (442/443).
// It combines the account's device list (DevicesForTab) with live presence
// (state.clients: one entry per session, `unique_id` is the account,
// `client_id` the session, `on_mobile` marks web/phone sessions).
//
// Presence does not name the device a session runs on, so "online now" is
// derived: this device is online by definition; for the other devices of a
// kind, as many of them as the account has live sessions of that kind are
// online, most recently used first. That is exact for the common case (one
// phone, one desktop) and errs toward "online" otherwise.

// deviceSessionKind maps a presence session to a device kind.
export function deviceSessionKind(session) {
    return session?.on_mobile ? "web" : "desktop";
}

// defaultDeviceName fills in a readable name when the server reports none or
// only its generic placeholder.
export function defaultDeviceName(device, translate) {
    const name = String(device?.name || "").trim();
    if (name && name !== "Phone") return name;
    return translate(device?.kind === "web" ? "devices.defaultMobileName" : "devices.defaultDesktopName", { platform: translate(`devices.kind.${device?.kind === "web" ? "web" : "desktop"}`) });
}

// mergeDevicePresence returns one row per active device, sorted: this
// device first, then online devices, then by last use.
export function mergeDevicePresence(devices, sessions, { myUniqueID = "", myClientID = "" } = {}) {
    const active = (Array.isArray(devices) ? devices : []).filter(device => device && device.device_id && !device.revoked_at);
    const own = (Array.isArray(sessions) ? sessions : []).filter(session => session && session.unique_id === myUniqueID && session.client_id !== myClientID);
    const budget = { web: 0, desktop: 0 };
    for (const session of own) budget[deviceSessionKind(session)]++;
    const byRecency = [...active].sort((a, b) => (b.last_seen_at || 0) - (a.last_seen_at || 0));
    const online = new Set();
    for (const device of byRecency) {
        if (device.current) { online.add(device.device_id); continue; }
        const kind = device.kind === "web" ? "web" : "desktop";
        if (budget[kind] > 0) { budget[kind]--; online.add(device.device_id); }
    }
    const rows = active.map(device => ({
        deviceID: device.device_id,
        name: String(device.name || ""),
        kind: device.kind === "web" ? "web" : "desktop",
        current: !!device.current,
        online: online.has(device.device_id),
        lastSeenAt: Number(device.last_seen_at) || 0,
        needsConfirm: !!device.needs_confirm,
    }));
    rows.sort((a, b) => Number(b.current) - Number(a.current) || Number(b.online) - Number(a.online) || b.lastSeenAt - a.lastSeenAt || a.name.localeCompare(b.name));
    return rows;
}

// renameDeviceLocally returns the device list with one device renamed, for
// optimistic rendering while the server call is in flight.
export function renameDeviceLocally(devices, deviceID, name) {
    return (Array.isArray(devices) ? devices : []).map(device => device?.device_id === deviceID ? { ...device, name } : device);
}

// validDeviceName trims and bounds a user-entered device name.
export function validDeviceName(value) {
    const name = String(value ?? "").replace(/\s+/g, " ").trim();
    return name.length >= 1 && name.length <= 64 ? name : "";
}
