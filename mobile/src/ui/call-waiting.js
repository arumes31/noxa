// call-waiting.js — two things around incoming calls that live outside the
// call screen:
//   * call waiting: a second call while in one shows a compact banner on
//     every screen; Accept ends the current call and answers, Decline
//     declines only the waiting call.
//   * notification actions: "Answer" opens the call and accepts it once it
//     rings on this session (a fresh window needs the worker's single-use
//     answer intent, a running one gets a message from the service worker); "Decline" from a running
//     app declines here (a closed app is handled by the service worker).
import { app } from "../app.js";
import { takeAnswerIntent } from "../core/push.js";
import { t } from "../i18n.js";
import { h, toast } from "./dom.js";
import "./video-kit.js";

const ANSWER_WINDOW = 45000;

function nameOf(uid) {
    return app.session?.clients().find(c => c.unique_id === uid)?.nickname
        || app.dm?.conversations.get(uid)?.nickname || t("dm.someone");
}

let banner = null;

function renderBanner() {
    const waiting = app.calls?.active ? app.calls.waiting : null;
    if (!waiting) {
        banner?.remove();
        banner = null;
        return;
    }
    if (banner?.dataset.call === waiting.id) return;
    banner?.remove();
    const calls = app.calls;
    const busy = button => { for (const b of banner.querySelectorAll("button")) b.disabled = button; };
    banner = h("div", { class: "call-waiting", role: "alertdialog", "aria-label": t("video.waitingLabel"), dataset: { call: waiting.id } },
        h("span", { class: "call-waiting-text", role: "status" }, t("video.waiting", { name: nameOf(waiting.caller) })),
        h("button", { class: "btn btn-danger", type: "button", onclick: async () => {
            busy(true);
            await calls.declineWaiting().catch(error => toast(error.message, "warn"));
        } }, t("video.waitingDecline")),
        h("button", { class: "btn btn-primary", type: "button", onclick: async () => {
            busy(true);
            try {
                await calls.acceptWaiting();
                location.hash = `#/call/${waiting.id}`;
            } catch (error) {
                toast(error.message, "warn");
            }
        } }, t("video.waitingAccept")));
    document.body.append(banner);
    navigator.vibrate?.([200, 100, 200]);
}

// --- Notification intents --------------------------------------------------------

let answer = null; // {id, until}

async function takeAnswerParam() {
    const params = new URLSearchParams(location.search);
    const id = params.get("answer");
    if (!id) return;
    params.delete("answer");
    const query = params.toString();
    history.replaceState(history.state, "", `${location.pathname}${query ? `?${query}` : ""}${location.hash}`);
    // Any site can link to "?answer=<id>": only the service worker's
    // single-use intent (the user chose Answer on the notification) accepts
    // automatically. Otherwise the call screen opens and waits for Answer.
    if (await takeAnswerIntent(id)) answerWhenRinging(id);
    else if (!location.hash.startsWith(`#/call/${id}`)) location.hash = `#/call/${id}`;
}

function answerWhenRinging(id) {
    answer = { id, until: Date.now() + ANSWER_WINDOW };
    if (!location.hash.startsWith(`#/call/${id}`)) location.hash = `#/call/${id}`;
    maybeAnswer();
}

function maybeAnswer() {
    if (!answer || Date.now() > answer.until) {
        answer = null;
        return;
    }
    const calls = app.calls;
    if (!calls || calls.call?.id !== answer.id || !calls.incoming) return;
    answer = null;
    calls.accept().catch(error => toast(error.message, "warn"));
}

async function declineFromNotification(id) {
    const calls = app.calls;
    if (!calls) return;
    if (calls.waiting?.id === id) return calls.declineWaiting();
    if (calls.call?.id !== id) await calls.refresh(id).catch(() => null);
    if (calls.call?.id === id && calls.incoming) await calls.decline();
}

app.addEventListener("call", () => {
    renderBanner();
    maybeAnswer();
});

navigator.serviceWorker?.addEventListener("message", event => {
    const data = event.data;
    if (data?.type !== "call-action" || typeof data.id !== "string" || !data.id) return;
    if (data.action === "answer") answerWhenRinging(data.id);
    else if (data.action === "decline") declineFromNotification(data.id).catch(() => {});
});

takeAnswerParam().catch(() => {});
