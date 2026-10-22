import { voiceQueueSection } from "./voice-queue.js";
import { canEditChannelPermissions } from "./workspace-ui.js";
import { t } from "./i18n.js";
import { icon } from "./icons.js";
import { memberTarget } from "./member-target.js";
import { setSafeImage } from "./safe-media.js";

const node = (tag, className, text) => { const el = document.createElement(tag); el.className = className; if (text) el.textContent = text; return el; };

export function renderRoomOverview({ avatarColor, voiceState }) {
    const v = window.__noxa, state = v.state;
    const host = document.getElementById("room-overview");
    if (!host) return;
    const scope = { tabID: state.activeTabID, generation: state.serverGeneration };
    const channelID = window.__noxaChat?.activeChannelID?.() ?? state.myChannelID;
    const channel = state.channels.find(entry => entry.ChannelID === channelID);
    const selectedMember = state.clients.some(entry => entry.client_id === state.selectedClientID);
    host.hidden = selectedMember;
    const back = document.getElementById("room-overview-back");
    back.hidden = !selectedMember;
    back.title = t("midnight.backToRoom");
    back.setAttribute("aria-label", t("midnight.backToRoom"));
    document.getElementById("details").classList.toggle("room-mode", !selectedMember);
    if (selectedMember) return;
    const hero = node("div", "room-hero"); hero.setAttribute("aria-hidden", "true");
    const title = node("h3", "room-name", channel?.Name || t("midnight.roomDetails"));
    const subtitle = node("p", "room-topic", channel?.Topic || (channel ? t("workspace.inVoice", { count: state.clients.filter(c => c.channel_id === channelID).length }) : t("midnight.roomEmpty")));
    const actions = node("div", "room-actions");
    const action = (label, image, run) => {
        const button = node("button", "room-action"); button.type = "button"; button.title = label; button.setAttribute("aria-label", label);
        button.innerHTML = icon(image); button.onclick = () => { if (button.isConnected && scope.tabID === state.activeTabID && scope.generation === state.serverGeneration) run(); }; actions.append(button); return button;
    };
    action(t("workspace.chat"), "chat", () => window.__noxaFiles.activateWorkspaceTab("chat"));
    action(t("workspace.files"), "file", () => window.__noxaFiles.activateWorkspaceTab("files"));
    action(t("workspace.pins"), "pin", () => document.getElementById("chat-pins-btn").click()).disabled = !channel;
    const settings = action(t("midnight.roomSettings"), "settings", async () => {
        const tabID = state.activeTabID, generation = state.serverGeneration;
        try {
            const { openChannelAccess } = await import("./channel-access-ui.js");
            if (state.activeTabID === tabID && state.serverGeneration === generation && channelID === window.__noxaChat?.activeChannelID?.() && canEditChannelPermissions()) openChannelAccess(channelID);
        } catch { if (state.activeTabID === tabID && state.serverGeneration === generation) v.toast(t("roles.unavailable"), "warn"); }
    });
    settings.hidden = !canEditChannelPermissions();
    settings.disabled = settings.hidden;
    const content = [hero, title, subtitle, actions];
    const inRoom = channel ? state.clients.filter(client => client.channel_id === channelID) : [];
    const section = (label, members) => {
        const group = node("section", "room-members"); group.append(node("h4", "", label));
        for (const client of members) {
            const row = node("div", "room-member");
            const picture = node("span", "avatar"); picture.style.setProperty("--avatar-color", avatarColor(client.unique_id || client.nickname));
            if (!setSafeImage(picture, state.avatars.get(client.unique_id))) picture.textContent = v.initials(client.nickname || "?");
            const name = node("span", "room-member-name", client.nickname || client.unique_id || "?");
            memberTarget(name, client.unique_id, client.nickname, { clientID: client.client_id });
            const status = voiceState(client), speaking = ["talking", "speaking"].includes(status);
            row.classList.toggle("speaking", speaking);
            const text = node("span", "room-member-state", t(`workspace.voice.${status}`));
            const copy = node("div", "room-member-copy"); copy.append(name, text);
            const microphone = node("span", "room-microphone"); microphone.innerHTML = icon(["muted", "serverMuted", "localMuted"].includes(status) ? "micOff" : "mic");
            microphone.setAttribute("aria-hidden", "true"); row.append(picture, copy, microphone); group.append(row);
        }
        return group;
    };
    const queue = voiceQueueSection(channelID);
    if (queue) content.push(queue);
    if (channel) content.push(section(t("midnight.roomMembers", { count: inRoom.length }), inRoom));
    const other = state.clients.filter(client => !channel || client.channel_id !== channelID);
    content.push(section(t("midnight.onlineMembers", { count: other.length }), other));
    // Presence updates can be frequent. Preserve keyboard focus when the same
    // room controls are rebuilt, without retaining another member's targets.
    const focused = host.contains(document.activeElement) ? [...host.querySelectorAll("button")].indexOf(document.activeElement) : -1;
    host.replaceChildren(...content);
    if (focused >= 0) host.querySelectorAll("button")[focused]?.focus({ preventScroll: true });
}
