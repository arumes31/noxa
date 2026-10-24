// voice-settings.js — the "Voice" section of You and of the voice room's
// gear sheet: microphone, processing, activation mode, volumes and the data
// saver. Every control writes core/settings.js; the running call follows
// through settings change events (core/voice.js).
import { app } from "../app.js";
import * as settings from "../core/settings.js";
import { meterFraction, METER_MAX, volumeFromPercent, volumePercent, MAX_VOLUME } from "../core/voice-gate.js";
import { t } from "../i18n.js";
import { h } from "./dom.js";
import { levelMeter, micTestSheet, panel, slider, toggleRow } from "./voice-sheets.js";
import "./voice-strings.js";
import "./voice.css";

/** Microphones for the picker; labels appear only after permission was given once. */
async function microphones() {
    const devices = await navigator.mediaDevices?.enumerateDevices?.().catch(() => []) || [];
    return devices.filter(d => d.kind === "audioinput" && d.deviceId && d.deviceId !== "default" && d.deviceId !== "communications");
}

function microphonePicker() {
    const select = h("select", { class: "input", "aria-label": t("vc.microphone"), onchange: () => settings.set({ microphoneID: select.value }) },
        h("option", { value: "" }, t("vc.micDefault")));
    microphones().then(list => {
        const current = settings.get("microphoneID");
        select.append(...list.map((device, index) => h("option", { value: device.deviceId, selected: device.deviceId === current },
            device.label || t("vc.micNumbered", { number: index + 1 }))));
        // A remembered microphone that is not plugged in still shows as chosen.
        if (current && !list.some(d => d.deviceId === current)) select.append(h("option", { value: current, selected: true }, t("vc.micMissing")));
    });
    return h("label", { class: "field" }, h("span", {}, t("vc.microphone")), select);
}

function activationPicker(onChange) {
    const current = settings.get("activation");
    const group = h("div", { class: "vc-segments", role: "radiogroup", "aria-label": t("vc.activation") },
        ["continuous", "voice", "ptt"].map(mode => {
            const input = h("input", { type: "radio", name: "vc-activation", value: mode, checked: current === mode, onchange: () => { settings.set({ activation: mode }); onChange(); } });
            return h("label", { class: "vc-segment" }, input, h("span", {}, t(`vc.mode.${mode}`)));
        }));
    return h("div", { class: "field" }, h("span", {}, t("vc.activation")), group);
}

/** The voice settings section; mount it anywhere (You, the voice room's sheet). */
export function voiceSettings() {
    const meter = levelMeter(t("vc.micLevel"));
    const modeBox = h("div", { class: "vc-stack" });
    const card = h("div", { class: "card vc-stack vc-settings" });
    const renderMode = () => {
        const mode = settings.get("activation");
        modeBox.replaceChildren(
            mode === "voice" ? h("div", { class: "vc-stack" },
                meter.node,
                slider({ label: t("vc.threshold"), min: 0, max: METER_MAX, step: 0.005, value: settings.get("voiceThreshold"),
                    format: v => `${Math.round(meterFraction(v) * 100)} %`, onInput: value => settings.set({ voiceThreshold: value }) }),
                h("p", { class: "hint" }, app.voice?.active ? t("vc.thresholdHint") : t("vc.thresholdHintIdle"))) : null,
            mode === "ptt" ? slider({ label: t("vc.releaseDelay"), min: 0, max: 1000, step: 50, value: settings.get("pttReleaseMs"),
                format: v => `${v} ms`, onInput: value => settings.set({ pttReleaseMs: value }) }) : null);
    };
    renderMode();
    card.append(
        microphonePicker(),
        toggleRow(t("vc.echoCancellation"), settings.get("echoCancellation"), on => settings.set({ echoCancellation: on })),
        toggleRow(t("vc.noiseSuppression"), settings.get("noiseSuppression"), on => settings.set({ noiseSuppression: on })),
        toggleRow(t("vc.autoGain"), settings.get("autoGainControl"), on => settings.set({ autoGainControl: on })),
        activationPicker(renderMode),
        modeBox,
        toggleRow(t("vc.warnMuted"), settings.get("warnMutedTalking"), on => settings.set({ warnMutedTalking: on })),
        slider({ label: t("vc.masterVolume"), min: 0, max: MAX_VOLUME * 100, step: 5, value: volumePercent(settings.get("masterVolume")),
            format: v => `${v} %`, onInput: value => settings.set({ masterVolume: volumeFromPercent(value) }) }),
        toggleRow(t("vc.lowBandwidth"), settings.get("lowBandwidth"), on => settings.set({ lowBandwidth: on }), t("vc.lowBandwidthHint")),
        h("button", { class: "btn btn-block", type: "button", onclick: () => micTestSheet(app.voice) }, t("vc.micTest")));
    const section = h("section", { class: "vc-section", "aria-label": t("vc.settingsTitle") }, h("p", { class: "section-label" }, t("vc.settingsTitle")), card);
    // The live meter follows the call's microphone; it stops once the section is gone.
    const timer = setInterval(() => {
        if (!section.isConnected) {
            if (section.dataset.mounted) clearInterval(timer);
            return;
        }
        section.dataset.mounted = "1";
        meter.set(app.voice?.level || 0, settings.get("voiceThreshold"));
    }, 100);
    return section;
}

/** The gear sheet of the voice room. */
export function openVoiceSettings() {
    return panel(t("vc.settingsTitle"), voiceSettings());
}
