import { escapeHTML as escapeTranslation } from "./markdown.js";
// polish-ui.js — wave-8c polish & accessibility: pane transitions (337),
// resizable panes (338), detachable chat (339), fullscreen video (340),
// zen mode (341), screen-reader labels + live
// announcements (343), notification center (346), DND (347/348), and tree
// virtualization (349).
import { isActivationKey } from "./a11y.js";
import { closeDialog, mountDialog, mountServerDialog } from "./modal.js";
import { icon } from "./icons.js";

import { t } from "./i18n.js";
import { mountSnoozeControls } from "./notification-snooze.js";
import { captureNotificationDestination, createNotificationNavigator } from "./notification-navigation.js";
import { openMessageReference } from "./message-tools.js";

const V = () => window.__noxa;
const App = () => window.go.main.App;

// ---------------------------------------------------------------------------
// Resizable panes (338)
// ---------------------------------------------------------------------------

function initResizablePanes() {
    const sidebar = document.getElementById("sidebar");
    const details = document.getElementById("details");
    const body = document.getElementById("app-body");
    const s = V().state.settings || {};
    const handles = new Map();
    let keyboardSaveTimer = null;

    const setPaneWidth = (field, width) => {
        const variable = field === "sidebar_width" ? "--sidebar-width" : "--details-width";
        if (width > 120) body.style.setProperty(variable, width + "px");
        else body.style.removeProperty(variable);
    };

    const apply = () => {
        const w = V().state.settings || {};
        setPaneWidth("sidebar_width", w.sidebar_width);
        setPaneWidth("details_width", w.details_width);
        requestAnimationFrame(() => {
            for (const { element, handle } of handles.values()) {
                handle.setAttribute("aria-valuenow", String(Math.round(element.getBoundingClientRect().width)));
            }
        });
    };

    const scheduleKeyboardSave = () => {
        clearTimeout(keyboardSaveTimer);
        keyboardSaveTimer = setTimeout(() => App().SaveSettings(V().state.settings), 250);
    };

    const makeHandle = (el, field, invert) => {
        const h = document.createElement("div");
        h.className = "pane-handle" + (invert ? " right" : "");
        const translate = () => {
            h.title = t("wins.resizeHelp");
            h.setAttribute("aria-label", t(el.id === "sidebar" ? "wins.resizeChannels" : "wins.resizeDetails"));
        };
        translate();
        window.addEventListener("noxa-language-changed", translate);
        h.tabIndex = 0;
        h.setAttribute("role", "separator");
        h.setAttribute("aria-orientation", "vertical");
        h.setAttribute("aria-valuemin", "160");
        h.setAttribute("aria-valuemax", "560");
        const updateValue = (width) => h.setAttribute("aria-valuenow", String(Math.round(width)));
        handles.set(field, { element: el, handle: h });
        updateValue(el.getBoundingClientRect().width);
        let startX = 0, startW = 0;
        h.addEventListener("mousedown", (e) => {
            e.preventDefault();
            startX = e.clientX;
            startW = el.getBoundingClientRect().width;
            const onMove = (ev) => {
                const w = Math.max(160, Math.min(560, startW + (invert ? startX - ev.clientX : ev.clientX - startX)));
                setPaneWidth(field, w);
                updateValue(w);
                (V().state.settings ||= {})[field] = Math.round(w);
            };
            const onUp = () => {
                window.removeEventListener("mousemove", onMove);
                window.removeEventListener("mouseup", onUp);
                App().SaveSettings(V().state.settings);
            };
            window.addEventListener("mousemove", onMove);
            window.addEventListener("mouseup", onUp);
        });
        h.addEventListener("dblclick", () => {
            V().state.settings[field] = 0;
            scheduleKeyboardSave();
            apply();
            updateValue(el.getBoundingClientRect().width);
        });
        h.addEventListener("keydown", (event) => {
            if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
            event.preventDefault();
            const direction = event.key === "ArrowRight" ? 1 : -1;
            const current = el.getBoundingClientRect().width;
            const width = Math.max(160, Math.min(560, current + direction * (invert ? -16 : 16)));
            setPaneWidth(field, width);
            updateValue(width);
            (V().state.settings ||= {})[field] = Math.round(width);
            scheduleKeyboardSave();
        });
        el.appendChild(h);
    };
    makeHandle(sidebar, "sidebar_width", false);
    makeHandle(details, "details_width", true);
    apply();
    window.runtime.EventsOn("settings_update", apply);
    void s;
}

// ---------------------------------------------------------------------------
// Detachable chat (339): compact floating chat panel. A true second OS
// window needs Wails v3; this CSS overlay is the documented stand-in.
// ---------------------------------------------------------------------------

let chatPop = null;
let chatHome = null; // {wrapMarker, inputMarker}: where the panes dock back

function toggleChatPopout() {
    if (chatPop) {
        const wrap = document.getElementById("chat-wrap");
        const inputRow = document.getElementById("chat-input-row");
        // The panes live inside the popout: dock them before removing it, or
        // remove() takes the whole chat pane with it.
        if (chatHome?.wrapMarker?.parentNode) {
            chatHome.wrapMarker.parentNode.replaceChild(wrap, chatHome.wrapMarker);
        }
        if (chatHome?.inputMarker?.parentNode) {
            chatHome.inputMarker.parentNode.replaceChild(inputRow, chatHome.inputMarker);
        }
        chatPop.remove();
        chatPop = null;
        chatHome = null;
        wrap.classList.remove("hidden");
        inputRow.classList.remove("hidden");
        return;
    }
    chatPop = document.createElement("div");
    chatPop.className = "chat-popout";
    chatPop.innerHTML = `<div class="chat-pop-head">${escapeTranslation(t("desktop.chat.drag.me"))} <button class="icon-btn chat-pop-close" aria-label="${escapeTranslation(t("desktop.close.chat.popout"))}">✕</button></div>`;
    const wrap = document.getElementById("chat-wrap");
    const inputRow = document.getElementById("chat-input-row");
    const wrapMarker = document.createComment("chat-wrap-marker");
    const inputMarker = document.createComment("chat-input-row-marker");
    wrap.parentNode.replaceChild(wrapMarker, wrap);
    inputRow.parentNode.replaceChild(inputMarker, inputRow);
    chatHome = { wrapMarker, inputMarker };
    chatPop.appendChild(wrap);
    chatPop.appendChild(inputRow);
    wrap.classList.remove("hidden");
    inputRow.classList.remove("hidden");
    chatPop.querySelector(".chat-pop-close").onclick = toggleChatPopout;
    // Drag by the header.
    const head = chatPop.querySelector(".chat-pop-head");
    head.addEventListener("mousedown", (e) => {
        const startX = e.clientX - chatPop.offsetLeft;
        const startY = e.clientY - chatPop.offsetTop;
        const onMove = (ev) => {
            chatPop.style.left = Math.max(0, ev.clientX - startX) + "px";
            chatPop.style.top = Math.max(0, ev.clientY - startY) + "px";
        };
        const onUp = () => {
            window.removeEventListener("mousemove", onMove);
            window.removeEventListener("mouseup", onUp);
        };
        window.addEventListener("mousemove", onMove);
        window.addEventListener("mouseup", onUp);
    });
    document.body.appendChild(chatPop);
}

// ---------------------------------------------------------------------------
// Zen mode (341)
// ---------------------------------------------------------------------------

function toggleZen() {
    const on = !document.body.classList.contains("zen");
    if (on) window.__noxaFiles?.activateWorkspaceTab?.("chat", { focus: false });
    document.body.classList.toggle("zen", on);
    let ind = document.getElementById("zen-indicator");
    if (on && !ind) {
        ind = document.createElement("button");
        ind.id = "zen-indicator";
        ind.textContent = t("desktop.zen.click.or.hotkey.to.exit");
        ind.onclick = toggleZen;
        document.body.appendChild(ind);
    } else if (!on && ind) {
        ind.remove();
    }
    window.__noxaFiles?.restoreVisibleWorkspaceFocus?.();
}

// ---------------------------------------------------------------------------
// Screen reader (343)
// ---------------------------------------------------------------------------

function initA11y() {
    const tree = document.getElementById("channel-tree");
    tree.setAttribute("role", "tree");
    tree.setAttribute("aria-label", t("desktop.channels.and.users"));
    const chatLog = document.getElementById("chat-log");
    chatLog.setAttribute("role", "log");
    // History is rerendered for filters, pagination and tab switches. Keep it
    // silent and announce only messages that arrive live via #chat-announcer.
    chatLog.setAttribute("aria-live", "off");
    const login = document.querySelector(".login-card");
    login.setAttribute("role", "dialog");
    login.setAttribute("aria-modal", "true");
    if (!document.getElementById("login-overlay").classList.contains("hidden")) {
        requestAnimationFrame(() => document.getElementById("login-addr")?.focus({ preventScroll: true }));
    }
}

// Speaking events share the application's single polite live region (343).
function announce(text) {
    V().announceLive(text, "polite");
}

// ---------------------------------------------------------------------------
// Notification center (346) + DND (347/348)
// ---------------------------------------------------------------------------

const notifHistory = []; // {kind, text, at, destination, read}
const notifViews = new Set();
const navigateNotification = createNotificationNavigator({
    state: () => V().state, app: App,
    on: (event, callback) => window.runtime.EventsOn(event, callback),
    open: (reference, isCurrent) => openMessageReference(reference, { continueConversation: true, isCurrent }),
    unavailable: () => V().toast(t("messages.notFound"), "warn", "alert", { record: false, bypassDND: true }),
    confirmSwitch: target => {
        const state = V().state;
        if (!state.myChannelID && !state.pc && !state.localStream && !state.shareStream && !window.__noxaPrivateCalls?.overlaySnapshot?.()?.active) return true;
        return new Promise(resolve => {
            const overlay = document.createElement("div"); overlay.className = "dlg-overlay";
            const dialog = document.createElement("section"); dialog.className = "dlg";
            const heading = document.createElement("h3"); heading.textContent = t("messages.switchServer");
            const text = document.createElement("p"); text.className = "dlg-text";
            text.textContent = t("messages.switchServerHint", { server: target.addr || target.id });
            const actions = document.createElement("div"); actions.className = "dlg-buttons";
            const cancel = document.createElement("button"); cancel.type = "button"; cancel.className = "dlg-cancel"; cancel.textContent = t("desktop.cancel");
            const accept = document.createElement("button"); accept.type = "button"; accept.className = "dlg-ok"; accept.textContent = t("messages.switchServer");
            let accepted = false;
            cancel.onclick = () => closeDialog(overlay);
            accept.onclick = () => { accepted = true; closeDialog(overlay); };
            actions.append(cancel, accept); dialog.append(heading, text, actions); overlay.append(dialog);
            mountServerDialog(overlay, { initialFocus: cancel, onClose: () => resolve(accepted) });
        });
    },
});

// recordNotification appends to the bell history (session-persisted, 50).
export function recordNotification(kind, text, ctx = {}) {
    notifHistory.unshift({ kind, text, at: Date.now(), destination: captureNotificationDestination(kind, ctx, V().state) });
    if (notifHistory.length > 50) notifHistory.pop();
    for (const render of notifViews) render();
    updateBellBadge();
}

function updateBellBadge() {
    const badge = document.getElementById("notif-badge");
    if (!badge) return;
    const n = notifHistory.filter((x) => !x.read).length;
    badge.textContent = n > 0 ? (n > 9 ? "9+" : n) : "";
    badge.classList.toggle("hidden", n === 0);
    document.getElementById("notif-bell")?.setAttribute(
        "aria-label", t("runtime.unreadNotifications", { count: n }));
}

// dndActive reports whether DND is on (toggle or quiet hours, 347/348).
export function dndActive(settings) {
    const s = settings || V().state.settings || {};
    if (Number.isFinite(s.notification_snooze_until) && s.notification_snooze_until > Date.now()) return true;
    if (s.dnd_enabled) return true;
    if (!s.dnd_from || !s.dnd_to) return false;
    const now = new Date();
    const cur = now.getHours() * 60 + now.getMinutes();
    const parse = (x) => {
        const [h, m] = x.split(":").map(Number);
        return (h || 0) * 60 + (m || 0);
    };
    const from = parse(s.dnd_from);
    const to = parse(s.dnd_to);
    if (from <= to) return cur >= from && cur < to;
    return cur >= from || cur < to; // overnight window
}

function openNotifCenter() {
    notifHistory.forEach((x) => { x.read = true; });
    updateBellBadge();
    const overlay = document.createElement("div");
    overlay.className = "dlg-overlay";
    const rows = new Map();
    const render = () => {
        // Removal can precede the shared modal lifecycle's cleanup observer.
        if (!overlay.isConnected) {
            notifViews.delete(render);
            return;
        }
        const list = overlay.querySelector(".nc-list");
        const focused = document.activeElement;
        const hadFocus = list.contains(focused);
        const anchor = list.scrollTop > 0
            ? [...list.children].find(row => row.getBoundingClientRect().bottom > list.getBoundingClientRect().top)
            : null;
        const anchorTop = anchor?.getBoundingClientRect().top;
        for (const [n, row] of rows) {
            if (notifHistory.includes(n)) continue;
            row.remove();
            rows.delete(n);
        }
        list.querySelector(".empty-state")?.remove();
        if (!notifHistory.length) list.innerHTML = `<div class="empty-state">${t("workspace.noNotifications")}</div>`;
        for (const n of notifHistory) {
            n.read = true;
            if (rows.has(n)) continue;
            const row = document.createElement("div");
            row.className = "nc-row";
            row.classList.toggle("nc-warning", n.kind === "warn");
            row.innerHTML = `<span class="nc-kind mono"></span><span class="nc-text"></span><span class="nc-time mono"></span>`;
            row.querySelector(".nc-kind").textContent = n.kind;
            row.querySelector(".nc-text").textContent = n.text;
            row.querySelector(".nc-time").textContent = new Date(n.at).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
            if (n.destination) {
                row.classList.add("clickable");
                row.tabIndex = 0;
                row.setAttribute("role", "button");
                row.onclick = () => {
                    overlay.remove();
                    void navigateNotification(n.destination);
                };
            }
            if (row.classList.contains("clickable")) {
                row.addEventListener("keydown", (event) => {
                    if (!isActivationKey(event.key)) return;
                    event.preventDefault();
                    row.click();
                });
            }
            rows.set(n, row);
            list.insertBefore(row, list.children[notifHistory.indexOf(n)] || null);
        }
        if (anchor?.isConnected) list.scrollTop += anchor.getBoundingClientRect().top - anchorTop;
        if (hadFocus && !focused.isConnected) overlay.querySelector(".nc-close").focus({ preventScroll: true });
    };
    overlay.innerHTML = `
        <div class="dlg notif-center">
            <div class="pm-head">
                <h3>${escapeTranslation(t("runtime.notifications"))}</h3>
                <button class="icon-btn nc-clear" title="${escapeTranslation(t("desktop.clear.all"))}" aria-label="${escapeTranslation(t("desktop.clear.all.notifications"))}">${icon("trash")}</button>
                <button class="icon-btn nc-close" title="${escapeTranslation(t("desktop.close"))}" aria-label="${escapeTranslation(t("desktop.close.notifications"))}">${icon("close")}</button>
            </div>
            <div class="nc-list"></div>
            <div class="nc-snooze"></div>
        </div>`;
    overlay.querySelector(".nc-close").onclick = () => overlay.remove();
    overlay.querySelector(".nc-clear").onclick = () => {
        notifHistory.length = 0;
        updateBellBadge();
        render();
    };
    overlay.onclick = (e) => { if (e.target === overlay) overlay.remove(); };
    const cleanupSnooze = mountSnoozeControls(overlay.querySelector(".nc-snooze"));
    mountDialog(overlay, { onClose: () => { notifViews.delete(render); cleanupSnooze(); } });
    notifViews.add(render);
    render();
}

// ---------------------------------------------------------------------------
// Tree virtualization (349)
// ---------------------------------------------------------------------------

// When the total row count (channels + users) exceeds 500 the tree goes
// windowed: only channels on the path to my channel (and channels the user
// explicitly expanded via double-click) render their member rows; every
// other channel renders as a collapsed header with its [n] count, so DOM
// size stays O(channels) instead of O(channels + users). Verified with
// window.__noxaFakeTree(n), which injects n synthetic channels of 3 users.
const VIRTUAL_THRESHOLD = 500;

// virtualizeEnabled reports whether the tree is in windowed mode.
export function virtualizeEnabled() {
    const { state } = V();
    return state.channels.length + state.clients.length > VIRTUAL_THRESHOLD;
}

// myBranchIDs returns the set of channel IDs on the path to my channel
// (itself + ancestors), which always renders fully when virtualized.
export function myBranchIDs() {
    const { state } = V();
    const out = new Set();
    let id = state.myChannelID;
    let guard = 0;
    while (id && guard++ < 1000) {
        out.add(id);
        const ch = state.channels.find((c) => c.ChannelID === id);
        id = ch ? ch.ParentID : 0;
    }
    return out;
}

// injectFakeTree adds n synthetic channels of 3 users each and measures the
// render time (349 verification).
function injectFakeTree(n) {
    const { state } = V();
    for (let i = 0; i < n; i++) {
        const cid = 900000 + i;
        state.channels.push({ ChannelID: cid, ParentID: 0, Name: "stress-" + i, ClientCount: 3 });
        for (let u = 0; u < 3; u++) {
            state.clients.push({
                client_id: "fake-" + cid + "-" + u,
                unique_id: "fake-uid-" + cid + "-" + u,
                nickname: "stress-user-" + i + "-" + u,
                channel_id: cid,
            });
        }
    }
    const t0 = performance.now();
    V().renderTree();
    const ms = performance.now() - t0;
    const domRows = document.querySelectorAll("#channel-tree .channel, #channel-tree .client").length;
    const total = state.channels.length + state.clients.length;
    if (import.meta.env.DEV) {
    }
    return { ms, domRows, total, windowed: virtualizeEnabled() };
}

// ---------------------------------------------------------------------------

export function initPolishUI() {
    initResizablePanes();
    initA11y();
    document.getElementById("notif-bell").onclick = openNotifCenter;
    window.__noxaPolish = { toggleChatPopout, toggleZen, openNotifCenter, announce, recordNotification, dndActive, virtualizeEnabled, myBranchIDs };
    window.__noxaFakeTree = injectFakeTree;
}
