import { memberMediaState } from "./member-media-state.js";
import { channelMediaStatus } from "./channel-media.js";
import { t } from "./i18n.js";
import { icon } from "./icons.js";

function paint(element, state, status) {
    const value = memberMediaState({ self: element.dataset.mediaClient === String(state.myClientID),
        connected: state.pc?.connectionState === "connected", status, clientID: element.dataset.mediaClient });
    element.dataset.state = value;
    const label = t(`e2ee.member.${value}`);
    element.title = label; element.setAttribute("aria-label", label);
    element.innerHTML = value === "ready" ? icon("lock") : '<span class="member-media-ring"></span>';
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
