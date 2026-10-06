import { t } from "./i18n.js";
import { escapeHTML } from "./markdown.js";

// A single footer hint follows the quality sampler's lifetime and refreshes.
export function createVoicePlaybackTooltip(trigger) {
    const tooltip = document.createElement("div");
    tooltip.id = "voice-playback-tooltip";
    tooltip.className = "voice-quality-tooltip";
    tooltip.setAttribute("role", "tooltip");
    tooltip.hidden = true;
    document.body.append(tooltip);
    trigger.tabIndex = 0;
    const listeners = new AbortController();
    const options = { signal: listeners.signal };
    let leaveTimer = null, overTrigger = false, overTooltip = false;
    const close = () => {
        clearTimeout(leaveTimer);
        // Hiding a hovered hint need not dispatch mouseleave (for example Escape).
        overTooltip = false;
        tooltip.hidden = true;
    };
    const position = () => {
        if (tooltip.hidden) return;
        const bounds = trigger.getBoundingClientRect();
        const width = document.documentElement.clientWidth;
        const height = document.documentElement.clientHeight;
        if (trigger.hidden || !trigger.isConnected || bounds.bottom <= 0 || bounds.top >= height) { close(); return; }
        tooltip.style.maxHeight = `${Math.max(0, Math.min(height - 16, bounds.top - 16))}px`;
        const size = tooltip.getBoundingClientRect();
        tooltip.style.left = `${Math.max(8, Math.min(bounds.left, width - size.width - 8))}px`;
        tooltip.style.top = `${Math.max(8, bounds.top - size.height - 8)}px`;
    };
    const open = () => {
        clearTimeout(leaveTimer);
        if (trigger.hidden || !tooltip.textContent) return;
        tooltip.hidden = false;
        position();
    };
    const leave = () => {
        clearTimeout(leaveTimer);
        // Keep the hint hoverable across the small gap above its trigger.
        leaveTimer = setTimeout(() => {
            if (!overTrigger && !overTooltip && document.activeElement !== trigger) close();
        }, 120);
    };
    trigger.addEventListener("mouseenter", () => { overTrigger = true; open(); }, options);
    trigger.addEventListener("mouseleave", () => { overTrigger = false; leave(); }, options);
    trigger.addEventListener("focus", open, options);
    trigger.addEventListener("click", open, options);
    trigger.addEventListener("blur", leave, options);
    tooltip.addEventListener("mouseenter", () => { overTooltip = true; clearTimeout(leaveTimer); }, options);
    tooltip.addEventListener("mouseleave", () => { overTooltip = false; leave(); }, options);
    document.addEventListener("keydown", event => {
        if (event.key === "Escape" && !tooltip.hidden) { close(); event.stopPropagation(); }
    }, options);
    window.addEventListener("resize", position, options);
    window.addEventListener("scroll", position, { ...options, capture: true });

    return {
        update(health, route) {
            if (!health) {
                close();
                tooltip.textContent = "";
                trigger.removeAttribute("aria-describedby");
                return;
            }
            const esc = value => escapeHTML(String(value ?? ""));
            const transport = route ? `${route.protocol.toUpperCase()} · ${route.local_candidate} → ${route.remote_candidate}${route.local_candidate === "relay" ? ` · TURN ${route.relay_protocol}` : ""}` : "";
            const content = `<div class="voice-quality-tooltip-row"><span>${esc(t("diagnostics.playbackHealth"))}</span><strong data-quality="${esc(health.quality)}">${esc(t(`diagnostics.health.${health.quality}`))}</strong></div>
                ${transport ? `<div class="voice-quality-tooltip-row"><span>${esc(t("diagnostics.transport"))}</span><strong>${esc(transport)}</strong></div>` : ""}
                ${health.reasons.length ? `<ul class="voice-quality-tooltip-reasons">${health.reasons.map(reason => `<li>${esc(t(`diagnostics.reason.${reason}`))}</li>`).join("")}</ul>` : ""}
                <div class="voice-quality-tooltip-notes"><p>${esc(t("diagnostics.healthMeaning"))}</p>${route ? `<p>${esc(t("diagnostics.transportMeaning"))}</p>` : ""}</div>`;
            if (tooltip.innerHTML !== content) tooltip.innerHTML = content;
            trigger.setAttribute("aria-describedby", tooltip.id);
            position();
        },
        destroy() {
            close();
            listeners.abort();
            trigger.removeAttribute("aria-describedby");
            tooltip.remove();
        },
    };
}
