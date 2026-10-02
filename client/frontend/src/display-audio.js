export function displayAudioOptions(mode) {
    if (mode === "application") return {
        audio: true, windowAudio: "window", systemAudio: "exclude", monitorTypeSurfaces: "exclude", surfaceSwitching: "exclude",
    };
    if (mode === "system") return { audio: true, windowAudio: "system", systemAudio: "include" };
    return { audio: false, windowAudio: "exclude", systemAudio: "exclude" };
}

export function validateDisplayAudio(stream, mode) {
    const audio = stream.getAudioTracks();
    if (mode === "application") {
        // windowAudio is only a preference: Chromium can fall back to system
        // loopback. Accept only its confirmed application source label and a
        // window. Unknown runtimes fail closed before any track is published.
        // chrome/browser/media/webrtc/desktop_capture_devices_util.cc:
        // GetAudioMediaStreamDeviceName returns this literal for kApplication.
        const isWindow = stream.getVideoTracks()[0]?.getSettings().displaySurface === "window";
        if (!isWindow || audio.length !== 1 || audio[0].label !== "Application Audio" || audio[0].readyState !== "live") {
            stream.getTracks().forEach(track => track.stop());
            throw new Error("share.applicationUnavailable");
        }
    } else if (mode !== "system") {
        audio.forEach(track => { track.stop(); stream.removeTrack(track); });
    }
}
