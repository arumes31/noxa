// voice.js — Voice room (09): join a channel, speaking tiles, mute,
// deafen, push-to-talk, whisper, raised hands, priority speaker, the
// channel's chat, audio output, data used and watching streams.
import { app, joinVoice, leaveVoice } from "../app.js";
import * as settings from "../core/settings.js";
import { Voice } from "../core/voice.js";
import { speakingQueue } from "../core/voice-gate.js";
import { t } from "../i18n.js";
import { avatar, h, toast } from "./dom.js";
import { icon } from "./icons.js";
import { header, nav } from "./common.js";
import { voiceChat } from "./voice-chat.js";
import { openVoiceSettings } from "./voice-settings.js";
import { memberSheet, micTestSheet, whisperSheet } from "./voice-sheets.js";
import "./voice-strings.js";
import "./voice.css";

function megabytes(bytes) {
    return (bytes / 1024 / 1024).toFixed(1);
}

function channelList(root, session) {
    const channels = session.channels().filter(c => !c.textOnly && !c.home);
    root.replaceChildren(h("section", { class: "screen" },
        header(t("voice.title"), { action: gearButton() }),
        h("div", { class: "screen-body" },
            h("p", { class: "section-label" }, t("voice.join")),
            h("ul", { class: "list" }, channels.map(c => h("li", {},
                h("button", { class: "row", type: "button", style: `padding-left:${10 + c.depth * 14}px`, onclick: async event => {
                    const button = event.currentTarget;
                    button.disabled = true;
                    try { await joinVoice(c.id); } catch (error) { toast(t("voice.joinFailed", { error: error.message }), "warn"); }
                    button.disabled = false;
                } },
                    icon("voice"),
                    h("span", { class: "row-main" },
                        h("span", { class: "row-title" }, c.name),
                        c.clients.length ? h("span", { class: "row-sub" }, c.clients.map(m => m.nickname).join(", ")) : null),
                    c.clients.length ? h("span", { class: "row-meta" }, String(c.clients.length)) : null))))),
        nav("voice")));
}

/** Gear icon (inline, so the shared icon set stays untouched). */
function gearIcon() {
    const ns = "http://www.w3.org/2000/svg";
    const svg = document.createElementNS(ns, "svg");
    for (const [key, value] of Object.entries({ viewBox: "0 0 24 24", class: "icon", width: "20", height: "20", fill: "none", stroke: "currentColor", "stroke-width": "1.7", "stroke-linecap": "round", "stroke-linejoin": "round", "aria-hidden": "true" })) svg.setAttribute(key, value);
    const path = document.createElementNS(ns, "path");
    path.setAttribute("d", "M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z");
    svg.append(path);
    return svg;
}

function gearButton() {
    return h("button", { class: "icon-btn", type: "button", "aria-label": t("vc.settingsTitle"), onclick: () => openVoiceSettings() }, gearIcon());
}

function videoTile(track, voice, stream) {
    const element = h("video", { class: "stream-video", autoplay: true, playsinline: true, muted: true, "aria-label": t("voice.stream") });
    element.srcObject = new MediaStream([track]);
    // Double tap switches between showing the whole picture and filling the tile.
    let lastTap = 0;
    element.addEventListener("pointerup", () => {
        const now = Date.now();
        if (now - lastTap < 300) element.classList.toggle("fill");
        lastTap = now;
    });
    return h("div", { class: "stream-tile" }, element,
        h("button", { class: "btn stream-close", type: "button", onclick: () => voice.watch(stream, false).catch(() => {}) }, t("voice.stopWatching")));
}

/** A hold-to-act button: start on press, stop on release, cancel or leaving it. */
function holdButton(label, className, onPress, onRelease) {
    const button = h("button", { class: className, type: "button", "aria-pressed": "false" }, label);
    let held = false;
    const press = event => {
        event.preventDefault();
        if (held) return;
        held = true;
        button.classList.add("active");
        button.setAttribute("aria-pressed", "true");
        onPress();
    };
    const release = event => {
        event?.preventDefault();
        if (!held) return;
        held = false;
        button.classList.remove("active");
        button.setAttribute("aria-pressed", "false");
        onRelease();
    };
    button.addEventListener("pointerdown", press);
    for (const type of ["pointerup", "pointercancel", "pointerleave"]) button.addEventListener(type, release);
    // Keyboard: hold Space or Enter.
    button.addEventListener("keydown", event => { if ((event.key === " " || event.key === "Enter") && !event.repeat) press(event); });
    button.addEventListener("keyup", event => { if (event.key === " " || event.key === "Enter") release(event); });
    button.addEventListener("click", event => event.preventDefault());
    button.release = release;
    return button;
}

export function renderVoice(root) {
    const session = app.session;
    const voice = app.voice;
    if (!voice?.active) {
        channelList(root, session);
        return () => {};
    }
    const tiles = h("div", { class: "voice-tiles" });
    const streamsBox = h("div", { class: "streams" });
    const usage = h("p", { class: "hint mono" });
    const notices = h("div", { class: "vc-notices", role: "status", "aria-live": "polite" });
    // Whether the microphone is sending right now (push-to-talk, voice activation, whisper).
    const micState = h("p", { class: "vc-mic-state" });
    const renderMicState = () => {
        const on = voice.transmitting;
        micState.classList.toggle("on", on);
        micState.textContent = on ? t("vc.micLive") : t("vc.micClosed");
    };
    const queueBox = h("section", { class: "card vc-queue", "aria-label": t("vc.queueTitle") });
    const muteButton = h("button", { class: "btn", type: "button", onclick: () => voice.setMuted(!voice.muted) });
    const deafenButton = h("button", { class: "btn", type: "button", onclick: () => voice.setDeafened(!voice.deafened) });
    const cameraButton = h("button", { class: "btn", type: "button", onclick: async () => {
        cameraButton.disabled = true;
        try { await (voice.camera ? voice.stopCamera() : voice.startCamera()); } catch (error) { toast(t("voice.cameraFailed", { error: error.message }), "warn"); }
        cameraButton.disabled = false;
    } });
    const handButton = h("button", { class: "btn", type: "button", onclick: async () => {
        const raised = !!session.self()?.hand_raised_order;
        handButton.disabled = true;
        try { await voice.queue(raised ? "lower" : "raise"); } catch (error) { toast(t("vc.queueFailed", { error: error.message }), "warn"); }
        handButton.disabled = false;
    } });
    const priorityButton = h("button", { class: "btn", type: "button", onclick: async () => {
        priorityButton.disabled = true;
        try { await voice.setPrioritySpeaker(!voice.prioritySpeaker); } catch (error) { toast(t("vc.priorityFailed", { error: error.message }), "warn"); }
        priorityButton.disabled = false;
    } });
    const targetsButton = h("button", { class: "btn btn-block", type: "button", onclick: () => whisperSheet(voice, { onSaved: render }) }, t("vc.whisperTargets"));
    const micTestButton = h("button", { class: "btn", type: "button", onclick: () => micTestSheet(voice) }, t("vc.micTest"));
    const selfView = h("video", { class: "self-view", autoplay: true, playsinline: true, muted: true, "aria-label": t("call.selfView"), hidden: true });
    const pttToggle = h("input", { type: "checkbox", role: "switch", checked: voice.pushToTalk, onchange: event => voice.setPushToTalk(event.target.checked) });
    const talk = holdButton(t("voice.holdToTalk"), "talk-button", () => voice.setTalking(true), () => voice.setTalking(false));
    const whisperFailed = error => toast(t("vc.whisperFailed", { error: error.message }), "warn");
    const whisper = holdButton(t("vc.holdToWhisper"), "talk-button vc-whisper", () => {
        const targets = voice.whisperTargets;
        if (!targets.uniqueIDs.length && !targets.channelIDs.length) {
            whisper.release();
            whisperSheet(voice, { onSaved: render });
            return;
        }
        voice.startWhisper().then(render, whisperFailed);
    }, () => voice.stopWhisper().then(render));
    const reply = holdButton("", "btn btn-block vc-reply", () => voice.replyWhisper().then(render, whisperFailed), () => voice.stopWhisper().then(render));
    const output = h("div", { class: "output" });
    const chat = voiceChat(session, voice.channelID);
    const echoBar = h("div", { class: "card vc-echo", role: "status" },
        h("span", { class: "row-main" }, t("vc.echoRunning")),
        h("button", { class: "btn", type: "button", onclick: () => voice.endEchoTest().catch(error => toast(error.message, "warn")) }, t("vc.echoStop")));

    async function renderOutputs() {
        if (Voice.outputSelectable()) {
            const outputs = await voice.outputs();
            if (outputs.length > 1) {
                const select = h("select", { class: "input", "aria-label": t("voice.output"), onchange: event => voice.setOutput(event.target.value) },
                    outputs.map(o => h("option", { value: o.deviceId, selected: o.deviceId === voice.sinkId }, o.label || t("voice.outputDefault"))));
                output.replaceChildren(h("label", { class: "field" }, h("span", {}, t("voice.output")), select));
                return;
            }
            if (navigator.mediaDevices?.selectAudioOutput) {
                output.replaceChildren(h("button", { class: "btn btn-block", type: "button", onclick: () => voice.chooseOutput().then(renderOutputs).catch(() => {}) }, t("voice.chooseOutput")));
                return;
            }
        }
        // iOS routes audio itself (earpiece, speaker, Bluetooth).
        output.replaceChildren(h("p", { class: "hint" }, t("voice.outputSystem")));
    }

    let shownStreams = "";

    async function renderStreams() {
        let streams = [];
        try { streams = await voice.streams(); } catch { streams = []; }
        const signature = streams.map(stream => {
            const key = `${stream.publisher_id}|${stream.slot}`;
            return `${key}|${stream.generation}|${voice.watching.has(key) && voice.video.has(key)}`;
        }).join(",");
        if (signature === shownStreams) return;
        shownStreams = signature;
        streamsBox.replaceChildren(...streams.map(stream => {
            const key = `${stream.publisher_id}|${stream.slot}`;
            const track = voice.watching.has(key) ? voice.video.get(key) : null;
            if (track) return videoTile(track, voice, stream);
            const publisher = session.clients().find(c => c.client_id === stream.publisher_id)?.nickname || t("dm.someone");
            return h("button", { class: "card row stream-card", type: "button", onclick: () => voice.watch(stream).catch(error => toast(error.message, "warn")) },
                icon("screen"), h("span", { class: "row-main" }, h("span", { class: "row-title" }, t(stream.slot === "screen" ? "voice.watchScreen" : "voice.watchCamera", { name: publisher }))));
        }));
    }

    function tile(member, queue) {
        const self = member.client_id === session.clientID;
        const mutedHere = (settings.get("mutedUsers") || []).includes(member.unique_id);
        const position = queue.findIndex(c => c.client_id === member.client_id);
        const badges = [
            member.priority_speaker ? h("span", { class: "vc-badge", title: t("vc.priority"), "aria-label": t("vc.priority") }, "★") : null,
            position >= 0 ? h("span", { class: "vc-badge", title: t("vc.handRaised"), "aria-label": t("vc.handPosition", { position: position + 1 }) }, `✋${position + 1}`) : null,
            voice.whisperers.has(member.client_id) ? h("span", { class: "vc-badge vc-badge-whisper", "aria-label": t("vc.whisperingToYou") }, t("vc.whisperBadge")) : null,
            mutedHere ? h("span", { class: "vc-badge", "aria-label": t("vc.mutedForMe") }, "🔕") : null,
            member.self_muted || member.server_muted ? h("span", { class: "tile-state", "aria-label": t("voice.muted") }, "🔇") : null,
        ];
        const name = self ? `${member.nickname} (${t("dm.you")})` : member.nickname;
        const classes = `voice-tile${voice.speaking.has(member.client_id) ? " speaking" : ""}${member.priority_speaker ? " priority" : ""}`;
        const body = [avatar(member.nickname, "lg"), h("span", { class: "tile-name" }, name), h("span", { class: "vc-badges" }, badges)];
        if (self) return h("div", { class: classes }, body);
        return h("button", { class: `${classes} vc-tile-button`, type: "button", "aria-label": t("vc.memberOptions", { name: member.nickname }),
            onclick: () => memberSheet(voice, member) }, body);
    }

    function renderQueue(channel, queue) {
        const moderator = !!channel?.canManageVoiceQueue;
        const called = channel?.clients.find(c => c.hand_called);
        const failed = error => toast(t("vc.queueFailed", { error: error.message }), "warn");
        queueBox.hidden = !queue.length && !called;
        queueBox.replaceChildren(
            h("p", { class: "section-label" }, t("vc.queueTitle")),
            called ? h("p", { class: "hint" }, called.client_id === session.clientID ? t("vc.called") : t("vc.queueCurrent", { name: called.nickname })) : null,
            queue.length ? h("ol", { class: "vc-queue-list" }, queue.map(member => h("li", {},
                h("span", { class: "row-main" }, member.nickname),
                moderator || member.client_id === session.clientID ? h("button", { class: "btn", type: "button",
                    onclick: () => voice.queue("lower", member.client_id === session.clientID ? "" : member.client_id).catch(failed) }, t("vc.lowerHand")) : null)))
                : null,
            moderator && (queue.length || called) ? h("div", { class: "vc-row" },
                h("button", { class: "btn btn-primary", type: "button", onclick: () => voice.queue("next").catch(failed) }, t("vc.callNext")),
                h("button", { class: "btn", type: "button", onclick: () => voice.queue("clear").catch(failed) }, t("vc.clearQueue"))) : null);
    }

    function render() {
        const channel = session.channels().find(c => c.id === voice.channelID);
        const members = channel?.clients || [];
        const queue = speakingQueue(members, voice.channelID);
        tiles.replaceChildren(...members.map(member => tile(member, queue)));
        renderQueue(channel, queue);
        const self = session.self();
        muteButton.textContent = voice.muted ? t("voice.unmute") : t("voice.mute");
        muteButton.setAttribute("aria-pressed", String(voice.muted));
        deafenButton.textContent = voice.deafened ? t("voice.undeafen") : t("voice.deafen");
        deafenButton.setAttribute("aria-pressed", String(voice.deafened));
        const raised = !!self?.hand_raised_order;
        handButton.textContent = raised ? t("vc.lowerHand") : t("vc.raiseHand");
        handButton.setAttribute("aria-pressed", String(raised));
        priorityButton.textContent = t("vc.priority");
        priorityButton.setAttribute("aria-pressed", String(voice.prioritySpeaker));
        pttToggle.checked = voice.pushToTalk;
        talk.hidden = !voice.pushToTalk;
        const last = voice.lastWhisperer;
        reply.hidden = !last?.uniqueID;
        if (last) reply.textContent = t("vc.holdToReply", { name: last.nickname || t("dm.someone") });
        const targets = voice.whisperTargets;
        const targetCount = targets.uniqueIDs.length + targets.channelIDs.length;
        targetsButton.textContent = targetCount ? t("vc.whisperTargetsCount", { count: targetCount }) : t("vc.whisperTargets");
        cameraButton.textContent = voice.camera ? t("voice.cameraOff") : t("voice.cameraOn");
        cameraButton.setAttribute("aria-pressed", String(!!voice.camera));
        if (selfView.srcObject !== voice.camera) selfView.srcObject = voice.camera;
        selfView.hidden = !voice.camera;
        echoBar.hidden = !voice.echo;
        renderMicState();
        usage.textContent = t("voice.dataUsed", { sent: megabytes(voice.bytes.sent), received: megabytes(voice.bytes.received) });
        const whisperers = members.filter(m => voice.whisperers.has(m.client_id)).map(m => m.nickname);
        // A whisperer in another channel has no tile here.
        if (!whisperers.length && voice.whisperers.size && last) whisperers.push(last.nickname);
        notices.replaceChildren(
            voice.whispering ? h("p", { class: "vc-notice vc-notice-whisper" }, t("vc.whisperingNow")) : null,
            whisperers.length ? h("p", { class: "vc-notice vc-notice-whisper" }, t("vc.whisperIncoming", { name: whisperers.join(", ") })) : null,
            members.length <= 1 && !voice.echo ? h("p", { class: "vc-notice" }, t("vc.alone")) : null,
            voice.lowBandwidth ? h("p", { class: "vc-notice" }, t("vc.lowBandwidthOn")) : null);
    }

    root.replaceChildren(h("section", { class: "screen" },
        header(session.channels().find(c => c.id === voice.channelID)?.name || t("voice.title"), { sub: t("voice.connected"), action: gearButton() }),
        h("div", { class: "screen-body" },
            echoBar,
            notices,
            micState,
            tiles,
            queueBox,
            streamsBox,
            selfView,
            h("div", { class: "voice-controls" }, muteButton, deafenButton,
                h("button", { class: "btn btn-danger", type: "button", onclick: () => leaveVoice() }, t("voice.leave"))),
            h("div", { class: "voice-controls" }, handButton, priorityButton, micTestButton),
            cameraButton,
            h("label", { class: "toggle" }, h("span", { class: "row-main" }, h("span", { class: "row-title" }, t("voice.pushToTalk"))), pttToggle),
            h("div", { class: "vc-hold-row" }, talk, whisper),
            targetsButton,
            reply,
            chat.node,
            output,
            usage,
            /iPhone|iPad/.test(navigator.userAgent) ? h("p", { class: "hint" }, t("voice.iosBackground")) : null),
        nav("voice")));
    render();
    renderOutputs();
    renderStreams();
    const onChange = () => render();
    const onTracks = () => { render(); renderStreams(); };
    const listeners = [["state", onChange], ["speaking", onChange], ["stats", onChange], ["tracks", onTracks], ["whisper", onChange], ["transmit", renderMicState]];
    for (const [type, listener] of listeners) voice.addEventListener(type, listener);
    const onSnapshot = () => render();
    session.addEventListener("snapshot", onSnapshot);
    const stopSettings = settings.onChange(() => render());
    // New publications show up without an event of their own.
    const streamTimer = setInterval(renderStreams, 5000);
    return () => {
        clearInterval(streamTimer);
        for (const [type, listener] of listeners) voice.removeEventListener(type, listener);
        session.removeEventListener("snapshot", onSnapshot);
        stopSettings();
        chat.stop();
        voice.setTalking(false);
        if (voice.whispering) voice.stopWhisper();
    };
}
