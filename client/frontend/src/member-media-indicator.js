import { memberMediaState } from "./member-media-state.js";
import { channelMediaStatus } from "./channel-media.js";
import { t } from "./i18n.js";

function paint(element, state, status) {
    const value = memberMediaState({ self: element.dataset.mediaClient === String(state.myClientID),
        connected: state.pc?.connectionState === "connected", failed: ["failed", "closed"].includes(state.pc?.connectionState), status, clientID: element.dataset.mediaClient });
    element.dataset.state = value;
    element.hidden = value === "ready";
    const row = element.closest('[role="treeitem"]');
    if (row) {
        row.dataset.mediaBaseAriaLabel ??= row.getAttribute("aria-label") || "";
        row.setAttribute("aria-label", row.dataset.mediaBaseAriaLabel + (element.hidden ? "" : `, ${t(`e2ee.member.${value}`)}`));
    }
    if (element.hidden) {
        element.removeAttribute("title"); element.removeAttribute("aria-label");
        element.textContent = "";
        return;
    }
    const label = t(`e2ee.member.${value}`);
    element.title = label; element.setAttribute("aria-label", label);
    element.innerHTML = value === "unavailable" ? '<span class="member-media-unavailable" aria-hidden="true">!</span>' : '<span class="member-media-ring"></span>';
}
export function createMemberMediaIndicator(client, state) {
    if (!(state.myChannelID > 0) || client.channel_id !== state.myChannelID) return null;
    const element = document.createElement("span");
    element.className = "member-media-indicator"; element.dataset.mediaClient = String(client.client_id);
    element.setAttribute("role", "img"); paint(element, state, channelMediaStatus());
    return element;
}
export function updateMemberMediaIndicators(state, status) {
    for (const element of document.querySelectorAll(".member-media-indicator")) paint(element, state, status);
}
