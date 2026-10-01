import { t } from "./i18n.js";
import { closeDialog, isCurrentServerDialog, mountServerDialog } from "./modal.js";
import { parsePoll, validatePoll, togglePollChoice } from "./poll-state.js";

const V = () => window.__noxa;
const app = () => window.go.main.App;
const refreshers = new WeakMap();
function node(tag, className, text) {
    const el = document.createElement(tag);
    el.className = className;
    if (text !== undefined) el.textContent = text;
    return el;
}

export function refreshPolls(event) {
    for (const card of document.querySelectorAll(".chat-poll")) {
        if (card.dataset.pollId === String(event.message_id)) refreshers.get(card)?.();
    }
}

export function renderPoll(container, message) {
    const definition = !message.direct && message.id > 0 ? parsePoll(message.text) : null;
    if (!definition) return false;
    const { activeTabID: tabID, serverGeneration: generation } = V().state;
    const card = node("section", "chat-poll");
    card.dataset.pollId = String(message.id);
    card.setAttribute("aria-label", t("poll.label"));
    const question = node("strong", "poll-question", definition.question);
    const hint = node("span", "poll-hint", t(definition.multiple ? "poll.multiple" : "poll.single"));
    const options = node("div", "poll-options");
    const status = node("p", "poll-status", t("poll.loading"));
    status.setAttribute("role", "status");
    const actions = node("div", "poll-actions");
    const reload = node("button", "", t("poll.refresh"));
    const close = node("button", "", t("poll.close"));
    // The server checks authorship/ManageMessages. Keeping this discoverable
    // also lets moderators close another author's poll without a roster leak.
    actions.append(reload, close);
    card.append(question, hint, options, status, actions);
    container.replaceChildren(card);
    let saved = null;
    let busy = false;
    let refreshPending = false;
    const current = () => card.isConnected && V().state.activeTabID === tabID && V().state.serverGeneration === generation;
    const renderOptions = () => {
        options.replaceChildren();
        definition.options.forEach((label, index) => {
            const count = saved?.counts[index] || 0;
            const button = node("button", "poll-option");
            button.setAttribute("aria-pressed", String(saved?.choices.includes(index) || false));
            button.disabled = busy || !saved || saved.closed || Date.now() >= definition.closes_at * 1000;
            button.style.setProperty("--poll-percent", `${saved?.total_voters ? Math.round(count * 100 / saved.total_voters) : 0}%`);
            button.append(node("span", "", label), node("span", "poll-count", saved ? String(count) : "—"));
            button.onclick = () => request("vote", togglePollChoice(saved.choices, index, definition.multiple));
            options.append(button);
        });
        close.disabled = busy || !saved || saved.closed;
        reload.disabled = busy;
    };
    const request = async (action = "get", choices = []) => {
        if (!current()) return;
        if (busy) { if (action === "get") refreshPending = true; return; }
        busy = true;
        renderOptions();
        try {
            const result = await app().PollForTab(tabID, { action, message_id: message.id, choices });
            if (!current()) return;
            if (result.counts?.length !== definition.options.length || !Array.isArray(result.choices)) throw new Error(t("poll.unavailable"));
            if (!saved || result.version >= saved.version) saved = result;
            status.textContent = `${t(saved.closed ? "poll.closed" : "poll.open")} · ${t("poll.voters", { count: saved.total_voters })} · ${new Date(saved.closes_at * 1000).toLocaleString()}`;
        } catch (error) {
            if (current()) status.textContent = t("poll.failed", { error: String(error) });
        } finally {
            busy = false;
            if (current()) {
                renderOptions();
                if (refreshPending) { refreshPending = false; void request(); }
            }
        }
    };
    reload.onclick = () => request();
    close.onclick = () => request("close");
    refreshers.set(card, () => request());
    renderOptions();
    queueMicrotask(() => request());
    return true;
}

export function createPoll(scope, target) {
    if (scope !== "global" && scope !== "channel") {
        V().toast(t("poll.channelOnly"), "warn");
        return;
    }
    const tabID = V().state.activeTabID;
    const overlay = node("div", "dlg-overlay");
    const dialog = node("form", "dlg poll-dialog");
    overlay.append(dialog);
    dialog.append(node("h2", "", t("poll.create")));
    const field = (label, input) => {
        const wrap = node("label", "poll-field", label);
        wrap.append(input); dialog.append(wrap); return input;
    };
    const question = field(t("poll.question"), node("input", ""));
    question.maxLength = 300; question.required = true;
    const choices = field(t("poll.options"), node("textarea", ""));
    choices.rows = 5; choices.maxLength = 810; choices.required = true;
    const multiple = node("input", ""); multiple.type = "checkbox";
    field(t("poll.multiple"), multiple);
    const duration = field(t("poll.duration"), node("select", ""));
    for (const [seconds, key] of [[3600, "poll.hour"], [86400, "poll.day"], [604800, "poll.week"]]) {
        const option = node("option", "", t(key)); option.value = String(seconds); duration.append(option);
    }
    duration.value = "86400";
    const status = node("p", "poll-status"); status.setAttribute("role", "alert"); dialog.append(status);
    const actions = node("div", "dlg-buttons");
    const cancel = node("button", "", t("chat.cancel")); cancel.type = "button";
    cancel.onclick = () => closeDialog(overlay, "cancel");
    const submit = node("button", "dlg-ok", t("poll.create")); submit.type = "submit";
    actions.append(cancel, submit); dialog.append(actions);
    dialog.onsubmit = async event => {
        event.preventDefault();
        if (submit.disabled || !isCurrentServerDialog(overlay)) return;
        const definition = { question: question.value.trim(), options: choices.value.split(/\r?\n/).map(value => value.trim()), multiple: multiple.checked, closes_at: Math.floor(Date.now() / 1000) + Number(duration.value) };
        const invalid = validatePoll(definition);
        if (invalid) { status.textContent = t(invalid); return; }
        submit.disabled = true;
        try {
            const error = await app().CreatePollForTab(tabID, scope, target, definition);
            if (!isCurrentServerDialog(overlay)) return;
            if (error) throw new Error(error);
            closeDialog(overlay, "saved");
        } catch (error) {
            if (isCurrentServerDialog(overlay)) status.textContent = t("poll.failed", { error: String(error) });
        } finally { if (isCurrentServerDialog(overlay)) submit.disabled = false; }
    };
    mountServerDialog(overlay, { initialFocus: question });
}
