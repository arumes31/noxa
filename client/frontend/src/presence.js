// Presence changes share ownership across manual controls and the idle timer.
const V = () => window.__noxa;
let pending = null;
let intent = 0;
let automaticRestore = null;

export function capturePresenceScope() {
    const s = V().state;
    return { tabID: s.activeTabID, generation: s.serverGeneration, session: s.sessionGeneration, clientID: s.myClientID };
}

export function presenceIsCurrent(scope) {
    const current = capturePresenceScope();
    return Object.keys(current).every(key => current[key] === scope[key]);
}

export function desiredPresence() {
    return pending && presenceIsCurrent(pending.scope) ? pending.status : V().state.myStatus;
}

export function restorePresenceOnActivity(scope = capturePresenceScope()) {
    if (desiredPresence() !== "away") return;
    if (automaticRestore && presenceIsCurrent(automaticRestore.scope) && automaticRestore.intent === intent) return;
    // One attempt per presence intent. Failure must not turn mouse movement
    // into a retry loop; a manual change or a new idle period permits recovery.
    void setPresence("online", "", scope);
    automaticRestore = { scope, intent };
}

export async function setPresence(status, message, scope = capturePresenceScope()) {
    if (!scope.tabID || !scope.clientID || !presenceIsCurrent(scope)) return false;
    intent++;
    const request = { scope, status: status === "online" ? "" : status };
    pending = request;
    let error;
    try {
        error = await window.go.main.App.SetStatusForTab(scope.tabID, status, message);
    } catch (err) {
        error = String(err);
    }
    if (pending !== request) return false;
    pending = null;
    if (!presenceIsCurrent(scope)) return false;
    if (error) {
        V().toast("status failed: " + error, "warn");
        return false;
    }
    // Role-mode native completion confirms server state. Legacy completion
    // retains the existing write-only semantics.
    V().state.myStatus = request.status;
    const me = V().state.clients.find(client => client.client_id === scope.clientID);
    if (me) me.status = request.status;
    return true;
}
