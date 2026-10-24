// voice.js — Voice room (09): join a channel, speaking tiles, mute,
// deafen, push-to-talk, audio output, data used and watching streams.
import { app, joinVoice, leaveVoice } from "../app.js";
import { Voice } from "../core/voice.js";
import { t } from "../i18n.js";
import { avatar, h, toast } from "./dom.js";
import { icon } from "./icons.js";
import { header, nav } from "./common.js";
import { streamsPanel } from "./streams.js";

function megabytes(bytes) {
    return (bytes / 1024 / 1024).toFixed(1);
}

function channelList(root, session) {
    const channels = session.channels().filter(c => !c.textOnly && !c.home);
    root.replaceChildren(h("section", { class: "screen" },
        header(t("voice.title")),
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

export function renderVoice(root) {
    const session = app.session;
    const voice = app.voice;
    if (!voice?.active) {
        channelList(root, session);
        return () => {};
    }
    const tiles = h("div", { class: "voice-tiles" });
    const streamsBox = streamsPanel(voice, session);
    const usage = h("p", { class: "hint mono" });
    const muteButton = h("button", { class: "btn", type: "button", onclick: () => voice.setMuted(!voice.muted) });
    const deafenButton = h("button", { class: "btn", type: "button", onclick: () => voice.setDeafened(!voice.deafened) });
    const cameraButton = h("button", { class: "btn", type: "button", onclick: async () => {
        cameraButton.disabled = true;
        try { await (voice.camera ? voice.stopCamera() : voice.startCamera()); } catch (error) { toast(t("voice.cameraFailed", { error: error.message }), "warn"); }
        cameraButton.disabled = false;
    } });
    const selfView = h("video", { class: "self-view", autoplay: true, playsinline: true, muted: true, "aria-label": t("call.selfView"), hidden: true });
    const pttToggle = h("input", { type: "checkbox", role: "switch", checked: voice.pushToTalk, onchange: event => { voice.setPushToTalk(event.target.checked); render(); } });
    const talk = h("button", { class: "talk-button", type: "button" }, t("voice.holdToTalk"));
    for (const [type, value] of [["pointerdown", true], ["pointerup", false], ["pointercancel", false], ["pointerleave", false]]) {
        talk.addEventListener(type, event => {
            event.preventDefault();
            voice.setTalking(value);
            talk.classList.toggle("active", value);
        });
    }
    const output = h("div", { class: "output" });

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

    function render() {
        const channel = session.channels().find(c => c.id === voice.channelID);
        const members = channel?.clients || [];
        tiles.replaceChildren(...members.map(member => h("div", { class: `voice-tile${voice.speaking.has(member.client_id) ? " speaking" : ""}` },
            avatar(member.nickname, "lg"),
            h("span", { class: "tile-name" }, member.client_id === session.clientID ? `${member.nickname} (${t("dm.you")})` : member.nickname),
            member.self_muted || member.server_muted ? h("span", { class: "tile-state", "aria-label": t("voice.muted") }, "🔇") : null)));
        muteButton.textContent = voice.muted ? t("voice.unmute") : t("voice.mute");
        muteButton.setAttribute("aria-pressed", String(voice.muted));
        deafenButton.textContent = voice.deafened ? t("voice.undeafen") : t("voice.deafen");
        deafenButton.setAttribute("aria-pressed", String(voice.deafened));
        talk.hidden = !voice.pushToTalk;
        cameraButton.textContent = voice.camera ? t("voice.cameraOff") : t("voice.cameraOn");
        cameraButton.setAttribute("aria-pressed", String(!!voice.camera));
        if (selfView.srcObject !== voice.camera) selfView.srcObject = voice.camera;
        selfView.hidden = !voice.camera;
        usage.textContent = t("voice.dataUsed", { sent: megabytes(voice.bytes.sent), received: megabytes(voice.bytes.received) });
    }

    root.replaceChildren(h("section", { class: "screen" },
        header(session.channels().find(c => c.id === voice.channelID)?.name || t("voice.title"), { sub: t("voice.connected") }),
        h("div", { class: "screen-body" },
            tiles,
            streamsBox,
            selfView,
            h("div", { class: "voice-controls" }, muteButton, deafenButton,
                h("button", { class: "btn btn-danger", type: "button", onclick: () => leaveVoice() }, t("voice.leave"))),
            cameraButton,
            h("label", { class: "toggle" }, h("span", { class: "row-main" }, h("span", { class: "row-title" }, t("voice.pushToTalk"))), pttToggle),
            talk,
            output,
            usage,
            /iPhone|iPad/.test(navigator.userAgent) ? h("p", { class: "hint" }, t("voice.iosBackground")) : null),
        nav("voice")));
    render();
    renderOutputs();
    const onChange = () => render();
    const onTracks = () => render();
    const listeners = [["state", onChange], ["speaking", onChange], ["stats", onChange], ["tracks", onTracks]];
    for (const [type, listener] of listeners) voice.addEventListener(type, listener);
    const onSnapshot = () => render();
    session.addEventListener("snapshot", onSnapshot);
    return () => {
        streamsBox.dispose();
        for (const [type, listener] of listeners) voice.removeEventListener(type, listener);
        session.removeEventListener("snapshot", onSnapshot);
        voice.setTalking(false);
    };
}
