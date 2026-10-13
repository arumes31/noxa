import "./connection-overlay.css";

// Rendering only: retry timers and native connection ownership remain with the
// existing tab reconnect coordinator. A replayed tree is not authentication.
export function createConnectionOverlay({ getState, translate, retry, cancel, canConfigure, closeMenus = () => {} }) {
    const records = new Map(), previousInert = new Map();
    const sidebar = document.getElementById("sidebar"), details = document.getElementById("details");
    const overlay = document.createElement("section");
    overlay.id = "channel-connection-overlay"; overlay.hidden = true;
    overlay.innerHTML = '<div class="connection-overlay-card"><button type="button" data-close-channels></button><h2 tabindex="-1"></h2><p class="connection-overlay-explanation"></p><p class="connection-overlay-countdown" aria-live="off"></p><p class="connection-overlay-reason"></p><div class="connection-overlay-actions"><button type="button" data-retry></button><button type="button" data-cancel></button></div><span class="sr-only" role="status" aria-live="polite"></span></div>';
    sidebar?.append(overlay);
    let announced = "", blocked = false;
    const matches = (tabID, generation, session) => { const state = getState();
        return state.activeTabID === tabID && state.serverGeneration === generation && state.sessionGeneration === session; };
    function entry(tabID) { if (!records.has(tabID)) records.set(tabID, { phase: "checking", retry: null, reason: "", terminal: false }); return records.get(tabID); }
    function render() {
        const state = getState(), record = records.get(state.activeTabID), visible = !!state.activeTabID && !!record && record.phase !== "connected";
        let hadPaneFocus = visible && !blocked && [sidebar, details].some(pane => pane?.contains(document.activeElement));
        if (visible && !blocked) { closeMenus(); hadPaneFocus ||= [sidebar, details].some(pane => pane?.contains(document.activeElement)); }
        for (const pane of [sidebar, details]) {
            if (!pane) continue;
            pane.classList.toggle("connection-unavailable", visible);
            for (const child of pane.children) {
                if (child === overlay) continue;
                if (visible) { if (!previousInert.has(child)) previousInert.set(child, child.inert); child.inert = true; }
                else if (previousInert.has(child)) { child.inert = previousInert.get(child); previousInert.delete(child); }
            }
        }
        const hadOverlayFocus = overlay.contains(document.activeElement);
        blocked = visible; overlay.hidden = !visible;
        if (!visible) {
            announced = "";
            if (hadOverlayFocus) {
                const target = document.getElementById(innerWidth <= 720 ? "workspace-sidebar-toggle" : "channels-heading");
                if (target) { if (target.id === "channels-heading") target.tabIndex = -1; target.focus({ preventScroll: true }); }
            }
            return;
        }
        overlay.querySelector("[data-close-channels]").textContent = translate("workspace.labels.closeChannels");
        const phase = record.phase === "checking" ? "checking" : record.retry?.inFlight ? "reconnecting" : "disconnected";
        const title = translate(`connectionOverlay.${phase}`);
        overlay.querySelector("h2").textContent = title;
        overlay.querySelector(".connection-overlay-explanation").textContent = translate(record.terminal ? "connectionOverlay.stopped" : "connectionOverlay.explanation");
        overlay.querySelector(".connection-overlay-countdown").textContent = record.retry
            ? translate(record.retry.inFlight ? "runtime.reconnectAttempt" : "runtime.retry", { attempt: record.retry.attempts, seconds: record.retry.remaining }) : "";
        overlay.querySelector(".connection-overlay-reason").textContent = record.reason || (record.unknown ? translate("connectionOverlay.unknown") : "");
        const retryButton = overlay.querySelector("[data-retry]"), cancelButton = overlay.querySelector("[data-cancel]");
        retryButton.textContent = translate("connectionOverlay.retry"); retryButton.disabled = phase === "checking" || !!record.retry?.inFlight || !canConfigure();
        cancelButton.textContent = translate("connectionOverlay.cancel"); cancelButton.hidden = !record.retry; cancelButton.disabled = !!record.retry?.inFlight;
        const announcement = `${state.activeTabID}:${phase}:${record.terminal}`;
        if (announced !== announcement) { announced = announcement; overlay.querySelector('[role="status"]').textContent = title; }
        if (hadPaneFocus) overlay.querySelector("h2").focus({ preventScroll: true });
    }
    overlay.querySelector("[data-close-channels]").onclick = () => document.getElementById("workspace-sidebar-close")?.click();
    overlay.querySelector("[data-retry]").onclick = () => { if (!overlay.hidden) void retry(); };
    overlay.querySelector("[data-cancel]").onclick = () => { if (!overlay.hidden) void cancel(); };
    window.addEventListener("noxa-language-changed", () => { announced = ""; render(); });
    return {
        select(tabID) {
            if (tabID) { const value = entry(tabID); if (value.phase === "connected") value.phase = "checking"; }
            render();
        },
        lost(tabID) { if (!tabID) return; Object.assign(entry(tabID), { phase: "disconnected", retry: null, unknown: false }); render(); },
        progress(tabID, progress) {
            if (!tabID) return;
            const value = entry(tabID);
            value.retry = progress ? { inFlight: !!progress.inFlight, attempts: progress.attempts, remaining: progress.remaining } : null;
            if (progress) Object.assign(value, { phase: "disconnected", terminal: false, reason: "", unknown: false });
            render();
        },
        terminal(tabID, reason) { if (!tabID) return; Object.assign(entry(tabID), { phase: "disconnected", retry: null, terminal: true, unknown: false, reason: String(reason || "").slice(0, 2048) }); render(); },
        confirmed(tabID, generation, session, info) {
            if (!matches(tabID, generation, session)) return;
            const value = entry(tabID);
            if (info?.connected && info.client_id) Object.assign(value, { phase: "connected", retry: null, reason: "", terminal: false, unknown: false });
            else value.phase = "disconnected";
            render();
        },
        unavailable(tabID, generation, session) {
            if (!matches(tabID, generation, session)) return;
            Object.assign(entry(tabID), { phase: "disconnected", unknown: true }); render();
        },
        attempt(tabID, generation, session, active, reason = "") {
            if (!matches(tabID, generation, session)) return;
            Object.assign(entry(tabID), { phase: "disconnected", retry: active ? { inFlight: true, attempts: 1, remaining: 0 } : null,
                reason: String(reason || "").slice(0, 2048), terminal: false, unknown: false }); render();
        },
        forget(tabID) { records.delete(tabID); render(); },
    };
}
