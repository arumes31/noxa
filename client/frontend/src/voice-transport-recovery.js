// A signaling-successful ICE restart still reuses the failed peer. After the
// bounded restart ladder, replace it at a bounded rate in the same voice scope.
export function voiceTransportFailed(pc) {
    return !!pc && (["failed", "disconnected"].includes(pc.connectionState) ||
        ["failed", "disconnected"].includes(pc.iceConnectionState));
}
export function createVoiceTransportRecovery({ state, rebuild, abort = () => {}, allowed = () => true, now = () => performance.now(), timers = globalThis }) {
    let scope = "", pending = null, busy = null, attempts = 0, next = 0;
    const key = () => JSON.stringify([state.activeTabID, state.serverGeneration, state.sessionGeneration, state.myChannelID, state.myClientID]);
    const unready = pc => !pc || (pc.connectionState !== "closed" && pc.iceConnectionState !== "closed" &&
        (typeof pc.connectionState === "string" ? pc.connectionState !== "connected" || voiceTransportFailed(pc) : !["connected", "completed"].includes(pc.iceConnectionState)));
    const current = (pc, owner) => state.pc === pc && key() === owner && state.myChannelID > 0;
    function cancel(pc) {
        if (!pending || (pc !== undefined && pending.pc !== pc)) return;
        timers.clearTimeout(pending.timer); pending = null;
    }
    function schedule(pc) {
        const owner = key();
        if (scope !== owner) { cancel(); scope = owner; busy = null; attempts = 0; next = 0; }
        if (pending || busy || !current(pc, owner) || !unready(pc) || !allowed()) return false;
        const request = { pc, owner, timer: null };
        pending = request;
        request.timer = timers.setTimeout(async () => {
            if (pending !== request) return;
            pending = null;
            if (!current(pc, owner) || !unready(pc) || !allowed()) return;
            const operation = { pc, owner };
            busy = operation;
            // A replacement peer never resets this budget. A stable connection
            // naturally outlives the cooldown; changing voice scope starts fresh.
            next = now() + [30000, 60000, 120000][Math.min(attempts++, 2)];
            let watchdog;
            const deadline = new Promise(resolve => {
                watchdog = timers.setTimeout(() => {
                    // A hung browser/native operation must release the recovery
                    // budget. Abort only the startup this scope still owns.
                    try {
                        if (busy === operation && key() === owner && allowed() && unready(state.pc)) abort();
                    } catch { /* Startup cancellation must not strand recovery. */ }
                    finally { resolve(); }
                }, 30000);
            });
            try { await Promise.race([rebuild(pc, () => current(pc, owner) && allowed()), deadline]); }
            catch { /* Startup reports its own error; keep recovery bounded. */ }
            finally {
                timers.clearTimeout(watchdog);
                if (busy === operation) {
                    busy = null;
                    if (key() === owner && unready(state.pc)) schedule(state.pc);
                }
            }
        }, Math.max(1000, next - now()));
        return true;
    }
    return { schedule, cancel };
}
