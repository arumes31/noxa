import { t } from "./i18n.js";
import { confirmDialog, promptDialog } from "./modal.js";
import { startPrivateCall, showCallHistory } from "./private-calls.js";
import { sessionUserID } from "./session-identity.js";
import { initGroupSidebarLayout } from "./group-sidebar-layout.js";

const V = () => window.__noxa;
const app = () => window.go.main.App;
let refreshOpen = null;
let viewedGroup = null;
let workspace = null;
const notified = new Set();
export async function conversationChanged(event = {}) {
    initConversations();
    refreshOpen?.(event);
    if (!event.id || (!event.message_id && !event.invitation)) return;
    const state = V().state;
    const tabID = state.activeTabID, generation = state.serverGeneration;
    const key = `${tabID}:${generation}:${event.id}:${event.message_id || "invitation"}`;
    if (notified.has(key)) return;
    notified.add(key);
    if (notified.size > 512) notified.delete(notified.values().next().value);
    const muted = () => (V().state.settings?.muted_conversations || []).includes(event.id);
    const viewed = () => viewedGroup?.() === event.id && document.hasFocus();
    if (muted() || viewed()) return;
    try {
        // An invalidation carries no content. Recheck membership before naming
        // a group in a notification, including events queued before removal.
        const result = await app().ConversationForTab(tabID, { action: "get", id: event.id });
        if (state !== V().state || tabID !== state.activeTabID || generation !== state.serverGeneration || muted() || viewed()) return;
        const group = result.conversations.find(group => group.id === event.id);
        const member = group?.members.find(member => member.unique_id === sessionUserID(state));
        if (!member || (member.pending && !event.invitation) || (!member.pending && event.invitation)) return;
        if ((state.settings?.blocked_users || []).includes(group.owner) && member.pending) return;
        window.__noxaNotify?.notify("dm", t(event.invitation ? "group.invitedNotification" : "group.messageNotification", { name: group.name }));
    } catch { /* Removed groups and stale sessions do not produce alerts. */ }
}
function el(tag, className, text) {
    const node = document.createElement(tag); node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
}
function button(key, action) {
    const node = el("button", "", t(key)); node.type = "button"; node.onclick = action; return node;
}

export function openConversations() {
    initConversations();
    workspace?.open();
}
export function closeConversations() { workspace?.close(); }
export function isPrivateGroupActive() { return !!workspace?.active(); }
export function privateGroupViewToken() { return workspace?.token(); }
export function filterConversations() { workspace?.filter(); }
export function resetConversations() {
    workspace?.destroy(); workspace = null;
    refreshOpen = null; viewedGroup = null;
}

export function initConversations() {
    if (workspace?.current()) return;
    resetConversations();
    const tree = document.getElementById("channel-tree"), center = document.getElementById("center");
    if (!tree || !center || !V().state.activeTabID) return;
    const tabID = V().state.activeTabID;
    const serverGeneration = V().state.serverGeneration;
    const uid = () => sessionUserID(V().state);
    const panel = el("section", "group-workspace"); panel.id = "private-groups"; panel.hidden = true;
    panel.tabIndex = -1;
    panel.setAttribute("aria-label", t("group.title"));
    const navigation = el("div", "group-navigation");
    const back = button("group.back", () => { closeConversations(); window.__noxaChat?.resumeChatView(); });
    const channels = button("workspace.labels.showChannels", () => {
        document.getElementById("workspace-sidebar-toggle")?.click();
        channels.setAttribute("aria-expanded", String(document.body.classList.contains("channels-open")));
    });
    channels.className = "group-show-channels";
    channels.setAttribute("aria-controls", "sidebar"); channels.setAttribute("aria-expanded", "false");
    navigation.append(channels, back, button("call.history", showCallHistory));
    const sidebar = el("nav", "group-sidebar"); sidebar.setAttribute("aria-label", t("group.title"));
    sidebar.id = "private-groups-sidebar";
    const heading = el("div", "pane-head sidebar-head");
    const title = el("span", "", t("group.title"));
    const headingTitle = el("h2", ""); headingTitle.append(title); heading.append(headingTitle);
    const add = button("group.create", () => { const collapsed = sidebarBody.hidden; layout.expand(); create.hidden = collapsed ? false : !create.hidden; add.setAttribute("aria-expanded", String(!create.hidden)); if (!create.hidden) name.focus(); });
    add.className = "icon-btn pane-head-btn"; add.textContent = "+"; add.setAttribute("aria-label", t("group.create")); add.setAttribute("aria-expanded", "false");
    heading.append(add);
    const create = el("form", "group-create");
    create.hidden = true;
    const name = el("input", ""); name.maxLength = 80; name.required = true; name.placeholder = t("group.name"); name.setAttribute("aria-label", t("group.name"));
    const createButton = button("group.create"); createButton.type = "submit"; create.append(name, createButton);
    const list = el("div", "group-list");
    const sidebarBody = el("div", "group-sidebar-body"); sidebarBody.id = "private-groups-list";
    sidebarBody.append(create, list); sidebar.append(heading, sidebarBody);
    const content = el("section", "group-content");
    const status = el("p", "group-status"); status.setAttribute("role", "status");
    panel.append(navigation, content, status);
    const sidebarStatus = el("p", "group-sidebar-status"); sidebarStatus.setAttribute("role", "status"); sidebarBody.append(sidebarStatus);
    tree.after(sidebar); center.append(panel);
    const layout = initGroupSidebarLayout({ tree, body: sidebarBody, title });
    const summary = el("span", "group-summary"); summary.setAttribute("role", "status");
    const summaryUnread = el("span", "group-summary-unread");
    const summaryInvitations = el("span", "group-summary-invitations");
    summary.append(summaryUnread, summaryInvitations); add.before(summary);
    const searchEmpty = el("p", "group-search-empty"); searchEmpty.hidden = true; sidebarBody.append(searchEmpty);
    let selected = "";
    let generation = 0;
    let refreshPending = 0, sidebarRefreshQueued = false;
    let mutationPending = false;
    const drafts = new Map();
    const memberForms = new Map();
    const references = new Map();
    const reading = new Set();
    const queuedReads = new Map();
    let listedGroups = [];
    let hiddenSurfaces = null;
    let viewToken = null;
    const current = () => panel.isConnected && tabID === V().state.activeTabID && serverGeneration === V().state.serverGeneration;
    const visible = () => current() && !panel.hidden;
    const setVisible = show => {
        viewToken = show ? {} : null;
        if (show && !hiddenSurfaces) {
            hiddenSurfaces = new Map(["chat-head", "center-tabs", "voice-participants", "chat-pane", "files-pane"].map(id => document.getElementById(id)).filter(Boolean).map(element => [element, element.hidden]));
            for (const element of hiddenSurfaces.keys()) element.hidden = true;
        } else if (!show && hiddenSurfaces) {
            for (const [element, hidden] of hiddenSurfaces) element.hidden = hidden;
            hiddenSurfaces = null;
        }
        panel.hidden = !show; center.classList.toggle("private-group-active", show);
        for (const item of list.querySelectorAll(".group-list-item")) item.setAttribute("aria-current", String(show && item.dataset.groupId === selected));
    };
    const request = command => app().ConversationForTab(tabID, command);
    const label = memberID => V().state.clients.find(client => client.unique_id === memberID)?.nickname || memberID;
    const blocked = memberID => (V().state.settings?.blocked_users || []).includes(memberID);
    const filter = () => {
        const query = (V().state.treeFilter || "").trim().toLocaleLowerCase();
        let matches = 0;
        for (const item of list.children) {
            const group = listedGroups.find(group => group.id === item.dataset.groupId);
            const haystack = group ? [group.name, ...group.members.map(member => label(member.unique_id))].join(" ").toLocaleLowerCase() : "";
            item.hidden = !!query && !haystack.includes(query);
            if (!item.hidden) matches++;
        }
        searchEmpty.textContent = t("group.noMatches"); searchEmpty.hidden = !query || matches > 0;
    };
    const updateSummary = () => {
        let unread = 0, invitations = 0;
        for (const group of listedGroups) {
            if (group.members.find(member => member.unique_id === uid())?.pending) invitations++;
            else unread += group.unread_count || 0;
        }
        for (const [badge, count, key] of [[summaryUnread, unread, "group.unread"], [summaryInvitations, invitations, "group.invitations"]]) {
            badge.textContent = count > 99 ? "99+" : String(count); badge.hidden = !count;
            badge.setAttribute("aria-label", t(key, { count })); badge.title = t(key, { count });
        }
    };
    const markRead = async (group, messageID, token) => {
        if (!messageID || messageID <= (group.read_message_id || 0)
            || !visible() || selected !== group.id || token !== generation || document.hidden || !document.hasFocus()) return;
        const previous = queuedReads.get(group.id);
        if (!previous || previous.messageID < messageID || previous.token !== token) queuedReads.set(group.id, { messageID, token });
        if (reading.has(group.id)) return;
        reading.add(group.id);
        try {
            while (queuedReads.has(group.id)) {
                const next = queuedReads.get(group.id); queuedReads.delete(group.id);
                if (!visible() || selected !== group.id || next.token !== generation || document.hidden || !document.hasFocus()) break;
                await request({ action: "mark_read", id: group.id, read_message_id: next.messageID });
                if (!current()) break;
            }
            if (current()) void refresh(true);
        } catch (error) { fail(error); }
        finally { reading.delete(group.id); queuedReads.delete(group.id); }
    };
    const fail = error => { if (current()) (visible() ? status : sidebarStatus).textContent = t("group.failed", { error: String(error) }); };
    const saveDraft = () => {
        const input = content.querySelector(".group-composer textarea");
        if (input?.dataset.groupId) drafts.set(input.dataset.groupId, input.value);
        const details = content.querySelector(".group-members");
        if (details && content.dataset.groupId) memberForms.set(content.dataset.groupId, {
            open: details.open, target: details.querySelector(".group-invite input")?.value || "",
        });
    };
    const refresh = async (sidebarOnly = false) => {
        if (!current()) return;
        if (mutationPending) return;
        if (sidebarOnly && refreshPending) { sidebarRefreshQueued = true; return; }
        if (sidebarOnly) sidebarRefreshQueued = false;
        refreshPending++;
        const token = sidebarOnly ? generation : ++generation;
        try {
            const result = await request({ action: "list" });
            if (!current() || token !== generation) return;
            const focusedGroup = list.contains(document.activeElement) ? document.activeElement.dataset.groupId : null;
            list.replaceChildren();
            const groups = result.conversations.filter(group => !group.members.find(member => member.unique_id === uid())?.pending || !blocked(group.owner));
            listedGroups = groups; updateSummary();
            sidebarStatus.textContent = groups.length ? "" : t("group.noGroups");
            for (const group of groups) {
                const pending = group.members.find(member => member.unique_id === uid())?.pending;
                const item = el("button", "group-list-item", `${group.name}${pending ? ` · ${t("group.invitation")}` : ""}`);
                item.dataset.groupId = group.id;
                if (!pending && group.active_call_count > 0) {
                    const activity = el("span", "group-call-status", t("group.callStatus", { count: group.call_participant_count }));
                    item.append(activity);
                }
                if (!pending && group.unread_count > 0) {
                    const badge = el("span", "group-unread", group.unread_count > 99 ? "99+" : String(group.unread_count));
                    badge.setAttribute("aria-label", t("group.unread", { count: group.unread_count })); item.append(badge);
                }
                item.setAttribute("aria-current", String(visible() && group.id === selected));
                item.onclick = () => {
                    saveDraft(); selected = group.id; setVisible(true);
                    document.body.classList.remove("channels-open");
                    document.getElementById("workspace-sidebar-toggle")?.setAttribute("aria-expanded", "false");
                    channels.setAttribute("aria-expanded", "false");
                    if (window.innerWidth <= 720) panel.focus({ preventScroll: true });
                    void refresh();
                };
                list.append(item);
            }
            filter();
            if (focusedGroup && document.activeElement === document.body) [...list.children].find(item => item.dataset.groupId === focusedGroup)?.focus({ preventScroll: true });
            if (sidebarOnly) return;
            const group = groups.find(group => group.id === selected);
            if (!group) { saveDraft(); selected = ""; content.replaceChildren(el("p", "group-empty", t("group.empty"))); return; }
            const activeInput = document.activeElement;
            const selector = activeInput?.matches(".group-composer textarea") ? ".group-composer textarea" : ".group-invite input";
            const selection = content.contains(activeInput) && activeInput?.matches(selector) && content.dataset.groupId === group.id
                ? [activeInput.selectionStart, activeInput.selectionEnd, activeInput.selectionDirection] : null;
            saveDraft();
            await showGroup(group, token);
            if (selection && visible() && token === generation && document.activeElement === document.body) {
                const input = content.querySelector(selector);
                input?.focus({ preventScroll: true }); input?.setSelectionRange(...selection);
            }
        } catch (error) { if (token === generation) fail(error); }
        finally { refreshPending--; if (!refreshPending && sidebarRefreshQueued && current()) void refresh(true); }
    };
    const mutate = async (group, action, target = "", extra = {}) => {
        if (!current() || mutationPending) return;
        mutationPending = true; ++generation; status.textContent = t("group.loading");
        const controls = new Map([...content.querySelectorAll("button"), createButton].map(node => [node, node.disabled]));
        for (const node of controls.keys()) node.disabled = true;
        try {
            const result = await request({ action, id: group?.id || "", revision: group?.revision || 0, target, ...extra });
            if (!current()) return;
            if (action === "create") { selected = result.conversations[0]?.id || ""; name.value = ""; create.hidden = true; add.setAttribute("aria-expanded", "false"); setVisible(true); }
            if (action === "invite" && content.dataset.groupId === group.id) {
                const input = content.querySelector(".group-invite input");
                if (input?.value.trim() === target) input.value = "";
            }
            if (action === "leave" || action === "decline") selected = "";
            status.textContent = "";
        } catch (error) { fail(error); }
        finally {
            mutationPending = false;
            if (current()) await refresh();
            for (const [node, disabled] of controls) if (node.isConnected) node.disabled = disabled;
        }
    };
    const confirmChange = async (group, action, target, key) => {
        if (await confirmDialog({ title: t(key, { name: label(target) }), serverScoped: true })) {
            if (current() && selected === group.id) void mutate(group, action, target);
        }
    };
    const showGroup = async (group, token) => {
        const previousMessages = content.dataset.groupId === group.id ? content.querySelector(".group-messages") : null;
        const preserveScroll = previousMessages && previousMessages.scrollHeight - previousMessages.scrollTop - previousMessages.clientHeight > 24;
        const previousScrollTop = previousMessages?.scrollTop || 0;
        content.dataset.groupId = group.id;
        const me = group.members.find(member => member.unique_id === uid());
        const owner = group.owner === uid();
        const top = el("header", "group-header"); top.append(el("h3", "", group.name), button("group.refresh", () => void refresh()));
        content.replaceChildren(top, el("p", "group-privacy", t("group.private")));
        if (me?.pending) {
            const actions = el("div", "group-actions");
            actions.append(button("group.accept", () => mutate(group, "accept")), button("group.decline", () => mutate(group, "decline")));
            content.append(actions); return;
        }
        top.append(button("call.group", () => { void startPrivateCall("", group.id); }));
        const muteLabel = el("label", "group-mute");
        const mute = el("input", ""); mute.type = "checkbox";
        mute.checked = (V().state.settings?.muted_conversations || []).includes(group.id);
        mute.onchange = async () => {
            mute.disabled = true;
            const snapshot = structuredClone(V().state.settings);
            const ids = new Set(snapshot.muted_conversations || []);
            if (mute.checked) ids.add(group.id); else ids.delete(group.id);
            snapshot.muted_conversations = [...ids];
            try {
                const error = await app().SaveSettings(snapshot);
                if (!current()) return;
                if (error) throw new Error(error);
                const settings = await app().GetSettings();
                if (current()) V().state.settings = settings;
            } catch (error) { if (current()) { mute.checked = !mute.checked; fail(error); } }
            finally { mute.disabled = false; }
        };
        muteLabel.append(mute, document.createTextNode(t("group.mute"))); content.append(muteLabel);
        const details = el("details", "group-members"); details.append(el("summary", "", `${t("group.members")} · ${group.members.length}/16`));
        details.open = memberForms.get(group.id)?.open || false;
        for (const member of group.members) {
            const row = el("div", "group-member");
            row.append(el("span", "", `${label(member.unique_id)}${member.pending ? ` · ${t("group.pending")}` : member.unique_id === group.owner ? ` · ${t("group.owner")}` : ""}`));
            if (owner && member.unique_id !== uid()) {
                row.append(button("group.remove", () => confirmChange(group, "remove", member.unique_id, "group.confirmRemove")));
                if (!member.pending) row.append(button("group.transfer", () => confirmChange(group, "transfer", member.unique_id, "group.confirmTransfer")));
            }
            details.append(row);
        }
        if (owner) {
            const invite = el("form", "group-invite");
            const target = el("input", ""); target.required = true; target.maxLength = 128; target.placeholder = t("group.target"); target.setAttribute("aria-label", t("group.target"));
            target.value = memberForms.get(group.id)?.target || "";
            const suggestions = el("datalist", ""); suggestions.id = "group-invite-identities"; target.setAttribute("list", suggestions.id);
            for (const client of V().state.clients.filter(client => client.unique_id && !blocked(client.unique_id) && !group.members.some(member => member.unique_id === client.unique_id))) {
                const option = el("option", "", client.nickname); option.value = client.unique_id; suggestions.append(option);
            }
            const add = button("group.invite"); add.type = "submit"; add.disabled = group.members.length >= 16;
            invite.append(target, suggestions, add); invite.onsubmit = event => { event.preventDefault(); void mutate(group, "invite", target.value.trim()); }; details.append(invite);
            details.append(button("group.rename", async () => {
                const value = await promptDialog({ title: t("group.rename"), label: t("group.name"), value: group.name, serverScoped: true });
                if (value !== null && current() && selected === group.id) void mutate(group, "rename", "", { name: value });
            }));
        }
        const leave = button("group.leave", () => confirmChange(group, "leave", uid(), "group.confirmLeave"));
        leave.disabled = owner && group.members.length > 1; if (leave.disabled) leave.title = t("group.leaveOwner"); details.append(leave);
        content.append(details);
        const messages = el("div", "group-messages"); messages.setAttribute("aria-label", t("group.message"));
        const older = button("group.older"); older.disabled = true;
        content.append(older, messages);
        const composer = el("form", "group-composer");
        const input = el("textarea", ""); input.rows = 2; input.maxLength = 8192; input.required = true; input.value = drafts.get(group.id) || ""; input.setAttribute("aria-label", t("group.message"));
        input.dataset.groupId = group.id;
        input.oninput = () => drafts.set(group.id, input.value);
        const send = button("group.send"); send.type = "submit"; composer.append(input, send); content.append(composer);
        composer.onsubmit = async event => {
            event.preventDefault();
            if (mutationPending || !input.value.trim() || !current()) return;
            const body = input.value;
            let pending = references.get(group.id);
            if (!pending || pending.body !== body) { pending = { reference: crypto.randomUUID(), body }; references.set(group.id, pending); }
            mutationPending = true; send.disabled = true;
            try {
                await app().SendConversationForTab(tabID, group.id, body, pending.reference);
                if (!current()) return;
                if (drafts.get(group.id) === body) drafts.delete(group.id);
                if (input.value === body) input.value = "";
                references.delete(group.id); status.textContent = t("group.sent");
            } catch (error) { fail(error); }
            finally { mutationPending = false; send.disabled = false; if (current()) void refresh(); }
        };
        let before = 0;
        let loading = false;
        let latestRendered = 0;
        messages.onscroll = () => {
            if (!loading && messages.scrollHeight - messages.scrollTop - messages.clientHeight <= 24) void markRead(group, latestRendered, token);
        };
        const history = async () => {
            if (loading || !current() || token !== generation) return;
            loading = true; older.disabled = true;
            try {
                const result = await request({ action: "history", id: group.id, before_id: before });
                if (!current() || token !== generation) return;
                const fragment = document.createDocumentFragment();
                for (const message of [...result.messages].reverse()) {
                    const row = el("article", "group-message");
                    row.append(el("strong", "", label(message.from_unique_id)), el("time", "", new Date(message.created_at * 1000).toLocaleString()), el("p", "", blocked(message.from_unique_id) ? t("group.blocked") : message.body));
                    fragment.append(row);
                }
                const initial = !before;
                latestRendered = Math.max(latestRendered, ...result.messages.map(message => message.id));
                messages.prepend(fragment);
                if (result.messages.length) before = Math.min(...result.messages.map(message => message.id));
                else if (initial) messages.append(el("p", "group-empty", t("group.noMessages")));
                older.disabled = result.messages.length < 25;
                if (initial) {
                    messages.scrollTop = preserveScroll ? previousScrollTop : messages.scrollHeight;
                    if (!preserveScroll) void markRead(group, latestRendered, token);
                }
            } catch (error) { if (token === generation) fail(error); }
            finally { loading = false; }
        };
        older.onclick = history;
        await history();
    };
    create.onsubmit = event => { event.preventDefault(); void mutate(null, "create", "", { name: name.value.trim() }); };
    const refreshHandler = () => {
        if (!current()) return;
        saveDraft(); void refresh();
    };
    refreshOpen = refreshHandler;
    const viewed = () => visible() ? selected : "";
    viewedGroup = viewed;
    const languageChanged = () => {
        sidebar.setAttribute("aria-label", t("group.title")); panel.setAttribute("aria-label", t("group.title"));
        layout.translate(); add.setAttribute("aria-label", t("group.create"));
        createButton.textContent = t("group.create"); name.placeholder = t("group.name"); name.setAttribute("aria-label", t("group.name"));
        back.textContent = t("group.back"); channels.textContent = t("workspace.labels.showChannels");
        navigation.lastChild.textContent = t("call.history"); refreshHandler();
    };
    window.addEventListener("noxa-language-changed", languageChanged);
    const focusChanged = () => { if (!document.hidden && document.hasFocus()) refreshHandler(); };
    window.addEventListener("focus", focusChanged); document.addEventListener("visibilitychange", focusChanged);
    const sidebarPoll = window.setInterval(() => { if (current() && !document.hidden) void refresh(true); }, 15000);
    workspace = {
        current, filter, active: visible, token: () => visible() ? viewToken : null,
        open: () => { setVisible(true); layout.expand(); create.hidden = false; add.setAttribute("aria-expanded", "true"); name.focus(); },
        close: () => { saveDraft(); setVisible(false); },
        destroy: () => { ++generation; window.clearInterval(sidebarPoll); window.removeEventListener("noxa-language-changed", languageChanged); window.removeEventListener("focus", focusChanged); document.removeEventListener("visibilitychange", focusChanged); setVisible(false); layout.destroy(); sidebar.remove(); panel.remove(); drafts.clear(); memberForms.clear(); references.clear(); },
    };
    void refresh();
}
