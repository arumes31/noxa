// app-shell.js — the application shell around the workspace: top bar
// (search/quick switcher, live ping), sidebar server header and menu, the
// Text and Direct sidebar sections, and the voice card's identity row.
// Everything here renders existing state; actions call the same functions
// as the menus and buttons they mirror.
import { t } from "./i18n.js";
import { icon } from "./icons.js";
import { setSafeImage } from "./safe-media.js";
import { avatarColor } from "./workspace-ui.js";
import { mountContextMenu, closeContextMenu } from "./context-menu.js";
import { copyToClipboard } from "./clipboard.js";
import { openConversations } from "./conversations.js";
import { shareSummaryStats } from "./share-status.js";
import * as chatUI from "./chat-ui.js";
import { formatStreamStats, serverInitial, textChannels } from "./shell-model.js";

const V = () => window.__noxa;
const $ = (id) => document.getElementById(id);
let groupUnread = 0;
let countsQueued = false;
let shareTimer = 0;
let directView = null;
let directViewUnread = NaN; // last count published by the DM view, if any

// ---------------------------------------------------------------- helpers

function activateChat() {
    window.__noxaFiles?.activateWorkspaceTab?.("chat");
    document.body.classList.remove("channels-open");
    $("workspace-sidebar-toggle")?.setAttribute("aria-expanded", "false");
}

export function openTextChannel(channelID) {
    if (V()?.isDirectMessagesOpen?.()) V().closeDirectMessages?.();
    activateChat();
    void chatUI.openChannelTab(channelID);
}

// openDirectMessages opens the direct-message view. A dedicated view may
// register itself through window.__noxaShell.setDirectView(); until then the
// newest unread DM opens, otherwise the private conversations workspace.
export function openDirectMessages() {
    if (typeof directView === "function") { directView(); return; }
    // The dedicated DM view (dm-view.js), when present, owns this action.
    if (typeof V()?.openDirectMessages === "function") { V().openDirectMessages(); return; }
    activateChat();
    const unread = chatUI.directPeers().filter((peer) => peer.unread > 0).sort((a, b) => b.unreadAt - a.unreadAt)[0];
    if (unread) { V().openPM(unread.uid, unread.nick); return; }
    openConversations();
}

// ---------------------------------------------------------------- sidebar

function renderServerHeader() {
    const { state } = V();
    const brand = document.querySelector("#sidebar .brand");
    const name = $("server-name")?.textContent?.trim() || state.lastConnect?.addr || "";
    if (brand) brand.dataset.initial = serverInitial(name);
    const button = $("server-menu-btn");
    if (button) {
        const label = name ? t("shell.serverMenuFor", { name }) : t("shell.serverMenu");
        button.setAttribute("aria-label", label);
        button.title = t("shell.serverMenu");
    }
}

function renderTextSection() {
    const { state } = V();
    const section = $("text-section"), list = $("text-channels");
    if (!section || !list) return;
    const filter = (state.treeFilter || "").toLocaleLowerCase();
    const channels = textChannels(state.channels).filter((channel) => !filter || channel.Name.toLocaleLowerCase().includes(filter));
    const manager = state.ownAuthority === "owner" || state.ownAuthority === "administrator";
    section.hidden = !state.myClientID || (!channels.length && (!manager || !!filter));
    $("text-channel-create").hidden = !manager;
    const focused = list.contains(document.activeElement) ? document.activeElement.dataset.chid : null;
    const activeKey = chatUI.activeChatKey?.() || "";
    list.replaceChildren();
    for (const channel of channels) {
        const item = document.createElement("div");
        item.setAttribute("role", "listitem");
        const row = document.createElement("button");
        row.type = "button";
        row.className = "channel text-channel side-row";
        row.dataset.chid = String(channel.ChannelID);
        const current = activeKey === `ch:${channel.ChannelID}`;
        if (current) row.setAttribute("aria-current", "true");
        row.innerHTML = `<span class="ch-icon side-row-icon" aria-hidden="true">${icon("hash")}</span><span class="ch-name side-row-name"></span>`;
        row.querySelector(".side-row-name").textContent = channel.Name;
        const labels = [t("shell.textChannel", { name: channel.Name })];
        const unread = chatUI.unreadFor(channel.ChannelID);
        if (unread?.n > 0 && !current) {
            const badge = document.createElement("span");
            badge.className = "side-count" + (unread.mention ? " mentioned" : " dot");
            badge.setAttribute("aria-hidden", "true");
            badge.textContent = unread.mention ? (unread.n > 99 ? "99+" : String(unread.n)) : "";
            row.append(badge);
            labels.push(unread.mention ? t("shell.textMention") : t("shell.textUnread", { count: unread.n }));
        }
        if (channel.Topic) row.title = channel.Topic;
        row.setAttribute("aria-label", labels.join(", "));
        row.onclick = () => openTextChannel(channel.ChannelID);
        item.append(row);
        list.append(item);
        if (focused === row.dataset.chid) row.focus({ preventScroll: true });
    }
}

function renderDirectSection() {
    const row = $("direct-messages");
    if (!row) return;
    const count = Number.isFinite(directViewUnread) ? directViewUnread
        : typeof V()?.directMessagesUnread === "function" ? Number(V().directMessagesUnread()) || 0
            : chatUI.directUnreadTotal() + groupUnread;
    const badge = row.querySelector(".side-count");
    badge.hidden = count <= 0;
    badge.textContent = count > 99 ? "99+" : String(count);
    row.querySelector(".side-row-name").textContent = t("shell.messages");
    row.setAttribute("aria-label", count > 0 ? t("shell.messagesUnread", { count }) : t("shell.messages"));
    const viewing = typeof V()?.isDirectMessagesOpen === "function" ? !!V().isDirectMessagesOpen()
        : document.getElementById("center")?.classList.contains("private-group-active") || (chatUI.activeChatKey?.() || "").startsWith("dm:");
    if (viewing) row.setAttribute("aria-current", "true"); else row.removeAttribute("aria-current");
}

// renderSidebarSections runs after every tree render.
export function renderSidebarSections() {
    if (!V()?.state) return;
    renderServerHeader();
    renderTextSection();
    renderDirectSection();
}

function queueCounts() {
    if (countsQueued) return;
    countsQueued = true;
    queueMicrotask(() => { countsQueued = false; renderTextSection(); renderDirectSection(); });
}

// ---------------------------------------------------------------- voice card

export function renderVoiceCard() {
    const { state } = V();
    const bar = $("voice-bar");
    if (!bar) return;
    const own = state.channels.find((channel) => channel.ChannelID === state.myChannelID);
    $("voice-channel-name").textContent = own?.Name || "";
    $("voice-channel-name").title = own ? t("shell.voiceConnectedIn", { channel: own.Name }) : "";
    bar.dataset.inChannel = String(!!own);
    const self = $("voice-self");
    const me = state.clients.find((client) => client.client_id === state.myClientID);
    self.hidden = !state.myClientID;
    if (state.myClientID) {
        const name = me?.nickname || state.myNickname || state.lastConnect?.nick || "";
        const uid = me?.unique_id || state.myUniqueID || "";
        self.querySelector(".voice-self-name").textContent = name;
        const avatar = self.querySelector(".voice-self-avatar");
        avatar.style.setProperty("--avatar-color", avatarColor(uid || name));
        if (!setSafeImage(avatar, state.avatars.get(uid), { avatar: true })) avatar.textContent = V().initials(name || "?");
    }
    const sharing = !!state.screenSharing && !!own;
    const stream = self.querySelector(".voice-self-stream");
    stream.hidden = !sharing;
    stream.querySelector(".voice-live-badge").textContent = t("shell.live");
    stream.querySelector(".voice-self-stats").textContent = sharing ? formatStreamStats(shareSummaryStats()) : "";
    if (sharing && !shareTimer) shareTimer = setInterval(renderVoiceCard, 2000);
    else if (!sharing && shareTimer) { clearInterval(shareTimer); shareTimer = 0; }
}

// ---------------------------------------------------------------- server menu

function openServerMenu(button) {
    const { state, toast } = V();
    const menu = document.createElement("div");
    menu.className = "ctx-menu server-menu";
    menu.setAttribute("aria-label", t("shell.serverMenu"));
    const add = (key, action, { hidden = false, danger = false } = {}) => {
        if (hidden) return;
        const item = document.createElement("a");
        item.textContent = t(key);
        if (danger) item.className = "ctx-danger";
        item.onclick = () => { closeContextMenu(menu, true); action(); };
        menu.append(item);
    };
    const divider = () => { const line = document.createElement("div"); line.className = "ctx-divider"; menu.append(line); };
    const connected = !!state.myClientID;
    add("shell.menu.info", () => window.__noxaMeta?.openServerInfo?.());
    add("shell.menu.copyAddress", () => void copyToClipboard(state.lastConnect?.addr || "", { success: t("desktop.server.address.copied") }), { hidden: !state.lastConnect?.addr });
    add("shell.menu.notifications", () => $("notif-bell")?.click());
    if (connected) {
        divider();
        add("shell.menu.createChannel", () => $("channel-create-btn")?.click());
        add("shell.menu.createText", () => void createTextChannel().catch(() => toast(t("roles.unavailable"), "warn")));
    }
    divider();
    add("shell.menu.alpha", () => $("alpha-badge")?.click(), { hidden: !$("alpha-badge") || $("alpha-badge").hidden });
    add("shell.menu.disconnect", () => V().disconnect(), { hidden: !connected, danger: true });
    button.setAttribute("aria-expanded", "true");
    mountContextMenu(menu, { trigger: button, onClose: () => button.setAttribute("aria-expanded", "false") });
}

async function createTextChannel() {
    const generation = V().state.serverGeneration;
    const { openRoleChannel } = await import("./channel-lifecycle-ui.js");
    if (generation === V().state.serverGeneration) openRoleChannel("channel_create", 0, { textOnly: true });
}

// ---------------------------------------------------------------- init

function translateShell() {
    const search = $("quick-switch-open");
    search.querySelector(".topbar-search-label").textContent = t("shell.search");
    search.querySelector("kbd").textContent = t("shell.searchShortcut");
    search.setAttribute("aria-label", t("shell.searchLabel"));
    search.title = `${t("shell.searchLabel")} (${t("shell.searchShortcut")})`;
    $("voice-heading").textContent = t("shell.voice");
    $("text-heading").textContent = t("shell.text");
    $("direct-heading").textContent = t("shell.direct");
    $("tree-filter-toggle").setAttribute("aria-label", t("shell.findInTree"));
    $("tree-filter-toggle").title = t("shell.findInTree");
    $("text-channel-create").setAttribute("aria-label", t("shell.createTextChannel"));
    $("text-channel-create").title = t("shell.createTextChannel");
}

function setTreeToolsOpen(open, focus = false) {
    const sidebar = $("sidebar");
    sidebar.classList.toggle("tree-tools-open", open);
    $("tree-filter-toggle").setAttribute("aria-expanded", String(open));
    if (open && focus) $("tree-filter").focus();
}

export function initAppShell() {
    const v = V();
    $("quick-switch-open").querySelector(".topbar-search-icon").innerHTML = icon("search");
    $("server-menu-btn").innerHTML = icon("chevron");
    $("tree-filter-toggle").innerHTML = icon("search");
    $("text-channel-create").innerHTML = icon("plus");
    $("direct-messages").querySelector(".side-row-icon").innerHTML = icon("messages");
    document.querySelector("#voice-bar .voice-signal").innerHTML = icon("signal");
    translateShell();

    // Ctrl+K is bound by the chat module's quick switcher (135).
    $("quick-switch-open").onclick = () => chatUI.openQuickSwitcher();
    $("server-menu-btn").onclick = (event) => { event.stopPropagation(); openServerMenu(event.currentTarget); };
    $("tree-filter-toggle").onclick = () => {
        const open = !$("sidebar").classList.contains("tree-tools-open");
        if (!open && v.state.treeFilter) { $("tree-filter").value = ""; v.state.treeFilter = ""; v.renderTree(); }
        setTreeToolsOpen(open, open);
    };
    $("tree-filter").addEventListener("keydown", (event) => {
        if (event.key !== "Escape" || !$("sidebar").classList.contains("tree-tools-open")) return;
        event.preventDefault();
        if ($("tree-filter").value) { $("tree-filter").value = ""; v.state.treeFilter = ""; v.renderTree(); return; }
        setTreeToolsOpen(false);
        $("tree-filter-toggle").focus();
    });
    $("text-channel-create").onclick = () => void createTextChannel().catch(() => v.toast(t("roles.unavailable"), "warn"));
    $("direct-messages").onclick = () => {
        if (v.isDirectMessagesOpen?.()) {
            v.closeDirectMessages();
            void chatUI.openChannelTab(v.state.myChannelID);
        } else openDirectMessages();
    };

    // Unread changes: chat-ui reports every badge change through
    // syncTrayUnread, private groups through the tray "groups" source.
    const syncTray = v.syncTrayUnread;
    v.syncTrayUnread = (...args) => { syncTray(...args); queueCounts(); };
    const tray = v.trayUnread, setSource = tray.set.bind(tray);
    tray.set = (source, count) => {
        setSource(source, count);
        if (source === "groups") { groupUnread = Math.max(0, Math.floor(Number(count) || 0)); queueCounts(); }
    };

    // The card's signal icon follows the same quality as the top-bar ping.
    const latency = $("voice-latency");
    new MutationObserver(() => {
        const quality = latency.hidden ? "" : latency.dataset.quality || "";
        if ($("voice-bar").dataset.quality !== quality) $("voice-bar").dataset.quality = quality;
    }).observe(latency, { attributes: true, attributeFilter: ["data-quality", "hidden"] });
    new MutationObserver(renderServerHeader).observe($("server-name"), { childList: true, characterData: true, subtree: true });
    // The private-groups workspace toggles a class on the centre pane.
    new MutationObserver(renderDirectSection).observe($("center"), { attributes: true, attributeFilter: ["class"] });

    // The DM view publishes its own unread total and open state.
    window.addEventListener("noxa-direct-unread", (event) => {
        const count = Number(event.detail?.count);
        directViewUnread = Number.isFinite(count) && count >= 0 ? count : NaN;
        renderDirectSection();
    });
    window.addEventListener("noxa-direct-view", renderDirectSection);
    window.addEventListener("noxa-language-changed", () => { translateShell(); renderSidebarSections(); renderVoiceCard(); });
    window.__noxaShell = {
        openDirectMessages, openTextChannel, renderSidebarSections, renderVoiceCard,
        setDirectView: (open) => { directView = typeof open === "function" ? open : null; },
    };
    renderSidebarSections();
    renderVoiceCard();
}
