// call.js — Incoming call (10) and private call (11, 12): accept with or
// without video, decline, "Can't talk, text me", mute, camera, hang up,
// call tiles and data used.
import { app, remindLater } from "../app.js";
import { t } from "../i18n.js";
import { Calls } from "../core/calls.js";
import { avatar, h, toast } from "./dom.js";
import { icon } from "./icons.js";
import {
    cameraSheet, enterFullscreen, exitFullscreen, fullscreenElement, fullscreenSupported, iconButton,
    pipSupported, togglePictureInPicture, videoIcon,
} from "./video-kit.js";

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

    // Only this call's screen goes back: call waiting may already show the next call.
    const leaveScreen = () => { if (location.hash.startsWith(`#/call/${id}`)) history.length > 1 ? history.back() : (location.hash = "#/home"); };
    /** Video tiles survive re-renders so fullscreen and PiP are not cut off. */
    const media = new Map();

    function incoming(call) {
        const caller = nameOf(call.caller);
        body.replaceChildren(
            h("div", { class: "call-hero" }, avatar(caller, "xl", call.caller), h("h1", {}, caller), h("p", { class: "hint" }, t("call.incoming"))),
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
        const used = new Set();
        const tiles = others.flatMap(p => {
            const peer = calls.peers.get(p.unique_id);
            const name = nameOf(p.unique_id);
            const camera = peer?.video?.camera;
            const screen = peer?.video?.screen;
            const out = [];
            if (screen && peer?.remoteMedia?.screen) out.push(mediaTile(used, `${p.unique_id}|screen`, screen, t("video.screenOf", { name }), { screen: true }));
            if (camera && peer?.remoteMedia?.camera !== false) {
                const tile = mediaTile(used, `${p.unique_id}|camera`, camera, name, {});
                tile.querySelector(".tile-state").textContent = t(`call.state.${p.state}`);
                out.push(tile);
            } else {
                out.push(h("div", { class: "call-tile" }, avatar(name, "lg", p.unique_id),
                    h("span", { class: "tile-name" }, name),
                    h("span", { class: "tile-state" }, t(`call.state.${p.state}`))));
            }
            return out;
        });
        for (const [key, entry] of media) {
            if (used.has(key)) continue;
            entry.video.srcObject = null;
            media.delete(key);
        }
        let own = null;
        if (calls.camera) {
            own = media.get("self")?.video;
            if (!own) {
                own = h("video", { class: "self-view", autoplay: true, playsinline: true, muted: true, "aria-label": t("call.selfView") });
                media.set("self", { video: own });
            }
            if (own.srcObject !== calls.camera) own.srcObject = calls.camera;
            own.dataset.facing = calls.facingMode;
        } else media.delete("self");
        const round = (name, label, onclick, { on = false, pressed, className = "" } = {}) => h("button", {
            class: `round-btn${on ? " on" : ""}${className ? ` ${className}` : ""}`, type: "button", "aria-label": label,
            ...(pressed === undefined ? {} : { "aria-pressed": String(pressed) }), onclick,
        }, typeof name === "string" ? icon(name) : name);
        const failed = error => toast(errorText(error), "warn");
        body.replaceChildren(
            h("p", { class: "call-status", role: "status" }, status),
            h("div", { class: "call-tiles" }, tiles),
            own,
            usage,
            h("div", { class: "call-actions call-extra" },
                calls.camera ? round(videoIcon("flip"), t("video.flip"), () => calls.flipCamera().catch(failed), { pressed: calls.facingMode === "environment", className: "flip-camera" }) : null,
                round(videoIcon(calls.deafened ? "speakerOff" : "speaker"), calls.deafened ? t("video.undeafen") : t("video.deafen"), () => calls.setDeafened(!calls.deafened), { on: calls.deafened, pressed: calls.deafened }),
                Calls.screenShareSupported() ? round("screen", calls.screen ? t("video.stopScreen") : t("video.shareScreen"), () => calls.setScreen(!calls.screen).catch(failed), { on: !!calls.screen, pressed: !!calls.screen }) : null,
                round(videoIcon("settings"), t("video.cameraOptions"), () => cameraSheet(calls, app.session))),
            h("div", { class: "call-actions" },
                round(calls.muted ? "micOff" : "mic", calls.muted ? t("voice.unmute") : t("voice.mute"), () => calls.setMuted(!calls.muted), { on: calls.muted, pressed: calls.muted }),
                round("camera", t("call.camera"), () => calls.setCamera(!calls.camera).catch(failed), { on: !!calls.camera, pressed: !!calls.camera }),
                round("hangup", t("call.hangUp"), () => calls.hangUp(), { className: "decline" })));
    }

    /** A remote camera or screen: fit/fill, fullscreen and picture-in-picture. */
    function mediaTile(used, key, track, label, { screen = false }) {
        used.add(key);
        const cached = media.get(key);
        if (cached?.track === track) return cached.tile;
        const video = h("video", { autoplay: true, playsinline: true, muted: true, "aria-label": label });
        video.srcObject = new MediaStream([track]);
        const fit = iconButton("fit", t("video.fitToggle"), () => {
            video.classList.toggle("fill");
            fit.setAttribute("aria-pressed", String(video.classList.contains("fill")));
        }, { "aria-pressed": "false" });
        const tile = h("div", { class: `call-tile${screen ? " screen-tile" : ""}`, dataset: { key } }, video,
            h("span", { class: "tile-name" }, label),
            h("span", { class: "tile-state" }),
            h("div", { class: "tile-tools" },
                screen ? fit : null,
                fullscreenSupported() ? iconButton("fullscreen", t("video.fullscreen"), () => {
                    if (fullscreenElement() === tile) exitFullscreen();
                    else enterFullscreen(tile, video).catch(error => toast(error.message, "warn"));
                }) : null,
                pipSupported() ? iconButton("pip", t("video.pip"), () => togglePictureInPicture(video, { title: label, artist: t("call.title") }).catch(error => toast(error.message, "warn"))) : null));
        media.set(key, { tile, video, track });
        return tile;
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
