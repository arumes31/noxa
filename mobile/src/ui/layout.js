// layout.js — the wide layout (tablets, desktop browsers, ≥ 900 px): a
// persistent three-pane shell like the desktop client's workspace. The
// sidebar holds the channel tree, direct conversations and the voice card,
// the centre shows a tab bar above the regular screen modules, and the
// details pane lists the members of what is on screen. Below 900 px the
// phone screens render into #app unchanged.
import "./layout.css";
import { app, joinVoice, leaveVoice, startCall, startGroupCall } from "../app.js";
import * as tabs from "../core/chat-tabs.js";
import * as settings from "../core/settings.js";
import { rank } from "../core/switcher.js";
import { Voice } from "../core/voice.js";
import { addStrings, t } from "../i18n.js";
import { avatar, h, sheet, toast } from "./dom.js";
import { icon } from "./icons.js";
import { dmHref, scopeName, unreadBadge } from "./common.js";

addStrings({
    en: {
        "layout.search": "Search or jump to…",
        "layout.searchLabel": "Search or jump to",
        "layout.searchShortcut": "Ctrl K",
        "layout.results": "Results",
        "layout.empty": "Nothing matches “{query}”.",
        "layout.count": "{count} results",
        "layout.hint": "↑↓ to move · Enter to open · Esc to close",
        "layout.kind.text": "Text channel",
        "layout.kind.voice": "Join voice",
        "layout.kind.person": "Message",
        "layout.kind.dm": "Direct message",
        "layout.kind.group": "Private group",
        "layout.kind.page": "Page",
        "layout.voice": "Voice",
        "layout.text": "Text",
        "layout.direct": "Direct",
        "layout.online": "{count} online",
        "layout.tabs": "Open conversations",
        "layout.closeTab": "Close {name}",
        "layout.pin": "Pin tab",
        "layout.unpin": "Unpin tab",
        "layout.moveLeft": "Move left",
        "layout.moveRight": "Move right",
        "layout.close": "Close tab",
        "layout.details": "Details",
        "layout.toggleDetails": "Details pane",
        "layout.membersOf": "Members of {name}",
        "layout.onlineNow": "Online",
        "layout.nobody": "Nobody is here.",
        "layout.compact": "Compact density",
        "layout.resizeSidebar": "Resize sidebar",
        "layout.resizeDetails": "Resize details pane",
        "layout.voiceCard": "Voice room",
        "layout.voiceIn": "Voice connected in {channel}",
        "layout.speaking": "{name}, speaking",
        "layout.devices": "Audio devices",
        "layout.microphone": "Microphone: {name}",
        "layout.output": "Output: {name}",
        "layout.micDefault": "Default microphone",
        "layout.micNext": "A different microphone is used the next time you join.",
        "layout.statusOnline": "Online",
        "layout.statusIn": "Online in {channel}",
        "layout.statusOffline": "Not online",
        "layout.self": "{name} (you)",
        "layout.openChat": "Chat of {name}",
        "layout.connected": "connected",
    },
    de: {
        "layout.search": "Suchen oder springen…",
        "layout.searchLabel": "Suchen oder springen",
        "layout.searchShortcut": "Strg K",
        "layout.results": "Ergebnisse",
        "layout.empty": "Nichts passt zu „{query}“.",
        "layout.count": "{count} Ergebnisse",
        "layout.hint": "↑↓ zum Wählen · Enter zum Öffnen · Esc schließt",
        "layout.kind.text": "Textkanal",
        "layout.kind.voice": "Sprache beitreten",
        "layout.kind.person": "Nachricht",
        "layout.kind.dm": "Direktnachricht",
        "layout.kind.group": "Private Gruppe",
        "layout.kind.page": "Seite",
        "layout.voice": "Sprache",
        "layout.text": "Text",
        "layout.direct": "Direkt",
        "layout.online": "{count} online",
        "layout.tabs": "Offene Unterhaltungen",
        "layout.closeTab": "{name} schließen",
        "layout.pin": "Tab anheften",
        "layout.unpin": "Tab lösen",
        "layout.moveLeft": "Nach links",
        "layout.moveRight": "Nach rechts",
        "layout.close": "Tab schließen",
        "layout.details": "Details",
        "layout.toggleDetails": "Detailbereich",
        "layout.membersOf": "Mitglieder von {name}",
        "layout.onlineNow": "Online",
        "layout.nobody": "Niemand ist hier.",
        "layout.compact": "Kompakte Darstellung",
        "layout.resizeSidebar": "Seitenleiste anpassen",
        "layout.resizeDetails": "Detailbereich anpassen",
        "layout.voiceCard": "Sprachraum",
        "layout.voiceIn": "Sprache verbunden in {channel}",
        "layout.speaking": "{name} spricht",
        "layout.devices": "Audiogeräte",
        "layout.microphone": "Mikrofon: {name}",
        "layout.output": "Ausgabe: {name}",
        "layout.micDefault": "Standardmikrofon",
        "layout.micNext": "Ein anderes Mikrofon wird beim nächsten Beitritt verwendet.",
        "layout.statusOnline": "Online",
        "layout.statusIn": "Online in {channel}",
        "layout.statusOffline": "Nicht online",
        "layout.self": "{name} (du)",
        "layout.openChat": "Chat von {name}",
        "layout.connected": "verbunden",
    },
});

// --- Wide or narrow -------------------------------------------------------------

const media = typeof matchMedia === "function" ? matchMedia("(min-width: 900px)") : null;
// Between 900 and 1100 px the details pane floats over the centre as a drawer.
const drawerMedia = typeof matchMedia === "function" ? matchMedia("(max-width: 1099.98px)") : null;

/** Whether the desktop-style shell is used: the one wide/narrow decision. */
export function isWide() {
    return !!media?.matches;
}

/** Calls listener(wide) when the viewport crosses the breakpoint. */
export function onWideChange(listener) {
    media?.addEventListener("change", () => listener(isWide()));
}

const SIDEBAR = { name: "sidebarWidth", fallback: 272, min: 200, max: 440 };
const DETAILS = { name: "detailsWidth", fallback: 280, min: 220, max: 440 };

let shell = null;
let current = { screen: "home", arg: "" };
let drawerOpen = false;

/**
 * Where route() renders a screen: the shell's centre when wide and signed
 * in, else #app itself. Builds or removes the shell as needed.
 */
export function screenRoot(appRoot, signedIn) {
    if (!signedIn || !isWide()) {
        if (shell) {
            shell.root.remove();
            shell = null;
            closeSwitcher(false);
        }
        return appRoot;
    }
    if (!shell || !appRoot.contains(shell.root)) {
        shell = buildShell();
        appRoot.replaceChildren(shell.root);
    }
    return shell.center;
}

/** route() calls this after a screen rendered: tabs, selection, details. */
export function afterRoute(screen, arg) {
    current = { screen, arg: String(arg ?? "") };
    if (!shell) return;
    const key = tabs.tabKey(screen, current.arg);
    if (key) saveTabs(tabs.open(loadTabs(), key));
    renderAll();
}

function loadTabs() {
    return tabs.normalize(settings.get("chatTabs"));
}

function saveTabs(state) {
    settings.set({ chatTabs: state });
}

function activeKey() {
    return tabs.tabKey(current.screen, current.arg);
}

// --- Icons the phone screens do not use -------------------------------------------

const GLYPHS = {
    search: "M11 18a7 7 0 1 0 0-14 7 7 0 0 0 0 14zM20 20l-4-4",
    gear: "M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z",
    panel: "M3 4h18v16H3zM15 4v16",
    density: "M4 6h16M4 10h16M4 14h16M4 18h16",
    headphones: "M3 18v-6a9 9 0 0 1 18 0v6M21 19a2 2 0 0 1-2 2h-1v-6h3zM3 19a2 2 0 0 0 2 2h1v-6H3z",
    sliders: "M4 21v-7M4 10V3M12 21v-9M12 8V3M20 21v-5M20 12V3M1 14h6M9 8h6M17 16h6",
    plus: "M12 5v14M5 12h14",
    close: "M6 6l12 12M18 6L6 18",
};

function glyph(name) {
    const svg = icon("home");
    svg.firstChild.setAttribute("d", GLYPHS[name]);
    return svg;
}

// --- Shell ---------------------------------------------------------------------

function buildShell() {
    const serverName = h("span", { class: "shell-server-name" });
    const serverInitial = h("span", { class: "server-initial", "aria-hidden": "true" });
    const density = h("button", { class: "icon-btn", type: "button", "aria-label": t("layout.compact"), onclick: () => settings.set({ compact: !settings.get("compact") }) }, glyph("density"));
    const detailsButton = h("button", { class: "icon-btn", type: "button", "aria-label": t("layout.toggleDetails"), "aria-controls": "shell-details", onclick: toggleDetails }, glyph("panel"));
    const top = h("header", { class: "shell-top" },
        h("div", { class: "shell-server" }, serverInitial, h("span", { class: "shell-server-text" }, serverName, h("span", { class: "shell-online" })), h("span", { id: "ping-badge" })),
        h("button", { class: "shell-search", type: "button", "aria-label": t("layout.searchLabel"), "aria-keyshortcuts": "Control+K Meta+K", onclick: () => openSwitcher() },
            glyph("search"), h("span", {}, t("layout.search")), h("kbd", {}, t("layout.searchShortcut"))),
        h("div", { class: "shell-actions" },
            h("span", { id: "status-quick" }),
            density, detailsButton,
            h("a", { class: "icon-btn", href: "#/you", "aria-label": t("settings.title") }, glyph("gear"))));
    const sideNav = h("nav", { class: "side-nav", "aria-label": "Main" });
    const cards = h("div", { class: "side-cards" });
    const side = h("div", { class: "shell-side" }, sideNav, cards);
    const tabBar = h("div", { class: "chat-tabs", role: "tablist", "aria-label": t("layout.tabs"), hidden: true });
    const center = h("div", { class: "shell-screen" });
    const details = h("aside", { class: "shell-details", id: "shell-details", "aria-label": t("layout.details") });
    const root = h("div", { class: "shell" }, top, side, splitter("left", SIDEBAR), h("main", { class: "shell-main" }, tabBar, center), splitter("right", DETAILS), details);
    const built = { root, top, serverName, serverInitial, density, detailsButton, sideNav, cards, tabBar, center, details };
    applyLayout(built);
    return built;
}

/** CSS widths, density and which panes show, from settings. */
function applyLayout(target = shell) {
    if (!target) return;
    const { root } = target;
    for (const spec of [SIDEBAR, DETAILS]) {
        const width = Number(settings.get(spec.name)) || 0;
        const property = spec === SIDEBAR ? "--sidebar-w" : "--details-w";
        if (width) root.style.setProperty(property, `${clamp(width, spec)}px`);
        else root.style.removeProperty(property);
        root.querySelector(`.splitter-${spec === SIDEBAR ? "left" : "right"}`)?.setAttribute("aria-valuenow", String(width ? clamp(width, spec) : spec.fallback));
    }
    const compact = !!settings.get("compact");
    root.dataset.density = compact ? "compact" : "comfortable";
    target.density.setAttribute("aria-pressed", String(compact));
    const open = detailsOpen();
    root.classList.toggle("details-closed", !settings.get("detailsOpen"));
    root.classList.toggle("drawer-open", drawerOpen);
    target.detailsButton.setAttribute("aria-expanded", String(open));
}

function detailsOpen() {
    return drawerMedia?.matches ? drawerOpen : !!settings.get("detailsOpen");
}

function toggleDetails() {
    if (drawerMedia?.matches) drawerOpen = !drawerOpen;
    else settings.set({ detailsOpen: !settings.get("detailsOpen") });
    applyLayout();
}

drawerMedia?.addEventListener("change", () => { drawerOpen = false; applyLayout(); });
settings.onChange(({ changed }) => {
    if (changed.some(name => ["compact", "detailsOpen", "sidebarWidth", "detailsWidth"].includes(name))) applyLayout();
});

function clamp(width, spec) {
    return Math.round(Math.min(spec.max, Math.max(spec.min, width)));
}

/** A pointer-draggable divider; widths persist, double-click resets. */
function splitter(side, spec) {
    const element = h("div", { class: `splitter splitter-${side}`, role: "separator", tabindex: "0", "aria-orientation": "vertical",
        "aria-label": t(side === "left" ? "layout.resizeSidebar" : "layout.resizeDetails"), "aria-valuemin": String(spec.min), "aria-valuemax": String(spec.max) });
    const property = side === "left" ? "--sidebar-w" : "--details-w";
    let dragging = false;
    let width = 0;
    let startX = 0;
    let startWidth = 0;
    const show = value => {
        width = clamp(value, spec);
        shell?.root.style.setProperty(property, `${width}px`);
        element.setAttribute("aria-valuenow", String(width));
    };
    const measure = () => {
        const pane = shell?.root.querySelector(side === "left" ? ".shell-side" : ".shell-details");
        return pane?.getBoundingClientRect().width || spec.fallback;
    };
    element.addEventListener("pointerdown", event => {
        if (event.button !== 0) return;
        event.preventDefault();
        element.setPointerCapture?.(event.pointerId);
        dragging = true;
        // Relative to where the drag began, so grabbing the handle off-centre does not jump.
        startX = event.clientX;
        startWidth = width = measure();
        shell?.root.classList.add("resizing");
    });
    element.addEventListener("pointermove", event => {
        if (!dragging) return;
        const delta = event.clientX - startX;
        show(startWidth + (side === "left" ? delta : -delta));
    });
    const finish = () => {
        if (!dragging) return;
        dragging = false;
        shell?.root.classList.remove("resizing");
        settings.set({ [spec.name]: width });
    };
    element.addEventListener("pointerup", finish);
    element.addEventListener("pointercancel", finish);
    element.addEventListener("lostpointercapture", finish);
    element.addEventListener("dblclick", () => settings.set({ [spec.name]: 0 }));
    element.addEventListener("keydown", event => {
        const grow = side === "left" ? "ArrowRight" : "ArrowLeft";
        const shrink = side === "left" ? "ArrowLeft" : "ArrowRight";
        if (event.key !== grow && event.key !== shrink && event.key !== "Home") return;
        event.preventDefault();
        if (event.key === "Home") { settings.set({ [spec.name]: 0 }); return; }
        show(measure() + (event.key === grow ? 16 : -16));
        settings.set({ [spec.name]: width });
    });
    return element;
}

// --- Rendering ---------------------------------------------------------------------

let frame = 0;
function schedule() {
    if (!shell || frame) return;
    frame = requestAnimationFrame(() => {
        frame = 0;
        if (shell) renderAll();
    });
}

for (const type of ["unread", "dm", "groups", "voice", "call", "online"]) app.addEventListener(type, schedule);
app.addEventListener("session", event => {
    for (const type of ["snapshot", "subscriptions", "keys"]) event.detail.addEventListener(type, schedule);
    schedule();
});

function renderAll() {
    if (!shell || !app.session) return;
    renderTop();
    keepFocus(shell.sideNav, renderSidebar);
    keepFocus(shell.cards, renderCards);
    keepFocus(shell.tabBar, renderTabs);
    keepFocus(shell.details, renderDetails);
}

/** Re-renders a region and returns focus to the element with the same data-key. */
function keepFocus(container, render) {
    const focused = container.contains(document.activeElement) ? document.activeElement.dataset?.key : null;
    render();
    if (focused) container.querySelector(`[data-key="${CSS.escape(focused)}"]`)?.focus();
}

function onlineClients(session) {
    const seen = new Set();
    return session.clients().filter(client => {
        const id = client.unique_id || client.client_id;
        if (seen.has(id)) return false;
        seen.add(id);
        return true;
    });
}

function renderTop() {
    const name = app.info?.server_name || "noXa";
    shell.serverName.textContent = name;
    shell.serverInitial.textContent = (name.replace(/[^\p{L}\p{N}]/gu, "")[0] || "N").toUpperCase();
    shell.top.querySelector(".shell-online").textContent = t("layout.online", { count: onlineClients(app.session).length });
}

function nickname(uid) {
    if (uid === app.session?.uniqueID) return app.session.nickname;
    return app.session?.clients().find(c => c.unique_id === uid)?.nickname || app.dm?.conversations.get(uid)?.nickname || t("dm.someone");
}

/** An avatar with a ring that lights while the member speaks. */
function speakingAvatar(client, size = "sm") {
    const speaking = !!app.voice?.speaking.has(client.client_id);
    return h("span", { class: `speak-ring${speaking ? " speaking" : ""}`, role: "img", "aria-label": speaking ? t("layout.speaking", { name: client.nickname }) : client.nickname },
        avatar(client.nickname, size));
}

function memberRow(client, className = "side-member") {
    const session = app.session;
    const self = client.client_id === session.clientID || (client.unique_id && client.unique_id === session.uniqueID && !client.on_mobile);
    const link = client.unique_id && client.unique_id !== session.uniqueID;
    return h("li", {}, h(link ? "a" : "div", {
        class: className, href: link ? dmHref(client.unique_id) : null, "data-key": `member:${client.client_id || client.unique_id}`,
        "aria-label": link ? t("dm.messageTo", { name: client.nickname }) : null,
    },
    speakingAvatar(client),
    h("span", { class: "member-name" }, self ? t("layout.self", { name: client.nickname }) : client.nickname),
    client.self_deafened || client.server_deafened ? h("span", { class: "member-state", title: t("voice.undeafen") }, "🔈") : null,
    client.self_muted || client.server_muted ? h("span", { class: "member-state", title: t("voice.muted"), "aria-label": t("voice.muted") }, "🔇") : null,
    client.on_mobile ? h("span", { class: "row-meta", title: t("presence.onMobile") }, t("presence.mobileBadge")) : null));
}

function section(label, items, action) {
    return h("section", { class: "side-section", "aria-label": label },
        h("div", { class: "side-heading" }, h("h2", {}, label), action || null),
        h("ul", { class: "list" }, items));
}

let joining = false;

async function joinChannel(channel) {
    if (app.voice?.active && app.voice.channelID === channel.id) {
        location.hash = "#/voice";
        return;
    }
    if (joining) return;
    joining = true;
    try {
        await joinVoice(channel.id);
    } catch (error) {
        toast(t("voice.joinFailed", { error: error.message }), "warn");
    } finally {
        joining = false;
    }
}

function renderSidebar() {
    const session = app.session;
    const voice = app.voice;
    const active = activeKey();
    const channels = session.channels().map(c => ({ ...c, clients: c.home ? c.clients.filter(member => !member.on_mobile) : c.clients }));
    const indent = c => `padding-left:${8 + c.depth * 14}px`;

    const voiceRows = channels.filter(c => !c.textOnly).map(c => {
        const connected = voice?.active && voice.channelID === c.id;
        const label = h("span", { class: "row-main" }, h("span", { class: "row-title" }, c.name), connected ? h("span", { class: "row-sub" }, t("layout.connected")) : null);
        const count = c.clients.length ? h("span", { class: "row-meta" }, String(c.clients.length)) : null;
        const row = c.home
            ? h("div", { class: "row side-row", style: indent(c) }, icon("home"), label, count)
            : h("button", { class: `row side-row${connected ? " connected" : ""}`, type: "button", style: indent(c), "data-key": `voice/${c.id}`,
                "aria-label": connected ? t("layout.voiceIn", { channel: c.name }) : t("voice.joinNamed", { name: c.name }),
                onclick: () => joinChannel(c) }, icon("voice"), label, count);
        return h("li", {}, row,
            c.clients.length ? h("ul", { class: "list side-members", style: `margin-left:${c.depth * 14}px` }, c.clients.map(client => memberRow(client))) : null);
    });

    const textRow = (scope, name, mark, extra = {}) => h("li", {}, h("a", {
        class: "row side-row", href: `#/chat/${scope}`, "data-key": `chat/${scope}`, "aria-current": active === `chat/${scope}` ? "page" : null, ...extra,
    }, h("span", { class: "hash" }, mark), h("span", { class: "row-main" }, h("span", { class: "row-title" }, name)), unreadBadge(`channel:${scope}`)));
    const textRows = [
        session.current.has(0) ? textRow(0, t("channels.global"), "◎") : null,
        ...channels.filter(c => c.chat).map(c => textRow(c.id, c.name, c.restricted ? "🔒" : "#", { style: indent(c), title: c.topic || null })),
    ];

    const online = new Set(session.clients().map(c => c.unique_id));
    const dmRows = (app.dm?.list() || []).map(conversation => ({
        at: conversation.messages.at(-1)?.sentAt || 0,
        row: h("li", {}, h("a", {
            class: "row side-row", href: dmHref(conversation.peer), "data-key": `dm/${conversation.peer}`,
            "aria-current": active === `dm/${conversation.peer}` ? "page" : null,
        }, h("span", { class: "side-avatar" }, avatar(nickname(conversation.peer), "sm"), online.has(conversation.peer) ? h("span", { class: "presence-dot", "aria-hidden": "true" }) : null),
        h("span", { class: "row-main" }, h("span", { class: "row-title" }, conversation.nickname || nickname(conversation.peer))),
        unreadBadge(`dm:${conversation.peer}`))),
    }));
    const groupRows = [...(app.groups?.list.values() || [])]
        .filter(g => !g.members?.find(m => m.unique_id === session.uniqueID)?.pending)
        .map(group => {
            const unread = app.unread.get(`conversation:${group.id}`)?.count || group.unread_count || 0;
            return {
                at: app.groups.messages.get(group.id)?.at(-1)?.created_at || 0,
                row: h("li", {}, h("a", {
                    class: "row side-row", href: `#/group/${encodeURIComponent(group.id)}`, "data-key": `group/${group.id}`,
                    "aria-current": active === `group/${group.id}` ? "page" : null,
                }, h("span", { class: "hash" }, "◇"), h("span", { class: "row-main" }, h("span", { class: "row-title" }, group.name)),
                unread ? h("span", { class: "badge", "aria-label": t("home.unread", { count: unread }) }, String(unread)) : null)),
            };
        });
    const direct = [...dmRows, ...groupRows].sort((a, b) => b.at - a.at).map(entry => entry.row);
    const newButton = h("button", { class: "icon-btn", type: "button", "aria-label": t("home.new"), "data-key": "new", onclick: async () => {
        const destination = await sheet(t("home.new"), "", [
            { label: t("home.newMessage"), value: "new-chat", primary: true },
            { label: t("group.create"), value: "new-group" },
            { label: t("home.cancel"), value: null },
        ]);
        if (destination) location.hash = `#/${destination}`;
    } }, glyph("plus"));

    shell.sideNav.replaceChildren(
        h("a", { class: "row side-row side-home", href: "#/home", "data-key": "home", "aria-current": current.screen === "home" ? "page" : null }, icon("home"), h("span", { class: "row-main" }, h("span", { class: "row-title" }, t("nav.home")))),
        section(t("layout.voice"), voiceRows),
        section(t("layout.text"), textRows),
        section(t("layout.direct"), direct.length ? direct : [h("li", { class: "hint side-empty" }, t("home.empty"))], newButton));
}

/** The voice card and the private call card at the bottom of the sidebar. */
function renderCards() {
    const session = app.session;
    const voice = app.voice;
    const cards = [];
    if (voice?.active) {
        const channel = session.channels().find(c => c.id === voice.channelID);
        const name = channel?.name || t("voice.title");
        const button = (label, glyphNode, pressed, onclick, extra = "") => h("button", {
            class: `icon-btn${extra}`, type: "button", "aria-label": label, "data-key": `card:${label}`,
            "aria-pressed": pressed === null ? null : String(pressed), onclick,
        }, glyphNode);
        cards.push(h("div", { class: "voice-card", role: "region", "aria-label": t("layout.voiceCard") },
            h("a", { class: "voice-card-title", href: "#/voice", "data-key": "card:room" },
                h("span", { class: `speaking-dot${voice.speaking.size ? " on" : ""}`, "aria-hidden": "true" }),
                h("span", { class: "row-main" }, h("span", { class: "row-title" }, name), h("span", { class: "row-sub" }, t("voice.connected")))),
            h("ul", { class: "voice-card-members", "aria-label": t("layout.membersOf", { name }) },
                (channel?.clients || []).map(client => h("li", {}, speakingAvatar(client)))),
            h("div", { class: "voice-card-controls" },
                button(t("voice.mute"), icon(voice.muted ? "micOff" : "mic"), voice.muted, () => voice.setMuted(!voice.muted)),
                button(t("voice.deafen"), glyph("headphones"), voice.deafened, () => voice.setDeafened(!voice.deafened)),
                button(t("layout.devices"), glyph("sliders"), null, () => deviceMenu()),
                button(t("voice.leave"), icon("hangup"), null, () => leaveVoice(), " danger"))));
    }
    const calls = app.calls;
    if (calls?.active && !calls.incoming) {
        const others = calls.call.participants.filter(p => p.unique_id !== calls.uid).map(p => nickname(p.unique_id));
        cards.push(h("div", { class: "voice-card", role: "region", "aria-label": t("call.title") },
            h("div", { class: "voice-card-title" },
                h("a", { class: "row-main", href: `#/call/${calls.call.id}`, "data-key": "card:call" }, h("span", { class: "row-title" }, others.join(", ")), h("span", { class: "row-sub" }, t("call.active"))),
                h("button", { class: "icon-btn danger", type: "button", "aria-label": t("call.hangUp"), onclick: () => calls.hangUp() }, icon("hangup")))));
    }
    shell.cards.replaceChildren(...cards);
}

/** Quick device menu of the voice card: microphone and audio output. */
async function deviceMenu() {
    const voice = app.voice;
    if (!voice?.active) return;
    const devices = await navigator.mediaDevices?.enumerateDevices().catch(() => []) || [];
    const mics = devices.filter(d => d.kind === "audioinput");
    const outputs = Voice.outputSelectable() ? devices.filter(d => d.kind === "audiooutput") : [];
    const chosenMic = settings.get("microphoneID");
    const buttons = [
        ...mics.map((d, i) => ({ label: `${d.deviceId === chosenMic || (!chosenMic && i === 0) ? "✓ " : ""}${t("layout.microphone", { name: d.label || t("layout.micDefault") })}`, value: `mic:${d.deviceId}` })),
        ...outputs.map(d => ({ label: `${d.deviceId === (voice.sinkId || "default") ? "✓ " : ""}${t("layout.output", { name: d.label || t("voice.outputDefault") })}`, value: `out:${d.deviceId}` })),
    ];
    if (!outputs.length && navigator.mediaDevices?.selectAudioOutput) buttons.push({ label: t("voice.chooseOutput"), value: "choose" });
    buttons.push({ label: t("common.close"), value: null });
    const choice = await sheet(t("layout.devices"), mics.length > 1 ? t("layout.micNext") : null, buttons);
    if (!choice) return;
    if (choice === "choose") voice.chooseOutput().catch(() => {});
    else if (choice.startsWith("out:")) voice.setOutput(choice.slice(4)).catch(() => {});
    else if (choice.startsWith("mic:")) settings.set({ microphoneID: choice.slice(4) });
}

function tabTitle(key) {
    const { screen, arg } = tabs.parseKey(key);
    if (screen === "chat") return scopeName(app.session, Number(arg) || 0).replace(/^# /, "#");
    if (screen === "dm") return nickname(arg);
    return app.groups?.list.get(arg)?.name || t("group.title");
}

function unreadTag(key) {
    const { screen, arg } = tabs.parseKey(key);
    return screen === "chat" ? `channel:${arg}` : screen === "dm" ? `dm:${arg}` : `conversation:${arg}`;
}

function renderTabs() {
    const state = loadTabs();
    const keys = tabs.ordered(state);
    const active = activeKey();
    shell.tabBar.hidden = !keys.length;
    shell.tabBar.replaceChildren(...keys.map(key => {
        const title = tabTitle(key);
        const pinned = state.pinned.includes(key);
        return h("div", { class: `chat-tab${key === active ? " active" : ""}${pinned ? " pinned" : ""}`, role: "presentation" },
            h("a", { class: "chat-tab-link", role: "tab", href: tabs.tabHref(key), "aria-selected": String(key === active), "data-key": `tab:${key}`,
                title: pinned ? `📌 ${title}` : title,
                oncontextmenu: event => { event.preventDefault(); tabMenu(key, title); },
                onauxclick: event => { if (event.button === 1) { event.preventDefault(); closeTab(key); } },
                onkeydown: event => { if (event.key === "Delete") { event.preventDefault(); closeTab(key); } } },
            pinned ? h("span", { class: "chat-tab-pin", "aria-hidden": "true" }, "📌") : null,
            h("span", { class: "chat-tab-name" }, title),
            unreadBadge(unreadTag(key))),
            pinned ? null : h("button", { class: "chat-tab-close", type: "button", "aria-label": t("layout.closeTab", { name: title }), onclick: () => closeTab(key) }, glyph("close")));
    }));
    shell.tabBar.querySelector(".chat-tab.active")?.scrollIntoView?.({ block: "nearest", inline: "nearest" });
}

function closeTab(key) {
    const active = activeKey();
    const { state, next } = tabs.close(loadTabs(), key, active);
    saveTabs(state);
    if (key === active) location.hash = next ? tabs.tabHref(next) : "#/home";
    else renderAll();
}

async function tabMenu(key, title) {
    const state = loadTabs();
    const pinned = state.pinned.includes(key);
    const choice = await sheet(title, null, [
        { label: t(pinned ? "layout.unpin" : "layout.pin"), value: "pin", primary: true },
        { label: t("layout.moveLeft"), value: "left" },
        { label: t("layout.moveRight"), value: "right" },
        { label: t("layout.close"), value: "close", danger: true },
        { label: t("home.cancel"), value: null },
    ]);
    if (choice === "pin") saveTabs(tabs.togglePin(loadTabs(), key));
    else if (choice === "left" || choice === "right") saveTabs(tabs.move(loadTabs(), key, choice === "left" ? -1 : 1));
    else if (choice === "close") return closeTab(key);
    renderAll();
}

function detailsHeader(title, sub) {
    return h("div", { class: "details-header" },
        h("h2", {}, title, sub ? h("span", { class: "sub" }, sub) : null),
        h("button", { class: "icon-btn", type: "button", "aria-label": t("common.close"), "data-key": "details-close", onclick: toggleDetails }, glyph("close")));
}

function memberList(clients) {
    return clients.length ? h("ul", { class: "list details-members" }, clients.map(client => memberRow(client, "row side-member details-member")))
        : h("p", { class: "hint" }, t("layout.nobody"));
}

function renderDetails() {
    const session = app.session;
    const { screen, arg } = current;
    const channels = session.channels();
    const parts = [];
    if (screen === "chat" || (screen === "voice" && app.voice?.active)) {
        const id = screen === "chat" ? Number(arg) || 0 : app.voice.channelID;
        const channel = channels.find(c => c.id === id);
        const roomMembers = channel && !channel.textOnly && channel.clients.length;
        const name = id === 0 ? t("channels.global") : channel?.name || String(id);
        parts.push(detailsHeader(roomMembers ? t("layout.membersOf", { name }) : t("layout.onlineNow"), channel?.topic || null));
        if (channel?.description) parts.push(h("p", { class: "hint details-about" }, channel.description));
        parts.push(memberList(roomMembers ? channel.clients : onlineClients(session)));
    } else if (screen === "dm") {
        const client = session.clients().find(c => c.unique_id === arg);
        const channel = client && channels.find(c => c.clients.includes(client));
        const name = nickname(arg);
        parts.push(detailsHeader(t("layout.details")),
            h("div", { class: "details-card" },
                client ? speakingAvatar(client, "lg") : avatar(name, "lg"),
                h("p", { class: "details-name" }, name),
                h("p", { class: "hint" }, !client ? t("layout.statusOffline") : channel ? t("layout.statusIn", { channel: channel.name }) : t("layout.statusOnline")),
                h("p", { class: "hint" }, icon("lock"), " ", t("dm.encrypted")),
                h("button", { class: "btn btn-primary", type: "button", "data-key": "details-call", onclick: () => startCall(arg).catch(error => toast(error.code ? t(`call.error.${error.code}`) : error.message, "warn")) },
                    icon("phone"), t("call.start", { name }))));
    } else if (screen === "group") {
        const group = app.groups?.list.get(arg);
        const members = (group?.members || []).filter(m => !m.pending);
        parts.push(detailsHeader(group?.name || t("group.title"), t("group.members", { count: members.length })),
            h("ul", { class: "list details-members" }, members.map(member => {
                const client = session.clients().find(c => c.unique_id === member.unique_id) || { unique_id: member.unique_id, client_id: `uid:${member.unique_id}`, nickname: nickname(member.unique_id) };
                return memberRow(client, "row side-member details-member");
            })),
            h("button", { class: "btn btn-block", type: "button", "data-key": "details-call", onclick: () => startGroupCall(arg).catch(error => toast(error.code ? t(`call.error.${error.code}`) : error.message, "warn")) }, icon("phone"), t("group.call")));
    } else {
        // Room overview: who is online, by channel.
        parts.push(detailsHeader(t("layout.onlineNow"), t("layout.online", { count: onlineClients(session).length })));
        for (const channel of channels.filter(c => c.clients.length)) {
            parts.push(h("p", { class: "section-label" }, channel.name), memberList(channel.home ? channel.clients.filter(m => !m.on_mobile) : channel.clients));
        }
        const loose = session.snapshot?.unassigned_clients || [];
        if (loose.length) parts.push(h("p", { class: "section-label" }, t("layout.onlineNow")), memberList(loose));
    }
    shell.details.replaceChildren(...parts);
}

// --- Quick switcher ------------------------------------------------------------------

const sources = [];

/** Lets other features add switcher items: fn() -> [{label, kind, run, keywords?, typed?}]. */
export function addSwitcherSource(fn) {
    sources.push(fn);
}

function switcherItems() {
    const session = app.session;
    const go = hash => () => { location.hash = hash; };
    const boost = tag => (app.unread.get(tag)?.count ? 5 : 0);
    const items = [];
    const seen = new Set([session.uniqueID]);
    for (const conversation of app.dm?.list() || []) {
        seen.add(conversation.peer);
        items.push({ kind: "dm", label: conversation.nickname || nickname(conversation.peer), boost: boost(`dm:${conversation.peer}`), run: go(dmHref(conversation.peer)) });
    }
    for (const group of app.groups?.list.values() || []) {
        if (group.members?.find(m => m.unique_id === session.uniqueID)?.pending) continue;
        items.push({ kind: "group", label: group.name || group.id, boost: boost(`conversation:${group.id}`), run: go(`#/group/${encodeURIComponent(group.id)}`) });
    }
    if (session.current.has(0)) items.push({ kind: "text", label: t("channels.global"), boost: boost("channel:0"), run: go("#/chat/0") });
    for (const channel of session.channels()) {
        if (channel.chat) items.push({ kind: "text", label: channel.name, keywords: channel.topic ? [channel.topic] : [], boost: boost(`channel:${channel.id}`), run: go(`#/chat/${channel.id}`) });
        if (!channel.textOnly && !channel.home) items.push({ kind: "voice", label: channel.name, run: () => joinChannel(channel) });
    }
    for (const client of session.clients()) {
        if (!client.unique_id || seen.has(client.unique_id)) continue;
        seen.add(client.unique_id);
        items.push({ kind: "person", label: client.nickname, run: go(dmHref(client.unique_id)) });
    }
    const pages = [
        ["#/home", t("nav.home")], ["#/servers", t("nav.servers")], ["#/voice", t("nav.voice")],
        ["#/you", t("settings.title"), [t("nav.you"), "settings", "preferences"]],
        ["#/new-chat", t("chatNew.title")], ["#/new-group", t("group.create")],
    ];
    for (const [hash, label, keywords] of pages) items.push({ kind: "page", typed: true, label, keywords, run: go(hash) });
    for (const source of sources) {
        try { items.push(...source()); } catch { /* a broken source must not break the switcher */ }
    }
    return items;
}

const KIND_GLYPH = { text: "#", voice: "🔊", person: "@", dm: "✉", group: "◇", page: "⚙" };
let switcher = null;

/** Opens the quick switcher (Ctrl/Cmd+K, or the top bar's search field). */
export function openSwitcher() {
    if (!app.session) return;
    if (switcher) { switcher.input.focus(); return; }
    const items = switcherItems();
    const input = h("input", { class: "input qs-input", type: "text", role: "combobox", autocomplete: "off", spellcheck: "false",
        "aria-label": t("layout.searchLabel"), "aria-expanded": "true", "aria-controls": "qs-list", "aria-autocomplete": "list", placeholder: t("layout.search") });
    const list = h("ul", { class: "qs-list", id: "qs-list", role: "listbox", "aria-label": t("layout.results") });
    const status = h("p", { class: "qs-status", role: "status" });
    const overlay = h("div", { class: "qs-overlay", onclick: event => { if (event.target === overlay) closeSwitcher(); } },
        h("div", { class: "qs-box", role: "dialog", "aria-modal": "true", "aria-label": t("layout.searchLabel") },
            input, list, h("p", { class: "hint qs-hint" }, t("layout.hint")), status));
    let matches = [];
    let selected = 0;
    const choose = item => {
        closeSwitcher(false);
        item.run();
    };
    const render = () => {
        matches = rank(input.value, items);
        selected = Math.min(selected, Math.max(0, matches.length - 1));
        list.replaceChildren(...matches.map((item, index) => h("li", {
            id: `qs-option-${index}`, class: "qs-row", role: "option", "aria-selected": String(index === selected),
            onmousedown: event => event.preventDefault(), onclick: () => choose(item),
        }, h("span", { class: "qs-glyph", "aria-hidden": "true" }, KIND_GLYPH[item.kind] || "›"),
        h("span", { class: "qs-label" }, item.label),
        h("span", { class: "qs-kind" }, item.hint || t(`layout.kind.${item.kind}`)))));
        if (matches.length) input.setAttribute("aria-activedescendant", `qs-option-${selected}`);
        else input.removeAttribute("aria-activedescendant");
        list.children[selected]?.scrollIntoView?.({ block: "nearest" });
        status.textContent = matches.length ? t("layout.count", { count: matches.length }) : t("layout.empty", { query: input.value.trim() });
    };
    input.addEventListener("input", () => { selected = 0; render(); });
    input.addEventListener("keydown", event => {
        if (event.key === "ArrowDown" || event.key === "ArrowUp") {
            event.preventDefault();
            selected = (selected + (event.key === "ArrowDown" ? 1 : -1) + matches.length) % Math.max(1, matches.length);
            render();
        } else if (event.key === "Enter" && matches[selected]) {
            event.preventDefault();
            choose(matches[selected]);
        } else if (event.key === "Tab") {
            event.preventDefault(); // the input is the dialog's only stop
        }
    });
    switcher = { overlay, input, previous: document.activeElement };
    document.body.append(overlay);
    render();
    input.focus();
}

function closeSwitcher(restore = true) {
    if (!switcher) return;
    const { overlay, previous } = switcher;
    switcher = null;
    overlay.remove();
    if (restore && previous?.isConnected) previous.focus?.();
}

// --- Keyboard shortcuts --------------------------------------------------------------

if (typeof document !== "undefined") document.addEventListener("keydown", event => {
    if (event.defaultPrevented || !app.session) return;
    const key = event.key.length === 1 ? event.key.toLowerCase() : event.key;
    const mod = event.ctrlKey || event.metaKey;
    if (mod && !event.shiftKey && !event.altKey && key === "k") {
        event.preventDefault();
        if (switcher) closeSwitcher();
        else openSwitcher();
        return;
    }
    if (key === "Escape") {
        if (switcher) {
            event.preventDefault();
            closeSwitcher();
            return;
        }
        // Sheets close as if their backdrop was tapped (they resolve null).
        const overlays = document.querySelectorAll(".sheet-overlay");
        if (overlays.length) {
            event.preventDefault();
            overlays[overlays.length - 1].click();
            return;
        }
        if (shell && drawerOpen && drawerMedia?.matches) {
            drawerOpen = false;
            applyLayout();
        }
        return;
    }
    const voice = app.voice;
    if (mod && event.shiftKey && !event.altKey && (key === "m" || key === "d")) {
        if (!voice?.active) return;
        event.preventDefault();
        if (key === "m") voice.setMuted(!voice.muted);
        else voice.setDeafened(!voice.deafened);
        return;
    }
    if (shell && event.altKey && !mod && !event.shiftKey && (key === "ArrowUp" || key === "ArrowDown")) {
        const next = tabs.step(loadTabs(), activeKey(), key === "ArrowDown" ? 1 : -1);
        if (!next) return;
        event.preventDefault();
        location.hash = tabs.tabHref(next);
    }
});
