// dm-view.js is the Direct Messages view (Arctic Night design, screen
// "private messages"): a conversation list for direct messages AND private
// groups next to the selected thread. It replaces the main area while open.
//
// The thread itself is not reimplemented. A direct message is the existing
// chat pane in its DM view (receipts, typing, attachments, voice notes,
// export, offline catch-up, per-message encryption badge, plaintext refusal);
// a private group is the existing private-group panel (conversations.js).
// This module adds the list, the thread header and the composer extras, and
// arctic-dm.css restyles both threads as bubbles.
//
// Entry points (for the sidebar "Direct › Messages" row):
//   window.__noxa.openDirectMessages(target?)   target: {uid} | {groupID}
//   window.__noxa.closeDirectMessages()
//   window.__noxa.isDirectMessagesOpen()
//   window.__noxa.directMessagesUnread()        DM + private group unread
//   window event "noxa-direct-unread" {detail:{count}} whenever that changes,
//   window event "noxa-direct-view" {detail:{open}} when the view opens/closes.
import { t } from "./i18n.js";
import { icon } from "./icons.js";
import * as chat from "./chat-ui.js";
import { closeConversations, createPrivateGroup, initConversations, isPrivateGroupActive, listedPrivateGroups, openConversationsAt, viewedConversationID } from "./conversations.js";
import { showCallHistory, startPrivateCall, startPrivateVideoCall } from "./private-calls.js";
import { encryptionBadge } from "./message-encryption.js";
import { avatarColor } from "./workspace-ui.js";
import { setSafeImage } from "./safe-media.js";
import { confirmDialog, promptDialog } from "./modal.js";
import { mountContextMenu, closeContextMenu } from "./context-menu.js";
import {
    conversationEncryption, daySeparators, filterConversations, mergeDirect, presenceLine, presenceOf,
    previewLine, relativeTime, sortConversations, totalUnread, unreadLabel,
} from "./dm-model.js";
import "./arctic-dm.css";

const V = () => window.__noxa;
const app = () => window.go.main.App;
const $ = id => document.getElementById(id);

// Glyphs the shared icon set does not carry, drawn with the same stroke.
const EXTRA_ICONS = {
    phone: '<path d="M5 3h3l2 5-2.5 1.5a11 11 0 0 0 7 7L16 14l5 2v3a2 2 0 0 1-2 2A16 16 0 0 1 3 5a2 2 0 0 1 2-2"/>',
    back: '<path d="m15 5-7 7 7 7"/>',
};
function glyph(name) {
    if (!EXTRA_ICONS[name]) return icon(name);
    return `<svg class="ui-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${EXTRA_ICONS[name]}</svg>`;
}

let ui = null;
let active = false;
let selected = null; // {kind: "dm", uid} | {kind: "group", id}
let scope = "";
let stored = null;
let storedAt = 0;
let storedRequest = null;
let entries = [];
let query = "";
let renderQueued = false;
let lastUnread = -1;
let refreshTimer = 0;
let dayObserver = null;
let decorating = false;
const groupPreviews = new Map(); // groupID -> {latest, message, pending}
let previewChain = Promise.resolve();
const peerKeys = new Map(); // uid -> {value, at}

const currentScope = () => `${V()?.state.activeTabID || ""}:${V()?.state.serverGeneration ?? ""}`;
const clients = () => V()?.state.clients || [];
const blocked = uid => (V()?.state.settings?.blocked_users || []).includes(uid);
const memberName = uid => clients().find(client => client.unique_id === uid)?.nickname || chat.directPeerName(uid);
const selectedKey = () => selected ? (selected.kind === "dm" ? "dm:" + selected.uid : "grp:" + selected.id) : "";

function node(tag, className = "", text) {
    const element = document.createElement(tag);
    if (className) element.className = className;
    if (text !== undefined) element.textContent = text;
    return element;
}

function iconButton(name, labelKey, onClick) {
    const button = node("button", "icon-btn dm-icon-btn");
    button.type = "button";
    button.innerHTML = glyph(name);
    button.dataset.label = labelKey;
    button.setAttribute("aria-label", t(labelKey));
    button.title = t(labelKey);
    button.onclick = onClick;
    return button;
}

function srText(text) { return node("span", "sr-only", text); }

function avatar(uid, name, size = "") {
    const element = node("span", `avatar dm-avatar${size ? " dm-avatar-" + size : ""}`);
    element.setAttribute("aria-hidden", "true");
    if (uid) element.dataset.uid = uid;
    element.style.setProperty("--avatar-color", avatarColor(uid || name || ""));
    const image = uid ? V()?.state.avatars?.get(uid) : null;
    if (!image || !setSafeImage(element, image, { avatar: true })) element.textContent = (V()?.initials?.(name || "?") || String(name || "?").slice(0, 1)).toUpperCase();
    if (uid && V()?.state.avatars && !V().state.avatars.has(uid)) V().fetchAvatar?.(uid);
    return element;
}

function presenceDot(state) {
    const dot = node("span", "dm-presence");
    dot.dataset.presence = state;
    dot.setAttribute("aria-hidden", "true");
    return dot;
}

function badgeIcon(kind) {
    const badge = node("span", "dm-enc");
    badge.dataset.encryption = kind;
    badge.innerHTML = icon(kind);
    return badge;
}

// ------------------------------------------------------------------ data

function ensureScope() {
    const next = currentScope();
    if (next === scope) return true;
    scope = next;
    stored = null; storedAt = 0; storedRequest = null;
    groupPreviews.clear(); peerKeys.clear();
    selected = null;
    return false;
}

function loadStored(force = false) {
    if (!V()?.state.activeTabID) return;
    if (storedRequest || (!force && stored && Date.now() - storedAt < 60000)) return;
    const requestScope = scope;
    storedRequest = chat.storedDirectPeers().then(peers => {
        if (requestScope !== scope || peers === null) return;
        stored = peers; storedAt = Date.now();
    }).catch(() => { if (requestScope === scope) stored ||= []; })
        .finally(() => { if (requestScope === scope) { storedRequest = null; scheduleRender(); } });
}

function groupMessage(message, uid) {
    const verified = !!message.enc_verified;
    return {
        id: message.id, text: message.body, ts: (Number(message.created_at) || 0) * 1000,
        self: message.from_unique_id === uid, fromUID: message.from_unique_id,
        enc: verified, encVerified: verified, encMode: verified ? "e2e" : "", blocked: blocked(message.from_unique_id),
    };
}

// The newest message of a group is opened locally (exactly like the group
// notification preview) once per new latest_message_id.
function queuePreview(snapshot, group) {
    const latest = Number(group.latest_message_id) || 0;
    const cached = groupPreviews.get(group.id);
    if (!latest) { groupPreviews.delete(group.id); return; }
    if (cached && (cached.latest === latest || cached.pending === latest)) return;
    const pending = { latest: 0, message: null, pending: latest };
    groupPreviews.set(group.id, pending);
    const requestScope = scope;
    previewChain = previewChain.then(async () => {
        if (requestScope !== scope || groupPreviews.get(group.id) !== pending) return;
        try {
            const page = await app().ConversationForTab(snapshot.tabID, { action: "history", id: group.id, before_id: latest + 1 });
            if (requestScope !== scope || groupPreviews.get(group.id) !== pending) return;
            const newest = (page?.messages || []).reduce((best, message) => !best || message.id > best.id ? message : best, null);
            groupPreviews.set(group.id, { latest, message: newest ? groupMessage(newest, snapshot.uid) : null, pending: 0 });
        } catch {
            if (requestScope === scope && groupPreviews.get(group.id) === pending) groupPreviews.set(group.id, { latest, message: null, pending: 0 });
        }
        scheduleRender();
    });
}

function directEntries() {
    const st = V().state;
    return mergeDirect(stored || [], chat.liveDirectConversations(), chat.directMessageFromEntry).map(item => {
        const name = chat.directPeerName(item.uid, item.name);
        const badge = item.last ? encryptionBadge(item.last) : null;
        return {
            key: "dm:" + item.uid, kind: "dm", uid: item.uid, name, unread: item.unread, offline: item.offline,
            last: item.last, lastAt: item.lastAt, badge: badge?.kind || "", memberNames: [name],
            preview: previewLine(item.last), presence: presenceOf(item.uid, st.clients, st.channels),
        };
    });
}

function groupEntries() {
    const snapshot = listedPrivateGroups();
    if (!snapshot || `${snapshot.tabID}:${snapshot.generation}` !== scope) return [];
    return snapshot.groups.map(group => {
        const pending = !!group.members.find(member => member.unique_id === snapshot.uid)?.pending;
        const others = group.members.filter(member => member.unique_id !== snapshot.uid && !member.pending);
        if (!pending) queuePreview(snapshot, group);
        const last = groupPreviews.get(group.id)?.message || null;
        const preview = pending ? t("dm.invitation") : last?.blocked ? t("group.blocked")
            : previewLine(last, { group: true, senderName: last && !last.self ? memberName(last.fromUID) : "" });
        return {
            key: "grp:" + group.id, kind: "group", id: group.id, name: group.name, group, invitation: pending,
            unread: pending ? 0 : group.unread_count || 0, last, lastAt: last?.ts || 0, badge: "lock", preview,
            members: others.map(member => member.unique_id), memberNames: others.map(member => memberName(member.unique_id)),
        };
    });
}

function collect() {
    if (!V()?.state.activeTabID) return [];
    return sortConversations([...directEntries(), ...groupEntries()]);
}

function publishUnread(list) {
    const count = totalUnread(list);
    if (count === lastUnread) return;
    lastUnread = count;
    window.dispatchEvent(new CustomEvent("noxa-direct-unread", { detail: { count } }));
}

// ------------------------------------------------------------------ render

function scheduleRender() {
    if (renderQueued) return;
    renderQueued = true;
    queueMicrotask(() => { renderQueued = false; render(); });
}

function render() {
    if (!ensureScope() && active) { closeDirectMessages(); return; }
    entries = collect();
    publishUnread(entries);
    if (!active || !ui) return;
    // A group this user left or was removed from leaves the selection once the
    // authoritative list no longer has it. Nothing is opened in its place:
    // opening a conversation sends read receipts (and a group's "Seen by"), so
    // only the user's own choice opens one. Until then the hint is shown.
    const snapshot = listedPrivateGroups();
    if (selected?.kind === "group" && snapshot && viewedConversationID() !== selected.id && !snapshot.groups.some(group => group.id === selected.id)) {
        selected = null;
        $("center").classList.remove("dm-thread-open");
    }
    renderList();
    renderThread();
}

function rowFor(entry) {
    const item = node("li", "dm-row-item");
    const row = node("button", "dm-row");
    row.type = "button";
    row.dataset.key = entry.key;
    const isSelected = entry.key === selectedKey();
    row.setAttribute("aria-current", String(isSelected));
    if (entry.unread) row.classList.add("unread");
    const avatars = node("span", "dm-row-avatar");
    if (entry.kind === "dm") {
        avatars.append(avatar(entry.uid, entry.name), presenceDot(entry.presence.state));
    } else {
        avatars.classList.add("stack");
        const shown = entry.members.slice(0, 2);
        if (!shown.length) avatars.append(avatar("", entry.name));
        for (const uid of shown) avatars.append(avatar(uid, memberName(uid), "small"));
    }
    const main = node("span", "dm-row-main");
    const top = node("span", "dm-row-top");
    top.append(node("span", "dm-row-name", entry.name));
    const encryption = entry.kind === "group" ? "lock" : entry.badge;
    if (encryption) top.append(badgeIcon(encryption));
    top.append(node("span", "dm-row-time", relativeTime(entry.lastAt)));
    const bottom = node("span", "dm-row-bottom");
    bottom.append(node("span", "dm-row-preview", entry.preview || (entry.kind === "group" ? t("dm.privateGroup") : "")));
    const count = unreadLabel(entry.unread);
    if (count) {
        const unread = node("span", "dm-row-unread", count);
        unread.setAttribute("aria-hidden", "true");
        bottom.append(unread);
    }
    main.append(top, bottom);
    row.append(avatars, main);
    const facts = [entry.kind === "group" ? t("dm.privateGroup") : t(`dm.presence.${entry.presence.state}`)];
    // Conversations are end-to-end only: anything but the lock is unverified.
    if (encryption) facts.push(t(encryption === "lock" ? "dm.encryption.e2e" : "dm.encryption.unverified"));
    if (entry.invitation) facts.push(t("dm.invitation"));
    if (entry.unread) facts.push(t("dm.unread", { count: entry.unread }));
    row.append(srText(", " + facts.join(", ")));
    row.onclick = () => select(entry, { focus: false });
    item.append(row);
    return item;
}

function startRow(client) {
    const item = node("li", "dm-row-item");
    const row = node("button", "dm-row dm-row-start");
    row.type = "button";
    row.dataset.key = "start:" + client.unique_id;
    row.setAttribute("aria-current", "false");
    const avatars = node("span", "dm-row-avatar");
    avatars.append(avatar(client.unique_id, client.nickname), presenceDot(presenceOf(client.unique_id, clients(), V().state.channels).state));
    const main = node("span", "dm-row-main");
    main.append(node("span", "dm-row-top", client.nickname), node("span", "dm-row-bottom", t("dm.startWith", { name: client.nickname })));
    row.append(avatars, main);
    row.onclick = () => select({ kind: "dm", uid: client.unique_id, name: client.nickname }, { focus: true });
    item.append(row);
    return item;
}

function renderList() {
    const focusedKey = ui.rows.contains(document.activeElement) ? document.activeElement.dataset.key : "";
    const shown = filterConversations(entries, query);
    const known = new Set(entries.filter(entry => entry.kind === "dm").map(entry => entry.uid));
    const me = V().state.myUniqueID;
    const q = query.trim().toLocaleLowerCase();
    const starts = q ? clients().filter(client => client.unique_id && client.unique_id !== me && !known.has(client.unique_id) && !blocked(client.unique_id)
        && String(client.nickname || "").toLocaleLowerCase().includes(q)).slice(0, 6) : [];
    ui.rows.replaceChildren(...shown.map(rowFor), ...starts.map(startRow));
    const buttons = [...ui.rows.querySelectorAll(".dm-row")];
    const current = buttons.find(button => button.getAttribute("aria-current") === "true") || buttons[0];
    for (const button of buttons) button.tabIndex = button === current ? 0 : -1;
    ui.empty.hidden = buttons.length > 0;
    ui.empty.classList.toggle("searching", !!q);
    ui.empty.textContent = q ? t("dm.noMatches", { query: query.trim() }) : t("dm.empty");
    if (focusedKey) buttons.find(button => button.dataset.key === focusedKey)?.focus({ preventScroll: true });
}

function selectedEntry() {
    return entries.find(entry => entry.key === selectedKey()) || null;
}

function renderThread() {
    const entry = selectedEntry();
    const center = $("center");
    center.dataset.dmKind = selected?.kind || "";
    center.classList.toggle("dm-view-empty", !selected);
    ui.head.hidden = !selected;
    ui.emptyThread.hidden = !!selected;
    if (!selected) { ui.emptyThread.textContent = entries.length ? t("dm.selectHint") : t("dm.empty"); return; }
    const st = V().state;
    const group = selected.kind === "group";
    const name = entry?.name || (group ? "" : chat.directPeerName(selected.uid));
    ui.headAvatar.replaceChildren();
    if (group) {
        ui.headAvatar.classList.add("stack");
        const members = entry?.members || [];
        if (!members.length) ui.headAvatar.append(avatar("", name));
        for (const uid of members.slice(0, 2)) ui.headAvatar.append(avatar(uid, memberName(uid), "small"));
    } else {
        ui.headAvatar.classList.remove("stack");
        const presence = presenceOf(selected.uid, st.clients, st.channels);
        ui.headAvatar.append(avatar(selected.uid, name, "large"), presenceDot(presence.state));
    }
    ui.headName.textContent = name;
    if (group) {
        const g = entry?.group;
        const accepted = g ? g.members.filter(member => !member.pending) : [];
        const online = accepted.filter(member => clients().some(client => client.unique_id === member.unique_id)).length;
        ui.headStatus.textContent = entry?.invitation ? t("dm.status.pending")
            : g?.active_call_count > 0 ? t("dm.status.groupCall", { count: g.call_participant_count })
            : t("dm.status.group", { online, count: accepted.length });
    } else {
        ui.headStatus.textContent = presenceLine(presenceOf(selected.uid, st.clients, st.channels));
    }
    const peerKey = group ? null : peerKeyState(selected.uid);
    // The header claim follows the connection's active secure path.
    const secure = secureState();
    if (secure !== null) maybeSecureNotice();
    const encryption = conversationEncryption({ group, peerKey, last: entry?.last || null, secure });
    ui.pill.dataset.encryption = encryption.kind;
    ui.pill.innerHTML = icon(encryption.kind === "legacy" ? "key" : encryption.kind);
    ui.pill.append(node("span", "", t(encryption.labelKey)));
    ui.pill.title = t(encryption.helpKey);
    ui.pill.setAttribute("aria-label", `${t(encryption.labelKey)}. ${t(encryption.helpKey)}`);
    const pending = !!entry?.invitation;
    ui.call.disabled = pending; ui.video.disabled = pending; ui.search.disabled = pending;
    const typing = $("chat-typing");
    if (typing && !group) {
        typing.dataset.initial = (name || "?").slice(0, 1).toUpperCase();
        typing.style.setProperty("--dm-typing-color", avatarColor(selected.uid));
    }
    observeDays();
}

// maybeSecureNotice explains the secure-messaging upgrade once (en/de), the
// first time a conversation shows its encryption state.
let secureNoticeOpen = false;
function maybeSecureNotice() {
    const settings = V().state.settings;
    if (!settings || settings.secure_notice_seen || secureNoticeOpen) return;
    secureNoticeOpen = true;
    settings.secure_notice_seen = true;
    Promise.resolve(app().SaveSettings(settings)).catch(() => {});
    Promise.resolve(confirmDialog({ title: t("security.secureNoticeTitle"), message: t("security.secureNoticeBody"), confirmLabel: t("common.ok"), cancelLabel: t("common.close") }))
        .catch(() => {}).finally(() => { secureNoticeOpen = false; });
}

// secureState: whether this connection's secure-messaging path (Olm) is
// active, from the same diagnostics call (cached like peer keys).
function secureState() {
    peerKeyState("");
    const cached = peerKeys.get("");
    return cached?.active ?? null;
}

function peerKeyState(uid) {
    const cached = peerKeys.get(uid);
    if (cached && Date.now() - cached.at < 60000) return cached.value;
    if (!cached?.pending) {
        const requestScope = scope;
        peerKeys.set(uid, { value: cached?.value ?? null, active: cached?.active ?? null, at: cached?.at || 0, pending: true });
        Promise.resolve().then(() => app().E2EEDiagnostics(uid)).then(result => {
            if (requestScope !== scope) return;
            peerKeys.set(uid, { value: !!result?.peer_key_available, active: typeof result?.active === "boolean" ? result.active : null, at: Date.now(), pending: false });
            scheduleRender();
        }).catch(() => {
            if (requestScope === scope) peerKeys.set(uid, { value: null, at: Date.now(), pending: false });
        });
    }
    return cached?.value ?? null;
}

// ------------------------------------------------------------------ selection

function select(target, { focus = false } = {}) {
    if (!active) return;
    closeThreadSearch();
    if (target.kind === "group" || target.id) {
        selected = { kind: "group", id: target.id };
        $("center").classList.add("dm-thread-open");
        const id = target.id;
        void openConversationsAt(id, 0, () => active && selected?.kind === "group" && selected.id === id);
    } else {
        selected = { kind: "dm", uid: target.uid };
        $("center").classList.add("dm-thread-open");
        if (isPrivateGroupActive()) closeConversations();
        chat.openPM(target.uid, target.name || entries.find(entry => entry.kind === "dm" && entry.uid === target.uid)?.name || "");
    }
    render();
    if (focus) {
        const input = selected.kind === "dm" ? $("chat-text") : document.querySelector("#private-groups .group-composer textarea");
        (input || ui.rows.querySelector('[aria-current="true"]'))?.focus({ preventScroll: true });
    }
}

// ------------------------------------------------------------------ thread search (groups)

function closeThreadSearch() {
    if (!ui?.threadSearch || ui.threadSearch.hidden) return;
    ui.threadSearch.hidden = true;
    ui.threadSearchInput.value = "";
    applyThreadSearch();
}

function applyThreadSearch() {
    const q = ui.threadSearchInput.value.trim().toLocaleLowerCase();
    const rows = [...document.querySelectorAll("#private-groups .group-message")];
    let matches = 0;
    for (const row of rows) {
        const show = !q || row.textContent.toLocaleLowerCase().includes(q);
        row.classList.toggle("dm-search-hidden", !show);
        if (show) matches++;
    }
    ui.threadSearchCount.textContent = q ? t("dm.searchCount", { count: matches, total: rows.length }) : "";
}

function openSearch() {
    if (selected?.kind === "dm") { $("chat-search-btn")?.click(); return; }
    ui.threadSearch.hidden = false;
    ui.threadSearchInput.focus();
}

// ------------------------------------------------------------------ menus

function menuItem(labelKey, action) {
    const item = node("button", "ctx-action", t(labelKey));
    item.type = "button";
    item.onclick = event => { event.stopPropagation(); closeContextMenu(menu(), true); action(); };
    return item;
}
const menu = () => document.querySelector(".dm-menu");

function openMenu(trigger, items) {
    const element = node("div", "ctx-menu dm-menu");
    element.append(...items);
    const rect = trigger.getBoundingClientRect();
    trigger.setAttribute("aria-expanded", "true");
    mountContextMenu(element, { trigger, x: rect.right - 220, y: rect.bottom + 6, onClose: () => trigger.setAttribute("aria-expanded", "false") });
}

function groupControl(selector) { return document.querySelector(`#private-groups ${selector}`); }

function openMoreMenu() {
    if (!selected) return;
    const items = [];
    if (selected.kind === "dm") {
        const uid = selected.uid;
        items.push(menuItem("dm.menu.encryption", () => $("chat-e2ee-btn")?.click()));
        items.push(menuItem("dm.menu.export", () => $("chat-export-btn")?.click()));
        items.push(menuItem("dm.menu.callHistory", () => void showCallHistory()));
        items.push(menuItem("dm.menu.clearHistory", () => chat.clearDirectHistory(uid)));
        items.push(menuItem("common.close", () => chat.closeDirectConversation(uid)));
    } else {
        const details = groupControl(".group-members");
        if (details) items.push(menuItem("dm.menu.members", () => {
            details.open = !details.open;
            if (details.open) details.querySelector("summary")?.focus();
        }));
        const mute = groupControl(".group-mute input");
        if (mute) items.push(menuItem(mute.checked ? "dm.menu.unmute" : "dm.menu.mute", () => mute.click()));
        const rename = groupControl(".group-rename");
        if (rename) items.push(menuItem("dm.menu.rename", () => rename.click()));
        items.push(menuItem("dm.menu.callHistory", () => void showCallHistory()));
        const refresh = groupControl(".group-refresh");
        if (refresh) items.push(menuItem("dm.menu.refresh", () => refresh.click()));
        const remove = groupControl(".group-delete");
        if (remove) items.push(menuItem("group.delete", () => remove.click()));
        const leave = groupControl(".group-leave");
        if (leave && !leave.disabled) items.push(menuItem("dm.menu.leave", () => leave.click()));
    }
    openMenu(ui.more, items);
}

function openNewMenu() {
    openMenu(ui.add, [
        // The member picker lists members online, contacts, open conversations
        // and group members by name; an existing conversation with the chosen
        // member is selected rather than started again.
        menuItem("dm.newDirect", () => {
            chat.openNewDirectMessagePicker({ onPick: entry => {
                if (!active) return;
                const existing = entries.find(item => item.kind === "dm" && item.uid === entry.uid);
                select({ kind: "dm", uid: entry.uid, name: entry.name || existing?.name || "" }, { focus: true });
            } });
        }),
        menuItem("dm.newGroup", async () => {
            const name = await promptDialog({ title: t("dm.newGroup"), label: t("dm.newGroupName"), serverScoped: true });
            if (name && name.trim() && active) await createPrivateGroup(name.trim());
        }),
    ]);
}

// ------------------------------------------------------------------ day separators

// Separators are kept in step with whichever thread is shown. The visible
// reading position is preserved, and a log scrolled to the bottom stays there.
function decorateDays(container, selector) {
    if (!container) return;
    const atBottom = container.scrollHeight - container.scrollTop - container.clientHeight < 8;
    const top = container.getBoundingClientRect().top;
    const rows = [...container.querySelectorAll(`:scope > ${selector}`)];
    const anchor = rows.find(row => row.getBoundingClientRect().bottom > top);
    const anchorOffset = anchor ? anchor.getBoundingClientRect().top - top : 0;
    const wanted = new Map(daySeparators(rows.map(row => Number(row.dataset.ts) || 0)).map(item => [rows[item.index], item]));
    for (const separator of container.querySelectorAll(":scope > .dm-day")) {
        const next = separator.nextElementSibling;
        if (!wanted.has(next) || wanted.get(next).key !== separator.dataset.day || wanted.get(next).label !== separator.textContent) separator.remove();
    }
    for (const [row, item] of wanted) {
        const previous = row.previousElementSibling;
        if (previous?.classList.contains("dm-day") && previous.dataset.day === item.key) continue;
        const separator = node("div", "dm-day");
        separator.dataset.day = item.key;
        separator.setAttribute("role", "separator");
        separator.append(node("span", "", item.label));
        row.before(separator);
    }
    if (atBottom) container.scrollTop = container.scrollHeight;
    else if (anchor?.isConnected) container.scrollTop += anchor.getBoundingClientRect().top - top - anchorOffset;
}

function decorateThreads() {
    if (!active || decorating) return;
    decorating = true;
    try {
        if (selected?.kind === "dm") decorateDays($("chat-log"), ".msg[data-ts]");
        else {
            decorateDays(document.querySelector("#private-groups .group-messages"), ".group-message");
            const input = document.querySelector("#private-groups .group-composer textarea");
            const name = selectedEntry()?.name;
            if (input && name) input.placeholder = t("dm.composer", { name });
        }
        if (selected?.kind === "group" && ui.threadSearchInput.value) applyThreadSearch();
    } finally {
        dayObserver?.takeRecords();
        decorating = false;
    }
}

function observeDays() {
    if (dayObserver) return;
    // #private-groups is created lazily, so the whole main area is observed and
    // only changes inside the two thread surfaces trigger a pass.
    dayObserver = new MutationObserver(records => {
        if (decorating || !records.some(record => record.target.id === "chat-log" || record.target.closest?.("#private-groups"))) return;
        queueMicrotask(decorateThreads);
    });
    dayObserver.observe($("center"), { childList: true, subtree: true });
    queueMicrotask(decorateThreads);
}

function stopObservingDays() {
    dayObserver?.disconnect();
    dayObserver = null;
    for (const separator of document.querySelectorAll("#center .dm-day")) separator.remove();
    for (const row of document.querySelectorAll(".dm-search-hidden")) row.classList.remove("dm-search-hidden");
}

// ------------------------------------------------------------------ composer extras

function pickGIF() {
    const input = $("chat-file");
    if (!input) return;
    const previous = input.accept;
    input.accept = "image/gif,.gif";
    const restore = () => { input.accept = previous; window.removeEventListener("focus", restore); };
    input.addEventListener("change", restore, { once: true });
    window.addEventListener("focus", restore);
    input.click();
}

// ------------------------------------------------------------------ keyboard

function listKeydown(event) {
    const buttons = [...ui.rows.querySelectorAll(".dm-row")];
    const index = buttons.indexOf(document.activeElement);
    if (index < 0 || !["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) return;
    event.preventDefault();
    const next = event.key === "Home" ? 0 : event.key === "End" ? buttons.length - 1
        : Math.max(0, Math.min(buttons.length - 1, index + (event.key === "ArrowDown" ? 1 : -1)));
    for (const button of buttons) button.tabIndex = -1;
    buttons[next].tabIndex = 0;
    buttons[next].focus();
}

function searchKeydown(event) {
    if (event.key === "ArrowDown") {
        event.preventDefault();
        ui.rows.querySelector(".dm-row")?.focus();
    } else if (event.key === "Enter") {
        event.preventDefault();
        ui.rows.querySelector(".dm-row")?.click();
    } else if (event.key === "Escape" && ui.searchInput.value) {
        event.preventDefault(); event.stopPropagation();
        ui.searchInput.value = ""; query = ""; renderList();
    }
}

// ------------------------------------------------------------------ DOM

function translate() {
    if (!ui) return;
    ui.title.textContent = t("dm.title");
    ui.list.setAttribute("aria-label", t("dm.title"));
    ui.rows.setAttribute("aria-label", t("dm.list"));
    ui.searchInput.placeholder = t("dm.search");
    ui.searchInput.setAttribute("aria-label", t("dm.search"));
    ui.threadSearchInput.placeholder = t("dm.searchLoaded");
    ui.threadSearchInput.setAttribute("aria-label", t("dm.searchLoaded"));
    for (const button of [ui.add, ui.channels, ui.back, ui.call, ui.video, ui.search, ui.more, ui.gif, ui.threadSearchClose]) {
        button.setAttribute("aria-label", t(button.dataset.label));
        button.title = t(button.dataset.label);
    }
    ui.gif.textContent = "GIF";
    scheduleRender();
}

function build() {
    const center = $("center");
    if (!center) return null;
    const list = node("nav", "dm-list");
    list.id = "dm-list";
    list.hidden = true;
    const head = node("div", "dm-list-head");
    const titleIcon = node("span", "dm-list-icon");
    titleIcon.innerHTML = icon("chat");
    const title = node("h2", "dm-list-title");
    title.id = "dm-list-title";
    const add = iconButton("plus", "dm.new", () => openNewMenu());
    add.setAttribute("aria-haspopup", "menu");
    add.setAttribute("aria-expanded", "false");
    // The channel sidebar collapses on narrow windows and the workspace's own
    // toggle lives in #chat-head, which this view hides. This button drives the
    // same state (body.channels-open) through that toggle, so Escape, the
    // sidebar's close button and a channel choice all close it as usual.
    const channels = iconButton("speaker", "workspace.labels.showChannels", () => {
        $("workspace-sidebar-toggle")?.click();
        channels.setAttribute("aria-expanded", String(document.body.classList.contains("channels-open")));
    });
    channels.classList.add("dm-show-channels");
    channels.setAttribute("aria-controls", "sidebar");
    channels.setAttribute("aria-expanded", "false");
    head.append(channels, titleIcon, title, add);
    const search = node("div", "dm-search");
    const searchIcon = node("span", "dm-search-icon");
    searchIcon.innerHTML = icon("search");
    const searchInput = node("input", "dm-search-input");
    searchInput.type = "search";
    searchInput.autocomplete = "off";
    searchInput.setAttribute("aria-controls", "dm-conversations");
    searchInput.oninput = () => { query = searchInput.value; renderList(); };
    searchInput.onkeydown = searchKeydown;
    search.append(searchIcon, searchInput);
    const rows = node("ul", "dm-rows");
    rows.id = "dm-conversations";
    rows.onkeydown = listKeydown;
    const empty = node("p", "dm-list-empty");
    empty.setAttribute("role", "status");
    list.append(head, search, rows, empty);

    const threadHead = node("header", "dm-thread-head");
    threadHead.id = "dm-thread-head";
    threadHead.hidden = true;
    const back = iconButton("back", "dm.back", () => { center.classList.remove("dm-thread-open"); ui.rows.querySelector('[aria-current="true"]')?.focus(); });
    back.classList.add("dm-back");
    const headAvatar = node("span", "dm-head-avatar");
    const headText = node("div", "dm-head-text");
    const headName = node("h2", "dm-head-name");
    headName.id = "dm-head-name";
    const headStatus = node("span", "dm-head-status");
    headText.append(headName, headStatus);
    const pill = node("button", "dm-e2e-pill");
    pill.type = "button";
    // A direct message opens the safety-number diagnostics; a group has no
    // per-peer key to verify, so its pill repeats what the encryption covers.
    pill.onclick = () => {
        if (selected?.kind === "dm") $("chat-e2ee-btn")?.click();
        else if (selected) V().toast(pill.title, "info");
    };
    const spacer = node("span", "dm-head-spacer");
    const call = iconButton("phone", "dm.call", () => {
        if (selected?.kind === "dm") void startPrivateCall(selected.uid, "");
        else if (selected) void startPrivateCall("", selected.id);
    });
    const video = iconButton("camera", "dm.video", () => {
        if (selected?.kind === "dm") void startPrivateVideoCall(selected.uid, "");
        else if (selected) void startPrivateVideoCall("", selected.id);
    });
    const searchButton = iconButton("search", "dm.searchThread", openSearch);
    const more = iconButton("more", "dm.more", openMoreMenu);
    more.setAttribute("aria-haspopup", "menu");
    more.setAttribute("aria-expanded", "false");
    const threadSearch = node("div", "dm-thread-search");
    threadSearch.hidden = true;
    const threadSearchInput = node("input", "");
    threadSearchInput.type = "search";
    threadSearchInput.autocomplete = "off";
    threadSearchInput.oninput = applyThreadSearch;
    threadSearchInput.onkeydown = event => { if (event.key === "Escape") { event.preventDefault(); event.stopPropagation(); closeThreadSearch(); ui.search.focus(); } };
    const threadSearchCount = node("span", "dm-thread-search-count");
    threadSearchCount.setAttribute("role", "status");
    const threadSearchClose = iconButton("close", "common.close", () => { closeThreadSearch(); ui.search.focus(); });
    threadSearch.append(threadSearchInput, threadSearchCount, threadSearchClose);
    threadHead.append(back, headAvatar, headText, pill, spacer, call, video, searchButton, more, threadSearch);

    const emptyThread = node("p", "dm-thread-empty");
    emptyThread.id = "dm-thread-empty";
    emptyThread.hidden = true;
    center.prepend(list, threadHead, emptyThread);

    const gif = node("button", "icon-btn dm-gif", "GIF");
    gif.type = "button";
    gif.id = "dm-gif";
    gif.dataset.label = "dm.gif";
    gif.onclick = pickGIF;
    $("chat-emoji")?.before(gif);

    return {
        list, title, add, channels, searchInput, rows, empty, head: threadHead, back, headAvatar, headName, headStatus, pill,
        call, video, search: searchButton, more, threadSearch, threadSearchInput, threadSearchCount, threadSearchClose, emptyThread, gif,
    };
}

// ------------------------------------------------------------------ open / close

export function openDirectMessages(target = null) {
    window.__noxaActivity?.close();
    if (!ui || !V()?.state.activeTabID) return false;
    ensureScope();
    const center = $("center");
    if (!active) {
        active = true;
        center.classList.add("dm-view-active");
        ui.list.hidden = false;
        if ($("files-pane") && !$("files-pane").hidden) $("tab-chat")?.click();
        window.dispatchEvent(new CustomEvent("noxa-direct-view", { detail: { open: true } }));
        refreshTimer = window.setInterval(() => { if (active && !document.hidden) { loadStored(); scheduleRender(); } }, 15000);
    }
    initConversations(); // the private-group list loads with the view, not only on connect
    loadStored(true);
    entries = collect();
    if (target?.uid) select({ kind: "dm", uid: target.uid, name: target.name || "" });
    else if (target?.groupID) select({ kind: "group", id: target.groupID });
    else if (!selected && isPrivateGroupActive() && viewedConversationID()) selected = { kind: "group", id: viewedConversationID() };
    else if (!selected && chat.activeDirectPeer()) selected = { kind: "dm", uid: chat.activeDirectPeer() };
    else if (selected?.kind === "dm" && chat.activeDirectPeer() !== selected.uid) select(selected);
    else if (selected?.kind === "group" && !isPrivateGroupActive()) select(selected);
    render();
    chat.refreshHeader?.();
    (ui.rows.querySelector('[aria-current="true"]') || ui.searchInput).focus({ preventScroll: true });
    return true;
}

export function closeDirectMessages() {
    if (!active) return;
    active = false;
    window.clearInterval(refreshTimer);
    closeContextMenu();
    closeThreadSearch();
    stopObservingDays();
    const center = $("center");
    center.classList.remove("dm-view-active", "dm-view-empty", "dm-thread-open");
    delete center.dataset.dmKind;
    ui.list.hidden = true; ui.head.hidden = true; ui.emptyThread.hidden = true;
    const typing = $("chat-typing");
    if (typing) { delete typing.dataset.initial; typing.style.removeProperty("--dm-typing-color"); }
    if (isPrivateGroupActive()) { closeConversations(); chat.resumeChatView(); }
    chat.refreshHeader?.();
    window.dispatchEvent(new CustomEvent("noxa-direct-view", { detail: { open: false } }));
}

export function isDirectMessagesOpen() { return active; }
export function directMessagesUnread() { return Math.max(0, lastUnread); }

export function initDirectMessages() {
    if (ui) return;
    ui = build();
    if (!ui) return;
    translate();
    Object.assign(V(), { openDirectMessages, closeDirectMessages, isDirectMessagesOpen, directMessagesUnread });
    window.addEventListener("noxa-direct-changed", scheduleRender);
    window.addEventListener("noxa-private-groups-changed", () => {
        // A group created or opened elsewhere (sidebar, notification) becomes
        // the selection while the view is open.
        if (active && isPrivateGroupActive() && viewedConversationID() && selectedKey() !== "grp:" + viewedConversationID()) selected = { kind: "group", id: viewedConversationID() };
        scheduleRender();
    });
    window.addEventListener("noxa-chat-view-changed", event => {
        if (!active) return;
        // Choosing a channel anywhere leaves the Messages view.
        if (event.detail?.kind !== "dm") { closeDirectMessages(); return; }
        if (event.detail.uid && selectedKey() !== "dm:" + event.detail.uid) selected = { kind: "dm", uid: event.detail.uid };
        scheduleRender();
    });
    window.addEventListener("noxa-language-changed", translate);
}
