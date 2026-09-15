import { icon, labelButton } from "./icons.js";
import { getUserVolume, isUserMuted, setUserVolume } from "./audio.js";
import { setSafeImage } from "./safe-media.js";

const V = () => window.__noxa;
const $ = (id) => document.getElementById(id);
let memberKey = "";

export function avatarColor(name = "") {
    const colors = ["#70b7f5", "#aa92e8", "#73c590", "#ec91b3", "#efb36f"];
    let hash = 0;
    for (const character of name) hash = (hash * 31 + character.codePointAt(0)) >>> 0;
    return colors[hash % colors.length];
}

function avatar(element, client) {
    const name = client.nickname || client.unique_id || "?";
    element.style.setProperty("--avatar-color", avatarColor(client.unique_id || name));
    if (!setSafeImage(element, V().state.avatars.get(client.unique_id))) {
        element.textContent = V().initials(name);
    }
}

function voiceDescription(client) {
    const state = V().state;
    if (client.client_id === state.myClientID) {
        if (state.muted) return "Microphone muted";
        return client.is_speaking && client.channel_id === state.myChannelID ? "Talking" : "In voice";
    }
    if (isUserMuted(client.unique_id)) return "Muted for you";
    if (client.is_speaking && client.channel_id === state.myChannelID) return "Speaking";
    return "In voice";
}

export function renderWorkspace() {
    const state = V().state;
    const strip = $("voice-participants");
    const members = state.myChannelID ? state.clients.filter((c) => c.channel_id === state.myChannelID) : [];
    const current = new Map([...strip.querySelectorAll(".participant")].map((el) => [el.dataset.clientId, el]));
    strip.querySelector(".participant-empty")?.remove();
    for (const client of members) {
        let button = current.get(client.client_id);
        if (!button) {
            button = document.createElement("button");
            button.type = "button";
            button.className = "participant";
            button.dataset.clientId = client.client_id;
            button.innerHTML = '<span class="avatar"></span><span class="participant-copy"><span class="participant-name"></span><span class="participant-state"></span></span>';
            button.onclick = () => {
                state.selectedClientID = client.client_id;
                state.multiSelect = new Set([client.client_id]);
                V().setDetailsOpen(true);
                V().renderTree();
            };
            strip.appendChild(button);
        }
        current.delete(client.client_id);
        const name = (client.nickname || client.unique_id) + (client.client_id === state.myClientID ? " (you)" : "");
        const description = voiceDescription(client);
        button.querySelector(".participant-name").textContent = name;
        const status = button.querySelector(".participant-state");
        const speaking = description === "Speaking" || description === "Talking";
        const statusIcon = speaking ? "signal" : description.includes("muted") || description.includes("Muted") ? "micOff" : "mic";
        labelButton(status, statusIcon, description);
        button.setAttribute("aria-label", `${name}, ${description.toLowerCase()}. Member details`);
        button.classList.toggle("speaking", speaking);
        button.classList.toggle("selected", state.selectedClientID === client.client_id);
        avatar(button.querySelector(".avatar"), client);
    }
    for (const element of current.values()) {
        if (element === document.activeElement) $("details-toggle").focus();
        element.remove();
    }
    if (!members.length) {
        const empty = document.createElement("p");
        empty.className = "participant-empty";
        empty.textContent = state.myChannelID ? "No one else is here yet" : "Join a channel to start talking";
        strip.appendChild(empty);
    }
    const ownChannel = state.channels.find((c) => c.ChannelID === state.myChannelID);
    strip.title = ownChannel ? `Voice participants in ${ownChannel.Name}` : "Voice participants";
    $("server-summary").textContent = `${state.clients.length} online`;
    $("channel-member-count").textContent = String(members.length);
    $("channel-member-count").title = ownChannel ? `${members.length} in ${ownChannel.Name}` : "Not in a voice channel";
    $("voice-deafen").classList.toggle("active", state.deafened);
    $("voice-deafen").setAttribute("aria-pressed", String(state.deafened));
    $("voice-deafen").setAttribute("aria-label", state.deafened ? "Undeafen" : "Deafen");
    labelButton($("voice-deafen"), state.deafened ? "headphonesOff" : "headphones", state.deafened ? "Undeafen" : "Deafen");
    $("ptt-btn").classList.toggle("hidden", (state.settings?.activation_mode || "ptt") !== "ptt");
    for (const element of document.querySelectorAll("#channel-tree .avatar[data-uid]")) {
        element.style.setProperty("--avatar-color", avatarColor(element.dataset.uid));
    }
}

export function renderMember() {
    const state = V().state;
    const client = state.clients.find((c) => c.client_id === state.selectedClientID);
    const card = $("client-card");
    if (!client) {
        memberKey = "";
        card.innerHTML = '<p class="empty-state">Select someone in the channel tree or participant strip to see their details.</p>';
        $("member-heading").textContent = "Member details";
        return;
    }
    const key = `${state.serverGeneration}:${client.client_id}:${client.unique_id}`;
    if (key !== memberKey) {
        memberKey = key;
        card.innerHTML = `<div class="member-profile"><div class="card-avatar"></div><div class="member-identity"><div class="card-nick"></div><div class="card-channel"></div><div class="card-groups"></div></div></div>
            <div class="member-audio"><label for="member-volume">User volume <output id="member-volume-value" for="member-volume"></output></label><input id="member-volume" type="range" min="0" max="200" step="5" aria-describedby="member-volume-value" />
            <button id="member-message" type="button"></button><p id="member-action-error" role="alert" hidden></p></div>
            <details class="member-identity-details"><summary>Identity</summary><div class="card-uid mono"></div></details>`;
        const slider = $("member-volume");
        slider.value = Math.round(getUserVolume(client.unique_id) * 100);
        slider.oninput = () => { $("member-volume-value").textContent = slider.value + "%"; };
        slider.onchange = async () => {
            const error = $("member-action-error");
            const priorVolume = Math.round(getUserVolume(client.unique_id) * 100);
            error.hidden = true;
            try { await setUserVolume(client.unique_id, Number(slider.value)); }
            catch {
                if (memberKey !== key || !error.isConnected) return;
                slider.value = priorVolume;
                $("member-volume-value").textContent = priorVolume + "%";
                error.textContent = "Could not save volume. Try again.";
                error.hidden = false;
            }
        };
        $("member-message").onclick = () => {
            window.__noxaFiles.activateWorkspaceTab("chat");
            V().openPM(client.unique_id, client.nickname);
            if (innerWidth <= 1100) V().setDetailsOpen(false);
            $("chat-text").focus();
        };
    }
    const name = client.nickname || client.unique_id;
    $("member-heading").textContent = name;
    card.querySelector(".card-nick").textContent = name;
    const channel = state.channels.find((c) => c.ChannelID === client.channel_id);
    card.querySelector(".card-channel").textContent = channel ? "In " + channel.Name : "Not in a channel";
    card.querySelector(".card-uid").textContent = client.unique_id || "Identity unavailable";
    const groups = state.groupByUID?.get(client.unique_id) || [];
    card.querySelector(".card-groups").textContent = groups.map((group) => group.name).join(" · ");
    avatar(card.querySelector(".card-avatar"), client);
    card.querySelector(".card-avatar").classList.toggle("speaking", ["Speaking", "Talking"].includes(voiceDescription(client)));
    $("member-volume-value").textContent = $("member-volume").value + "%";
    const isSelf = client.client_id === state.myClientID;
    card.querySelector(".member-audio").hidden = isSelf || !client.unique_id;
    labelButton($("member-message"), "chat", "Message");
    $("member-message").setAttribute("aria-label", "Message " + name);
}

export function initWorkspace() {
    const icons = {
        "details-close": "close", "details-toggle": "users", "channel-create-btn": "plus",
        "notif-bell": "bell", "chat-search-btn": "search", "chat-pins-btn": "pin",
        "chat-info-btn": "info", "chat-e2ee-btn": "lock", "chat-export-btn": "download",
        "chat-attach": "attach", "chat-emoji": "smile", "chat-send": "send", "tab-transfers": "transfer",
        "tree-collapse": "chevron", "tree-expand": "chevron", "chat-search-close": "close",
    };
    for (const [id, name] of Object.entries(icons)) {
        const button = $(id);
        if (id === "notif-bell") button.prepend(document.createRange().createContextualFragment(icon(name)));
        else button.innerHTML = icon(name);
    }
    // Notification counts are owned by notifications.js; preserve the badge.
    for (const node of [...$("notif-bell").childNodes]) if (node.nodeType === Node.TEXT_NODE) node.remove();
    $("chat-send").setAttribute("aria-label", "Send message");
    labelButton($("tab-chat"), "chat", "Chat");
    labelButton($("tab-files"), "file", "Files");
    labelButton($("ptt-btn"), "mic", "Hold to talk");
    labelButton($("voice-settings"), "settings", "Audio preferences");
    labelButton($("voice-options").querySelector("summary"), "settings", "Voice settings");
    labelButton($("voice-disconnect"), "disconnect", "Disconnect");
    $("voice-deafen").onclick = () => V().setDeafened(!V().state.deafened);
    $("voice-settings").onclick = () => V().openSettings("capture");
    $("voice-disconnect").onclick = () => V().disconnect();
    $("workspace-sidebar-toggle").innerHTML = icon("speaker");
    $("workspace-sidebar-toggle").onclick = () => {
        const open = document.body.classList.toggle("channels-open");
        $("workspace-sidebar-toggle").setAttribute("aria-expanded", String(open));
    };
    $("channel-tree").addEventListener("click", (event) => {
        if (event.target.closest(".channel")) {
            document.body.classList.remove("channels-open");
            $("workspace-sidebar-toggle").setAttribute("aria-expanded", "false");
            if (innerWidth <= 720) $("center").focus();
        }
    });
    document.addEventListener("keydown", (event) => {
        if (event.key === "Escape" && document.body.classList.contains("channels-open")) {
            document.body.classList.remove("channels-open");
            $("workspace-sidebar-toggle").setAttribute("aria-expanded", "false");
            $("workspace-sidebar-toggle").focus();
        }
    });
    renderWorkspace();
    renderMember();
}
