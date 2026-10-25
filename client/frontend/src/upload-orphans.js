// Orphaned upload sweeps (504). Go tracks every uploaded attachment until its
// message is published; this module asks it to clean the current server's
// leftovers shortly after connecting and then once a minute, and reports the
// result. Uncertain deliveries are never deleted and are announced once.
import { t } from "./i18n.js";

export function createUploadOrphanSweeper({ app, state, toast, now = Date.now, interval = 60_000, pollInterval = 5_000, setTimer = setInterval, clearTimer = clearInterval }) {
    const swept = new Map();
    const announcedUncertain = new Set();
    let busy = false;
    const sessionKey = s => s.activeTabID + "|" + s.serverGeneration;
    async function sweep(force = false) {
        const s = state();
        if (busy || !s.activeTabID || !s.myClientID || typeof app()?.CleanupOrphanUploadsForTab !== "function") return null;
        const key = sessionKey(s);
        const last = swept.get(key);
        if (!force && last !== undefined && now() - last < interval) return null;
        busy = true; swept.set(key, now());
        try {
            const result = await app().CleanupOrphanUploadsForTab(s.activeTabID);
            if (sessionKey(state()) !== key) return result;
            if (result?.removed > 0) toast(t("upload.orphans.cleaned", { count: result.removed }), "info");
            if (result?.kept > 0 && !announcedUncertain.has(key)) { announcedUncertain.add(key); toast(t("upload.orphans.uncertain", { count: result.kept }), "warn"); }
            return result;
        } catch { return null; }
        finally { busy = false; }
    }
    const timer = setTimer(() => void sweep(), pollInterval);
    return { sweep, dispose() { clearTimer(timer); } };
}
