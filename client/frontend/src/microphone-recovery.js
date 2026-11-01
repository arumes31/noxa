// Observe one capture owner. Hardware loss never starts a replacement capture.
export function watchMicrophone(track, onLost, devices = navigator.mediaDevices) {
    if (!track) return () => {};
    const settings = track.getSettings?.() || {};
    const deviceID = settings.deviceId || "";
    const groupID = ["default", "communications"].includes(deviceID) ? settings.groupId : "";
    let active = true, revision = 0;
    const stop = () => {
        active = false;
        track.removeEventListener("ended", lost);
        devices?.removeEventListener?.("devicechange", changed);
    };
    const lost = () => {
        if (!active) return;
        stop(); onLost(track);
    };
    const changed = async () => {
        if (!active || !deviceID) return;
        const request = ++revision;
        try {
            const inventory = await devices.enumerateDevices();
            if (active && request === revision && !inventory.some(device => device.kind === "audioinput" && device.deviceId === deviceID && (!groupID || device.groupId === groupID))) lost();
        } catch { /* A discovery failure does not prove the microphone is gone. */ }
    };
    track.addEventListener("ended", lost);
    devices?.addEventListener?.("devicechange", changed);
    if (track.readyState === "ended") queueMicrotask(lost);
    return stop;
}

// Report loss of an explicitly selected output without silently changing it.
export function watchAudioOutput(selected, onLost, devices = navigator.mediaDevices) {
    let active = true, missing = "", revision = 0;
    const changed = async () => {
        const request = ++revision, id = selected();
        if (!active || !id || id === "default") return;
        try {
            const inventory = await devices.enumerateDevices();
            if (!active || request !== revision || selected() !== id) return;
            if (inventory.some(device => device.kind === "audiooutput" && device.deviceId === id)) missing = "";
            else if (missing !== id) { missing = id; onLost(); }
        } catch { /* Discovery failure alone does not establish device loss. */ }
    };
    devices?.addEventListener?.("devicechange", changed);
    return () => { active = false; devices?.removeEventListener?.("devicechange", changed); };
}

// Only the explicitly registered microphone slot may be reused. Serialize detach
// and replacement so a late device-loss operation cannot clear its successor.
export function createMicrophoneSenderOwner() {
    const owners = new WeakMap();
    const queue = (owner, action) => {
        const result = owner.pending.catch(() => {}).then(action);
        owner.pending = result;
        return result;
    };
    return {
        remember(pc, transceiver) {
            owners.set(pc, { transceiver, track: transceiver.sender.track, pending: Promise.resolve() });
            return transceiver;
        },
        detach(pc, track) {
            const owner = owners.get(pc);
            if (!owner) return Promise.resolve(false);
            return queue(owner, async () => {
                if (owner.track !== track) return false;
                await owner.transceiver.sender.replaceTrack(null);
                owner.track = null;
                return true;
            });
        },
        attach(pc, track, options, current) {
            let owner = owners.get(pc);
            if (!owner) {
                owner = { transceiver: null, track: null, pending: Promise.resolve() };
                owners.set(pc, owner);
            }
            return queue(owner, async () => {
                if (!current()) throw new DOMException("voice session changed", "AbortError");
                if (!owner.transceiver || owner.transceiver.stopped) {
                    owner.transceiver = pc.addTransceiver(track, options);
                } else {
                    await owner.transceiver.sender.replaceTrack(track);
                }
                owner.track = track;
                owner.transceiver.direction = options.direction;
                if (!current()) {
                    await owner.transceiver.sender.replaceTrack(null);
                    owner.track = null;
                    throw new DOMException("voice session changed", "AbortError");
                }
                return owner.transceiver;
            });
        },
    };
}
