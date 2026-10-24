// call.js — Incoming call (10) and private call (11, 12): accept with or
// without video, decline, "Can't talk, text me", mute, camera, hang up,
// call tiles and data used.
import { app, remindLater } from "../app.js";
import { t } from "../i18n.js";
import { avatar, h, toast } from "./dom.js";
import { icon } from "./icons.js";

function nameOf(uid) {
    return app.session?.clients().find(c => c.unique_id === uid)?.nickname
        || app.dm?.conversations.get(uid)?.nickname || t("dm.someone");
}

function megabytes(bytes) {
    return (bytes / 1024 / 1024).toFixed(1);
}

function errorText(error) {
    return error?.code ? t(`call.error.${error.code}`) : error?.message || String(error);
}

export function renderCall(root, id) {
    const calls = app.calls;
    const body = h("div", { class: "call-body" });
    const usage = h("p", { class: "hint mono call-usage" });
    let usageTimer = 0;

    const leaveScreen = () => { if (location.hash.startsWith("#/call/")) history.length > 1 ? history.back() : (location.hash = "#/home"); };

    function incoming(call) {
        const caller = nameOf(call.caller);
        body.replaceChildren(
            h("div", { class: "call-hero" }, avatar(caller, "xl"), h("h1", {}, caller), h("p", { class: "hint" }, t("call.incoming"))),
            h("div", { class: "call-actions" },
                h("button", { class: "round-btn decline", type: "button", "aria-label": t("call.decline"), onclick: () => calls.decline() }, icon("hangup")),
                h("button", { class: "round-btn video", type: "button", "aria-label": t("call.acceptVideo"), onclick: () => calls.accept({ video: true }).catch(error => toast(errorText(error), "warn")) }, icon("camera")),
                h("button", { class: "round-btn accept", type: "button", "aria-label": t("call.accept"), onclick: () => calls.accept().catch(error => toast(errorText(error), "warn")) }, icon("phone"))),
            h("div", { class: "voice-controls", style: "grid-template-columns:1fr 1fr" },
                h("button", { class: "btn", type: "button", onclick: async () => {
                    await calls.decline();
                    app.dm?.send(call.caller, t("call.cantTalkMessage"), caller).catch(() => {});
                } }, t("call.cantTalk")),
                h("button", { class: "btn", type: "button", onclick: async () => {
                    await calls.decline();
                    remindLater(call.caller, caller, t("call.remindBody"));
                    toast(t("call.reminderSet"));
                } }, t("call.remind"))));
    }

    function active(call) {
        const others = call.participants.filter(p => p.unique_id !== app.session.uniqueID);
        const connected = others.filter(p => p.state === "accepted");
        const status = connected.length ? t("call.active") : t("call.ringing");
        const tiles = others.map(p => {
            const peer = calls.peers.get(p.unique_id);
            const track = peer?.video?.camera;
            const showVideo = track && peer?.remoteMedia?.camera !== false;
            return h("div", { class: "call-tile" },
                showVideo ? Object.assign(h("video", { autoplay: true, playsinline: true, muted: true, "aria-label": nameOf(p.unique_id) }), { srcObject: new MediaStream([track]) }) : avatar(nameOf(p.unique_id), "lg"),
                h("span", { class: "tile-name" }, nameOf(p.unique_id)),
                h("span", { class: "tile-state" }, t(`call.state.${p.state}`)));
        });
        const own = calls.camera ? Object.assign(h("video", { class: "self-view", autoplay: true, playsinline: true, muted: true, "aria-label": t("call.selfView") }), { srcObject: calls.camera }) : null;
        body.replaceChildren(
            h("p", { class: "call-status", role: "status" }, status),
            h("div", { class: "call-tiles" }, tiles),
            own,
            usage,
            h("div", { class: "call-actions" },
                h("button", { class: `round-btn${calls.muted ? " on" : ""}`, type: "button", "aria-pressed": String(calls.muted), "aria-label": calls.muted ? t("voice.unmute") : t("voice.mute"), onclick: () => calls.setMuted(!calls.muted) }, icon(calls.muted ? "micOff" : "mic")),
                h("button", { class: `round-btn${calls.camera ? " on" : ""}`, type: "button", "aria-pressed": String(!!calls.camera), "aria-label": t("call.camera"), onclick: () => calls.setCamera(!calls.camera).catch(error => toast(errorText(error), "warn")) }, icon("camera")),
                h("button", { class: "round-btn decline", type: "button", "aria-label": t("call.hangUp"), onclick: () => calls.hangUp() }, icon("hangup"))));
    }

    function render() {
        const call = calls?.call;
        if (!call || call.id !== id) {
            body.replaceChildren(h("p", { class: "hint" }, t("call.ended")));
            return;
        }
        if (calls.incoming) incoming(call);
        else if (calls.active) active(call);
        else body.replaceChildren(h("p", { class: "hint" }, t("call.ended")));
    }

    async function refreshUsage() {
        const { sent, received } = await calls.usage();
        usage.textContent = t("voice.dataUsed", { sent: megabytes(sent), received: megabytes(received) });
    }

    root.replaceChildren(h("section", { class: "screen call-screen", "aria-label": t("call.title") }, body));
    if (!calls) {
        render();
        return () => {};
    }
    if (calls.call?.id !== id) calls.refresh(id).catch(() => {}).finally(render);
    render();
    const onState = () => render();
    const onEnded = () => { render(); setTimeout(leaveScreen, 1200); };
    const onError = event => toast(errorText(event.detail), "warn");
    calls.addEventListener("state", onState);
    calls.addEventListener("ended", onEnded);
    calls.addEventListener("error", onError);
    usageTimer = setInterval(() => { if (calls.active && !calls.incoming) refreshUsage().catch(() => {}); }, 2000);
    return () => {
        clearInterval(usageTimer);
        calls.removeEventListener("state", onState);
        calls.removeEventListener("ended", onEnded);
        calls.removeEventListener("error", onError);
    };
}
