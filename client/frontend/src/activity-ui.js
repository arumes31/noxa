import { mountSnoozeControls } from "./notification-snooze.js";
import { currentLanguage, t } from "./i18n.js";
import { parseRuntimeObject } from "./runtime-json.js";
import { icon } from "./icons.js";
import { createSafeImage } from "./safe-media.js";
import { previewLine } from "./dm-model.js";
import { memberRoles, roleChip } from "./role-presentation.js";
import { sessionUserID } from "./session-identity.js";
import { activityMessages, bindSharedLinks, jumpMessageReference, openPM, renderActivityMessage } from "./chat-ui.js";
import { startPrivateCall } from "./private-calls.js";
import { youtubeVideo } from "./youtube-previews.js";
import { xPost } from "./x-previews.js";
import { videoPortal, portalThumbnail } from "./video-portal-previews.js";
import { loadPreviewMetadata } from "./preview-metadata.js";
import { mountActivityAudit } from "./activity-audit.js";
import { avatarColor } from "./workspace-ui.js";
import "./activity.css";

const state = () => window.__noxa.state;
const el = (tag, cls = "", text = "") => {
    const node = document.createElement(tag); node.className = cls; node.textContent = text; return node;
};
const button = (label, action, cls = "activity-button", glyph = "") => {
    const node = el("button", cls); node.type = "button";
    if (glyph) { const symbol = el("span"); symbol.innerHTML = icon(glyph); node.append(symbol); }
    node.append(document.createTextNode(label)); node.onclick = action; return node;
};
const scope = () => JSON.stringify([state().activeTabID, state().serverGeneration, state().sessionGeneration, state().myUniqueID]);
const clock = at => new Date(at).toLocaleTimeString(currentLanguage(), { hour: "2-digit", minute: "2-digit", hourCycle: "h23" });

function day(at) {
    const now = new Date(), yesterday = new Date(now.getFullYear(), now.getMonth(), now.getDate() - 1);
    const date = new Date(at).toDateString();
    return date === now.toDateString() ? "today" : date === yesterday.toDateString() ? "yesterday" : "earlier";
}

function avatar(uid, name) {
    const node = el("span", "activity-avatar", (name || "?").slice(0, 1).toLocaleUpperCase());
    const image = createSafeImage(state().avatars?.get(uid), { avatar: true, alt: "" });
    if (image) node.replaceChildren(image);
    else { node.classList.add("activity-avatar-initial"); node.style.setProperty("--avatar-color", avatarColor(uid || name)); }
    return node;
}

function media(text) {
    for (const url of String(text).match(/https?:\/\/[^\s<>\])]+/g) || []) {
        const yt = youtubeVideo(url);
        if (yt) return { ...yt, provider: "youtube", label: "YouTube" };
        const post = xPost(url);
        if (post) return { ...post, provider: "x", label: "X" };
        const portal = videoPortal(url);
        if (portal) return portal;
    }
    return null;
}

// Fixed local glyphs only; server text never becomes SVG markup.
function platformIcon(provider) {
    const paths = {
        youtube: '<rect x="1" y="4" width="30" height="24" rx="7" fill="#ff0033"/><path d="m13 10 10 6-10 6Z" fill="white"/>',
        twitch: '<rect width="32" height="32" rx="7" fill="#9146ff"/><path d="M7 5h21v15l-7 7h-6l-4 4v-4H5V9Zm3 3v15h6v4l4-4h4V8Z" fill="white"/><path d="M15 11v7m6-7v7" stroke="white" stroke-width="2"/>',
        x: '<rect width="32" height="32" rx="5" fill="#050709"/><path d="M7 5h5l14 22h-5ZM25 5 7 27" stroke="white" stroke-width="2" fill="none"/>',
        whatnot: '<rect width="32" height="32" rx="5" fill="#070909"/><path d="M3 13c0-6 7-7 10-2 3-6 9-6 12-1 7-2 8 5 3 10l-5 5-7-5-5 5-6-6Z" fill="#ffe937"/>',
    };
    const node = el("span", `activity-platform ${provider || "generic"}`);
    node.setAttribute("aria-hidden", "true");
    node.innerHTML = paths[provider] ? `<svg viewBox="0 0 32 32">${paths[provider]}</svg>` : icon("chat");
    return node;
}

function previewText(node, message) {
    // previewLine redacts attachment secrets; text nodes keep mentions inert.
    for (const part of previewLine(message).slice(0, 240).split(/(@[^\s@]+)/u)) {
        node.append(part.startsWith("@") ? el("span", "activity-mention", part) : document.createTextNode(part));
    }
}

export function initActivityWorkspace(notifications) {
    let root = null, panel, content, sidebar, nav, filters, search, currentTab = "activity", filter = "all", query = "";
    let openedScope = "", revision = 0, stopAudit = null, stopSnooze = null, refreshQueued = false;
    let calls = [], callsLoaded = false, callsRequest = null, callsRevision = 0;
    const current = captured => !!root?.isConnected && captured === openedScope && captured === scope();
    const close = () => {
        revision++; callsRevision++; calls = []; callsLoaded = false; callsRequest = null;
        stopAudit?.(); stopAudit = null;
        stopSnooze?.(); stopSnooze = null;
        root?.remove(); root = null;
        document.getElementById("center")?.classList.remove("activity-open");
        document.getElementById("notif-bell")?.setAttribute("aria-expanded", "false");
    };
    const openReference = reference => {
        const captured = scope(); close();
        void jumpMessageReference(reference, { continueConversation: true, isCurrent: () => captured === scope() })
            .catch(() => { if (captured === scope()) window.__noxa.toast(t("activity.failed"), "warn"); });
    };
    function thumbnail(row, preview) {
        if (!preview || state().settings?.link_previews === false) return;
        const captured = openedScope;
        const valid = () => row.isConnected && current(captured) && state().settings?.link_previews !== false;
        const attach = url => {
            if (!url || !valid()) return;
            const image = el("img", "activity-thumbnail"); image.alt = ""; image.loading = "lazy"; image.referrerPolicy = "no-referrer";
            image.onerror = () => image.remove(); image.src = url; row.append(image);
        };
        queueMicrotask(() => {
            if (!valid()) return;
            if (preview.thumbnail) attach(preview.thumbnail);
            else if (!["youtube", "x"].includes(preview.provider)) {
                const owner = { tabID: state().activeTabID, generation: state().serverGeneration };
                void loadPreviewMetadata(`portal:${preview.provider}`, preview.id, owner, valid,
                    () => window.go.main.App.VideoPortalPreviewForTab(owner.tabID, preview.provider, preview.id))
                    .then(result => attach(portalThumbnail(preview.provider, result?.thumbnail)));
            }
        });
    }
    const notifyRow = n => {
        const row = button("", () => { close(); void notifications.open(n); }, "activity-notification");
        row.disabled = !n.destination;
        row.dataset.activityKey = `notification:${n.at}:${n.kind}`;
        row.classList.toggle("unread", !n.read);
        const reference = n.destination?.reference;
        const entry = activityMessages().find(e => reference && e.reference.kind === reference.kind && (reference.kind === "channel"
            ? e.reference.channel_id === reference.channel_id && e.reference.message_id === reference.message_id
            : e.reference.peer_id === reference.peer_id && !!reference.client_message_id && e.reference.client_message_id === reference.client_message_id));
        const uid = n.uid || entry?.message.fromUID || reference?.peer_id;
        const name = state().clients.find(c => c.unique_id === uid)?.nickname || entry?.message.from || "";
        const body = el("span", "activity-notification-body"), heading = el("span", "activity-notification-heading");
        if (name) heading.append(el("strong", "", name));
        heading.append(el("time", "activity-time", clock(n.at)));
        body.append(heading, el("span", "activity-notification-text", n.text));
        const preview = media(entry?.message.text || n.text);
        const face = preview ? platformIcon(preview.provider) : avatar(uid, name);
        if (preview) {
            const context = el("span", "activity-context", `${preview.label} · ${entry?.name || ""}`);
            body.append(context);
            thumbnail(row, preview);
        }
        row.prepend(face, body); return row;
    };
    function renderSidebar() {
        const scroll = sidebar.scrollTop;
        const focused = sidebar.contains(document.activeElement) ? document.activeElement.dataset.activityKey : "";
        const snoozeOpen = sidebar.querySelector(".activity-snooze")?.open || false;
        stopSnooze?.();
        sidebar.replaceChildren();
        const heading = el("header", "activity-section-heading");
        const markRead = button(t("activity.markRead"), notifications.markRead, "activity-button activity-mark-read", "check"); markRead.dataset.activityKey = "mark-read";
        heading.append(el("h3", "", t("activity.notifications")), markRead);
        const snooze = el("details", "activity-snooze"), snoozeToggle = el("summary", "activity-link", t("activity.snooze"));
        snooze.open = snoozeOpen; snoozeToggle.dataset.activityKey = "snooze";
        snooze.append(snoozeToggle); stopSnooze = mountSnoozeControls(snooze);
        sidebar.append(heading, snooze);
        const entries = notifications.entries();
        for (const group of ["today", "yesterday", "earlier"]) {
            const values = entries.filter(n => day(n.at) === group);
            if (!values.length) continue;
            sidebar.append(el("h4", "activity-day", t(`activity.${group}`)));
            for (const n of values) sidebar.append(notifyRow(n));
        }
        if (!entries.length) sidebar.append(el("p", "activity-empty", t("activity.empty")));
        sidebar.scrollTop = scroll;
        if (focused) [...sidebar.querySelectorAll("[data-activity-key]")].find(n => n.dataset.activityKey === focused)?.focus({ preventScroll: true });
    }
    function renderFriends() {
        const section = el("section", "activity-friends");
        section.setAttribute("aria-label", t("activity.friends"));
        const strip = el("div", "activity-friends-strip");
        const blocked = new Set(state().settings?.blocked_users || []);
        for (const friend of (state().settings?.contacts || []).filter(f => !blocked.has(f.unique_id))) {
            const peer = state().clients.find(c => c.unique_id === friend.unique_id);
            const name = friend.label || peer?.nickname || friend.unique_id;
            const room = state().channels.find(c => c.ChannelID === peer?.channel_id);
            const status = !peer ? "offline" : peer.sharing ? "streaming" : peer.status === "away" ? "away" : room && room.SystemKind !== "home" ? "inVoice" : "online";
            const tile = button("", () => { close(); openPM(friend.unique_id, name); }, "activity-friend");
            tile.dataset.status = status; tile.dataset.activityKey = `friend:${friend.unique_id}`;
            const face = avatar(friend.unique_id, name), presence = el("span", "activity-presence");
            if (status === "streaming") presence.textContent = "LIVE";
            if (status === "inVoice") presence.innerHTML = icon("headphones");
            face.append(presence);
            tile.append(face, el("strong", "", name), el("small", "", t(`activity.${status}`)));
            strip.append(tile);
        }
        const add = button(t("activity.addFriends"), () => window.__noxaSocial?.openContacts(), "activity-friend activity-add-friend", "plus");
        strip.append(add); section.append(strip);
        if (!state().settings?.contacts?.length) section.append(el("p", "activity-empty", t("activity.noFriends")));
        return section;
    }
    function messageRow(entry, compact = false) {
        const m = entry.message, card = el("article", compact ? "activity-message activity-feed-row" : "activity-message");
        card.dataset.activityKey = entry.key;
        if (m.mentioned) card.classList.add("mentioned");
        const header = el("header");
        const face = avatar(m.fromUID, m.from);
        if (!compact) header.append(face);
        header.append(el("strong", "", m.from));
        const role = memberRoles(state(), m.fromUID)[0];
        if (role) header.append(roleChip(role));
        header.append(el("time", "activity-time", clock(m.ts)));
        const body = el("div", "activity-message-body");
        if (compact) previewText(body, m);
        else { renderActivityMessage(body, entry); bindSharedLinks(body); }
        const footer = el("footer");
        const action = button(t(m.mentioned || entry.reference.kind === "dm" || m.replyToID ? "activity.reply" : "activity.openChat"), () => openReference(entry.reference), "activity-button activity-row-action", "reply");
        const context = el("span", "activity-context", `${entry.reference.kind === "dm" ? "@" : "#"} ${entry.name}`);
        if (compact) {
            const copy = el("div", "activity-row-copy"); copy.append(header, body, context);
            const actions = el("div", "activity-row-actions"); actions.append(action);
            if (!m.mentioned) thumbnail(actions, media(m.text));
            card.append(face, copy, actions);
        } else {
            footer.append(context, action); card.append(header, body, footer);
        }
        return card;
    }
    function renderFeed() {
        content.replaceChildren();
        if (currentTab === "activity") content.append(renderFriends());
        const entries = activityMessages();
        const visible = entries.filter(entry => {
            const m = entry.message;
            if (query && !`${m.from} ${m.text} ${entry.name}`.toLocaleLowerCase().includes(query.toLocaleLowerCase())) return false;
            if (currentTab === "media") return !!media(m.text) && (filter === "all" || media(m.text).provider === filter);
            return filter === "all" || filter === "mentions" && m.mentioned || filter === "dm" && entry.reference.kind === "dm"
                || filter === "replies" && !!m.replyToID;
        });
        if (currentTab === "media") {
            content.append(el("p", "activity-empty", t("activity.mediaHint")));
            const grid = el("section", "activity-media-grid"); grid.setAttribute("aria-label", t("activity.media"));
            for (const entry of visible.slice(0, 48)) grid.append(messageRow(entry));
            if (!grid.childElementCount) grid.append(el("p", "activity-empty", t("activity.empty")));
            content.append(grid); return;
        }
        if (["all", "mentions"].includes(filter)) {
            const mentions = el("section", "activity-mentions");
            mentions.append(el("h3", "activity-section-heading", t("activity.yourMentions")));
            for (const entry of visible.filter(e => e.message.mentioned).slice(0, 10)) mentions.append(messageRow(entry, true));
            if (mentions.childElementCount === 1) mentions.append(el("p", "activity-empty", t("activity.empty")));
            content.append(mentions);
        }
        if (filter === "mentions") return;
        content.append(el("h3", "activity-section-heading", t("activity.recent")));
        const list = el("section", "activity-feed"); list.setAttribute("aria-label", t("activity.recent"));
        const rows = visible.filter(e => !e.message.mentioned || filter !== "all").slice(0, 50).map(entry => ({ at: entry.message.ts, node: messageRow(entry, true) }));
        const categories = { joins: ["join_leave", "buddy_online"], streams: ["stream", "stream_started"], invitations: ["invite", "invitation", "poke"] };
        for (const n of notifications.entries().filter(n => (filter === "all" || categories[filter]?.includes(n.kind)) && (!query || n.text.toLocaleLowerCase().includes(query.toLocaleLowerCase())))) {
            // Loaded chat messages already have their own richer row above.
            if (filter === "all" && ["mention", "dm", "channel_message", "keyword"].includes(n.kind)) continue;
            rows.push({ at: n.at, node: notifyRow(n) });
        }
        if (["all", "invitations"].includes(filter)) {
            for (const call of calls) {
                const row = callRow(call, openedScope);
                if (!query || row.textContent.toLocaleLowerCase().includes(query.toLocaleLowerCase())) rows.push({ at: call.created_at * 1000, node: row });
            }
        }
        for (const row of rows.sort((a, b) => b.at - a.at)) list.append(row.node);
        if (!list.childElementCount) list.append(el("p", "activity-empty", t("activity.empty")));
        content.append(list);
        if (filter === "all") renderRooms();
    }
    function renderRooms() {
        const rooms = state().channels.filter(c => c.SystemKind !== "home" && !c.TextOnly && state().clients.some(p => p.channel_id === c.ChannelID));
        if (!rooms.length) return;
        const section = el("section", "activity-rooms"); section.append(el("h3", "", t("activity.liveRooms")));
        for (const room of rooms.slice(0, 8)) {
            const peers = state().clients.filter(p => p.channel_id === room.ChannelID);
            const row = el("div", "activity-room");
            row.append(el("strong", "", room.Name), el("small", "", t("activity.voiceCount", { count: peers.length })));
            for (const peer of peers.slice(0, 4)) row.append(avatar(peer.unique_id, peer.nickname));
            row.append(button(t("activity.viewRoom"), () => openReference({ kind: "channel", channel_id: room.ChannelID }), "activity-link"));
            section.append(row);
        }
        content.append(section);
    }
    function callRow(call, captured) {
        const me = sessionUserID(state()), peers = (call.participants || []).filter(p => p.unique_id !== me);
        const missed = call.participants?.find(p => p.unique_id === me)?.state === "missed";
        const names = peers.map(p => state().clients.find(c => c.unique_id === p.unique_id)?.nickname || state().settings?.contacts?.find(c => c.unique_id === p.unique_id)?.label || p.unique_id).join(", ");
        const row = el("article", "activity-call"); row.dataset.activityKey = `call:${call.id}`; row.classList.toggle("missed", missed);
        const symbol = el("span", "activity-call-icon"); symbol.innerHTML = icon(missed ? "phoneOff" : "phone");
        const info = el("div", "activity-call-info"); info.append(el("strong", "", names), el("span", "", t(missed ? "activity.missed" : call.ended_at ? "activity.completed" : "activity.active")));
        const heading = el("header", "activity-call-heading");
        heading.append(el("strong", "", names), el("time", "activity-time", clock(call.created_at * 1000)));
        info.firstChild.replaceWith(heading);
        row.append(avatar(peers[0]?.unique_id, names), info);
        info.append(symbol);
        if (call.conversation_id || peers.length === 1) {
            const again = button(t(missed ? "activity.callBack" : "activity.callAgain"), async () => {
                if (!current(captured)) return;
                again.disabled = true;
                try { await startPrivateCall(call.conversation_id ? "" : peers[0].unique_id, call.conversation_id || ""); }
                finally { if (again.isConnected) again.disabled = false; }
            }, "activity-button activity-row-action", "phone");
            row.append(again);
        }
        if (peers.length === 1 && currentTab === "calls") row.append(button(t("activity.message"), () => { close(); openPM(peers[0].unique_id, names); }, "activity-button", "chat"));
        return row;
    }
    function loadCalls(force = false) {
        if (callsRequest) return callsRequest;
        if (callsLoaded && !force) return Promise.resolve();
        const captured = openedScope, token = ++callsRevision, tabID = state().activeTabID;
        callsRequest = Promise.resolve().then(() => window.go.main.App.PrivateCallForTab(tabID, { action: "history" })).then(result => {
            if (!current(captured) || token !== callsRevision) return;
            const me = sessionUserID(state());
            calls = (result.history || []).filter(call => call.participants?.some(p => p.unique_id === me)).sort((a, b) => b.created_at - a.created_at).slice(0, 100);
            callsLoaded = true;
        }).finally(() => { if (token === callsRevision) callsRequest = null; });
        return callsRequest;
    }
    async function renderCalls() {
        const captured = openedScope, token = ++revision;
        content.replaceChildren(el("p", "activity-empty", t("activity.loading")));
        try {
            await loadCalls();
            if (!current(captured) || token !== revision || currentTab !== "calls") return;
            content.replaceChildren();
            let previousDay = "";
            for (const call of calls) {
                const row = callRow(call, captured);
                if (query && !row.textContent.toLocaleLowerCase().includes(query.toLocaleLowerCase())) continue;
                const group = day(call.created_at * 1000);
                if (group !== previousDay) { content.append(el("h4", "activity-day", t(`activity.${group}`))); previousDay = group; }
                content.append(row);
            }
            if (!content.childElementCount) content.append(el("p", "activity-empty", t("activity.empty")));
        } catch { if (current(captured) && token === revision) content.replaceChildren(el("p", "activity-empty", t("activity.failed"))); }
    }
    function select(tab) {
        if (!root || tab === "audit" && !state().canViewAuditLog) return;
        revision++; stopAudit?.(); stopAudit = null;
        currentTab = tab; filter = "all"; query = ""; search.value = "";
        root.dataset.view = tab;
        for (const node of nav.querySelectorAll("[role=tab]")) {
            node.setAttribute("aria-selected", String(node.dataset.tab === tab)); node.tabIndex = node.dataset.tab === tab ? 0 : -1;
        }
        panel.setAttribute("aria-labelledby", `activity-tab-${tab}`);
        filters.replaceChildren();
        const choices = tab === "activity" ? ["all", "mentions", "dm", "replies", "joins", "streams", "invitations"] : tab === "media" ? ["all", "youtube", "twitch", "x", "whatnot", "vimeo", "dailymotion"] : [];
        for (const key of choices) {
            const label = ["youtube", "twitch", "x", "whatnot", "vimeo", "dailymotion"].includes(key) ? { youtube: "YouTube", twitch: "Twitch", x: "X", whatnot: "Whatnot", vimeo: "Vimeo", dailymotion: "Dailymotion" }[key] : t(`activity.${key}`);
            const item = button(label, () => {
                filter = key;
                for (const b of filters.children) b.setAttribute("aria-pressed", String(b === item));
                renderFeed();
            });
            item.setAttribute("aria-pressed", String(key === "all")); filters.append(item);
        }
        filters.hidden = !choices.length; search.hidden = tab === "audit";
        search.closest("details").hidden = tab === "audit";
        if (tab === "audit") {
            const captured = openedScope;
            stopAudit = mountActivityAudit(content, () => current(captured) && currentTab === "audit" && state().canViewAuditLog);
        } else if (tab === "calls") void renderCalls();
        else {
            renderFeed();
            if (tab === "activity") {
                const captured = openedScope;
                void loadCalls().then(() => { if (current(captured) && currentTab === "activity") renderFeed(); }).catch(() => {});
            }
        }
    }
    function refresh() {
        if (!root) return;
        if (!current(openedScope)) { close(); return; }
        const audit = nav.querySelector('[data-tab="audit"]'), auditFocused = document.activeElement === audit;
        audit.hidden = !state().canViewAuditLog;
        if (currentTab === "audit" && !state().canViewAuditLog) {
            select("activity");
            if (auditFocused) nav.querySelector('[aria-selected="true"]')?.focus();
        }
        renderSidebar();
        if (["activity", "media"].includes(currentTab)) {
            const scroll = content.scrollTop, focusedKey = document.activeElement?.closest("[data-activity-key]")?.dataset.activityKey;
            renderFeed(); content.scrollTop = scroll;
            if (focusedKey) {
                const item = [...content.querySelectorAll("[data-activity-key]")].find(n => n.dataset.activityKey === focusedKey);
                (item?.matches("button") ? item : item?.querySelector("button"))?.focus({ preventScroll: true });
            }
        }
    }
    function queueRefresh() {
        if (refreshQueued || !root) return;
        refreshQueued = true;
        queueMicrotask(() => { refreshQueued = false; refresh(); });
    }
    function open(tab = "activity") {
        if (!state().myClientID || tab === "audit" && !state().canViewAuditLog) return;
        if (root && current(openedScope)) { select(tab); return; }
        close(); openedScope = scope();
        const center = document.getElementById("center");
        root = el("section", "activity-workspace"); root.id = "activity-workspace";
        const header = el("header", "activity-header"), heading = el("h2", "", t("activity.title"));
        const titleIcon = el("span", "activity-title-icon"); titleIcon.innerHTML = icon("activity"); heading.prepend(titleIcon);
        nav = el("div", "activity-tabs"); nav.setAttribute("role", "tablist"); nav.setAttribute("aria-label", t("activity.title"));
        for (const [key, glyph] of [["activity", "activity"], ["media", "image"], ["calls", "phone"], ["audit", "shield"]]) {
            const item = button(t(key === "activity" ? "activity.title" : `activity.${key}`), () => select(key), "activity-tab", glyph);
            item.dataset.tab = key; item.id = `activity-tab-${key}`; item.setAttribute("role", "tab"); item.setAttribute("aria-controls", "activity-panel");
            item.hidden = key === "audit" && !state().canViewAuditLog; nav.append(item);
        }
        nav.onkeydown = event => {
            if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
            const tabs = [...nav.children].filter(n => !n.hidden), index = tabs.indexOf(document.activeElement);
            if (index < 0) return;
            event.preventDefault();
            const next = event.key === "Home" ? 0 : event.key === "End" ? tabs.length - 1 : (index + (event.key === "ArrowRight" ? 1 : -1) + tabs.length) % tabs.length;
            tabs[next].click(); tabs[next].focus();
        };
        const exit = button("", () => { close(); document.getElementById("notif-bell")?.focus(); }, "activity-close", "close"); exit.setAttribute("aria-label", t("common.close"));
        header.append(heading, nav, exit);
        const toolbar = el("div", "activity-toolbar"); filters = el("div", "activity-filters");
        search = el("input", "activity-search"); search.type = "search"; search.placeholder = t("activity.search"); search.setAttribute("aria-label", t("activity.search"));
        search.oninput = () => { query = search.value; if (currentTab === "calls") void renderCalls(); else renderFeed(); };
        const searchTools = el("details", "activity-search-tools"), searchToggle = el("summary");
        searchToggle.innerHTML = icon("search"); searchToggle.setAttribute("aria-label", t("activity.search")); searchToggle.title = t("activity.search");
        searchTools.append(searchToggle, search);
        const reload = button("", () => { callsLoaded = false; select(currentTab); }, "activity-link activity-refresh", "refresh");
        reload.setAttribute("aria-label", t("activity.refresh")); reload.title = t("activity.refresh");
        toolbar.append(filters, searchTools, reload);
        const layout = el("div", "activity-layout"); panel = el("div", "activity-panel"); panel.id = "activity-panel"; panel.setAttribute("role", "tabpanel");
        content = el("div", "activity-content"); panel.append(content);
        sidebar = el("aside", "activity-notifications"); sidebar.setAttribute("aria-label", t("activity.notifications"));
        layout.append(panel, sidebar); root.append(header, toolbar, layout); center.append(root); center.classList.add("activity-open");
        document.getElementById("notif-bell")?.setAttribute("aria-expanded", "true");
        select(tab); renderSidebar(); nav.querySelector('[aria-selected="true"]')?.focus();
    }
    notifications.subscribe(queueRefresh);
    window.addEventListener("noxa-audit-access-changed", refresh);
    window.addEventListener("noxa-language-changed", () => { if (root) { const tab = currentTab; close(); open(tab); } });
    for (const event of ["tab_reset", "disconnected", "dm_history_identity_changed", "tab_identity"]) window.runtime.EventsOn(event, close);
    for (const event of ["snapshot", "subscriptions", "settings_update", "avatar"]) window.runtime.EventsOn(event, queueRefresh);
    window.runtime.EventsOn("event", json => {
        const event = parseRuntimeObject(json);
        if (event?.type === "private_call" && root) {
            callsLoaded = false;
            const captured = openedScope;
            void loadCalls(true).then(() => {
                if (!current(captured)) return;
                if (currentTab === "calls") void renderCalls();
                else if (currentTab === "activity") renderFeed();
            }).catch(() => {});
        }
        if (["chat", "chat_edited", "chat_deleted", "user_joined", "user_left", "user_moved", "user_updated", "client_updated", "screenshare_changed", "avatar_changed", "client_moved", "status_changed"].includes(event?.type)) queueRefresh();
    });
    document.addEventListener("keydown", event => { if (root && event.key === "Escape" && !document.querySelector(".dlg-overlay")) { close(); document.getElementById("notif-bell")?.focus(); } });
    window.__noxaActivity = { open, close };
}
