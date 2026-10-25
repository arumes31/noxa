// poll.js — polls in channel and server chat: the card a [noxa-poll:v1]
// message renders as, and the "Create poll" sheet. The definition travels
// in the sealed body; counts, the own ballot and closing come from the
// server (PollRequest/PollState), which never reveals who voted for what.
import { ChatSession } from "../core/chat-session.js";
import { POLL_DURATIONS, newerPollState, withRateRetry, pollClosed, pollPercentages, pollRemaining, togglePollChoice, validatePoll } from "../core/poll-state.js";
import { lang, t } from "../i18n.js";
import { h, toast } from "./dom.js";
import "./chat-tools-strings.js";
import "./poll.css";

/** Whether a message body is a poll. */
export function isPoll(text) {
    return ChatSession.parsePoll(text) !== null;
}

// One "poll" listener per session refreshes every card on screen.
const cards = new WeakMap(); // session -> Map(messageID -> Set(refresh))

function register(session, messageID, refresh) {
    if (!cards.has(session)) {
        const byID = new Map();
        cards.set(session, byID);
        session.addEventListener("poll", event => {
            for (const fn of byID.get(event.detail.messageID) || []) fn();
        });
    }
    const byID = cards.get(session);
    if (!byID.has(messageID)) byID.set(messageID, new Set());
    byID.get(messageID).add(refresh);
    return () => {
        byID.get(messageID)?.delete(refresh);
        if (!byID.get(messageID)?.size) byID.delete(messageID);
    };
}

function remainingText(closesAt) {
    const left = pollRemaining(closesAt);
    if (left.days) return t("poll.closesIn", { time: t("poll.daysHours", { days: left.days, hours: left.hours }) });
    if (left.hours) return t("poll.closesIn", { time: t("poll.hoursMinutes", { hours: left.hours, minutes: left.minutes }) });
    if (left.minutes) return t("poll.closesIn", { time: t("poll.minutes", { minutes: left.minutes }) });
    return t("poll.closesIn", { time: t("poll.seconds", { seconds: left.seconds }) });
}

/** The card of a poll message; message.own or an administrator may close it. */
export function pollCard(message, session) {
    const poll = ChatSession.parsePoll(message.text);
    const card = h("section", { class: "poll-card", role: "group", "aria-label": t("poll.label", { question: poll.question }), dataset: { pollId: String(message.id || "") } });
    const options = h("div", { class: "poll-options" });
    const status = h("p", { class: "poll-status", role: "status", "aria-live": "polite" });
    const countdown = h("p", { class: "poll-countdown" });
    const vote = h("button", { class: "btn btn-primary poll-vote", type: "button" }, t("poll.vote"));
    const close = h("button", { class: "btn poll-close", type: "button" }, t("poll.close"));
    const actions = h("div", { class: "poll-actions" }, vote, message.own || session.isAdmin ? close : null);
    card.append(
        h("strong", { class: "poll-question" }, poll.question),
        h("span", { class: "poll-hint" }, t(poll.multiple ? "poll.multiple" : "poll.single")),
        options, actions, countdown, status);

    let state = null;
    let selection = [];
    let busy = false;
    let timer = 0;
    let stop = () => {};

    const closed = () => pollClosed(state, poll.closesAt);
    const voted = () => (state?.choices || []).length > 0;

    function render() {
        const showResults = !!state && (voted() || closed());
        const percents = pollPercentages(state?.counts || [], state?.total_voters || 0);
        options.replaceChildren(...poll.options.map((label, index) => {
            const chosen = selection.includes(index);
            const count = state?.counts?.[index] || 0;
            const button = h("button", { class: `poll-option${chosen ? " chosen" : ""}`, type: "button", "aria-pressed": String(chosen),
                disabled: busy || !state || closed() || !message.id,
                onclick: () => { selection = togglePollChoice(selection, index, poll.multiple); render(); } },
                h("span", { class: "poll-option-label" }, label),
                showResults ? h("span", { class: "poll-option-count" }, t("poll.count", { count, percent: percents[index] })) : null);
            if (showResults) button.style.setProperty("--poll-percent", `${percents[index]}%`);
            if (showResults) button.setAttribute("aria-label", `${label}: ${t("poll.count", { count, percent: percents[index] })}`);
            return button;
        }));
        const same = selection.length === (state?.choices || []).length && selection.every(c => state.choices.includes(c));
        vote.hidden = closed();
        vote.textContent = voted() ? t("poll.changeVote") : t("poll.vote");
        vote.disabled = busy || !state || !selection.length || same;
        close.hidden = closed();
        close.disabled = busy || !state;
        if (state) {
            const parts = [t(closed() ? "poll.closed" : "poll.open"), t("poll.voters", { count: state.total_voters || 0 })];
            if (voted() && !closed()) parts.push(t("poll.youVoted"));
            status.textContent = parts.join(" · ");
        }
        countdown.textContent = closed() ? t("poll.closedAt", { time: new Date((state?.closes_at || poll.closesAt) * 1000).toLocaleString(lang()) })
            : poll.closesAt ? remainingText(poll.closesAt) : "";
    }

    async function request(action = "get", choices = []) {
        if (!message.id) return;
        if (busy && action === "get") return;
        busy = true;
        render();
        try {
            const result = await withRateRetry(() => session.poll(message.id, action, choices), { attempts: action === "get" ? 1 : 4 });
            if (!Array.isArray(result.counts) || result.counts.length !== poll.options.length) throw new Error(t("poll.unavailable"));
            const before = state;
            state = newerPollState(state, result);
            // Keep an unsent selection across refreshes; adopt the ballot otherwise.
            if (action !== "get" || !before || selection.join() === (before.choices || []).join()) selection = [...(state.choices || [])];
        } catch (error) {
            if (action !== "get") toast(t("poll.failed", { error: error.message }), "warn");
            else status.textContent = t("poll.failed", { error: error.message });
        } finally {
            busy = false;
            if (card.isConnected || action === "get") render();
        }
    }

    function tick() {
        clearTimeout(timer);
        if (!card.isConnected && state) { stop(); return; }
        render();
        const left = pollRemaining(poll.closesAt).total;
        if (poll.closesAt && left === 0 && state && !state.closed) { request(); return; }
        if (!closed()) timer = setTimeout(tick, left < 120 ? 1000 : 30000);
    }

    vote.onclick = () => request("vote", selection);
    close.onclick = () => request("close");
    if (message.id) {
        const unregister = register(session, message.id, () => {
            if (!card.isConnected) { stop(); return; }
            request();
        });
        stop = () => { unregister(); clearTimeout(timer); };
        queueMicrotask(() => request().then(tick));
    } else {
        status.textContent = t("poll.sending");
    }
    render();
    return card;
}

/** The "Create poll" sheet; sends with session.createPoll on submit. */
export function openCreatePoll(session, scope) {
    const opener = document.activeElement;
    const question = h("input", { class: "input", id: "poll-question", maxlength: "300", required: true, autocomplete: "off" });
    const list = h("div", { class: "poll-form-options" });
    const multiple = h("input", { type: "checkbox", id: "poll-multiple" });
    const error = h("p", { class: "error poll-error", role: "alert" });
    let duration = "day";
    const durations = h("div", { class: "segmented", role: "radiogroup", "aria-label": t("poll.duration") });
    const renderDurations = () => durations.replaceChildren(...Object.keys(POLL_DURATIONS).map(key => h("button", {
        type: "button", role: "radio", class: "segment", "aria-checked": String(duration === key), onclick: () => { duration = key; renderDurations(); },
    }, t(`poll.${key}`))));
    renderDurations();

    const addOption = (value = "") => {
        if (list.children.length >= 10) return;
        const input = h("input", { class: "input", maxlength: "80", autocomplete: "off", value });
        const row = h("div", { class: "poll-form-option" }, input,
            h("button", { class: "icon-btn", type: "button", "aria-label": t("poll.removeOption"), onclick: () => {
                if (list.children.length > 2) { row.remove(); relabel(); }
            } }, "×"));
        list.append(row);
        relabel();
        return input;
    };
    const relabel = () => {
        [...list.children].forEach((row, index) => {
            row.querySelector("input").setAttribute("aria-label", t("poll.optionN", { n: index + 1 }));
            row.querySelector("button").disabled = list.children.length <= 2;
        });
        add.disabled = list.children.length >= 10;
    };
    const add = h("button", { class: "btn", type: "button", onclick: () => addOption()?.focus() }, t("poll.addOption"));
    const submit = h("button", { class: "btn btn-primary", type: "submit" }, t("poll.create"));
    const cancel = h("button", { class: "btn", type: "button", onclick: () => close() }, t("link.cancel"));
    const form = h("form", { class: "sheet poll-form", role: "dialog", "aria-modal": "true", "aria-label": t("poll.create") },
        h("h2", { class: "sheet-title" }, t("poll.create")),
        h("div", { class: "field" }, h("label", { for: "poll-question" }, t("poll.question")), question),
        h("div", { class: "field" }, h("span", { class: "field-label" }, t("poll.options")), list, add),
        h("label", { class: "toggle" }, multiple, h("span", {}, t("poll.allowMultiple"))),
        h("div", { class: "field" }, h("span", { class: "field-label" }, t("poll.duration")), durations),
        error,
        h("div", { class: "sheet-actions" }, submit, cancel));
    addOption();
    addOption();
    const overlay = h("div", { class: "sheet-overlay", onclick: event => { if (event.target === overlay) close(); } }, form);
    const onKey = event => { if (event.key === "Escape") close(); };
    function close() {
        overlay.remove();
        document.removeEventListener("keydown", onKey);
        opener?.focus?.();
    }
    form.onsubmit = async event => {
        event.preventDefault();
        const definition = {
            question: question.value.trim(),
            options: [...list.querySelectorAll("input")].map(input => input.value.trim()),
            multiple: multiple.checked,
            closesAt: Math.floor(Date.now() / 1000) + POLL_DURATIONS[duration],
        };
        const invalid = validatePoll(definition);
        if (invalid) { error.textContent = t(invalid); return; }
        submit.disabled = true;
        try {
            await session.createPoll(scope, definition);
            close();
        } catch (failure) {
            error.textContent = t("poll.failed", { error: failure.message });
            submit.disabled = false;
        }
    };
    document.addEventListener("keydown", onKey);
    document.body.append(overlay);
    question.focus();
}
