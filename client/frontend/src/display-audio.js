export function displayAudioOptions(mode) {
    if (mode === "application") return {
        audio: true, windowAudio: "window", systemAudio: "exclude", monitorTypeSurfaces: "exclude", surfaceSwitching: "exclude",
    };
    if (mode === "system") return { audio: true, windowAudio: "system", systemAudio: "include" };
    return { audio: false, windowAudio: "exclude", systemAudio: "exclude" };
}

// Returns the audio mode safe to publish. Validation never stops the video.
export function validateDisplayAudio(stream, mode) {
    const audio = stream.getAudioTracks();
    if (mode === "application") {
        // windowAudio is only a preference: Chromium can fall back to system
        // loopback. Accept only its confirmed application source label and a
        // window. Drop unconfirmed audio before publication, keeping video live.
        // chrome/browser/media/webrtc/desktop_capture_devices_util.cc:
        // GetAudioMediaStreamDeviceName returns this literal for kApplication.
        const isWindow = stream.getVideoTracks()[0]?.getSettings().displaySurface === "window";
        if (!isWindow || audio.length !== 1 || audio[0].label !== "Application Audio" || audio[0].readyState !== "live") {
            mode = "none";
        }
    }
    if (mode !== "application" && mode !== "system") {
        audio.forEach(track => { track.stop(); stream.removeTrack(track); });
        return "none";
    }
    return audio.length ? mode : "none";
}
