// The display track owns wake protection, independently of upload/viewer state.
// Native leases expire after 120 s if this renderer crashes or stops renewing.
export function createDisplayCaptureLeases({
    native = () => globalThis.window?.go?.main?.App,
    every = callback => setInterval(callback, 15000), cancel = clearInterval,
    page = globalThis.window, warn = () => console.warn("Display capture wake protection unavailable; capture continues."),
} = {}) {
    const entries = new Map();
    let pageHidden = false;
    // Register before any picker opens: getDisplayMedia may settle after pagehide.
    page?.addEventListener("pagehide", () => { pageHidden = true; releaseAll(); });
    page?.addEventListener("pageshow", () => { pageHidden = false; });
    const warning = entry => { if (!entry.warned) { entry.warned = true; warn(); } };
    const releaseID = async (entry, id) => {
        try { await entry.app.ReleaseDisplayCaptureLease(id); }
        catch { warning(entry); } // Native TTL still bounds failed best-effort cleanup.
    };
    function release(track) {
        const entry = entries.get(track);
        if (!entry) return;
        entries.delete(track);
        entry.active = false;
        cancel(entry.timer);
        track.removeEventListener("ended", entry.ended);
        if (entry.id) { const id = entry.id; entry.id = null; void releaseID(entry, id); }
    }
    async function maintain(entry) {
        if (!entry.active || entry.busy) return;
        if (entry.track.readyState === "ended") { release(entry.track); return; }
        entry.busy = true;
        try {
            if (entry.id) {
                const id = entry.id;
                try { await entry.app.RefreshDisplayCaptureLease(id); }
                catch {
                    if (!entry.active) return;
                    warning(entry);
                    entry.id = null;
                    void releaseID(entry, id);
                    // A suspended renderer may outlive the native TTL. Reacquire
                    // only for this still-owned live track, once per timer tick.
                }
            }
            if (!entry.active) return;
            if (entry.track.readyState === "ended") { release(entry.track); return; }
            if (!entry.id) {
                const app = native();
                if (typeof app?.AcquireDisplayCaptureLease !== "function" ||
                    typeof app.RefreshDisplayCaptureLease !== "function" || typeof app.ReleaseDisplayCaptureLease !== "function") {
                    warning(entry); return;
                }
                entry.app = app;
                const id = await app.AcquireDisplayCaptureLease();
                if (typeof id !== "string" || !id) { warning(entry); return; }
                if (!entry.active || entry.track.readyState === "ended") {
                    if (entry.active) release(entry.track);
                    void releaseID(entry, id); return;
                }
                entry.id = id;
            }
        } catch { warning(entry); }
        finally { entry.busy = false; }
    }
    function retain(track) {
        if (pageHidden || !track || track.kind !== "video" || track.readyState === "ended" || entries.has(track)) return;
        const entry = { track, active: true, busy: false, id: null, app: null, warned: false, ended: () => release(track) };
        entries.set(track, entry);
        track.addEventListener("ended", entry.ended, { once: true });
        entry.timer = every(() => { void maintain(entry); });
        void maintain(entry);
    }
    function releaseAll() { for (const track of entries.keys()) release(track); }
    return { retain, release, releaseAll };
}

const displayLeases = createDisplayCaptureLeases();
export function retainDisplayCapture(stream) {
    for (const track of stream?.getVideoTracks() || []) displayLeases.retain(track);
}
export function stopDisplayCapture(stream) {
    for (const track of stream?.getTracks() || []) {
        displayLeases.release(track); // MediaStreamTrack.stop() does not emit ended.
        track.stop();
    }
}
