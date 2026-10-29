import { t } from "./i18n.js";
import { setStatusIcon } from "./icons.js";
import { channelMediaStatus, mediaStatusText } from "./channel-media.js";
import { encryptionDetails } from "./encryption-details.js";

// Hoverable, scrollable and keyboard-accessible: native title tooltips clip
// long device rosters and cannot be read reliably using keyboard focus.
export function createEncryptionTooltip(trigger, { context, voiceOnly = false }) {
    const tip = document.createElement("div");
    tip.id = `${trigger.id}-details`;
    tip.className = "encryption-tooltip";
    tip.role = "tooltip";
    tip.hidden = true;
    document.body.append(tip);
    trigger.tabIndex = 0;
    trigger.removeAttribute("title");
    trigger.setAttribute("aria-describedby", tip.id);
    let request = 0, timer, refreshTimer, hovering = false, overTip = false;
    const label = () => (voiceOnly && mediaStatusText(channelMediaStatus())) || t("security.details.title");
    const position = () => {
        if (tip.hidden) return;
        const rect = trigger.getBoundingClientRect();
        const width = document.documentElement.clientWidth, height = document.documentElement.clientHeight;
        if (!trigger.checkVisibility() || rect.bottom < 0 || rect.top > height) { close(); return; }
        const above = Math.max(0, rect.top - 16), below = Math.max(0, height - rect.bottom - 16);
        const useAbove = above > below;
        tip.style.maxHeight = `${useAbove ? above : below}px`;
        tip.style.left = `${Math.max(8, Math.min(rect.left, width - tip.offsetWidth - 8))}px`;
        tip.style.top = `${useAbove ? rect.top - tip.offsetHeight - 8 : rect.bottom + 8}px`;
    };
    function close() { ++request; clearTimeout(timer); clearInterval(refreshTimer); tip.hidden = true; tip.textContent = ""; overTip = false; }
    async function refresh() {
        const revision = ++request;
        const snapshot = context();
        const matches = () => {
            const now = context();
            return revision === request && !tip.hidden && snapshot.tabID === now.tabID && snapshot.generation === now.generation && snapshot.sessionGeneration === now.sessionGeneration;
        };
        setStatusIcon(trigger, "lock", label());
        // close() clears the previous context. Keep an unchanged live roster
        // intact so periodic refreshes do not reset a reader's scroll position.
        if (!tip.textContent) tip.textContent = t("security.details.loading");
        position();
        const app = window.go?.main?.App;
        const [session, diagnostics, capability] = await Promise.allSettled([
            app?.SessionInfoForTab(snapshot.tabID),
            voiceOnly ? undefined : app?.E2EEDiagnosticsForTab?.(snapshot.tabID, ""),
            app?.MediaE2EEStatusForTab(snapshot.tabID),
        ]);
        if (!matches()) return;
        const value = result => result.status === "fulfilled" ? result.value : undefined;
        const text = encryptionDetails({ session: value(session), diagnostics: value(diagnostics), capability: value(capability), media: channelMediaStatus(), voiceOnly });
        if (tip.textContent !== text) tip.textContent = text;
        position();
    }
    const open = () => {
        clearTimeout(timer);
        if (!trigger.checkVisibility()) return;
        tip.hidden = false;
        void refresh();
        clearInterval(refreshTimer);
        refreshTimer = setInterval(() => { void refresh(); }, 5000);
    };
    const leave = () => { clearTimeout(timer); timer = setTimeout(() => { if (!hovering && !overTip && document.activeElement !== trigger) close(); }, 160); };
    trigger.addEventListener("mouseenter", () => { hovering = true; open(); });
    trigger.addEventListener("mouseleave", () => { hovering = false; leave(); });
    trigger.addEventListener("focus", open);
    trigger.addEventListener("blur", leave);
    trigger.addEventListener("click", open);
    tip.addEventListener("mouseenter", () => { overTip = true; clearTimeout(timer); });
    tip.addEventListener("mouseleave", () => { overTip = false; leave(); });
    // Arrow/Page keys scroll long key-holder lists without moving focus.
    trigger.addEventListener("keydown", event => {
        const amount = { ArrowDown: 40, ArrowUp: -40, PageDown: 240, PageUp: -240 }[event.key];
        if (!tip.hidden && amount) { event.preventDefault(); tip.scrollTop += amount; }
    });
    document.addEventListener("keydown", event => { if (event.key === "Escape" && !tip.hidden) { close(); event.stopPropagation(); } });
    window.addEventListener("resize", position);
    window.addEventListener("scroll", position, true);
    setStatusIcon(trigger, "lock", label());
    return { update() { if (!tip.hidden) void refresh(); }, close };
}
