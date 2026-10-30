// Retain only an already authorized capture, never acquire a new source here.
export function createShareResume({ stop, changed = () => {}, now = Date.now, schedule = setTimeout, cancelTimer = clearTimeout }) {
    let active = null, pending = null, timer;
    const live = entry => entry?.stream?.getVideoTracks().some(track => track.readyState === "live");
    function cancel() {
        const old = pending;
        pending = null; cancelTimer(timer); timer = null;
        if (old) { stop(old.stream); old.options.studio?.onEnded?.(); }
        changed();
    }
    return {
        remember(stream, options, scope) { active = { stream, options, scope: { ...scope } }; },
        settings(stream) { return active?.stream === stream && live(active) ? active.options : null; },
        pause(stream) {
            if (pending) return pending.stream;
            if (!stream || active?.stream !== stream || !live(active)) return null;
            pending = { ...active, expires: now() + 120000, enabled: stream.getTracks().map(track => [track, track.enabled]) };
            for (const [track] of pending.enabled) track.enabled = false;
            const owner = pending;
            stream.getVideoTracks()[0].addEventListener("ended", () => { if (pending === owner) cancel(); }, { once: true });
            timer = schedule(cancel, 120000);
            changed(); return stream;
        },
        take(scope) {
            if (!pending) return null;
            if (pending.expires <= now() || !live(pending) || scope.tabID !== pending.scope.tabID ||
                scope.channelID !== pending.scope.channelID || scope.uid !== pending.scope.uid) { cancel(); return null; }
            const result = pending; pending = null; cancelTimer(timer); timer = null;
            for (const [track, enabled] of result.enabled) track.enabled = enabled;
            changed(); return result;
        },
        pending: () => pending,
        cancel,
    };
}
