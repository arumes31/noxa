import { icon, labelButton } from "./icons.js";
import { isUserMuted } from "./audio.js";
import { bindMemberVolume } from "./member-volume.js";
import { memberTarget } from "./member-target.js";
import { currentWhisperRouting } from "./media-controls.js";
import { t } from "./i18n.js";
import { setSafeImage } from "./safe-media.js";

const V = () => window.__noxa;
const $ = (id) => document.getElementById(id);
let memberKey = "";
let memberPreview = null;

export function cancelMemberVolumePreview() {
    memberPreview?.control.cancel();
}

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

function voiceState(client) {
    const state = V().state;
    if (client.server_deafened) return "serverDeafened";
    if (client.server_muted) return "serverMuted";
    if (client.client_id === state.myClientID) {
        if (state.muted) return "muted";
        return client.is_speaking && client.channel_id === state.myChannelID ? "talking" : "idle";
    }
    if (client.self_deafened) return "deafened";
    if (isUserMuted(client.unique_id)) return "localMuted";
    if (client.self_muted) return "muted";
    if (client.is_speaking && client.channel_id === state.myChannelID) return "speaking";
    return "idle";
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
            const card = document.createElement("div");
            card.className = "participant-card";
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
            const menu = document.createElement("button");
            menu.type = "button";
            menu.className = "participant-menu icon-btn";
            menu.innerHTML = icon("more");
            card.append(button, menu);
            strip.appendChild(card);
        }
        current.delete(client.client_id);
        memberTarget(button, client.unique_id, client.nickname, { clientID: client.client_id, openOnClick: false });
        const name = (client.nickname || client.unique_id) + (client.client_id === state.myClientID ? t("workspace.you") : "");
        const menu = button.parentElement.querySelector(".participant-menu");
        memberTarget(menu, client.unique_id, client.nickname, { clientID: client.client_id });
        menu.title = t("context.memberOptions", { name });
        menu.setAttribute("aria-label", menu.title);
        const statusKey = voiceState(client);
        const description = t(statusKey === "deafened" ? "audioState.deafened" : "workspace.voice." + statusKey);
        button.querySelector(".participant-name").textContent = name;
        const status = button.querySelector(".participant-state");
        const speaking = statusKey === "speaking" || statusKey === "talking";
        const statusIcon = speaking ? "signal" : ["deafened", "serverDeafened"].includes(statusKey) ? "headphonesOff" : ["muted", "localMuted", "serverMuted"].includes(statusKey) ? "micOff" : "mic";
        labelButton(status, statusIcon, description);
        button.setAttribute("aria-label", t("workspace.memberLabel", { name, state: description.toLowerCase() }));
        button.classList.toggle("speaking", speaking);
        button.classList.toggle("selected", state.selectedClientID === client.client_id);
        avatar(button.querySelector(".avatar"), client);
    }
    for (const element of current.values()) {
        if (element.parentElement.contains(document.activeElement)) $("details-toggle").focus();
        element.parentElement.remove();
    }
    if (!members.length) {
        const empty = document.createElement("p");
        empty.className = "participant-empty";
        empty.textContent = state.myChannelID ? t("workspace.emptyVoice") : t("workspace.joinVoice");
        strip.appendChild(empty);
    }
    const ownChannel = state.channels.find((c) => c.ChannelID === state.myChannelID);
    const context = $("voice-context");
    context.textContent = t("polish.connection", {
        server: state.lastConnect?.addr || t("status.offline"),
        channel: ownChannel?.Name || t("workspace.noVoice"),
    });
    context.title = context.textContent;
    $("mic-meter").setAttribute("aria-label", t("polish.meter"));
    strip.title = ownChannel ? t("workspace.participantsIn", { channel: ownChannel.Name }) : t("workspace.participants");
    $("server-summary").textContent = t("workspace.online", { count: state.clients.length });
    $("channel-member-count").textContent = t("workspace.inVoice", { count: members.length });
    $("channel-member-count").title = ownChannel ? t("workspace.countIn", { count: members.length, channel: ownChannel.Name }) : t("workspace.noVoice");
    $("channel-member-count").setAttribute("aria-label", $("channel-member-count").textContent);
    $("voice-leave-channel").disabled = !state.myChannelID;
    $("voice-deafen").classList.toggle("active", state.deafened);
    $("voice-deafen").setAttribute("aria-pressed", String(state.deafened));
    $("voice-deafen").setAttribute("aria-label", state.deafened ? t("workspace.undeafen") : t("workspace.deafen"));
    labelButton($("voice-deafen"), state.deafened ? "headphonesOff" : "headphones", state.deafened ? t("workspace.undeafen") : t("workspace.deafen"));
    $("ptt-btn").classList.toggle("hidden", (state.settings?.activation_mode || "ptt") !== "ptt");
    renderVoiceHints();
    for (const element of document.querySelectorAll("#channel-tree .avatar[data-uid]")) {
        element.style.setProperty("--avatar-color", avatarColor(element.dataset.uid));
    }
}

export function renderMember() {
    const state = V().state;
    const client = state.clients.find((c) => c.client_id === state.selectedClientID);
    const card = $("client-card");
    if (memberPreview && (!client || memberPreview.key !== `${state.serverGeneration}:${client.client_id}:${client.unique_id}`)) {
        memberPreview.control(); memberPreview = null;
    }
    if (!client) {
        memberKey = "";
        card.innerHTML = `<p class="empty-state">${t("workspace.selectMember")}</p>`;
        $("member-heading").textContent = t("workspace.memberDetails");
        return;
    }
    const key = `${state.serverGeneration}:${client.client_id}:${client.unique_id}`;
    if (key !== memberKey) {
        memberKey = key;
        card.innerHTML = `<div class="member-profile"><div class="card-avatar"></div><div class="member-identity"><div class="card-nick"></div><div class="card-channel"></div><div class="card-groups"></div></div></div>
            <div class="member-audio"><label for="member-volume">${t("workspace.volume")} <output id="member-volume-value" for="member-volume"></output></label><input id="member-volume" type="range" min="0" max="200" step="5" aria-describedby="member-volume-value" />
            <button id="member-volume-reset" type="button">${t("wins.resetVolume")}</button>
            <button id="member-message" type="button"></button><p id="member-action-error" role="alert" hidden></p></div>
            <details class="member-identity-details"><summary>${t("workspace.identity")}</summary><div class="card-uid mono"></div></details>`;
        const slider = $("member-volume");
        const error = $("member-action-error");
        const control = bindMemberVolume(slider, $("member-volume-value"), $("member-volume-reset"), client.unique_id, {
            format: value => Number(value) > 100 ? t("wins.amplified", { value }) : value + "%",
            onSave: () => { error.hidden = true; },
            onError: () => { error.textContent = t("workspace.volumeFailed"); error.hidden = false; },
        });
        memberPreview = { key, control };
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
    memberTarget(card.querySelector(".card-nick"), client.unique_id, name, { clientID: client.client_id });
    card.querySelector(".card-avatar").dataset.memberUid = client.unique_id;
    card.querySelector(".card-avatar").dataset.memberClientId = client.client_id;
    const channel = state.channels.find((c) => c.ChannelID === client.channel_id);
    card.querySelector(".card-channel").textContent = channel ? t("workspace.inChannel", { channel: channel.Name }) : t("workspace.noChannel");
    card.querySelector(".card-uid").textContent = client.unique_id || t("workspace.noIdentity");
    card.querySelector(".card-groups").textContent = (client.roles || []).map((role) => role.name).join(" · ");
    avatar(card.querySelector(".card-avatar"), client);
    card.querySelector(".card-avatar").classList.toggle("speaking", ["speaking", "talking"].includes(voiceState(client)));
    memberPreview.control.refresh();
    const isSelf = client.client_id === state.myClientID;
    card.querySelector(".member-audio").hidden = isSelf || !client.unique_id;
    labelButton($("member-message"), "chat", t("workspace.message"));
    $("member-message").setAttribute("aria-label", t("workspace.messagePerson", { name }));
}

export function renderVoiceHints() {
    const state = V().state, settings = state.settings || {};
    const hint = $("voice-shortcut-hint");
    if (!hint) return;
    const ptt = (settings.activation_mode || "ptt") === "ptt";
    const status = state.pttShortcutStatus;
    const key = status?.spec ?? settings.hotkey_ptt;
    hint.hidden = !ptt;
    hint.textContent = !key ? t("wins.pttMissing") : status?.registered === false ? t("wins.pttFailed") : t("wins.pttBinding", { key });
    hint.classList.toggle("warn", ptt && (!key || status?.registered === false));
    const routing = currentWhisperRouting();
    const whisper = $("voice-whisper-preview");
    const unconfirmed = routing ? routing.status !== "confirmed" : !!settings.whisper_active;
    whisper.classList.toggle("warn", unconfirmed);
    if (unconfirmed) {
        whisper.hidden = false;
        whisper.textContent = t(routing?.status === "pending" ? "wins.whisperPending" : "wins.whisperUnconfirmed");
        return;
    }
    const config = routing?.config || { active: false, clients: [], channels: [] };
    const targets = [
        ...config.clients.map(uid => state.clients.find(client => client.unique_id === uid)?.nickname || t("wins.whisperUnknownMember")),
        ...config.channels.map(id => t("wins.whisperChannel", { name: state.channels.find(channel => channel.ChannelID === id)?.Name || t("wins.whisperUnknownChannel") })),
    ];
    whisper.hidden = !config.active;
    whisper.textContent = targets.length ? t("wins.whisperTargets", { targets: targets.join(", ") }) : t("wins.whisperNone");
}

export function initWorkspace() {
    $("compact-exit").onclick = () => V().toggleCompact();
    const icons = {
        "details-close": "close", "details-toggle": "users", "channel-create-btn": "plus", "workspace-sidebar-close": "close",
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
    translateWorkspace();
    $("voice-deafen").onclick = () => V().setDeafened(!V().state.deafened);
    $("voice-settings").onclick = () => V().openSettings("capture");
    $("voice-disconnect").onclick = () => V().disconnect();
    $("voice-leave-channel").onclick = async () => {
        const generation = V().state.serverGeneration;
        const tabID = V().state.activeTabID;
        const channel = V().state.myChannelID;
        if (!channel) return;
        $("voice-leave-channel").disabled = true;
        try {
            const error = await window.go.main.App.JoinChannelForTab(tabID, 0);
            if (error) throw new Error(error);
        } catch (error) {
            if (generation === V().state.serverGeneration) V().toast(t("workspace.leaveFailed", { error: String(error.message || error) }), "warn");
        } finally {
            if (generation === V().state.serverGeneration) $("voice-leave-channel").disabled = !V().state.myChannelID;
        }
    };
    const positionVoiceOptions = () => {
        const options = $("voice-options");
        if (!options.open) return;
        const anchor = options.querySelector("summary").getBoundingClientRect();
        const menu = options.querySelector(".voice-options-menu");
        menu.style.left = `${Math.max(8, Math.min(anchor.right - menu.offsetWidth, innerWidth - menu.offsetWidth - 8))}px`;
        menu.style.top = `${Math.max(8, anchor.top - menu.offsetHeight - 8)}px`;
    };
    $("voice-options").addEventListener("toggle", () => {
        if ($("voice-options").open) { positionVoiceOptions(); void refreshVoiceDevices().then(positionVoiceOptions); }
    });
    window.addEventListener("resize", positionVoiceOptions);
    $("voice-options").addEventListener("keydown", event => {
        if (event.key !== "Escape" || !$("voice-options").open) return;
        event.preventDefault(); event.stopPropagation();
        $("voice-options").open = false;
        $("voice-options").querySelector("summary").focus();
    });
    $("voice-options").addEventListener("click", event => {
        if (event.target.closest("#voice-settings, #voice-disconnect, [data-channel-undock]")) {
            $("voice-options").open = false;
            if ($("voice-options").contains(document.activeElement)) $("voice-options").querySelector("summary").focus();
        }
    });
    document.addEventListener("pointerdown", event => {
        if (!$("voice-options").contains(event.target)) $("voice-options").open = false;
    });
    navigator.mediaDevices?.addEventListener?.("devicechange", () => { if ($("voice-options").open) void refreshVoiceDevices(); });
    window.addEventListener("noxa-language-changed", () => {
        translateWorkspace();
        memberKey = "";
        renderWorkspace();
        renderMember();
        if ($("voice-options").open) void refreshVoiceDevices();
    });
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
    const closeChannels = () => {
        document.body.classList.remove("channels-open");
        $("workspace-sidebar-toggle").setAttribute("aria-expanded", "false");
        const groupToggle = document.querySelector("#center.private-group-active .group-show-channels");
        groupToggle?.setAttribute("aria-expanded", "false");
        (groupToggle || $("workspace-sidebar-toggle")).focus();
    };
    $("workspace-sidebar-close").onclick = closeChannels;
    document.addEventListener("keydown", (event) => {
        if (event.key === "Escape" && document.body.classList.contains("channels-open")) closeChannels();
    });
    renderWorkspace();
    renderMember();
}

function translateWorkspace() {
    for (const [selector, attribute, key] of [
        ["#channels-heading", "text", "workspace.labels.channels"],
        ["#tree-filter", "placeholder", "workspace.labels.findChannelsOrPeople"],
        ["#chat-scope", "aria-label", "workspace.labels.messageScope"],
        ["#chat-target", "placeholder", "workspace.labels.findAMemberOrEnterAnIdentity"],
        ["#chat-target", "aria-label", "workspace.labels.directMessageRecipient"],
        ["#chat-target-toggle", "aria-label", "workspace.labels.showConnectedMembers"],
        ["#details-toggle", "aria-label", "workspace.labels.openDetails"],
        ["#details-toggle", "title", "workspace.labels.memberDetails"],
        ["#details-close", "aria-label", "workspace.labels.closeDetails"],
        ["#channel-create-btn", "aria-label", "workspace.labels.createChannel"],
        ["#tree-collapse", "aria-label", "workspace.labels.collapseAllChannels"],
        ["#tree-expand", "aria-label", "workspace.labels.expandAllChannels"],
        ["#workspace-sidebar-toggle", "aria-label", "workspace.labels.showChannels"],
        ["#workspace-sidebar-close", "aria-label", "workspace.labels.closeChannels"],
        ["#chat-search-btn", "aria-label", "workspace.labels.searchChat"],
        ["#chat-search-btn", "title", "workspace.labels.searchChatCtrlF"],
        [".channel-actions > summary", "aria-label", "workspace.labels.moreChannelActions"],
        ["#chat-search", "placeholder", "workspace.labels.filterLoadedMessages"],
        ["#chat-search-server", "text", "workspace.labels.searchAllHistory"],
        ["#chat-search-close", "aria-label", "workspace.labels.closeChatSearch"],
        ["#chat-attach", "aria-label", "workspace.labels.attachFiles"],
        ["#chat-emoji", "aria-label", "workspace.labels.openEmojiPicker"],
        ["#tab-transfers", "aria-label", "workspace.labels.openTransfers"],
        ["#chat-file", "aria-label", "workspace.labels.chooseAttachments"],
        ["#voice-options > summary", "aria-label", "workspace.labels.voiceOptions"],
        ["#login-serverpw", "placeholder", "workspace.labels.ifRequired"],
        ["#login-nick", "placeholder", "workspace.labels.nickname"],
        ["#workspace-tablist", "aria-label", "workspace.labels.workspaceViews"],
        ["#voice-bar", "aria-label", "workspace.labels.voiceControls"],
        ["#center", "aria-label", "workspace.labels.voiceAndChatWorkspace"],
        ["#details .inspector-section:first-of-type summary", "text", "workspace.labels.serverConnection"],
        ["#details .inspector-section:last-of-type summary", "text", "workspace.labels.yourPermissions"],
        [".inspector-hint", "text", "workspace.labels.yourResolvedPermissionsOnThisServer"],
        [".skip-link", "text", "workspace.labels.skipToMessageComposer"],
        ["#server-name", "title", "workspace.labels.serverInformation"],
    ]) {
        const element = document.querySelector(selector);
        if (!element) continue;
        if (attribute === "text") element.textContent = t(key);
        else {
            element.setAttribute(attribute, t(key));
            if (attribute === "aria-label" && element.hasAttribute("title")) element.title = t(key);
        }
    }

    $("compact-exit").textContent = t("menu.exitCompact");
    $("chat-send").setAttribute("aria-label", t("workspace.send"));
    labelButton($("tab-chat"), "chat", t("workspace.chat"));
    labelButton($("tab-files"), "file", t("workspace.files"));
    labelButton($("ptt-btn"), "mic", t("workspace.ptt"));
    $("ptt-btn").setAttribute("aria-label", t("workspace.ptt"));
    $("ptt-btn").title = t("workspace.ptt");
    $("chat-text").setAttribute("aria-label", t("workspace.message"));
    labelButton($("voice-settings"), "settings", t("workspace.audioPreferences"));
    labelButton($("voice-options").querySelector("summary"), "more", t("context.voiceMore"));
    $("voice-options").querySelector("summary").setAttribute("aria-label", t("context.voiceMoreLabel"));
    labelButton($("voice-disconnect"), "disconnect", t("workspace.disconnect"));

    for (const [id, glyph, key] of [
        ["chat-pins-btn", "pin", "workspace.pins"],
        ["chat-info-btn", "info", "workspace.channelInfo"],
        ["chat-e2ee-btn", "lock", "workspace.encryption"],
        ["chat-export-btn", "download", "workspace.export"],
        ["voice-leave-channel", "leave", "workspace.leaveChannel"],
    ]) {
        labelButton($(id), glyph, t(key));
        $(id).title = t(key);
        $(id).setAttribute("aria-label", t(key));
    }
    for (const option of $("chat-scope").options) option.textContent = t("workspace.scope." + option.value);
    $("voice-prio").textContent = t("voice.priority");
    $("voice-prio").title = t("voice.priority");
    $("voice-prio").setAttribute("aria-label", t("voice.priority"));
    $("voice-lowbw").textContent = t("voice.lowBandwidth");
    $("voice-lowbw").setAttribute("aria-label", t("voice.lowBandwidth"));
    $("voice-input-label").textContent = t("workspace.microphone");
    $("voice-output-label").textContent = t("workspace.output");
    document.querySelector(".channel-actions > summary").innerHTML = icon("more");
}

let deviceRequest = 0;
async function refreshVoiceDevices() {
    const request = ++deviceRequest;
    let devices = [];
    try { devices = await navigator.mediaDevices?.enumerateDevices?.() || []; } catch { /* Labels are optional; never request capture permission here. */ }
    if (request !== deviceRequest) return;
    const state = V().state;
    const track = state.localStream?.getAudioTracks?.().find(track => track.readyState === "live");
    const output = V().voiceOutputDevice();
    const deviceName = (kind, id) => {
        const device = devices.find(item => item.kind === kind && item.deviceId === (id || "default"));
        if (!id || id === "default") return device?.label || t("workspace.defaultDevice");
        return device ? device.label || t("workspace.deviceUnknown") : t("workspace.deviceUnavailable");
    };
    $("voice-input-device").textContent = track
        ? track.label || deviceName("audioinput", track.getSettings?.().deviceId)
        : t("workspace.deviceSelected", { name: deviceName("audioinput", state.settings?.capture_device_id) });
    $("voice-output-device").textContent = output.active
        ? deviceName("audiooutput", output.id)
        : t("workspace.deviceSelected", { name: deviceName("audiooutput", state.settings?.playback_device_id) });
}
