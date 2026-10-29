// studio-captures.js — reference-counted capture ownership for the Studio.
//
// A capture (one explicit getDisplayMedia/getUserMedia result) is shared by
// every scene source that references it, in the draft AND in the immutable
// program snapshot. Holders ("draft", "live", "outgoing") declare the set of
// capture IDs they need; a capture stops the moment no holder needs it, so
// removing a draft source never stops a picture that is still on air, and an
// unused draft capture never lingers.
//
// An ended, revoked or muted track is neutral immediately: the registry
// detaches it from its video element (dropping the last decoded frame) and
// drawable() returns null, so no confidential frame can freeze on output.

export function createCaptureRegistry({ createVideo = () => globalThis.document.createElement("video"), onChange = () => {}, onRelease = () => {} } = {}) {
    const entries = new Map();
    const holders = new Map();
    let next = 1;
    let disposed = false;

    function detach(entry) {
        const video = entry.video;
        if (!video) return;
        entry.video = null;
        try { video.pause?.(); } catch { /* already stopped */ }
        video.srcObject = null;
        video.removeAttribute?.("src");
        try { video.load?.(); } catch { /* nothing loaded */ }
    }

    // The lease hook runs at most once per capture.
    function releaseOnce(entry) {
        if (entry.leaseReleased) return;
        entry.leaseReleased = true;
        onRelease(entry.stream);
    }

    function neutralize(entry, state) {
        if (entry.state === "ended" || entry.state === "released") return;
        entry.state = state;
        if (state === "ended") {
            detach(entry);
            releaseOnce(entry);
            for (const track of entry.stream.getTracks()) track.stop();
        }
        onChange(entry.id, state);
    }

    function release(entry) {
        entries.delete(entry.id);
        for (const [track, listeners] of entry.listeners) {
            for (const [type, listener] of listeners) track.removeEventListener(type, listener);
        }
        entry.listeners.clear();
        detach(entry);
        releaseOnce(entry);
        for (const track of entry.stream.getTracks()) track.stop();
        entry.state = "released";
        onChange(entry.id, "released");
    }

    function count(id) {
        let n = 0;
        for (const set of holders.values()) if (set.has(id)) n++;
        return n;
    }

    function collect() {
        for (const entry of [...entries.values()]) if (count(entry.id) === 0) release(entry);
    }

    return {
        // add takes ownership of a fresh capture stream. The caller must
        // reference it from a holder before the next setHolder() call, or it
        // is released then.
        add(stream, { kind, label = "", surface = "", audio = false } = {}) {
            if (disposed) { for (const track of stream.getTracks()) track.stop(); throw new Error("studio stopped"); }
            const video = stream.getVideoTracks()[0];
            if (!video || video.readyState === "ended") { for (const track of stream.getTracks()) track.stop(); throw new Error("capture produced no video track"); }
            const id = `c${next++}`;
            const element = createVideo();
            element.muted = true;
            element.playsInline = true;
            element.autoplay = true;
            // Video only: audio is routed explicitly by studio-audio.js.
            element.srcObject = new MediaStream([video]);
            const audioTrack = audio ? stream.getAudioTracks().find(track => track.readyState === "live") || null : null;
            // Audio that was not explicitly requested never stays alive.
            for (const track of stream.getAudioTracks()) if (track !== audioTrack) { track.stop(); stream.removeTrack(track); }
            const entry = { id, kind, label: String(label).slice(0, 120), surface, stream, video: element, audioTrack, state: "live", listeners: new Map() };
            const listen = (track, type, listener) => {
                track.addEventListener(type, listener);
                if (!entry.listeners.has(track)) entry.listeners.set(track, []);
                entry.listeners.get(track).push([type, listener]);
            };
            listen(video, "ended", () => neutralize(entry, "ended"));
            listen(video, "mute", () => { if (entry.state === "live") { entry.state = "muted"; onChange(id, "muted"); } });
            listen(video, "unmute", () => { if (entry.state === "muted") { entry.state = "live"; onChange(id, "live"); } });
            if (audioTrack) listen(audioTrack, "ended", () => { entry.audioTrack = null; onChange(id, "audio-ended"); });
            entries.set(id, entry);
            element.play?.()?.catch?.(() => {});
            return id;
        },
        get(id) { return entries.get(id) || null; },
        state(id) { return entries.get(id)?.state || "missing"; },
        // drawable returns a video element only while its track is live and
        // has decoded a frame; anything else renders neutral.
        drawable(id) {
            const entry = entries.get(id);
            if (!entry || entry.state !== "live" || !entry.video) return null;
            const track = entry.stream.getVideoTracks()[0];
            if (!track || track.readyState !== "live" || track.muted) {
                if (track?.readyState === "ended") neutralize(entry, "ended");
                return null;
            }
            return entry.video.readyState >= 2 && entry.video.videoWidth > 0 ? entry.video : null;
        },
        audioTracks() {
            return [...entries.values()].filter(entry => entry.audioTrack?.readyState === "live").map(entry => ({ id: entry.id, track: entry.audioTrack }));
        },
        setHolder(name, ids) {
            holders.set(name, new Set(ids));
            collect();
        },
        refs: count,
        ids() { return [...entries.keys()]; },
        dispose() {
            disposed = true;
            holders.clear();
            for (const entry of [...entries.values()]) release(entry);
        },
    };
}
