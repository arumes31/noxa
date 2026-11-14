// A stale tab or a reconnecting/offline client must never promise reconnection.
// Audio failure is non-fatal and must not prevent an otherwise valid update.
export const UPDATE_ANNOUNCEMENT_DELAY_MS = 3000;
// The speech waits delayMs: a fast update finishes first and cancel() keeps it
// silent. The returned promise carries cancel().
export function announceConnectedUpdate({ getState, sessionInfo, play, timeoutMs = 1500, delayMs = 0 }) {
    let cancelled = false, delay, wake = () => {};
    const run = (async () => {
        const state = getState(), tab = state?.activeTabID, generation = state?.serverGeneration;
        const sessionGeneration = state?.sessionGeneration;
        if (!tab) return;
        let timer;
        try {
            const session = await Promise.race([sessionInfo(tab), new Promise(resolve => {
                timer = setTimeout(() => resolve(null), timeoutMs);
            })]);
            const current = () => !cancelled && getState()?.activeTabID === tab && getState()?.serverGeneration === generation &&
                getState()?.sessionGeneration === sessionGeneration;
            if (session?.connected !== true || !current()) return;
            if (delayMs > 0) await new Promise(resolve => { wake = resolve; delay = setTimeout(resolve, delayMs); });
            if (current()) await play(current);
        } catch { /* An unavailable announcement must not block the update. */ }
        finally { clearTimeout(timer); }
    })();
    run.cancel = () => { cancelled = true; clearTimeout(delay); wake(); };
    return run;
}

// The process an update starts restores its session silently: no connected,
// reconnected or own channel join cue. Ordinary events announce again once the
// restore has settled.
export const UPDATE_RESTORE_SETTLE_MS = 5000;
export function beginQuietRestore(state) { state.quietRestoreUntil = Infinity; }
export function endQuietRestore(state, restored, now = Date.now()) {
    state.quietRestoreUntil = restored ? now + UPDATE_RESTORE_SETTLE_MS : 0;
}
export function quietRestore(state, now = Date.now()) { return now < (state?.quietRestoreUntil || 0); }
