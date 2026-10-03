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
