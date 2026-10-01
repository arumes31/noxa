import { t } from "./i18n.js";
import { closeDialog, isCurrentServerDialog, mountServerDialog } from "./modal.js";
import { openConversationsAt } from "./conversations.js";
import { openDiscussions } from "./discussions.js";
import { sessionUserID } from "./session-identity.js";
import { parseFileRef } from "./chat-parsers.js";
import "./message-tools.css";

const V = () => window.__noxa;
const app = () => window.go.main.App;
let hooks = {};
const el = (tag, className = "", text) => { const node = document.createElement(tag); node.className = className; if (text !== undefined) node.textContent = text; return node; };
const button = (key, action) => { const node = el("button", "ui-button", t(key)); node.type = "button"; node.onclick = action; return node; };
const field = (key, control) => { const label = el("label", "message-tools-field", t(key)); control.setAttribute("aria-label", t(key)); label.append(control); return label; };
const input = (type = "text") => { const node = el("input"); node.type = type; return node; };
const channelName = id => V().state.channels?.find(channel => Number(channel.ChannelID) === Number(id))?.Name || (Number(id) === 0 ? "Global" : `#${id}`);
const referenceName = ref => ref.kind === "dm" ? (V().state.clients?.find(client => client.unique_id === ref.peer_id)?.nickname || ref.peer_id) : ref.kind === "group" ? ref.group_id : ref.kind === "thread" ? `${channelName(ref.channel_id)} / ${ref.thread_id}` : channelName(ref.channel_id);
const snippet = body => String(body || "").replace(/\[file:([^\]]+)\]/g, (_, capture) => { const ref = parseFileRef(capture); return ref.valid ? `📎 ${ref.name}` : "📎"; }).slice(0, 500);

function dialog(title) {
    const tabID = V().state.activeTabID, generation = V().state.serverGeneration, identityUID = V().state.myUniqueID;
    const overlay = el("div", "dlg-overlay"), box = el("section", "dlg message-tools-dialog");
    const heading = el("div", "message-tools-heading"); heading.append(el("h2", "", t(title)), button("messages.close", () => closeDialog(overlay)));
    const controls = el("div", "message-tools-controls"), list = el("div", "message-tools-list"), status = el("p", "message-tool-status"); status.setAttribute("role", "status");
    box.append(heading, controls, status, list); overlay.append(box); mountServerDialog(overlay);
    const current = () => isCurrentServerDialog(overlay) && V().state.activeTabID === tabID && V().state.serverGeneration === generation && V().state.myUniqueID === identityUID;
    return { overlay, box, controls, list, status, current, tabID };
}
function resultRow(dlg, ref, text, meta = "") {
    const row = el("div", "message-tool-result");
    const jump = el("button", "", text); jump.type = "button";
    if (meta) jump.append(el("small", "", meta));
    jump.onclick = async () => {
        if (!dlg.current()) return;
        closeDialog(dlg.overlay);
        try {
            if (ref.kind === "thread") openDiscussions(ref.channel_id, null, ref.thread_id, ref.message_id || 0);
            else if (ref.kind === "group") await openConversationsAt(ref.group_id, ref.message_id || 0);
            else await hooks.jump?.(ref);
        } catch (error) { V().toast(t("messages.failed", { error: String(error) }), "warn"); }
    };
    row.append(jump); dlg.list.append(row); return row;
}

export async function saveMessageReference(reference, dmOwner = null) {
    const tabID = V().state.activeTabID, generation = V().state.serverGeneration;
    // Explicit allowlist: no body, file token, or sealed attachment key can be persisted.
    const ref = { kind: reference.kind, message_id: Number(reference.message_id), collection: "" };
    if (reference.kind === "dm") { ref.peer_id = reference.peer_id; ref.client_message_id = reference.client_message_id || ""; ref.local_seq = Number(reference.local_seq) || 0; ref.message_id = 0; }
    else if (reference.kind === "group") ref.group_id = reference.group_id;
    else { ref.channel_id = Number(reference.channel_id); if (reference.kind === "thread") ref.thread_id = Number(reference.thread_id); }
    try {
        await app().SavedMessagesForTab(tabID, { action: "save", reference: ref, ...(dmOwner ? { dm_owner: dmOwner } : {}) });
        if (V().state.activeTabID === tabID && V().state.serverGeneration === generation) V().toast(t("messages.savedToast"));
    } catch (error) { if (V().state.activeTabID === tabID && V().state.serverGeneration === generation) V().toast(t("messages.failed", { error: String(error) }), "warn"); }
}

export function openSavedMessages() {
    const dlg = dialog("messages.saved"); dlg.box.insertBefore(el("p", "message-tools-hint", t("messages.savedHint")), dlg.controls);
    const collection = el("select"); dlg.controls.append(field("messages.collection", collection));
    // Capture once, before any later identity switch or delayed menu action.
    const dmOwner = Promise.resolve(app().DMHistoryContextForTab?.(dlg.tabID)).then(value => value || null, () => null);
    let refs = [];
    const render = () => {
        const selected = collection.value; collection.replaceChildren(new Option(t("messages.allCollections"), "*"));
        for (const name of [...new Set(refs.map(ref => ref.collection || ""))].sort()) collection.add(new Option(name || t("messages.unfiled"), name));
        collection.value = [...collection.options].some(option => option.value === selected) ? selected : "*";
        dlg.list.replaceChildren();
        for (const ref of refs.filter(ref => collection.value === "*" || (ref.collection || "") === collection.value)) {
            const row = resultRow(dlg, ref, t("messages.reference", { name: referenceName(ref), id: ref.kind === "dm" ? ref.local_seq || ref.client_message_id : ref.message_id }));
            const target = input(); target.maxLength = 60; target.value = ref.collection || "";
            row.append(field("messages.collection", target), button("messages.move", () => mutate("move", { ...ref, collection: target.value.trim() })), button("messages.remove", () => mutate("remove", ref)));
        }
        dlg.status.textContent = refs.length ? "" : t("messages.empty");
    };
    const mutate = async (action, reference) => {
        if (!dlg.current()) return;
        const controls = [...dlg.controls.querySelectorAll("button,input,select"), ...dlg.list.querySelectorAll("button,input,select")]; controls.forEach(control => { control.disabled = true; });
        try { const owner = reference.kind === "dm" ? await dmOwner : null; if (!dlg.current()) return; const result = await app().SavedMessagesForTab(dlg.tabID, { action, reference, ...(owner ? { dm_owner: owner } : {}) }); if (dlg.current()) { refs = result.references || []; render(); } }
        catch (error) { if (dlg.current()) dlg.status.textContent = t("messages.failed", { error: String(error) }); }
        finally { controls.forEach(control => { control.disabled = false; }); }
    };
    collection.onchange = render; void mutate("list", {});
}

export function openUnreadInbox() {
    const dlg = dialog("messages.inbox"); dlg.box.insertBefore(el("p", "message-tools-hint", t("messages.inboxHint")), dlg.controls);
    const mentions = input("checkbox"), label = el("label", "message-tools-check", t("messages.mentions")); label.prepend(mentions);
    let sequence = 0, rows = [];
    const render = () => { dlg.list.replaceChildren(); for (const entry of rows.filter(entry => !mentions.checked || entry.mention)) resultRow(dlg, entry, `${entry.name} · ${entry.count || "•"}${entry.mention ? " · @" : ""}`); };
    const refresh = async () => {
        const token = ++sequence; rows = hooks.unread?.() || []; render(); dlg.status.textContent = t("messages.loading");
        let failed = false;
        try {
            const result = await app().ConversationForTab(dlg.tabID, { action: "list" });
            if (!dlg.current() || token !== sequence) return;
            for (const group of result.conversations || []) if (group.unread_count > 0 && group.members.some(member => member.unique_id === sessionUserID(V().state) && !member.pending)) rows.push({ kind: "group", group_id: group.id, message_id: group.latest_message_id, name: group.name, count: group.unread_count });
        } catch { failed = true; }
        for (const channel of V().state.channels || []) {
            if (!dlg.current() || token !== sequence) return;
            try {
                for (const archived of [false, true]) {
                    let before = 0, beforePinned = false;
                    for (let page = 0; page < 10; page++) {
                        const result = await app().DiscussionForTab(dlg.tabID, { action: "list", channel_id: Number(channel.ChannelID), subscribed: true, archived, before_id: before, before_pinned: beforePinned });
                        if (!dlg.current() || token !== sequence) return;
                        for (const thread of result.threads || []) if (thread.joined && thread.subscribed && thread.unread) rows.push({ kind: "thread", channel_id: Number(channel.ChannelID), thread_id: thread.id, name: `${channel.Name} / ${thread.title}`, count: 1 });
                        if (!result.has_more || !result.threads?.length) break;
                        const last = result.threads.at(-1); if (before === last.id && beforePinned === Boolean(last.pinned)) { failed = true; break; } before = last.id; beforePinned = Boolean(last.pinned);
                        if (page === 9) failed = true;
                    }
                }
            } catch { failed = true; }
        }
        if (!dlg.current() || token !== sequence) return;
        render(); dlg.status.textContent = failed ? t("messages.inboxPartial") : rows.length ? "" : t("messages.empty");
    };
    mentions.onchange = render; dlg.controls.append(label, button("messages.refresh", refresh)); void refresh();
}

export function openHistorySearch() {
    const dlg = dialog("messages.search"); dlg.box.insertBefore(el("p", "message-tools-hint", t("messages.searchHint")), dlg.controls);
    const query = input(), sender = input(), after = input("date"), before = input("date"), attachment = input("checkbox"), channel = el("select"), thread = el("select");
    query.maxLength = 1024; sender.maxLength = 128;
    channel.add(new Option(t("messages.allChannels"), "all"));
    for (const item of V().state.channels || []) channel.add(new Option(item.Name, String(item.ChannelID)));
    channel.add(new Option("Global", "0")); thread.add(new Option(t("messages.channelChat"), "0")); thread.disabled = true;
    const checkbox = el("label", "message-tools-check", t("messages.attachment")); checkbox.prepend(attachment);
    dlg.controls.append(field("messages.text", query), field("messages.sender", sender), field("messages.channel", channel), field("messages.thread", thread), field("messages.after", after), field("messages.before", before), checkbox);
    const actions = el("div", "message-tools-controls"); dlg.controls.after(actions);
    const form = el("form", "message-tools-form"); dlg.controls.before(form); form.append(dlg.controls, actions);
    let operation = null, threadSequence = 0, threadCursor = null;
    const moreThreads = button("messages.moreThreads", () => loadThreads(false)); moreThreads.hidden = true; dlg.controls.append(moreThreads);
    const loadThreads = async reset => {
        if (reset) { ++threadSequence; thread.replaceChildren(new Option(t("messages.channelChat"), "0")); thread.disabled = true; threadCursor = { before: 0, pinned: false, archived: false }; }
        moreThreads.hidden = true;
        if (!(Number(channel.value) > 0) || !threadCursor) return;
        const sequence = threadSequence, id = Number(channel.value), cursor = threadCursor;
        try {
            const result = await app().DiscussionForTab(dlg.tabID, { action: "list", channel_id: id, before_id: cursor.before, before_pinned: cursor.pinned, archived: cursor.archived });
            if (!dlg.current() || sequence !== threadSequence) return;
            for (const item of result.threads || []) if (![...thread.options].some(option => option.value === String(item.id))) thread.add(new Option(item.title, String(item.id)));
            thread.disabled = false;
            if (result.has_more && result.threads?.length) { const last = result.threads.at(-1); threadCursor = { before: last.id, pinned: Boolean(last.pinned), archived: cursor.archived }; }
            else threadCursor = cursor.archived ? null : { before: 0, pinned: false, archived: true };
            moreThreads.hidden = !threadCursor;
        } catch (error) { if (dlg.current() && sequence === threadSequence) { dlg.status.textContent = t("messages.failed", { error: String(error) }); moreThreads.hidden = false; } }
    };
    channel.onchange = () => loadThreads(true);
    const run = button("messages.run"), stop = button("messages.stop", () => { if (operation) operation.cancelled = true; }), more = button("messages.continue", () => scan(true));
    run.type = "submit";
    form.onsubmit = event => { event.preventDefault(); void scan(false); };
    for (const control of [after, before]) control.addEventListener("input", () => {
        if (!after.hasAttribute("aria-invalid")) return;
        after.removeAttribute("aria-invalid"); before.removeAttribute("aria-invalid"); dlg.status.textContent = "";
    });
    stop.disabled = true; more.hidden = true; actions.append(run, stop, more);
    const scan = async resume => {
        if (!dlg.current() || run.disabled) return;
        if (!resume) {
            const start = after.value ? Math.floor(new Date(`${after.value}T00:00:00`).getTime() / 1000) : 0;
            const end = before.value ? Math.floor(new Date(`${before.value}T23:59:59`).getTime() / 1000) : 0;
            if (start && end && start > end) {
                after.setAttribute("aria-invalid", "true"); before.setAttribute("aria-invalid", "true");
                dlg.status.textContent = t("messages.invalidDates"); after.focus(); return;
            }
            const channels = channel.value === "all" ? [...(V().state.channels || []).map(item => Number(item.ChannelID)), 0] : [Number(channel.value)];
            operation = { channels, index: 0, cursor: 0, scanned: 0, missing: 0, errors: [], filter: { query: query.value.trim(), sender: sender.value.trim(), after: start, before: end, has_attachment: attachment.checked, thread_id: Number(thread.value) }, cancelled: false };
            dlg.list.replaceChildren();
        }
        const current = operation; current.cancelled = false; const budget = current.scanned + 2000;
        run.disabled = true; stop.disabled = false; more.hidden = true;
        for (const control of dlg.controls.querySelectorAll("input,select")) control.disabled = true;
        try {
            while (dlg.current() && operation === current && !current.cancelled && current.index < current.channels.length && current.scanned < budget) {
                const id = current.channels[current.index];
                try {
                    const result = await app().HistorySearchPageForTab(dlg.tabID, { ...current.filter, channel_id: id, before_id: current.cursor });
                    if (!dlg.current() || operation !== current) return;
                    current.scanned += result.scanned || 0; current.missing += result.undecryptable || 0;
                    for (const message of result.messages || []) resultRow(dlg, { kind: current.filter.thread_id ? "thread" : "channel", channel_id: id, thread_id: current.filter.thread_id, message_id: message.id }, snippet(message.body), `${channelName(id)} · ${message.from_nickname || message.from_unique_id || "?"} · ${new Date(message.sent_at * 1000).toLocaleString()}`);
                    if (result.complete) { current.index++; current.cursor = 0; }
                    else if (!(result.next_before_id > 0) || current.cursor && result.next_before_id >= current.cursor) throw new Error("History cursor did not advance");
                    else current.cursor = result.next_before_id;
                } catch (error) { current.errors.push(`${channelName(id)}: ${String(error)}`); current.index++; current.cursor = 0; }
                dlg.status.textContent = t("messages.scan", { count: current.scanned, missing: current.missing });
            }
        } finally {
            if (dlg.current() && operation === current) {
                run.disabled = false; stop.disabled = true; more.hidden = current.index >= current.channels.length;
                for (const control of dlg.controls.querySelectorAll("input,select")) control.disabled = false;
                thread.disabled = !(Number(channel.value) > 0);
                dlg.status.textContent = t("messages.scan", { count: current.scanned, missing: current.missing }) + "\n" + t(current.errors.length ? "messages.inboxPartial" : more.hidden ? "messages.finished" : "messages.partial") + (current.errors.length ? "\n" + current.errors.join("\n") : "");
            }
        }
    };
}

export function initMessageTools(callbacks) {
    hooks = callbacks;
    window.__noxaMessageTools = { save: saveMessageReference, inbox: openUnreadInbox, search: openHistorySearch, saved: openSavedMessages };
    if (document.getElementById("message-tools-actions")) return;
    const actions = el("div", "message-tools-actions"); actions.id = "message-tools-actions";
    const entries = [["messages.inbox", openUnreadInbox], ["messages.search", openHistorySearch], ["messages.saved", openSavedMessages]];
    for (const [key, action] of entries) actions.append(button(key, action));
    document.getElementById("chat-head")?.append(actions);
    window.addEventListener("noxa-language-changed", () => [...actions.children].forEach((node, index) => { node.textContent = t(entries[index][0]); }));
}
