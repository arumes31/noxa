const V = () => window.__noxa;
let whisperRequest = null;

export function captureMediaScope() {
    const s = V().state;
    return { tabID: s.activeTabID, generation: s.serverGeneration, session: s.sessionGeneration, clientID: s.myClientID, channelID: s.myChannelID };
}

export function mediaScopeIsCurrent(scope) {
    const current = captureMediaScope();
    return Object.keys(current).every(key => current[key] === scope[key]);
}

// Null means obsolete; empty string confirms success; a string reports failure.
// Shared ownership prevents a hotkey completion from overwriting newer settings.
export async function setWhisperRouting(config, scope = captureMediaScope()) {
    if (!scope.tabID || !scope.clientID || !mediaScopeIsCurrent(scope)) return null;
    const request = { scope };
    whisperRequest = request;
    let error;
    try {
        error = await window.go.main.App.WhisperSetForTab(scope.tabID, config.clients, config.channels, config.active);
    } catch (err) { error = String(err); }
    if (whisperRequest !== request || !mediaScopeIsCurrent(scope)) return null;
    whisperRequest = null;
    return error || "";
}
