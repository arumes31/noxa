// saved.js — Saved messages (#/saved): references kept with app.saved
// (core/saved.js), one tab per collection. Each reference is resolved by
// re-reading its conversation (channel history, thread history, group
// pages, the local direct-message store); what can no longer be read shows
// as unavailable. Items open at the message, move between collections or go.
import { app } from "../app.js";
import { referenceMessageID, resolveAll } from "../core/saved-resolve.js";
import { excerpt } from "../core/search.js";
import { t } from "../i18n.js";
import { clock, h, sheet, toast } from "./dom.js";
import { header, scopeName } from "./common.js";
import { jumpAfterOpen } from "./chat-search.js";
import { discussionsFor } from "./chat-tools.js";
import { personName } from "./tool-sources.js";
import "./chat-tools-strings.js";
import "./chat-tools.css";

const PAGES = 20;

/** Lookups for core/saved-resolve.js against the signed-in session. */
export function savedSources(session = app.session) {
    const channelMessage = m => m && { id: m.id, from: m.from, text: m.deleted ? null : m.text, at: m.sentAt, deleted: m.deleted };
    return {
        async channel(channelID, id) {
            return channelMessage(session.messageByID(channelID, id) || await session.loadUntil(channelID, id, PAGES));
        },
        async thread(channelID, threadID, id) {
            const discussions = discussionsFor(session);
            const find = () => discussions.messages.get(threadID)?.find(m => m.id === id);
            for (let page = 0; page < PAGES && !find(); page++) {
                if (!await discussions.history(channelID, threadID)) break;
            }
            return channelMessage(find());
        },
        async group(groupID, id) {
            const find = () => app.groups?.messages.get(groupID)?.find(m => m.id === id);
            for (let page = 0; page < PAGES && !find() && app.groups; page++) if (!await app.groups.loadOlder(groupID)) break;
            const m = find();
            return m && { id: m.id, from: personName(session, m.from_unique_id), text: m.deleted ? null : m.text, at: m.created_at, deleted: m.deleted };
        },
        async dm(peer, id) {
            await app.dm?.ready;
            const m = app.dm?.conversations.get(peer)?.messages.find(message => message.id === id);
            return m && { id: m.id, from: m.self ? session.nickname : (m.fromNickname || app.dm.conversations.get(peer)?.nickname || t("dm.someone")), text: m.deleted ? null : m.body, at: m.sentAt, deleted: m.deleted };
        },
    };
}

function placeName(session, ref) {
    if (ref.kind === "dm") return app.dm?.conversations.get(ref.peer)?.nickname || session.clients().find(c => c.unique_id === ref.peer)?.nickname || t("dm.someone");
    if (ref.kind === "group") return app.groups?.list.get(ref.groupID)?.name || t("group.title");
    const channel = scopeName(session, ref.channelID);
    if (ref.kind !== "thread") return channel;
    const thread = discussionsFor(session).channels.get(ref.channelID)?.threads.find(th => th.id === ref.threadID);
    return `${channel} › ${thread?.title || t("threads.thread")}`;
}

const collectionLabel = name => name || t("saved.default");

export function renderSaved(root) {
    const session = app.session;
    const saved = app.saved;
    const tabs = h("div", { class: "tabs", role: "tablist", "aria-label": t("saved.collections") });
    const list = h("ul", { class: "list", role: "tabpanel" });
    const status = h("p", { class: "tool-progress", role: "status" });
    let active = "";
    const resolved = new Map(); // reference key -> resolution

    const keyOf = ref => JSON.stringify([ref.kind, ref.channelID, ref.threadID, ref.groupID, ref.peer, referenceMessageID(ref)]);

    function renderTabs() {
        const names = saved.collections();
        if (!names.includes(active)) active = "";
        tabs.replaceChildren(...names.map(name => h("button", { class: "tab", type: "button", role: "tab", "aria-selected": String(name === active),
            onclick: () => { active = name; render(); } }, `${collectionLabel(name)} (${saved.list(name).length})`)));
    }

    async function move(ref) {
        const names = saved.collections().filter(name => name !== (ref.collection || ""));
        const choice = await sheet(t("saved.moveTo"), null, [
            ...names.map(name => ({ label: collectionLabel(name), value: { name } })),
            { label: t("saved.newCollection"), value: { create: true }, primary: true },
            { label: t("link.cancel"), value: null },
        ]);
        if (!choice) return;
        let name = choice.name;
        if (choice.create) {
            name = await askName();
            if (!name) return;
        }
        await saved.move(ref, name);
        toast(t("saved.moved", { collection: collectionLabel(name) }));
    }

    function askName() {
        return new Promise(resolve => {
            const input = h("input", { class: "input", id: "saved-collection", maxlength: "40", required: true, autocomplete: "off" });
            const form = h("form", { class: "sheet tool-form", role: "dialog", "aria-modal": "true", "aria-label": t("saved.newCollection") },
                h("h2", { class: "sheet-title" }, t("saved.newCollection")),
                h("div", { class: "field" }, h("label", { for: "saved-collection" }, t("saved.collectionName")), input),
                h("div", { class: "sheet-actions" }, h("button", { class: "btn btn-primary", type: "submit" }, t("saved.create")),
                    h("button", { class: "btn", type: "button", onclick: () => done("") }, t("link.cancel"))));
            const overlay = h("div", { class: "sheet-overlay" }, form);
            const done = value => { overlay.remove(); resolve(value); };
            form.onsubmit = event => { event.preventDefault(); done(input.value.trim()); };
            document.body.append(overlay);
            input.focus();
        });
    }

    function render() {
        renderTabs();
        const refs = saved.list(active);
        list.setAttribute("aria-label", collectionLabel(active));
        list.replaceChildren(...refs.map(ref => {
            const result = resolved.get(keyOf(ref));
            const message = result?.message;
            const open = () => {
                const id = referenceMessageID(ref);
                if (ref.kind !== "thread") jumpAfterOpen(result?.tag || "", id);
                location.hash = result?.href || "#/saved";
            };
            return h("li", { class: "card tool-group" },
                h("div", { class: "tool-group-head" }, h("h2", {}, placeName(session, ref)),
                    message ? h("span", { class: "row-meta" }, `${new Date(message.at * 1000).toLocaleDateString()} ${clock(message.at)}`) : null),
                h("p", { class: `hint${message ? "" : " unavailable"}`, style: "padding:0 4px" },
                    !result ? t("saved.loading") : message ? h("span", {}, h("strong", {}, `${message.from}: `), excerpt(message.text, "", 120)) : t("saved.unavailable")),
                h("div", { class: "tool-actions", style: "margin-top:8px" },
                    result?.message ? h("button", { class: "btn btn-primary", type: "button", onclick: open }, t("saved.open")) : null,
                    h("button", { class: "btn", type: "button", onclick: () => move(ref) }, t("saved.move")),
                    h("button", { class: "btn btn-danger", type: "button", onclick: () => saved.remove(ref) }, t("saved.remove"))));
        }));
        if (!refs.length) list.replaceChildren(h("li", { class: "hint" }, t("saved.empty")));
    }

    let resolving = false;
    async function resolveVisible() {
        if (resolving) return;
        const refs = saved.list(null).filter(ref => !resolved.has(keyOf(ref)));
        if (!refs.length) return;
        status.textContent = t("saved.resolving", { count: refs.length });
        resolving = true;
        const results = await resolveAll(refs, savedSources(session)).finally(() => { resolving = false; });
        if (!list.isConnected) return;
        for (const result of results) resolved.set(keyOf(result.reference), result);
        status.textContent = "";
        render();
        resolveVisible();
    }

    const onChange = () => { render(); resolveVisible(); };
    saved.addEventListener("change", onChange);
    root.replaceChildren(h("section", { class: "screen chat-tools" },
        header(t("saved.title"), { back: "#/home" }),
        h("div", { class: "screen-body" }, tabs, status, list)));
    saved.ready.then(() => { render(); resolveVisible(); });
    render();
    return () => saved.removeEventListener("change", onChange);
}
