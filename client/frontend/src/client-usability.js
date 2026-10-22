import { t } from "./i18n.js";
import "./client-usability.css";

import { sidebarWidth } from "./client-usability-model.js";

export function initClientUsability() {
    const body = document.getElementById("app-body"), sidebar = document.getElementById("sidebar");
    const grip = document.createElement("div");
    grip.id = "sidebar-resize"; grip.tabIndex = 0; grip.setAttribute("role", "separator"); grip.setAttribute("aria-orientation", "vertical");
    grip.setAttribute("aria-controls", "sidebar"); sidebar.append(grip);
    let preferred = 290;
    try { preferred = Number(localStorage.getItem("noxa.sidebar.width")) || 290; } catch { /* Storage is optional. */ }
    const apply = () => {
        const width = sidebarWidth(preferred, innerWidth);
        body.style.setProperty("--sidebar-width", width + "px");
        grip.setAttribute("aria-valuemin", "220"); grip.setAttribute("aria-valuemax", String(Math.max(220, Math.min(420, innerWidth - 400)))); grip.setAttribute("aria-valuenow", String(width));
    };
    const save = () => { try { localStorage.setItem("noxa.sidebar.width", String(preferred)); } catch { /* Storage is optional. */ } };
    let drag = null;
    grip.onpointerdown = event => {
        if (event.button !== 0) return;
        event.preventDefault(); grip.focus(); drag = { id: event.pointerId, x: event.clientX, width: sidebar.getBoundingClientRect().width };
        grip.setPointerCapture(event.pointerId); document.body.classList.add("resizing-sidebar");
    };
    grip.onpointermove = event => { if (drag?.id !== event.pointerId) return; preferred = sidebarWidth(drag.width + event.clientX - drag.x, innerWidth); apply(); };
    const finish = () => { if (!drag) return; drag = null; document.body.classList.remove("resizing-sidebar"); save(); };
    grip.onpointerup = finish; grip.onpointercancel = finish; grip.onlostpointercapture = finish;
    grip.ondblclick = () => { preferred = 290; apply(); save(); };
    grip.onkeydown = event => {
        if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
        event.preventDefault(); preferred = event.key === "Home" ? 220 : event.key === "End" ? 420 : sidebarWidth(preferred, innerWidth) + (event.key === "ArrowLeft" ? -10 : 10);
        preferred = sidebarWidth(preferred, innerWidth); apply(); save();
    };
    const status = document.createElement("div"); status.id = "workspace-connection-status"; status.setAttribute("role", "status");
    document.querySelector("#voice-bar .voice-info").prepend(status);
    const render = () => {
        const state = window.__noxa.state, pill = document.getElementById("conn-pill");
        const phase = document.getElementById("channel-connection-overlay")?.dataset.phase;
        const connected = pill.classList.contains("up") && (!phase || phase === "connected");
        const server = state.reconnectInFlight || state.reconnectAttempts ? "reconnecting" : connected ? "connected" : "offline";
        const voice = window.__noxaPrivateCalls?.active?.() ? "privateCall" : !connected ? "voiceOffline" : state.myChannelID ? "voiceConnected" : "noVoice";
        status.textContent = t("usability.server." + server);
        status.title = status.textContent + " · " + t("usability." + voice);
        status.dataset.state = server;
        grip.setAttribute("aria-label", t("usability.resize")); grip.title = t("usability.resizeHelp");
    };
    new MutationObserver(render).observe(document.getElementById("conn-pill"), { attributes: true, childList: true, characterData: true, subtree: true });
    window.addEventListener("noxa-connection-state", render);
    window.addEventListener("noxa-private-call-changed", render);
    window.addEventListener("noxa-workspace-rendered", render);
    window.addEventListener("noxa-language-changed", render);
    window.addEventListener("resize", apply);
    apply(); render();
}
