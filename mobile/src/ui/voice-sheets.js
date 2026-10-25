// voice-sheets.js — bottom sheets of the voice room: a generic panel,
// per-member volume, whisper targets and the microphone test.
import { app } from "../app.js";
import * as settings from "../core/settings.js";
import { LevelMeter, installAudioUnlock } from "../core/voice-audio.js";
import { MAX_VOLUME, meterFraction, METER_MAX, volumeFromPercent, volumePercent } from "../core/voice-gate.js";
import { openMicrophone } from "../core/voice.js";
import { t } from "../i18n.js";
import { avatar, h, toast } from "./dom.js";
import "./voice-strings.js";
import "./voice.css";

/**
 * A bottom sheet holding arbitrary content. Escape, the backdrop and the
 * close button dismiss it; focus returns where it was. Returns close().
 */
export function panel(title, content, { onClose } = {}) {
    const previous = document.activeElement;
    let closed = false;
    const close = () => {
        if (closed) return;
        closed = true;
        overlay.remove();
        document.removeEventListener("keydown", onKey, true);
        onClose?.();
        if (previous?.isConnected) previous.focus?.();
    };
    const onKey = event => { if (event.key === "Escape") { event.stopPropagation(); close(); } };
    const overlay = h("div", { class: "sheet-overlay", onclick: event => { if (event.target === overlay) close(); } },
        h("div", { class: "sheet vc-panel", role: "dialog", "aria-modal": "true", "aria-label": title },
            h("div", { class: "vc-panel-head" },
                h("h2", { class: "sheet-title" }, title),
                h("button", { class: "btn", type: "button", onclick: close }, t("vc.close"))),
            content));
    document.body.append(overlay);
    document.addEventListener("keydown", onKey, true);
    overlay.querySelector("input, select, button")?.focus();
    return close;
}

/** A horizontal level meter with a threshold marker. */
export function levelMeter(label) {
    const fill = h("span", { class: "vc-meter-fill" });
    const mark = h("span", { class: "vc-meter-mark", "aria-hidden": "true" });
    const node = h("div", { class: "vc-meter", role: "meter", "aria-label": label, "aria-valuemin": "0", "aria-valuemax": "100", "aria-valuenow": "0" }, fill, mark);
    return {
        node,
        set(level, threshold) {
            const value = Math.round(meterFraction(level) * 100);
            fill.style.width = `${value}%`;
            node.setAttribute("aria-valuenow", String(value));
            node.classList.toggle("over", level >= threshold);
            mark.style.left = `${meterFraction(threshold) * 100}%`;
        },
    };
}

/** A labelled range input bound to a value; returns the wrapping label. */
export function slider({ label, min, max, step, value, format, onInput, onChange }) {
    const output = h("output", { class: "vc-slider-value mono" }, format(value));
    const input = h("input", { type: "range", min: String(min), max: String(max), step: String(step), value: String(value), "aria-label": label,
        oninput: () => { output.textContent = format(Number(input.value)); onInput?.(Number(input.value)); },
        onchange: () => onChange?.(Number(input.value)) });
    return h("label", { class: "vc-slider" }, h("span", { class: "vc-slider-head" }, h("span", {}, label), output), input);
}

export function toggleRow(label, checked, onChange, hint) {
    const input = h("input", { type: "checkbox", role: "switch", checked, onchange: () => onChange(input.checked) });
    return h("label", { class: "toggle" },
        h("span", { class: "row-main" }, h("span", { class: "row-title" }, label), hint ? h("span", { class: "row-sub" }, hint) : null),
        input);
}

const percent = value => `${Math.round(value)} %`;

function setMap(name, uid, value, fallback = 1) {
    const map = { ...(settings.get(name) || {}) };
    if (value === fallback) delete map[uid];
    else map[uid] = value;
    settings.set({ [name]: map });
}

/** Volume, "mute for me" and moderation of one member, from a tap on their tile. */
export function memberSheet(voice, member) {
    const session = app.session;
    const uid = member.unique_id;
    const muted = (settings.get("mutedUsers") || []).includes(uid);
    const channel = session.channels().find(c => c.id === voice.channelID);
    const content = h("div", { class: "vc-stack" },
        h("div", { class: "row" }, avatar(member.nickname, "md"), h("span", { class: "row-main" },
            h("span", { class: "row-title" }, member.nickname),
            member.hand_raised_order ? h("span", { class: "row-sub" }, t("vc.handRaised")) : null)),
        slider({ label: t("vc.userVolume"), min: 0, max: MAX_VOLUME * 100, step: 5, value: volumePercent(settings.get("userVolumes")?.[uid] ?? 1), format: percent,
            onInput: value => setMap("userVolumes", uid, volumeFromPercent(value)) }),
        toggleRow(t("vc.muteForMe"), muted, on => {
            const list = new Set(settings.get("mutedUsers") || []);
            if (on) list.add(uid); else list.delete(uid);
            settings.set({ mutedUsers: [...list] });
        }, t("vc.muteForMeHint")),
        slider({ label: t("vc.shareVolume"), min: 0, max: MAX_VOLUME * 100, step: 5, value: volumePercent(settings.get("shareVolumes")?.[uid] ?? 1), format: percent,
            onInput: value => setMap("shareVolumes", uid, volumeFromPercent(value)) }),
        h("button", { class: "btn btn-block", type: "button", onclick: () => {
            setMap("userVolumes", uid, 1);
            setMap("shareVolumes", uid, 1);
            close();
        } }, t("vc.resetVolume")),
        channel?.canManageVoiceQueue && member.hand_raised_order ? h("button", { class: "btn btn-block", type: "button", onclick: () => {
            voice.queue("lower", member.client_id).catch(error => toast(t("vc.queueFailed", { error: error.message }), "warn"));
            close();
        } }, t("vc.lowerTheirHand")) : null);
    const close = panel(t("vc.memberTitle", { name: member.nickname }), content);
    return close;
}

/** Choose whisper targets: members and channels. */
export function whisperSheet(voice, { onSaved } = {}) {
    const session = app.session;
    const targets = voice.whisperTargets;
    const users = new Set(targets.uniqueIDs);
    const channels = new Set(targets.channelIDs);
    const seen = new Set();
    const members = session.clients().filter(c => c.client_id !== session.clientID && c.channel_id > 0 && c.unique_id && !seen.has(c.unique_id) && seen.add(c.unique_id));
    const check = (label, sub, set, key) => h("label", { class: "toggle" },
        h("span", { class: "row-main" }, h("span", { class: "row-title" }, label), sub ? h("span", { class: "row-sub" }, sub) : null),
        h("input", { type: "checkbox", checked: set.has(key), onchange: event => { if (event.target.checked) set.add(key); else set.delete(key); } }));
    const voiceChannels = session.channels().filter(c => !c.textOnly && !c.home);
    const content = h("div", { class: "vc-stack" },
        h("p", { class: "hint" }, t("vc.whisperHint")),
        h("p", { class: "section-label" }, t("vc.whisperMembers")),
        members.length ? h("div", { class: "vc-checks" }, members.map(m => check(m.nickname, session.channels().find(c => c.id === m.channel_id)?.name, users, m.unique_id)))
            : h("p", { class: "hint" }, t("vc.whisperNobody")),
        h("p", { class: "section-label" }, t("vc.whisperChannels")),
        h("div", { class: "vc-checks" }, voiceChannels.map(c => check(c.name, null, channels, c.id))),
        h("button", { class: "btn btn-primary btn-block", type: "button", onclick: () => {
            voice.setWhisperTargets({ uniqueIDs: [...users], channelIDs: [...channels] });
            close();
            onSaved?.();
        } }, t("vc.whisperSave")));
    const close = panel(t("vc.whisperTitle"), content);
    return close;
}

/** The number of seconds a test recording lasts. */
const RECORD_SECONDS = 5;

/**
 * Microphone test: live meter with the voice-activation threshold, a five
 * second recording played back, and the server's network echo channel.
 */
export function micTestSheet(voice) {
    installAudioUnlock();
    const meter = levelMeter(t("vc.micLevel"));
    let stream = null;
    let levels = null;
    let timer = 0;
    let recorder = null;
    let playback = null;
    const status = h("p", { class: "hint", role: "status" });
    const record = h("button", { class: "btn btn-block", type: "button" }, t("vc.record", { seconds: RECORD_SECONDS }));
    const echo = h("button", { class: "btn btn-block", type: "button" }, t("vc.echoTest"));

    async function start() {
        try {
            // In a call the call's own microphone is measured (a second
            // capture would steal it on iOS).
            stream = voice?.active ? voice.monitorStream() : null;
            stream ||= await openMicrophone();
            levels = new LevelMeter(stream);
        } catch (error) {
            status.textContent = t("vc.micUnavailable", { error: error?.message || String(error) });
            return;
        }
        timer = setInterval(() => meter.set(levels.level(), settings.get("voiceThreshold")), 50);
    }

    record.onclick = () => {
        if (!stream || !globalThis.MediaRecorder) {
            status.textContent = t("vc.recordUnsupported");
            return;
        }
        record.disabled = true;
        const chunks = [];
        recorder = new MediaRecorder(stream);
        recorder.ondataavailable = event => { if (event.data.size) chunks.push(event.data); };
        recorder.onstop = () => {
            record.disabled = false;
            recorder = null;
            if (!chunks.length) return;
            playback?.pause();
            playback = h("audio", { class: "vc-playback", controls: true, "aria-label": t("vc.playback") });
            playback.src = URL.createObjectURL(new Blob(chunks, { type: chunks[0].type }));
            record.after(playback);
            status.textContent = t("vc.playing");
            playback.play().catch(() => {});
        };
        recorder.start();
        status.textContent = t("vc.recording", { seconds: RECORD_SECONDS });
        setTimeout(() => { if (recorder?.state === "recording") recorder.stop(); }, RECORD_SECONDS * 1000);
    };

    echo.onclick = async () => {
        echo.disabled = true;
        try {
            if (!voice) throw new Error(t("vc.notConnected"));
            if (voice.echo) await voice.endEchoTest();
            else if (!await voice.startEchoTest()) status.textContent = t("vc.echoUnavailable");
            else status.textContent = t("vc.echoRunning");
        } catch (error) {
            status.textContent = t("vc.echoFailed", { error: error.message });
        }
        echo.textContent = voice?.echo ? t("vc.echoStop") : t("vc.echoTest");
        echo.disabled = false;
    };
    if (voice?.echo) echo.textContent = t("vc.echoStop");

    const content = h("div", { class: "vc-stack" },
        h("p", { class: "hint" }, t("vc.micTestHint")),
        meter.node,
        slider({ label: t("vc.threshold"), min: 0, max: METER_MAX, step: 0.005, value: settings.get("voiceThreshold"), format: v => `${Math.round(meterFraction(v) * 100)} %`,
            onInput: value => settings.set({ voiceThreshold: value }) }),
        record,
        echo,
        status);
    const close = panel(t("vc.micTest"), content, { onClose: () => {
        clearInterval(timer);
        if (recorder?.state === "recording") recorder.stop();
        playback?.pause();
        levels?.close();
        stream?.getTracks().forEach(track => track.stop());
    } });
    start();
    return close;
}
