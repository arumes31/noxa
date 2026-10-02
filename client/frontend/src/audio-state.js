import { captureMediaScope, mediaScopeIsCurrent } from "./media-controls.js";
import { t } from "./i18n.js";

let pending = null, active = null, attempted = "";

// Coalesce rapid toggles and serialize acknowledgements. Snapshots never
// overwrite local controls, and a late reply cannot affect another server.
export function publishAudioState() {
    const state = window.__noxa.state, scope = captureMediaScope();
    if (typeof window.go?.main?.App?.SetAudioStateForTab !== "function") return;
    if (!scope.tabID || !scope.clientID || state.replayingTabID) return;
    const request = { scope, muted: !!(state.muted || state.deafened), deafened: !!state.deafened };
    request.key = JSON.stringify([scope, request.muted, request.deafened]);
    if (pending?.key === request.key || (!pending && (active?.key === request.key || attempted === request.key))) return;
    pending = request;
    if (!active) void pump();
}

async function pump() {
    while (pending) {
        const request = pending;
        pending = null;
        if (!mediaScopeIsCurrent(request.scope)) continue;
        active = request;
        attempted = request.key;
        let error;
        try { error = await window.go.main.App.SetAudioStateForTab(request.scope.tabID, request.muted, request.deafened); }
        catch (err) { error = String(err); }
        if (mediaScopeIsCurrent(request.scope)) {
            if (error && !pending) window.__noxa.sysMsg(t("audioState.failed", { error }));
        }
        active = null;
    }
}
