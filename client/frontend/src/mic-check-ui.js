import { MicCheck, micDB } from "./mic-check.js";
import { t } from "./i18n.js";
import { captureConstraints } from "./audio.js";
import "./mic-check.css";

function element(tag, className, text) {
    const node = document.createElement(tag); node.className = className;
    if (text) node.textContent = text;
    return node;
}
const position = db => `${Math.max(0, Math.min(100, (db + 60) / 60 * 100))}%`;
const decibels = db => db <= -60 ? "≤−60" : db.toFixed(1);
const text = (node, value) => { if (node.textContent !== value) node.textContent = value; };

export function createMicCheck(settings, { onStart }) {
    const root = element("section", "mic-check"); root.setAttribute("aria-label", t("mic.title"));
    root.append(element("h3", "set-subhead", t("mic.title")), element("p", "set-hint", t("mic.preview")));
    const actions = element("div", "mic-test");
    const button = (key, handler, parent = actions) => {
        const node = element("button", "", t(key)); node.type = "button"; node.onclick = handler; parent.append(node); return node;
    };
    const start = button("mic.start", () => begin("test"));
    const record = button("mic.record", () => begin("record"));
    const stop = button("settings.stop", () => { mic.stop(); status.textContent = t("settings.mic.stopped"); }); stop.hidden = true;
    const device = element("p", "set-hint mic-device");
    const meter = element("div", "mic-meter-control");
    const bar = element("div", "mic-bar");
    bar.setAttribute("role", "meter"); bar.setAttribute("aria-label", t("mic.meter"));
    bar.setAttribute("aria-valuemin", "-60"); bar.setAttribute("aria-valuemax", "0"); bar.setAttribute("aria-valuenow", "-60");
    const fill = element("div", "mic-fill"), peak = element("span", "mic-peak"), threshold = element("span", "mic-threshold");
    bar.append(fill, peak);
    const thresholdControl = element("div", "mic-threshold-control");
    thresholdControl.tabIndex = 0;
    thresholdControl.setAttribute("role", "slider");
    thresholdControl.setAttribute("aria-label", t("settings.vad.threshold"));
    thresholdControl.setAttribute("aria-valuemin", "1"); thresholdControl.setAttribute("aria-valuemax", "100");
    thresholdControl.append(threshold);
    meter.append(bar, thresholdControl);
    const scale = element("div", "mic-scale"); scale.setAttribute("aria-hidden", "true");
    for (const db of [-60, -48, -36, -24, -12, 0]) scale.append(element("span", "", String(db)));
    const readings = element("p", "mic-readings");
    const status = element("p", "set-hint", t("mic.idle")); status.id = "mic-test-status"; status.setAttribute("role", "status");
    const transmission = element("p", "mic-transmission"); transmission.setAttribute("role", "status");
    const thresholdLabel = element("p", "set-hint mic-threshold-label");
    const thresholdField = element("label", "mic-threshold-field");
    const thresholdNumber = element("input", ""); thresholdNumber.type = "number";
    thresholdNumber.min = "1"; thresholdNumber.max = "100"; thresholdNumber.step = "1";
    thresholdNumber.setAttribute("aria-label", t("settings.vad.threshold"));
    thresholdField.append(document.createTextNode(t("settings.vad.threshold") + " "), thresholdNumber, document.createTextNode(" %"));
    const thresholdHelp = element("p", "set-hint", t("mic.adjustThreshold"));
    const ptt = button("mic.ptt", undefined); ptt.hidden = true; ptt.setAttribute("aria-pressed", "false");
    let held = false, lastAbove = -Infinity, lastMode = "", suggested = null, previewThreshold = null, playbackError = false;
    function refreshThreshold() {
        const value = previewThreshold ?? settings.vad_threshold ?? 50;
        const enabled = previewThreshold !== null || settings.activation_mode === "vad";
        thresholdControl.hidden = thresholdField.hidden = thresholdHelp.hidden = thresholdLabel.hidden = !enabled;
        threshold.style.left = position(micDB(value / 100 * 0.2));
        thresholdControl.setAttribute("aria-valuenow", String(value));
        thresholdControl.setAttribute("aria-valuetext", `${value}% (${decibels(micDB(value / 100 * 0.2))} dBFS)`);
        thresholdNumber.value = value;
        thresholdLabel.textContent = t("mic.threshold", { value });
    }
    function setThreshold(value) {
        if (!Number.isFinite(value)) return;
        settings.vad_threshold = Math.max(1, Math.min(100, Math.round(value)));
        previewThreshold = null; lastAbove = -Infinity;
        refreshThreshold();
        root.dispatchEvent(new Event("input", { bubbles: true }));
    }
    const pointerThreshold = event => {
        const bounds = thresholdControl.getBoundingClientRect();
        if (!bounds.width) return;
        const db = Math.max(0, Math.min(1, (event.clientX - bounds.left) / bounds.width)) * 60 - 60;
        setThreshold(10 ** (db / 20) / 0.2 * 100);
    };
    thresholdControl.onpointerdown = event => {
        if (event.button !== 0) return;
        event.preventDefault(); thresholdControl.focus();
        thresholdControl.setPointerCapture(event.pointerId); pointerThreshold(event);
    };
    thresholdControl.onpointermove = event => { if (thresholdControl.hasPointerCapture(event.pointerId)) pointerThreshold(event); };
    thresholdControl.onpointerup = thresholdControl.onpointercancel = event => {
        if (thresholdControl.hasPointerCapture(event.pointerId)) thresholdControl.releasePointerCapture(event.pointerId);
    };
    thresholdControl.onkeydown = event => {
        const value = previewThreshold ?? settings.vad_threshold ?? 50;
        const values = { ArrowLeft: value - 1, ArrowDown: value - 1, ArrowRight: value + 1, ArrowUp: value + 1,
            PageDown: value - 10, PageUp: value + 10, Home: 1, End: 100 };
        if (Object.hasOwn(values, event.key)) { event.preventDefault(); setThreshold(values[event.key]); }
    };
    thresholdNumber.oninput = () => { if (thresholdNumber.validity.valid && thresholdNumber.value !== "") setThreshold(thresholdNumber.valueAsNumber); };
    thresholdNumber.onchange = () => { setThreshold(thresholdNumber.valueAsNumber); refreshThreshold(); };
    refreshThreshold();
    const hold = down => { held = down; ptt.setAttribute("aria-pressed", String(down)); };
    ptt.onpointerdown = event => { ptt.setPointerCapture(event.pointerId); hold(true); };
    ptt.onpointerup = ptt.onpointercancel = ptt.onlostpointercapture = ptt.onblur = () => hold(false);
    ptt.onkeydown = event => { if ([" ", "Enter"].includes(event.key)) { event.preventDefault(); hold(true); } };
    ptt.onkeyup = () => hold(false);
    const shortcut = element("p", "set-hint mic-shortcut");
    const keyStatus = element("span", "mic-key-status");
    const loopLabel = element("label", "mic-loopback");
    const loop = element("input", ""); loop.type = "checkbox"; loop.disabled = true;
    loopLabel.append(loop, document.createTextNode(t("settings.loopback.test.hear.yourself.use.headphones")));
    const output = element("p", "set-hint mic-output");
    const recordings = element("div", "mic-test");
    const listen = button("mic.listen", async () => {
        if (!mic.recordingURL) return;
        try { await mic.play(mic.recordingURL, settings.playback_device_id); }
        catch (error) { outputError(error); }
    }, recordings); listen.disabled = true;
    button("mic.stopPlayback", () => mic.stopPlayback(), recordings);
    const calibration = element("div", "mic-test");
    const calibrate = button("mic.calibrate", () => begin("calibration"), calibration);
    const calStatus = element("p", "set-hint"); calStatus.id = "mic-calibration-status"; calStatus.setAttribute("role", "status");
    const preview = button("mic.previewThreshold", () => { previewThreshold = suggested; begin("test", true); }, calibration); preview.hidden = true;
    const use = button("mic.use", () => {
        setThreshold(suggested); use.hidden = preview.hidden = true;
    }, calibration); use.hidden = true;
    root.append(actions, device, meter, scale, thresholdField, thresholdHelp, readings, status, thresholdLabel, transmission, shortcut, keyStatus,
        loopLabel, output, element("p", "set-hint", t("mic.recordHelp")), recordings, calibration, calStatus);
    let events = null;
    const channel = () => window.__noxa.state.channels?.find(ch => ch.ChannelID === window.__noxa.state.myChannelID);
    const profile = () => JSON.stringify([captureConstraints(channel(), settings), settings.playback_device_id || ""]);
    let currentProfile = profile();
    const outputError = error => { playbackError = true; loop.checked = false; status.textContent = t("mic.outputFailed", { error: error.message || String(error) }); };
    const dispose = () => { events?.abort(); events = null; hold(false); mic.dispose(); };
    const mic = new MicCheck({
        onState: (state, mode) => {
            const busy = state !== "idle";
            start.disabled = record.disabled = calibrate.disabled = busy;
            stop.hidden = !busy; loop.disabled = state !== "active" || mode !== "test";
            ptt.hidden = state !== "active" || mode !== "test" || settings.activation_mode !== "ptt";
            if (!busy) {
                loop.checked = false; hold(false); transmission.textContent = "";
                fill.style.width = "0%"; peak.style.left = "0%"; bar.setAttribute("aria-valuenow", "-60");
            }
            refreshThreshold();
            if (state === "requesting") {
                status.textContent = t("settings.mic.requesting"); listen.disabled = true;
                device.textContent = ""; readings.textContent = ""; hold(false); lastAbove = -Infinity;
            }
        },
        onDevice: name => { device.textContent = t("mic.device", { name: name || t("mic.deviceUnknown") }); },
        onLevel: level => {
            fill.style.width = position(level.db); peak.style.left = position(level.heldPeakDB);
            bar.dataset.quality = level.quality; bar.setAttribute("aria-valuenow", level.db.toFixed(1));
            readings.textContent = `${t("mic.average", { level: decibels(level.db) })} · ${t("mic.peak", { level: decibels(level.heldPeakDB) })}`;
            if (!mic.current) return;
            if (mic.current.mode === "test" && !playbackError) text(status, t("mic." + level.quality));
            const mode = previewThreshold !== null ? "vad" : settings.activation_mode || "ptt";
            if (mode !== lastMode) { hold(false); lastAbove = -Infinity; lastMode = mode; }
            const value = previewThreshold ?? settings.vad_threshold ?? 50;
            ptt.hidden = mode !== "ptt" || mic.current.mode !== "test";
            const above = level.mean > value / 100 * 0.2;
            if (above) lastAbove = performance.now();
            const active = mode === "continuous" || (mode === "ptt" ? held : performance.now() - lastAbove < 300);
            transmission.dataset.active = String(active);
            text(transmission, t(active ? mode === "vad" && !above ? "mic.release" : "mic.transmit" : mode === "ptt" ? "mic.waitPTT" : "mic.below"));
            const binding = window.__noxa.state.pttShortcutStatus;
            const key = binding?.registered === false ? "" : binding?.spec ?? window.__noxa.state.settings?.hotkey_ptt;
            shortcut.textContent = key ? t("mic.shortcut", { key }) : t("mic.noShortcut"); shortcut.hidden = keyStatus.hidden = mode !== "ptt";
        },
        onCountdown: (seconds, phase) => { (phase === "record" ? status : calStatus).textContent = t("mic." + (phase === "record" ? "recording" : phase), { seconds }); },
        onCalibrated: result => {
            suggested = result.valid ? result.suggested : null;
            use.hidden = preview.hidden = !result.valid;
            calStatus.textContent = result.valid ? t("mic.suggestion", { value: suggested }) : t("mic.calibrationFailed");
        },
        onRecorded: () => { listen.disabled = false; record.textContent = t("mic.again"); status.textContent = t("mic.recorded"); },
        onError: (error, mode) => {
            const target = mode === "calibration" ? calStatus : status;
            const key = { NotAllowedError: "settings.mic.denied", SecurityError: "settings.mic.denied", NotFoundError: "settings.mic.missing", NotReadableError: "settings.mic.unavailable" }[error.name];
            target.textContent = key ? t(key) : t("settings.mic.test.failed") + (error.message || error.name);
        },
    });
    function begin(mode, keepSuggestion = false) {
        onStart(dispose);
        playbackError = false;
        currentProfile = profile();
        if (!keepSuggestion) { suggested = previewThreshold = null; use.hidden = preview.hidden = true; calStatus.textContent = ""; }
        events = new AbortController();
        window.addEventListener("blur", () => hold(false), { signal: events.signal });
        window.addEventListener("noxa-mic-test-ptt", event => {
            if (!root.isConnected || !mic.current || mic.current.mode !== "test" || settings.activation_mode !== "ptt") return;
            event.preventDefault(); hold(event.detail === "ptt_down");
            keyStatus.textContent = t(held ? "mic.keyDetected" : "mic.keyReleased");
        }, { signal: events.signal });
        void mic.start(mode, captureConstraints(channel(), settings), settings.playback_device_id);
        output.textContent = t("mic.output", { name: settings.playback_device_id || t("settings.default.device") });
        void navigator.mediaDevices.enumerateDevices().then(devices => {
            if (!root.isConnected) return;
            const selected = devices.find(d => d.kind === "audiooutput" && d.deviceId === (settings.playback_device_id || "default"));
            if (selected?.label) output.textContent = t("mic.output", { name: selected.label });
        }).catch(() => {});
    }
    loop.onchange = async () => { playbackError = false; try { await mic.setLoopback(loop.checked); } catch (error) { if (root.isConnected) outputError(error); } };
    return { root, refresh: () => {
        refreshThreshold();
        if (currentProfile === profile()) return;
        currentProfile = profile();
        const mode = mic.current?.mode;
        mic.dispose(); listen.disabled = true; suggested = previewThreshold = null; use.hidden = preview.hidden = true; calStatus.textContent = "";
        if (mode) { begin(mode); calStatus.textContent = t("mic.changed"); }
    } };
}
