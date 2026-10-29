// A stale tab or a reconnecting/offline client must never promise reconnection.
// Audio failure is non-fatal and must not prevent an otherwise valid update.
export async function announceConnectedUpdate({ getState, sessionInfo, play, timeoutMs = 1500 }) {
    const state = getState(), tab = state?.activeTabID, generation = state?.serverGeneration;
    if (!tab) return;
    let timer;
    try {
        const session = await Promise.race([sessionInfo(tab), new Promise(resolve => {
            timer = setTimeout(() => resolve(null), timeoutMs);
        })]);
        const current = () => getState()?.activeTabID === tab && getState()?.serverGeneration === generation;
        if (session?.connected === true && current()) await play(current);
    } catch { /* An unavailable announcement must not block the update. */ }
    finally { clearTimeout(timer); }
}
