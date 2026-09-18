// settings-ui.js — TS3-style settings dialog with left icon nav.
import { icon } from "./icons.js";
import { currentLanguage, t } from "./i18n.js";
import { copyToClipboard } from "./clipboard.js";
import { captureConstraints } from "./audio.js";
import { MicCheck } from "./mic-check.js";
import { percentageInput } from "./percentage-input.js";
import { previewSounds, previewSpeech, speechPreviewLabel, audioStatus, SPEECH_EVENTS, stopPreviews, updateSoundOutput, SOUND_EVENT_GROUPS, testAll } from "./sounds.js";
import { MATRIX_EVENTS, defaultMatrixRow } from "./notifications.js";
import { associateControlLabel, wrappedIndex } from "./a11y.js";
import { createMediaDeviceInventory } from "./media-devices.js";
import { closeDialog, mountDialog } from "./modal.js";
import { cameraConstraints } from "./video.js";

const V = () => window.__noxa;

const PAGES = [
    { id: "application", icon: "settings", label: "settings.application" },
    { id: "capture", icon: "mic", label: "settings.capture" },
    { id: "playback", icon: "speaker", label: "settings.playback" },
    { id: "hotkeys", icon: "keyboard", label: "settings.hotkeys" },
    { id: "whisper", icon: "whisper", label: "settings.whisper" },
    { id: "downloads", icon: "download", label: "settings.downloads" },
    { id: "chat", icon: "chat", label: "settings.chat" },
    { id: "security", icon: "lock", label: "settings.security" },
    { id: "server", icon: "screen", label: "settings.server" },
    { id: "notifications", icon: "bell", label: "settings.notifications" },
];

let draft = null; // working copy of settings while the dialog is open
let stopCameraTest = () => {};
let stopMicCheck = () => {};

function settings() { return draft; }

class SavedAudioSettingsError extends Error {}

async function commit(snapshot) {
    const err = await window.go.main.App.SaveSettings(snapshot);
    if (err) throw new Error(err);
    // (282) the draft was cloned when the dialog opened: re-read the merged
    // truth so Go-owned fields (recents) written meanwhile survive.
    V().state.settings = await window.go.main.App.GetSettings();
    try {
        await V().applyLiveAudioSettings();
    } catch (error) {
        throw new SavedAudioSettingsError(t("settings.audioApplyFailed", { error: error.message || String(error) }), { cause: error });
    }
    void updateSoundOutput();
    // (126-129) chat display prefs apply live (CSS classes on #chat-log).
    if (V().applyChatPrefs) V().applyChatPrefs();
    // (294-297) appearance applies live (theme/accent/user CSS/font/compact).
    if (V().applyAppearance) V().applyAppearance();
    // (291) always-on-top and (292) opacity apply immediately.
    await window.go.main.App.SetAlwaysOnTop(!!snapshot.always_on_top);
    await window.go.main.App.SetWindowOpacity(snapshot.window_opacity || 100);
    return true;
}

function row(label, control) {
    const el = document.createElement("div");
    el.className = "set-row";
    const l = document.createElement("label");
    l.className = "set-label";
    l.textContent = label;
    el.appendChild(l);
    el.appendChild(control);
    let target = control.matches?.("input:not([type=hidden]), select, textarea") ? control : null;
    if (!target) {
        const nested = control.querySelectorAll?.("input:not([type=hidden]), select, textarea") || [];
        if (nested.length === 1) [target] = nested;
    }
    if (target) associateControlLabel(l, target);
    if (control.querySelector?.(".audio-percent")) {
        associateControlLabel(l, control.querySelector('input[type="range"]'));
        control.querySelector(".audio-percent").setAttribute("aria-label", label + " (%)");
    }
    return el;
}

function checkbox(checked, onchange) {
    const c = document.createElement("input");
    c.type = "checkbox";
    c.checked = !!checked;
    c.onchange = () => onchange(c.checked);
    return c;
}

function numberInput(value, min, max, onchange) {
    const i = document.createElement("input");
    i.type = "number";
    i.min = min; i.max = max; i.value = value;
    i.onchange = () => onchange(parseInt(i.value, 10));
    return i;
}

function slider(value, min, max, oninput, percent = false) {
    const wrap = document.createElement("div");
    wrap.className = "set-slider";
    const i = document.createElement("input");
    i.type = "range";
    i.min = min; i.max = max; i.value = value;
    const out = percent ? percentageInput(i) : document.createElement("span");
    out.classList.add("mono");
    if (!percent) out.textContent = value;
    i.oninput = () => { if (!percent) out.textContent = i.value; oninput(parseInt(i.value, 10)); };
    wrap.appendChild(i); wrap.appendChild(out);
    if (percent) { const unit = document.createElement("span"); unit.textContent = "%"; unit.setAttribute("aria-hidden", "true"); wrap.appendChild(unit); }
    return wrap;
}

function hint(text) {
    const el = document.createElement("div");
    el.className = "set-hint";
    el.textContent = text;
    return el;
}

// revertLivePreview puts the saved appearance back after a cancelled dialog:
// window opacity (292) and the theme swatches (295) both preview live, so
// discarding the draft has to discard what is on screen too.
function revertLivePreview() {
    const saved = V().state.settings || {};
    V().applyAppearance();
    window.go.main.App.SetWindowOpacity(saved.window_opacity || 100);
}

// --- device enumeration --------------------------------------------------------

const deviceInventory = createMediaDeviceInventory(() => {
    const mediaDevices = globalThis.navigator?.mediaDevices;
    if (typeof mediaDevices?.enumerateDevices !== "function") {
        throw new Error("media device discovery is not available");
    }
    return mediaDevices.enumerateDevices();
});

function deviceSelect(devices, selectedId, onchange) {
    const sel = document.createElement("select");
    const def = document.createElement("option");
    def.value = "";
    def.textContent = t("settings.default.device");
    sel.appendChild(def);
    for (const d of devices) {
        const o = document.createElement("option");
        o.value = d.deviceId;
        o.textContent = d.label || t("settings.device", { id: d.deviceId.slice(0, 8) });
        sel.appendChild(o);
    }
    sel.value = selectedId || "";
    sel.onchange = () => onchange(sel.value);
    return sel;
}

function devicePicker(kind, selectedId, onchange, label) {
    const wrap = document.createElement("div");
    wrap.className = "device-picker";

    let currentId = selectedId || "";
    let select = deviceSelect([], currentId, (value) => {
        currentId = value;
        onchange(value);
    });
    select.disabled = true;

    const refreshBtn = document.createElement("button");
    refreshBtn.type = "button";
    refreshBtn.className = "device-refresh";
    refreshBtn.textContent = t("settings.refresh.devices");
    refreshBtn.setAttribute("aria-label", t("settings.refresh", { label: currentLanguage() === "en" ? label.toLowerCase() : label }));

    const status = document.createElement("span");
    status.className = "set-hint device-status";
    status.setAttribute("role", "status");
    status.setAttribute("aria-live", "polite");

    let loaded = false;
    const deviceType = t(kind === "audioinput" ? "settings.devices.capture" : "settings.devices.playback");
    const refresh = async (force = false) => {
        currentId = select.value || currentId;
        if (!loaded) select.disabled = true;
        refreshBtn.disabled = true;
        refreshBtn.textContent = t("settings.refreshing");
        wrap.setAttribute("aria-busy", "true");
        status.classList.remove("warn");
        status.textContent = force ? t("settings.refreshing.audio.devices") : t("settings.loading.audio.devices");

        try {
            const inventory = await deviceInventory.load(force);
            const devices = inventory.filter((device) => device.kind === kind);
            const next = deviceSelect(devices, currentId, (value) => {
                currentId = value;
                onchange(value);
            });
            if (currentId && !devices.some((device) => device.deviceId === currentId)) {
                const unavailable = document.createElement("option");
                unavailable.value = currentId;
                unavailable.textContent = t("settings.saved.device.currently.unavailable");
                next.appendChild(unavailable);
                next.value = currentId;
            }
            // row() associates the initial select with its visible label.
            // Preserve that ID so the replacement remains labelled.
            next.id = select.id;
            select.replaceWith(next);
            select = next;
            loaded = true;
            status.textContent = force
                ? t("settings.devices.refreshed", { count: devices.length, type: deviceType })
                : "";
            if (devices.length === 0) {
                status.textContent = t("settings.devices.empty", { type: deviceType });
            }
        } catch (error) {
            if (!loaded) {
                select.options[0].textContent = t("settings.devices.unavailable");
                select.disabled = true;
            }
            status.classList.add("warn");
            const detail = error?.message || error?.name || t("settings.unknown.error");
            status.textContent = t("settings.devices.failed", { detail });
        } finally {
            refreshBtn.disabled = false;
            refreshBtn.textContent = t("settings.refresh.devices");
            wrap.removeAttribute("aria-busy");
        }
    };

    refreshBtn.onclick = () => refresh(true);
    wrap.append(select, refreshBtn, status);
    refresh();
    return wrap;
}

// --- pages ---------------------------------------------------------------------

function pageApplication() {
    const s = settings();
    const el = document.createElement("div");
    const langSel = document.createElement("select");
    for (const [value, label] of [["system", t("settings.system.default")], ["en", "English"], ["de", "Deutsch"]]) {
        const option = document.createElement("option");
        option.value = value;
        option.textContent = label;
        langSel.appendChild(option);
    }
    langSel.value = s.language || "system";
    langSel.onchange = () => { s.language = langSel.value; };
    el.appendChild(row(t("settings.language"), langSel));
    el.appendChild(hint(t("settings.language.help")));
    el.appendChild(row(t("settings.chat.max.lines"), numberInput(s.chat_max_lines, 10, 5000, (v) => { s.chat_max_lines = v; })));
    el.appendChild(row(t("settings.toasts.for.join.leave"), checkbox(s.notify_join_leave, (v) => { s.notify_join_leave = v; })));
    el.appendChild(row(t("settings.toasts.for.connection.events"), checkbox(s.notify_connection, (v) => { s.notify_connection = v; })));
    el.appendChild(row(t("settings.reconnect.on.connection.loss.5.tries"), checkbox(s.reconnect_on_loss, (v) => { s.reconnect_on_loss = v; })));
    el.appendChild(row(t("settings.check.for.updates.at.startup"), checkbox(s.updates_auto_check !== false, (v) => { s.updates_auto_check = v; })));

    // Presence (308/390): the idle timer and the status line it publishes.
    const psep = document.createElement("div");
    psep.className = "set-subhead";
    psep.textContent = t("settings.presence");
    el.appendChild(psep);
    el.appendChild(row(t("settings.auto.away.after.minutes.0.off"), numberInput(s.auto_away_minutes ?? 15, 0, 240, (v) => { s.auto_away_minutes = v; })));
    const awayMsg = document.createElement("input");
    awayMsg.className = "dlg-input";
    awayMsg.maxLength = 200;
    awayMsg.placeholder = t("settings.auto.away");
    awayMsg.value = s.auto_away_message ?? "";
    awayMsg.onchange = () => { s.auto_away_message = awayMsg.value; };
    el.appendChild(row(t("settings.auto.away.status.message"), awayMsg));
    el.appendChild(hint(t("settings.other.clients.see.this.text.next.to.your.away.icon.while.you.are.idle")));

    // Window / system integration (wave 8a).
    const sep = document.createElement("div");
    sep.className = "set-subhead";
    sep.textContent = t("settings.window.appearance");
    el.appendChild(sep);
    const themeSel = document.createElement("select");
    for (const [v, label] of [["dark", t("settings.dark.default")], ["light", t("settings.light")], ["hc", t("settings.high.contrast")]]) {
        const o = document.createElement("option");
        o.value = v;
        o.textContent = label;
        themeSel.appendChild(o);
    }
    themeSel.value = s.theme || "dark";
    themeSel.onchange = () => { s.theme = themeSel.value; };
    el.appendChild(row(t("settings.theme"), themeSel));
    const accent = document.createElement("input");
    accent.type = "color";
    accent.value = s.accent_color || "#2ee6a8";
    accent.onchange = () => { s.accent_color = accent.value; };
    el.appendChild(row(t("settings.accent.color"), accent));
    const fontSel = document.createElement("select");
    for (const [v, label] of [["outfit", "Outfit"], ["sora", "Sora"], ["jetbrains", "JetBrains Mono"]]) {
        const o = document.createElement("option");
        o.value = v;
        o.textContent = label;
        fontSel.appendChild(o);
    }
    fontSel.value = s.ui_font || "outfit";
    fontSel.onchange = () => { s.ui_font = fontSel.value; };
    el.appendChild(row(t("settings.ui.font"), fontSel));
    el.appendChild(row(t("settings.ui.font.size"), slider(s.ui_font_size || 14, 10, 20, (v) => { s.ui_font_size = v; })));
    el.appendChild(row(t("settings.always.on.top"), checkbox(s.always_on_top, (v) => { s.always_on_top = v; })));
    el.appendChild(row(t("settings.compact.mode"), checkbox(s.compact_mode, (v) => { s.compact_mode = v; })));
    el.appendChild(row(t("settings.reduce.motion"), checkbox(s.reduce_motion, (v) => { s.reduce_motion = v; })));
    el.appendChild(row(t("settings.pause.video.when.unfocused"), checkbox(s.idle_video_pause !== false, (v) => { s.idle_video_pause = v; })));
    el.appendChild(row(t("settings.close.to.tray"), checkbox(s.close_to_tray, (v) => { s.close_to_tray = v; })));
    el.appendChild(row(t("settings.minimize.to.tray"), checkbox(s.minimize_to_tray, (v) => { s.minimize_to_tray = v; })));
    // (292) the floor keeps the window clickable. Applied on release, not per
    // drag frame: the binding persists the settings file on every call.
    const opacity = slider(s.window_opacity || 100, 20, 100, (v) => { s.window_opacity = v; });
    opacity.querySelector("input").addEventListener("change",
        () => window.go.main.App.SetWindowOpacity(s.window_opacity || 100));
    el.appendChild(row(t("settings.window.opacity"), opacity));
    el.appendChild(themeEditor(s)); // (295)
    const css = document.createElement("textarea");
    css.className = "dlg-input user-css";
    css.rows = 4;
    css.placeholder = t("settings.custom.css.overrides.e.g.channel.letter.spacing.0.5px");
    css.value = s.user_css || "";
    css.onchange = () => { s.user_css = css.value; };
    el.appendChild(row(t("settings.user.css"), css));
    return el;
}

function pageServer() {
    const el = document.createElement("div");
    const status = hint(t("settings.loading.effective.server.configuration"));
    el.appendChild(status);
    if (!V().state.isAdmin) {
        status.textContent = t("settings.server.configuration.is.available.to.administrators.only");
        return el;
    }

    const form = document.createElement("div");
    form.hidden = true;
    el.appendChild(form);
    const values = {};
    const addNumber = (label, key, min, max) => {
        const input = numberInput(0, min, max, (v) => { values[key] = v; });
        form.appendChild(row(label, input));
        values[key] = 0;
        return input;
    };
    const maxClients = addNumber(t("settings.maximum.clients.0.unlimited"), "max_clients", 0, 100000);
    const timeout = addNumber(t("settings.connection.timeout.seconds"), "client_timeout_seconds", 30, 86400);
    const bitrate = addNumber(t("settings.default.opus.bitrate.bit.s"), "opus_bitrate", 6000, 510000);
    const fec = checkbox(false, (v) => { values.opus_fec = v; });
    const dtx = checkbox(false, (v) => { values.opus_dtx = v; });
    const stereo = checkbox(false, (v) => { values.opus_stereo = v; });
    form.appendChild(row(t("settings.default.opus.in.band.fec"), fec));
    form.appendChild(row(t("settings.default.opus.dtx"), dtx));
    form.appendChild(row(t("settings.default.opus.stereo"), stereo));
    form.appendChild(hint(t("settings.codec.defaults.apply.to.newly.created.channels.existing.channels.keep.their")));
    const applyToForm = (cfg) => {
        Object.assign(values, cfg);
        maxClients.value = cfg.max_clients;
        timeout.value = cfg.client_timeout_seconds;
        bitrate.value = cfg.opus_bitrate;
        fec.checked = !!cfg.opus_fec;
        dtx.checked = !!cfg.opus_dtx;
        stereo.checked = !!cfg.opus_stereo;
    };
    const apply = document.createElement("button");
    apply.textContent = t("settings.apply.server.configuration");
    apply.onclick = async () => {
        apply.disabled = true;
        try {
            const result = await window.go.main.App.SetServerConfig(values);
            applyToForm(result);
            status.textContent = t("settings.server.configuration.saved.and.active");
            V().toast(t("settings.server.configuration.updated"));
        } catch (err) {
            status.textContent = t("settings.could.not.save.server.configuration") + err;
            V().toast(status.textContent, "warn");
        } finally {
            apply.disabled = false;
        }
    };
    form.appendChild(row(t("settings.runtime.settings"), apply));

    window.go.main.App.GetServerConfig().then((cfg) => {
        applyToForm(cfg);
        status.textContent = t("settings.changes.take.effect.immediately.and.are.restored.after.restart");
        form.hidden = false;
    }).catch((err) => {
        status.textContent = t("settings.could.not.load.server.configuration") + err;
    });
    return el;
}

// --- theme editor (295) --------------------------------------------------------

// THEME_VARS are the palette variables the editor exposes. Only the opaque
// ones: a color input cannot express the rgba() borders and shadows, and
// --accent already has its own control (296) whose inline style would win.
const THEME_VARS = [
    ["--bg", "settings.background"],
    ["--bg-raised", "settings.raised.surface"],
    ["--bg-panel", "settings.panel"],
    ["--bg-hover", "settings.hover"],
    ["--text", "settings.text"],
    ["--text-dim", "settings.text.dim"],
    ["--text-faint", "settings.text.faint"],
    ["--warn", "settings.warning"],
    ["--danger", "settings.danger"],
];

// The editor owns the block between these markers inside user_css; anything
// the user typed by hand around it survives an edit.
const THEME_START = "/* noxa-theme-start */";
const THEME_END = "/* noxa-theme-end */";

// themeBlockBody returns the CSS between the markers ("" when absent).
function themeBlockBody(css) {
    const a = (css || "").indexOf(THEME_START);
    const b = (css || "").indexOf(THEME_END);
    return a >= 0 && b > a ? css.slice(a + THEME_START.length, b) : "";
}

// parseThemeOverrides reads the editor's own block back into a map.
function parseThemeOverrides(css) {
    const out = {};
    for (const m of themeBlockBody(css).matchAll(/(--[a-z-]+)\s*:\s*([^;]+);/g)) {
        out[m[1]] = m[2].trim();
    }
    return out;
}

// writeThemeOverrides splices the block back into user_css. The selector
// carries an attribute so it outranks the :root[data-theme="…"] palettes,
// which a bare :root block would lose to.
function writeThemeOverrides(css, overrides) {
    const keys = Object.keys(overrides);
    const block = keys.length === 0 ? "" : THEME_START + "\n:root, :root[data-theme] {\n" +
        keys.map((k) => `    ${k}: ${overrides[k]};`).join("\n") + "\n}\n" + THEME_END;
    const a = (css || "").indexOf(THEME_START);
    const b = (css || "").indexOf(THEME_END);
    if (a >= 0 && b > a) {
        return (css.slice(0, a) + block + css.slice(b + THEME_END.length)).trim();
    }
    return (block + "\n" + (css || "")).trim();
}

// currentVar resolves a variable to a #rrggbb value for the color input.
function currentVar(name, overrides) {
    const raw = overrides[name] || getComputedStyle(document.documentElement).getPropertyValue(name).trim();
    const rgb = raw.match(/^rgba?\((\d+)[,\s]+(\d+)[,\s]+(\d+)/);
    if (rgb) {
        return "#" + [1, 2, 3].map((i) => Number(rgb[i]).toString(16).padStart(2, "0")).join("");
    }
    // #abc shorthand: the color input only accepts the six-digit form.
    if (/^#[0-9a-f]{3}$/i.test(raw)) return "#" + raw.slice(1).split("").map((c) => c + c).join("");
    return /^#[0-9a-f]{6}$/i.test(raw) ? raw : "#000000";
}

// themeEditor builds the CSS-variable editor (295): every swatch rewrites the
// managed block in user_css, which applyAppearance injects as a stylesheet.
function themeEditor(s) {
    const wrap = document.createElement("div");
    const head = document.createElement("div");
    head.className = "set-subhead";
    head.textContent = t("settings.theme.colors");
    wrap.appendChild(head);
    const grid = document.createElement("div");
    grid.className = "theme-grid";
    const overrides = parseThemeOverrides(s.user_css);
    const apply = () => {
        s.user_css = writeThemeOverrides(s.user_css, overrides);
        // Preview by writing the same style element applyAppearance owns. A
        // full applyAppearance would rebuild the menu bar on every frame of a
        // swatch drag; cancelling the dialog calls it and restores the saved
        // stylesheet.
        let st = document.getElementById("user-css");
        if (!st) {
            st = document.createElement("style");
            st.id = "user-css";
            document.head.appendChild(st);
        }
        st.textContent = s.user_css;
    };
    for (const [name, label] of THEME_VARS) {
        const cell = document.createElement("label");
        cell.className = "theme-cell";
        const inp = document.createElement("input");
        inp.type = "color";
        inp.value = currentVar(name, overrides);
        inp.oninput = () => {
            overrides[name] = inp.value;
            apply();
        };
        const txt = document.createElement("span");
        txt.textContent = t(label);
        txt.title = name;
        cell.appendChild(inp);
        cell.appendChild(txt);
        grid.appendChild(cell);
    }
    wrap.appendChild(grid);
    const reset = document.createElement("button");
    reset.textContent = t("settings.reset.theme.colors");
    reset.onclick = () => {
        for (const k of Object.keys(overrides)) delete overrides[k];
        apply();
        for (const [i, [name]] of THEME_VARS.entries()) {
            grid.children[i].querySelector("input").value = currentVar(name, overrides);
        }
    };
    wrap.appendChild(reset);
    return wrap;
}

function pageCapture() {
    const s = settings();
    const el = document.createElement("div");
    el.appendChild(row(t("settings.capture.device"), devicePicker(
        "audioinput",
        s.capture_device_id,
        (v) => { s.capture_device_id = v; },
        t("settings.capture.devices"),
    )));

    // Activation mode.
    const modeWrap = document.createElement("div");
    modeWrap.className = "set-modes";
    for (const [id, label] of [["ptt", t("settings.push.to.talk")], ["vad", t("settings.voice.activity.detection")], ["continuous", t("settings.continuous.transmission")]]) {
        const l = document.createElement("label");
        const r = document.createElement("input");
        r.type = "radio";
        r.name = "actmode";
        r.checked = (s.activation_mode || "ptt") === id;
        r.onchange = () => {
            s.activation_mode = id;
            vadRow.style.display = id === "vad" ? "" : "none";
        };
        l.appendChild(r);
        l.appendChild(document.createTextNode(" " + label));
        modeWrap.appendChild(l);
    }
    el.appendChild(modeWrap);

    const vadRow = row(t("settings.vad.threshold"), slider(s.vad_threshold, 1, 100, (v) => { s.vad_threshold = v; }, true));
    vadRow.style.display = (s.activation_mode || "ptt") === "vad" ? "" : "none";
    el.appendChild(vadRow);

    el.appendChild(row(t("settings.echo.cancellation"), checkbox(s.echo_cancellation !== false, (v) => { s.echo_cancellation = v; })));
    el.appendChild(row(t("settings.noise.suppression"), checkbox(s.noise_suppression !== false, (v) => { s.noise_suppression = v; })));

    // Camera capture starts only when enabled or explicitly tested.
    const fpsSelect = document.createElement("select");
    for (const fps of [15, 30, 60]) {
        const o = document.createElement("option");
        o.value = String(fps);
        o.textContent = fps + " fps";
        fpsSelect.appendChild(o);
    }
    fpsSelect.value = String(s.camera_fps || 30);
    fpsSelect.onchange = () => { s.camera_fps = parseInt(fpsSelect.value, 10); };
    el.appendChild(row(t("settings.camera.frame.rate"), fpsSelect));
    el.appendChild(hint(t("settings.camera.is.off.when.joining.turn.it.on.in.the.voice.controls.or.test.it.here")));
    const cameraTest = document.createElement("div");
    const cameraBtn = document.createElement("button");
    cameraBtn.type = "button";
    cameraBtn.textContent = t("settings.test.camera");
    const preview = document.createElement("video");
    preview.autoplay = true;
    preview.playsInline = true;
    preview.muted = true;
    preview.hidden = true;
    preview.style.maxWidth = "100%";
    preview.setAttribute("aria-label", t("settings.camera.test.preview"));
    const cameraStatus = hint("");
    cameraStatus.setAttribute("role", "status");
    let testing = false;
    let testStream = null;
    const stop = () => {
        testing = false;
        testStream?.getTracks().forEach((track) => track.stop());
        testStream = null;
        preview.srcObject = null;
        preview.hidden = true;
        cameraBtn.disabled = false;
        cameraBtn.textContent = t("settings.test.camera");
    };
    cameraBtn.onclick = async () => {
        if (testing) { stop(); return; }
        stopCameraTest();
        stopCameraTest = stop;
        testing = true;
        cameraBtn.disabled = true;
        cameraStatus.textContent = "";
        try {
            const stream = await navigator.mediaDevices.getUserMedia({ audio: false, video: cameraConstraints(s) });
            if (!testing || !cameraTest.isConnected) {
                stream.getTracks().forEach((track) => track.stop());
                return;
            }
            testStream = stream;
            preview.srcObject = stream;
            preview.hidden = false;
            cameraBtn.textContent = t("settings.stop.camera.test");
        } catch (error) {
            if (testing) {
                stop();
                cameraStatus.textContent = t("settings.camera.test.failed") + (error.message || error.name);
            }
        } finally {
            cameraBtn.disabled = false;
        }
    };
    cameraTest.append(cameraBtn, preview, cameraStatus);
    el.appendChild(cameraTest);

    el.appendChild(row(t("settings.ptt.delay"), slider(s.ptt_release_delay_ms || 0, 0, 2000, (v) => { s.ptt_release_delay_ms = v; })));
    el.appendChild(hint(t("settings.capture.reconnect")));
    // (25) the music profile overrides these two, so say so where they live.
    el.appendChild(hint(t("settings.music.channels.stereo.96.kbit.s.or.more.capture.in.stereo.with.echo.cancell")));

    const testWrap = document.createElement("div");
    testWrap.className = "mic-test";
    const startBtn = document.createElement("button");
    startBtn.type = "button";
    startBtn.textContent = t("settings.begin.test");
    const stopBtn = document.createElement("button");
    stopBtn.type = "button";
    stopBtn.textContent = t("settings.stop");
    stopBtn.hidden = true;
    const bar = document.createElement("div");
    bar.className = "mic-bar"; bar.hidden = true;
    const fill = document.createElement("div");
    fill.className = "mic-fill"; bar.appendChild(fill);
    const testStatus = hint("");
    testStatus.id = "mic-test-status"; testStatus.setAttribute("role", "status");
    const calBtn = document.createElement("button");
    calBtn.type = "button";
    calBtn.textContent = t("settings.auto.calibrate.5s.ambient");
    const calStatus = hint("");
    calStatus.id = "mic-calibration-status"; calStatus.setAttribute("role", "status");
    const loopChk = checkbox(false, enabled => mic.setLoopback(enabled));
    loopChk.disabled = true;
    const mic = new MicCheck({
        onState: (status, mode) => {
            const busy = status !== "idle";
            startBtn.disabled = busy; calBtn.disabled = busy;
            stopBtn.hidden = !busy;
            bar.hidden = !busy || mode !== "test";
            loopChk.disabled = status !== "active" || mode !== "test";
            if (!busy) loopChk.checked = false;
            if (status === "requesting") {
                testStatus.textContent = ""; calStatus.textContent = "";
                (mode === "test" ? testStatus : calStatus).textContent = t("settings.mic.requesting");
            } else if (status === "active" && mode === "test") {
                testStatus.textContent = t("settings.mic.listening");
            }
        },
        onLevel: level => { fill.style.width = level * 100 + "%"; },
        onSilence: silent => { testStatus.textContent = t(silent ? "settings.mic.silent" : "settings.mic.detected"); },
        onCountdown: seconds => { calStatus.textContent = t("settings.mic.countdown", { seconds }); },
        onCalibrated: result => {
            s.vad_threshold = result.suggested;
            const controls = vadRow.querySelectorAll("input");
            for (const control of controls) control.value = result.suggested;
            calStatus.textContent = t("settings.calibration.result", { floor: (result.floor * 100).toFixed(1), threshold: result.suggested });
        },
        onError: (error, mode) => {
            const status = mode === "test" ? testStatus : calStatus;
            const key = { NotAllowedError: "settings.mic.denied", SecurityError: "settings.mic.denied",
                NotFoundError: "settings.mic.missing", NotReadableError: "settings.mic.unavailable" }[error.name];
            status.textContent = key ? t(key) : t("settings.mic.test.failed") + (error.message || error.name);
        },
    });
    const begin = mode => {
        if (mic.current) return;
        stopMicCheck();
        stopMicCheck = () => mic.stop();
        const channel = V().state.channels?.find(channel => channel.ChannelID === V().state.myChannelID);
        void mic.start(mode, captureConstraints(channel, s));
    };
    startBtn.onclick = () => begin("test");
    calBtn.onclick = () => begin("calibration");
    stopBtn.onclick = () => {
        const mode = mic.current?.mode;
        mic.stop();
        (mode === "calibration" ? calStatus : testStatus).textContent = t("settings.mic.stopped");
    };
    testWrap.append(startBtn, stopBtn, bar);
    el.append(testWrap, testStatus, row(t("settings.loopback.test.hear.yourself.use.headphones"), loopChk));
    const calWrap = document.createElement("div");
    calWrap.className = "mic-test";
    calWrap.append(calBtn, calStatus);
    el.appendChild(calWrap);
    return el;
}

function renderAudioStatus(panel) {
    const reasons = audioStatus(settings());
    panel.textContent = reasons.length ? reasons.map(({ reason, count }) => t("settings.audio." + reason, { count })).join(" · ") : t("settings.audio.enabled");
}

function audioStatusPanel() {
    const wrap = document.createElement("div");
    wrap.className = "set-hint audio-status-panel";
    const status = document.createElement("div");
    status.className = "audio-status"; status.setAttribute("role", "status");
    renderAudioStatus(status);
    const preview = document.createElement("div");
    preview.className = "audio-preview-status"; preview.setAttribute("role", "status");
    wrap.append(status, preview);
    return wrap;
}

function pagePlayback() {
    const s = settings();
    const el = document.createElement("div");
    el.appendChild(audioStatusPanel());
    el.appendChild(row(t("settings.output.device"), devicePicker(
        "audiooutput",
        s.playback_device_id,
        (v) => { s.playback_device_id = v; },
        t("settings.playback.devices"),
    )));
    el.appendChild(row(t("settings.voice.volume"), slider(s.volume, 0, 200, (v) => {
        s.volume = v;
        const rv = document.getElementById("remote-video");
        if (rv) V().applyOutputSettings(rv);
    }, true)));
    el.appendChild(row(t("settings.voice.limiter.compressor"), checkbox(s.voice_limiter !== false, (v) => { s.voice_limiter = v; })));
    el.appendChild(row(t("settings.per.user.gain.normalization.cap.4x"), checkbox(s.gain_normalize, (v) => { s.gain_normalize = v; })));
    // (53) each publisher is levelled on its own chain, so a loud speaker no
    // longer sets the gain for a quiet one.
    el.appendChild(hint(t("settings.normalization.levels.every.speaker.separately.limiter.and.normalizer.apply")));
    const testBtn = document.createElement("button");
    testBtn.textContent = t("settings.play.test.sound");
    testBtn.onclick = () => previewSounds(["own_channel_join"], s);
    el.appendChild(row(t("settings.test"), testBtn));
    return el;
}

// hkErrors tracks the last activation error per action (301 conflict
// detection), fed by hotkey_status events.
const hkErrors = new Map();
let stopActiveHotkeyCapture = null;

function cancelHotkeyCapture() {
    stopActiveHotkeyCapture?.();
    stopActiveHotkeyCapture = null;
}

// hotkeyCapture builds a click-to-rebind button (shared by the map rows).
function hotkeyCapture(initial, oncapture) {
    const b = document.createElement("button");
    b.className = "hotkey-capture";
    b.textContent = initial || t("settings.click.and.press.a.key");
    b.onclick = () => {
        cancelHotkeyCapture();
        b.textContent = t("settings.press.keys");
        b.classList.add("capturing");
        const onKey = (e) => {
            e.preventDefault();
            const parts = [];
            if (e.ctrlKey) parts.push("Ctrl");
            if (e.altKey) parts.push("Alt");
            if (e.shiftKey) parts.push("Shift");
            if (e.metaKey) parts.push("Win");
            let key = e.key;
            if (key === " ") key = "Space";
            if (key.length === 1) key = key.toUpperCase();
            if (!["Control", "Alt", "Shift", "Meta"].includes(e.key)) {
                parts.push(key);
                b.textContent = parts.join("+");
                stopCapture();
                oncapture(parts.join("+"));
            }
        };
        const stopCapture = () => {
            document.removeEventListener("keydown", onKey, true);
            b.classList.remove("capturing");
            if (stopActiveHotkeyCapture === stopCapture) stopActiveHotkeyCapture = null;
        };
        stopActiveHotkeyCapture = stopCapture;
        document.addEventListener("keydown", onKey, true);
    };
    return b;
}

function pageHotkeys() {
    const s = settings();
    const el = document.createElement("div");

    // (299) the full bindable action map: profile selector + one row per
    // action with rebind/unbind. Overrides land in the selected profile;
    // "default" edits the base bindings.
    const ACTIONS = [
        ["ptt", t("settings.push.to.talk.alternate"), "hotkey_ptt", "ptt"],
        ["mute_toggle", t("settings.mute.toggle"), "hotkey_mute", "mute"],
        ["deafen_toggle", t("settings.deafen.toggle"), "hotkey_deafen", "deafen"],
        ["whisper_reply", t("settings.whisper.reply"), "whisper_reply_hotkey", "whisper_reply"],
        ["quick_connect", t("settings.quick.connect.new.tab"), "hotkey_quick_connect", "quick_connect"],
        ["compact_toggle", t("settings.compact.mode.toggle"), "hotkey_compact", "compact"],
        ["zen_toggle", t("settings.zen.mode.toggle"), "hotkey_zen", "zen"],
    ];
    const profiles = Object.keys(s.hotkey_profiles || {}).sort();
    let curProfile = "default";

    const head = document.createElement("div");
    head.className = "set-row";
    const sel = document.createElement("select");
    for (const p of ["default", ...profiles]) {
        const o = document.createElement("option");
        o.value = p;
        o.textContent = p === "default" ? t("settings.default.profile") : t("settings.profile") + p;
        sel.appendChild(o);
    }
    sel.onchange = () => { curProfile = sel.value; renderRows(); };
    const saveAs = document.createElement("button");
    saveAs.textContent = t("settings.save.as.new.profile");
    saveAs.onclick = () => {
        const name = prompt(t("settings.profile.name.assign.it.to.a.bookmark.in.the.bookmark.manager"));
        if (!name) return;
        s.hotkey_profiles = s.hotkey_profiles || {};
        s.hotkey_profiles[name] = {
            ptt: s.hotkey_ptt, mute: s.hotkey_mute, deafen: s.hotkey_deafen,
            whisper_reply: s.whisper_reply_hotkey, quick_connect: s.hotkey_quick_connect,
            compact: s.hotkey_compact,
        };
        renderPage("hotkeys");
    };
    head.appendChild(sel);
    head.appendChild(saveAs);
    el.appendChild(head);

    const rowsEl = document.createElement("div");
    el.appendChild(rowsEl);

    const renderRows = () => {
        rowsEl.innerHTML = "";
        const prof = (s.hotkey_profiles || {})[curProfile];
        for (const [action, label, field, profField] of ACTIONS) {
            const override = curProfile !== "default" && prof && prof[profField];
            const spec = curProfile === "default" ? (s[field] || "") : (override || t("settings.default") + (s[field] || t("settings.unbound")) + ")");
            const wrap = document.createElement("div");
            wrap.className = "hk-map-row";
            const cap = hotkeyCapture(override || s[field] || "", (v) => {
                if (curProfile === "default") {
                    s[field] = v;
                } else {
                    s.hotkey_profiles = s.hotkey_profiles || {};
                    s.hotkey_profiles[curProfile] = s.hotkey_profiles[curProfile] || {};
                    s.hotkey_profiles[curProfile][profField] = v;
                }
                if (/^[A-Z]$/.test(v)) V().toast(t("settings.warning.bare.letter.hotkeys.fire.while.typing"), "warn");
            });
            const unbind = document.createElement("button");
            unbind.textContent = "✕";
            unbind.title = curProfile === "default" ? t("settings.unbind") : t("settings.clear.override.fall.back.to.default");
            unbind.onclick = () => {
                if (curProfile === "default") s[field] = "";
                else if (prof) prof[profField] = "";
                renderRows();
            };
            const errEl = document.createElement("span");
            errEl.className = "hk-err warn";
            if (hkErrors.has(action)) errEl.textContent = hkErrors.get(action);
            wrap.appendChild(row(label, cap));
            wrap.appendChild(unbind);
            wrap.appendChild(errEl);
            if (override) wrap.classList.add("hk-override");
            rowsEl.appendChild(wrap);
        }
    };
    renderRows();

    const reset = document.createElement("button");
    reset.textContent = t("settings.reset.to.defaults.ptt.unbound.ctrl.m");
    reset.onclick = () => {
        s.hotkey_ptt = ""; s.hotkey_mute = "Ctrl+M";
        renderPage("hotkeys");
    };
    el.appendChild(reset);
    el.appendChild(hint(t("settings.hotkeys.are.applied.when.you.apply.or.ok.on.windows.they.do.not.reserve.or")));
    return el;
}

function pageWhisper() {
    const s = settings();
    const st = V().state;
    const el = document.createElement("div");
    el.appendChild(row(t("settings.activate.whisper"), checkbox(s.whisper_active, (v) => { s.whisper_active = v; })));
    el.appendChild(hint(t("settings.while.active.your.voice.goes.to.the.checked.targets.instead.of.your.channel")));

    const clientsWrap = document.createElement("div");
    clientsWrap.className = "set-subhead";
    clientsWrap.textContent = t("settings.clients.online.now");
    el.appendChild(clientsWrap);
    const clients = st.clients.filter((c) => c.client_id !== st.myClientID);
    if (clients.length === 0) el.appendChild(hint(t("settings.no.other.clients.online")));
    for (const c of clients) {
        el.appendChild(row(c.nickname || c.unique_id, checkbox(
            (s.whisper_clients || []).includes(c.unique_id),
            (v) => {
                s.whisper_clients = s.whisper_clients || [];
                if (v && !s.whisper_clients.includes(c.unique_id)) s.whisper_clients.push(c.unique_id);
                if (!v) s.whisper_clients = s.whisper_clients.filter((u) => u !== c.unique_id);
            })));
    }

    const chWrap = document.createElement("div");
    chWrap.className = "set-subhead";
    chWrap.textContent = t("settings.channels");
    el.appendChild(chWrap);
    for (const ch of st.channels) {
        el.appendChild(row(ch.Name, checkbox(
            (s.whisper_channels || []).includes(ch.ChannelID),
            (v) => {
                s.whisper_channels = s.whisper_channels || [];
                if (v && !s.whisper_channels.includes(ch.ChannelID)) s.whisper_channels.push(ch.ChannelID);
                if (!v) s.whisper_channels = s.whisper_channels.filter((id) => id !== ch.ChannelID);
            })));
    }

    el.appendChild(hint(t("settings.applying.also.sends.the.whisper.list.to.the.server.now")));
    return el;
}

function pageDownloads() {
    const s = settings();
    const el = document.createElement("div");
    const folder = document.createElement("span");
    folder.className = "mono";
    folder.textContent = s.download_folder || t("settings.not.set");
    const change = document.createElement("button");
    change.textContent = t("settings.change");
    change.onclick = async () => {
        const dir = await window.go.main.App.PickDownloadFolder();
        if (dir) {
            s.download_folder = dir;
            folder.textContent = dir;
        }
    };
    const wrap = document.createElement("div");
    wrap.className = "set-folder";
    wrap.appendChild(folder); wrap.appendChild(change);
    el.appendChild(row(t("settings.download.folder"), wrap));
    el.appendChild(hint(t("settings.downloads.from.the.file.browser.land.here.without.asking.leave.it.unset.to")));
    return el;
}

function pageChat() {
    const s = settings();
    const el = document.createElement("div");

    // (126-129) display prefs — applied live via CSS classes on #chat-log.
    const sel = (options, value, onchange) => {
        const i = document.createElement("select");
        for (const [v, label] of options) {
            const o = document.createElement("option");
            o.value = v;
            o.textContent = label;
            i.appendChild(o);
        }
        i.value = value;
        i.onchange = () => onchange(i.value);
        return i;
    };
    el.appendChild(row(t("settings.timestamps"), sel(
        [["absolute", t("settings.absolute")], ["relative", t("settings.relative")], ["off", t("settings.off")]],
        s.chat_timestamps || "absolute", (v) => { s.chat_timestamps = v; })));
    el.appendChild(row(t("settings.density"), sel(
        [["comfortable", t("settings.comfortable")], ["compact", t("settings.compact")]],
        s.chat_density || "comfortable", (v) => { s.chat_density = v; })));
    el.appendChild(row(t("settings.layout"), sel(
        [["irc", t("settings.irc.lines")], ["bubbles", t("settings.bubbles")]],
        s.chat_layout || "irc", (v) => { s.chat_layout = v; })));
    el.appendChild(row(t("settings.font.size"), slider(s.chat_font_size || 14, 12, 18, (v) => { s.chat_font_size = v; })));
    // (130) system-message category filters.
    el.appendChild(row(t("settings.show.join.leave.system.lines"), checkbox(s.sys_join_leave !== false, (v) => { s.sys_join_leave = v; })));
    el.appendChild(row(t("settings.show.kick.system.lines"), checkbox(s.sys_kick !== false, (v) => { s.sys_kick = v; })));

    el.appendChild(row(t("settings.chat.max.lines"), numberInput(s.chat_max_lines, 10, 5000, (v) => { s.chat_max_lines = v; })));
    el.appendChild(row(t("settings.log.channel.chats.to.file"), checkbox(s.log_channel_chat, (v) => { s.log_channel_chat = v; })));
    el.appendChild(row(t("settings.log.private.chats.to.file"), checkbox(s.log_private_chat, (v) => { s.log_private_chat = v; })));
    el.appendChild(row(t("settings.log.server.global.chats.to.file"), checkbox(s.log_server_chat, (v) => { s.log_server_chat = v; })));

    // (388) keyword highlights for the current server (one per line).
    const kwAddr = V().state.lastConnect?.addr || "";
    const kw = document.createElement("textarea");
    kw.className = "dlg-input user-css";
    kw.rows = 3;
    kw.placeholder = t("settings.keyword.highlights.one.per.line.current.server");
    kw.value = ((s.keywords || {})[kwAddr] || []).join("\n");
    kw.onchange = () => {
        s.keywords = s.keywords || {};
        s.keywords[kwAddr] = kw.value.split("\n").map((x) => x.trim()).filter(Boolean);
    };
    el.appendChild(row(t("settings.keywords"), kw));

    el.appendChild(hint(t("settings.chat.log.config.noxa.chat.log.help.open.log.folder")));
    // (4b) encryption note.
    const enc = document.createElement("div");
    enc.className = "set-hint";
    enc.textContent = t("settings.encryption.direct.messages.are.end.to.end.encrypted.the.server.cannot.read");
    el.appendChild(enc);
    return el;
}

// refreshIdentities repaints the identity manager table (351) from the Go
// side, which owns the store.
async function refreshIdentities(tbody) {
    let list = [];
    try {
        list = await window.go.main.App.ListIdentities();
    } catch (e) {
        tbody.innerHTML = "";
        const tr = document.createElement("tr");
        tr.innerHTML = `<td colspan="6" class="warn"></td>`;
        tr.querySelector("td").textContent = t("settings.identities.unavailable") + e;
        tbody.appendChild(tr);
        return;
    }
    // (351) the draft is a clone taken when the dialog opened; switching an
    // identity writes settings behind its back, so mirror it or OK reverts it.
    const active = list.find((x) => x.active);
    if (active && settings()) settings().active_identity = active.id;
    tbody.innerHTML = "";
    for (const e of list) {
        const tr = document.createElement("tr");
        if (e.active) tr.classList.add("id-active");

        const name = document.createElement("td");
        name.textContent = (e.active ? "● " : "") + e.name;
        name.title = e.path;
        tr.appendChild(name);

        const uid = document.createElement("td");
        uid.className = "mono id-uid";
        uid.textContent = e.unique_id ? e.unique_id.slice(0, 16) + "…" : t("settings.unreadable");
        uid.title = e.unique_id || "";
        uid.onclick = () => {
            if (!e.unique_id) return;
            void copyToClipboard(e.unique_id, { success: t("settings.unique.id.copied"), isCurrent: () => uid.isConnected });
        };
        tr.appendChild(uid);

        // (352) proof-of-work level on the unique ID.
        const lvl = document.createElement("td");
        lvl.className = "mono";
        lvl.textContent = String(e.security_level ?? 0);
        tr.appendChild(lvl);

        // (354) which storage mode the key is actually in.
        const prot = document.createElement("td");
        prot.textContent = e.protection === "dpapi" ? "🔒 DPAPI" : t("settings.plaintext");
        prot.title = e.protection === "dpapi"
            ? t("settings.private.key.sealed.to.this.windows.account.a.copy.of.this.file.will.not.ope")
            : t("settings.private.key.stored.in.the.clear.no.os.key.store.in.use");
        tr.appendChild(prot);

        // (353) backup state.
        const backup = document.createElement("td");
        backup.textContent = e.exported_at ? "✓ " + e.exported_at : t("settings.never");
        if (!e.exported_at) backup.className = "warn";
        tr.appendChild(backup);

        const actions = document.createElement("td");
        actions.className = "id-actions";
        const mk = (label, title, fn) => {
            const b = document.createElement("button");
            b.textContent = label;
            b.title = title;
            b.onclick = fn;
            actions.appendChild(b);
            return b;
        };
        mk(t("settings.use"), t("settings.make.this.the.identity.used.on.the.next.connect"), async () => {
            const err = await window.go.main.App.SwitchIdentity(e.id);
            if (err) V().toast(t("settings.switch.failed") + err, "warn");
            else V().toast(t("settings.active.identity") + e.name + t("settings.reconnect.to.use.it"), "warn");
            refreshIdentities(tbody);
        }).disabled = e.active;
        mk(t("settings.rename"), t("settings.change.the.display.label"), async () => {
            const name = prompt(t("settings.identity.name"), e.name);
            if (!name) return;
            const err = await window.go.main.App.RenameIdentity(e.id, name);
            if (err) V().toast(t("settings.rename.failed") + err, "warn");
            refreshIdentities(tbody);
        });
        mk(t("settings.export"), t("settings.save.a.portable.backup.of.this.identity"), async () => {
            const err = await window.go.main.App.ExportIdentity(e.id);
            if (err) V().toast(t("settings.export.failed") + err, "warn");
            else V().toast(t("settings.identity.exported.keep.the.file.safe"));
            refreshIdentities(tbody);
        });
        mk(t("settings.level"), t("settings.raise.the.proof.of.work.security.level"), async () => {
            const target = parseInt(prompt(t("settings.target.security.level.leading.zero.bits.1.40"), String((e.security_level || 0) + 4)), 10);
            if (!target) return;
            V().toast(t("settings.computing.security.level.up.to.30s"));
            const res = await window.go.main.App.ImproveIdentityLevel(e.id, target, 30);
            if (res.error) V().toast(t("settings.level.failed") + res.error, "warn");
            else V().toast(t("settings.identity.level", { level: res.level, counter: res.counter }));
            refreshIdentities(tbody);
        });
        mk(t("settings.delete"), t("settings.remove.this.identity.from.this.machine"), async () => {
            const warn = e.exported_at
                ? t("settings.identity.delete", { name: e.name })
                : t("settings.identity.delete.unbacked", { name: e.name });
            if (!confirm(warn)) return;
            const err = await window.go.main.App.DeleteIdentity(e.id, true);
            if (err) V().toast(t("settings.delete.failed") + err, "warn");
            refreshIdentities(tbody);
        }).classList.add("danger-btn");
        tr.appendChild(actions);
        tbody.appendChild(tr);
    }
}

function pageSecurity() {
    const s = settings();
    const el = document.createElement("div");

    // (351) multiple identities: the identity IS the account, so the manager
    // is the primary control here.
    const sub = document.createElement("div");
    sub.className = "set-subhead";
    sub.textContent = t("settings.identities");
    el.appendChild(sub);

    const table = document.createElement("table");
    table.className = "perm-grid identity-grid";
    table.innerHTML = `<thead><tr>${["name", "uid", "level.heading", "storage", "backup"].map(key => `<th>${t("settings.identity." + key)}</th>`).join("")}<th></th></tr></thead><tbody></tbody>`;
    const tbody = table.querySelector("tbody");
    const tableScroll = document.createElement("div");
    tableScroll.className = "identity-table-scroll";
    tableScroll.tabIndex = 0;
    tableScroll.setAttribute("role", "region");
    tableScroll.setAttribute("aria-label", t("settings.identities"));
    tableScroll.appendChild(table);
    el.appendChild(tableScroll);
    // Search builds detached pages to index their labels; only
    // the page that is really on screen may hit the identity store, which
    // unseals a protected key per row.
    setTimeout(() => { if (tbody.isConnected) refreshIdentities(tbody); }, 0);

    const bar = document.createElement("div");
    bar.className = "set-folder";
    const newBtn = document.createElement("button");
    newBtn.textContent = t("settings.new.identity");
    newBtn.onclick = async () => {
        const name = prompt(t("settings.name.for.the.new.identity.e.g.gaming"));
        if (!name) return;
        const err = await window.go.main.App.CreateIdentity(name);
        if (err) V().toast(t("settings.create.failed") + err, "warn");
        refreshIdentities(tbody);
    };
    const importBtn = document.createElement("button");
    importBtn.textContent = t("settings.import");
    importBtn.onclick = async () => {
        const err = await window.go.main.App.ImportIdentity();
        if (err) V().toast(t("settings.import.failed") + err, "warn");
        else V().toast(t("settings.identity.imported.and.selected.reconnect.to.use.it"), "warn");
        refreshIdentities(tbody);
    };
    const regen = document.createElement("button");
    regen.className = "danger-btn";
    regen.textContent = t("settings.regenerate.active");
    regen.onclick = async () => {
        if (!confirm(t("settings.regenerating.replaces.the.active.identity.s.key.servers.will.see.you.as.a.n"))) return;
        const uid = await window.go.main.App.RegenerateIdentity();
        V().toast(t("settings.identity.regenerated") + uid.slice(0, 16) + t("settings.reconnect.to.use.it.alternate"), "warn");
        refreshIdentities(tbody);
    };
    bar.append(newBtn, importBtn, regen);
    el.appendChild(bar);
    el.appendChild(hint(t("settings.the.active.identity.is.used.on.the.next.connect.click.a.unique.id.to.copy.i")));

    // (354) key storage at rest, with its fallback stated plainly.
    const protSel = document.createElement("select");
    for (const [v, label] of [["auto", t("settings.use.the.os.key.store.when.available.default")], ["off", t("settings.always.store.in.plaintext")]]) {
        const o = document.createElement("option");
        o.value = v;
        o.textContent = label;
        protSel.appendChild(o);
    }
    protSel.value = s.identity_key_protection === "off" ? "off" : "auto";
    protSel.onchange = () => { s.identity_key_protection = protSel.value; };
    el.appendChild(row(t("settings.private.key.storage"), protSel));
    el.appendChild(hint(t("settings.on.windows.the.private.key.is.sealed.with.dpapi.to.your.user.account.so.a.s")));

    // (4a) Transport security: TLS is the default; plaintext is an explicit
    // dev opt-in.
    const tsub = document.createElement("div");
    tsub.className = "set-subhead";
    tsub.textContent = t("settings.transport");
    el.appendChild(tsub);
    el.appendChild(row(t("settings.allow.plaintext.connections.dev.servers"), checkbox(!!s.allow_plaintext, (v) => { s.allow_plaintext = v; })));
    el.appendChild(hint(t("settings.server.connections.use.tls.with.trust.on.first.use.fingerprint.pinning.know")));
    return el;
}

function pageNotifications() {
    const s = settings();
    const el = document.createElement("div");
    el.appendChild(audioStatusPanel());
    const chatLevel = document.createElement("select");
    chatLevel.className = "dlg-input";
    for (const [value, label] of [
        ["direct", t("settings.direct.messages.only")],
        ["channel_mentions", t("settings.dms.channel.mentions")],
        ["role_mentions", t("settings.dms.role.mentions")],
        ["all", t("settings.all.messages")],
    ]) {
        const option = document.createElement("option");
        option.value = value;
        option.textContent = label;
        chatLevel.appendChild(option);
    }
    chatLevel.value = s.chat_notification_level || "all";
    chatLevel.onchange = () => { s.chat_notification_level = chatLevel.value; };
    el.appendChild(row(t("settings.chat.notification.category"), chatLevel));
    el.appendChild(hint(t("settings.per.channel.mute.and.notification.matrix.outputs.still.apply.after.this.cat")));
    // (385) notification matrix: rows = events, columns = outputs. The rows
    // are the dispatcher's own event list, so every event it can fire has
    // reachable toggles here and the two cannot drift apart.
    const EVENTS = MATRIX_EVENTS;
    const matrix = document.createElement("table");
    matrix.className = "perm-grid notify-matrix";
    matrix.innerHTML = `<thead><tr>${["event", "toast", "sound", "flash", "native", "preview"].map(key => `<th>${t("settings.matrix." + key)}</th>`).join("")}</tr></thead><tbody></tbody>`;
    const tbody = matrix.querySelector("tbody");
    s.notify_matrix = s.notify_matrix || {};
    for (const [event, englishLabel] of EVENTS) {
        const label = currentLanguage() === "en" ? englishLabel : t("settings.sound." + event);
        // Unset rows seed from the dispatcher's default: this grid writes what
        // it shows, so a local guess would change behaviour on first visit.
        const rowData = s.notify_matrix[event] || defaultMatrixRow(event);
        s.notify_matrix[event] = rowData;
        const tr = document.createElement("tr");
        tr.innerHTML = `<td class="mono">${label}</td>`;
        for (const col of ["toast", "sound", "flash", "native"]) {
            const td = document.createElement("td");
            td.appendChild(checkbox(rowData[col], (v) => { rowData[col] = v; }));
            td.firstChild.setAttribute("aria-label", label + ": " + t("settings.matrix." + col));
            tr.appendChild(td);
        }
        const td = document.createElement("td");
        const preview = document.createElement("button");
        preview.type = "button";
        preview.textContent = t("settings.play");
        preview.setAttribute("aria-label", t("settings.preview", { label }));
        preview.onclick = () => previewSounds([event], s);
        td.appendChild(preview);
        tr.appendChild(td);
        tbody.appendChild(tr);
    }
    el.appendChild(matrix);
    el.appendChild(hint(t("settings.previews.use.your.sound.volume.and.event.choices.dnd.silences.all.previews")));
    // (347/348) do-not-disturb: toggle + quiet hours schedule.
    el.appendChild(row(t("settings.do.not.disturb"), checkbox(s.dnd_enabled, (v) => { s.dnd_enabled = v; })));
    const from = document.createElement("input");
    from.type = "time";
    from.setAttribute("aria-label", t("settings.quiet.from"));
    from.value = s.dnd_from || "";
    from.onchange = () => { s.dnd_from = from.value; };
    const to = document.createElement("input");
    to.type = "time";
    to.setAttribute("aria-label", t("settings.quiet.to"));
    to.value = s.dnd_to || "";
    to.onchange = () => { s.dnd_to = to.value; };
    const hours = document.createElement("div");
    hours.className = "dnd-hours";
    hours.append(from, document.createTextNode(" – "), to);
    el.appendChild(row(t("settings.quiet.hours.empty.off"), hours));
    el.appendChild(hint(t("settings.dnd.suppresses.toasts.sounds.and.taskbar.flashes.mentions.still.badge.silen")));
    el.appendChild(row(t("settings.toasts.for.join.leave"), checkbox(s.notify_join_leave, (v) => { s.notify_join_leave = v; })));
    el.appendChild(row(t("settings.toasts.for.connection.events"), checkbox(s.notify_connection, (v) => { s.notify_connection = v; })));
    el.appendChild(row(t("settings.warn.when.talking.while.muted"), checkbox(s.warn_muted_talking !== false, (v) => { s.warn_muted_talking = v; })));
    el.appendChild(row(t("settings.hint.when.talking.to.an.empty.channel"), checkbox(s.warn_empty_channel !== false, (v) => { s.warn_empty_channel = v; })));

    // (28) master gate, now actually read by sounds.js: only an explicit
    // false silences playback, so a settings blob without the field is on.
    el.appendChild(row(t("settings.play.sounds.master"), checkbox(s.play_sounds !== false, (v) => { s.play_sounds = v; })));

    const pack = document.createElement("span");
    pack.textContent = "noXa";
    el.appendChild(row(t("settings.sound.set"), pack));
    el.appendChild(hint(t("settings.original.noxa.sounds.replace.soft.bright.retro.and.custom.beeps.your.event")));
    el.appendChild(hint(t("settings.audio.output.fallback")));
    el.appendChild(row(t("settings.sound.volume"), slider(s.sound_volume ?? 100, 0, 200, (v) => { s.sound_volume = v; }, true)));
    el.appendChild(row(t("settings.sound.effects"), checkbox(s.effects_enabled !== false, v => { s.effects_enabled = v; })));
    el.appendChild(row(t("settings.audio.duck"), checkbox(s.duck_effects_while_speaking, v => { s.duck_effects_while_speaking = v; })));
    el.appendChild(hint(t("settings.audio.duck.hint")));
    el.appendChild(row(t("settings.spoken.system.messages"), checkbox(s.spoken_messages !== false, v => { s.spoken_messages = v; })));
    el.appendChild(row(t("settings.speech.volume"), slider(s.speech_volume ?? 100, 0, 200, v => { s.speech_volume = v; }, true)));
    const speechLocale = document.createElement("select");
    for (const [value, label] of [["interface", t("settings.audio.follow.interface")], ["en", "English"], ["de", "Deutsch"]]) {
        const option = document.createElement("option"); option.value = value; option.textContent = label; speechLocale.appendChild(option);
    }
    speechLocale.value = s.speech_language || "interface";
    speechLocale.onchange = () => { s.speech_language = speechLocale.value; stopPreviews(); renderPage("notifications"); };
    el.appendChild(row(t("settings.audio.speech.language"), speechLocale));
    el.appendChild(row(t("settings.speak.connection.problems"), checkbox(s.speech_connection !== false, v => { s.speech_connection = v; })));
    el.appendChild(row(t("settings.speak.administrative.actions"), checkbox(s.speech_admin !== false, v => { s.speech_admin = v; })));
    el.appendChild(row(t("settings.speak.removal"), checkbox(s.speech_removal !== false, v => { s.speech_removal = v; })));
    el.appendChild(row(t("settings.speak.permissions"), checkbox(s.speech_permissions !== false, v => { s.speech_permissions = v; })));
    const speechTest = document.createElement("button");
    speechTest.type = "button";
    speechTest.textContent = t("settings.test.spoken.message");
    speechTest.onclick = () => previewSpeech(s);
    el.appendChild(speechTest);
    el.appendChild(hint(t("settings.fixed.english.or.german.recordings.follow.your.interface.language.other.lan")));
    el.appendChild(hint(t("settings.previews.use.unsaved.settings.master.mute.can.be.bypassed.for.previews.dnd")));
    const status = document.createElement("div");
    status.className = "set-hint";
    status.setAttribute("role", "status");
    const onLabel = (label, event) => {
        status.textContent = label ? t("settings.playing", { label: t("settings.sound." + event) }) : t("settings.preview.finished");
    };
    const button = (label, events) => {
        const b = document.createElement("button");
        b.type = "button";
        b.textContent = label;
        b.onclick = () => previewSounds(events, s, onLabel);
        return b;
    };
    const controls = document.createElement("div");
    controls.className = "sound-controls";
    const all = button(t("settings.test.all.sounds"));
    all.onclick = () => testAll(s, onLabel);
    const stop = document.createElement("button");
    stop.type = "button";
    stop.textContent = t("settings.stop.preview");
    stop.onclick = () => { stopPreviews(); status.textContent = t("settings.preview.stopped"); };
    controls.append(all, stop);
    el.append(controls, status);
    const speechButton = (label, events) => {
        const b = document.createElement("button");
        b.type = "button";
        b.textContent = label;
        b.onclick = () => previewSpeech(s, events, event => {
            status.textContent = event ? t("settings.playing", { label: speechPreviewLabel(event, s) }) : t("settings.preview.finished");
        });
        return b;
    };
    const speechControls = document.createElement("div");
    speechControls.className = "sound-controls";
    speechControls.appendChild(speechButton(t("settings.preview.all.speech"), Object.keys(SPEECH_EVENTS)));
    for (const [category, label] of [["connection", "settings.speak.connection.problems"], ["admin", "settings.speak.administrative.actions"]]) {
        speechControls.appendChild(speechButton(t("settings.preview", { label: t(label) }), Object.keys(SPEECH_EVENTS).filter(id => SPEECH_EVENTS[id].category === category)));
    }
    el.appendChild(speechControls);
    for (const event of Object.keys(SPEECH_EVENTS)) {
        const label = speechPreviewLabel(event, s);
        const controls = document.createElement("div");
        controls.className = "sound-controls";
        if (event !== "test") controls.appendChild(checkbox(s.speech_events?.[event] !== false, enabled => {
            s.speech_events ||= {};
            s.speech_events[event] = enabled;
        }));
        const b = speechButton(t("settings.play"), [event]);
        b.setAttribute("aria-label", t("settings.preview", { label }));
        controls.appendChild(b);
        el.appendChild(row(label, controls));
    }
    for (const group of SOUND_EVENT_GROUPS) {
        const heading = document.createElement("div");
        heading.className = "set-subhead sound-controls";
        const label = document.createElement("span");
        label.textContent = t("settings.soundgroup." + group.events[0][0]);
        heading.append(label, button(t("settings.preview", { label: currentLanguage() === "en" ? label.textContent.toLowerCase() : label.textContent }), group.events.map(([event]) => event)));
        el.appendChild(heading);
        for (const [event] of group.events) {
            const label = t("settings.sound." + event);
            const controls = document.createElement("div");
            controls.className = "sound-controls";
            controls.append(checkbox(s.event_sounds?.[event] !== false, (enabled) => {
                s.event_sounds = s.event_sounds || {};
                s.event_sounds[event] = enabled;
            }), button(t("settings.play"), [event]));
            controls.querySelector("button").setAttribute("aria-label", t("settings.preview", { label }));
            el.appendChild(row(label, controls));
        }
    }
    return el;
}

const PAGE_BUILDERS = {
    application: pageApplication,
    capture: pageCapture,
    playback: pagePlayback,
    hotkeys: pageHotkeys,
    whisper: pageWhisper,
    downloads: pageDownloads,
    chat: pageChat,
    security: pageSecurity,
    server: pageServer,
    notifications: pageNotifications,
};

function renderPage(id) {
    stopPreviews();
    stopMicCheck();
    stopCameraTest();
    cancelHotkeyCapture();
    document.querySelectorAll(".settings-nav-item").forEach((n) => {
        const active = n.dataset.page === id;
        n.classList.toggle("active", active);
        n.setAttribute("aria-selected", String(active));
        n.tabIndex = active ? 0 : -1;
    });
    const container = document.getElementById("settings-content");
    const summary = document.querySelector(".settings-search-summary");
    if (summary) { summary.textContent = ""; summary.hidden = true; }
    container.setAttribute("aria-labelledby", `settings-page-${id}`);
    container.innerHTML = "";
    container.appendChild(PAGE_BUILDERS[id]());
}

function translateDialog(overlay) {
    overlay.querySelector("#settings-title").textContent = t("settings.settings");
    overlay.querySelector(".settings-nav").setAttribute("aria-label", t("settings.settings.sections"));
    overlay.querySelector('label[for="settings-search"]').textContent = t("settings.search.settings");
    overlay.querySelector("#settings-search").placeholder = t("settings.searchPlaceholder");
    for (const [id, key] of [["set-ok", "ok"], ["set-cancel", "cancel"], ["set-apply", "apply"]]) {
        overlay.querySelector("#" + id).textContent = t("common." + key);
    }
    for (const page of PAGES) {
        overlay.querySelector(`#settings-page-${page.id} span:last-child`).textContent = t(page.label);
    }
}

function openSettings(pageId = "application") {
    if (document.getElementById("settings-overlay")?.getAttribute("aria-busy") === "true") return;
    draft = JSON.parse(JSON.stringify(V().state.settings || {}));
    deviceInventory.invalidate();

    let overlay = document.getElementById("settings-overlay");
    if (overlay) closeDialog(overlay, "cancel");

    overlay = document.createElement("div");
    overlay.id = "settings-overlay";
    overlay.className = "dlg-overlay";
    overlay.innerHTML = `
        <div class="settings-dialog">
            <h2 id="settings-title" class="sr-only"></h2>
            <div class="settings-nav" role="tablist" aria-orientation="vertical"></div>
            <div class="settings-main">
                <label class="sr-only" for="settings-search"></label>
                <input id="settings-search" class="dlg-input" placeholder="" autocomplete="off" />
                <div class="settings-search-summary set-hint" role="status" hidden></div>
                <div id="settings-content" role="tabpanel"></div>
                <div class="settings-save-status set-hint" role="status" hidden></div>
                <div class="settings-footer">
                    <button id="set-ok"></button>
                    <button id="set-cancel"></button>
                    <button id="set-apply"></button>
                </div>
            </div>
        </div>`;

    // (350) settings search: filters rows by label text across all pages,
    // jumping to the matching page and highlighting hits.
    const search = overlay.querySelector("#settings-search");
    search.placeholder = t("settings.searchPlaceholder");
    const content = overlay.querySelector("#settings-content");
    const refreshAudioStatus = () => {
        for (const panel of content.querySelectorAll(".audio-status")) renderAudioStatus(panel);
    };
    content.addEventListener("input", refreshAudioStatus);
    content.addEventListener("change", refreshAudioStatus);
    window.addEventListener("noxa-audio-status", refreshAudioStatus);
    const previewBlocked = event => {
        for (const panel of content.querySelectorAll(".audio-preview-status")) panel.textContent = event.detail ? t("settings.audio." + event.detail) : "";
    };
    window.addEventListener("noxa-audio-preview-blocked", previewBlocked);
    const summary = overlay.querySelector(".settings-search-summary");
    // Keep only text, not detached controls or their callbacks. Draft edits
    // invalidate dynamic labels; the cache is discarded with this dialog.
    let searchIndex = null;
    let searchLanguage = "";
    const invalidateSearch = () => { searchIndex = null; };
    content.addEventListener("input", invalidateSearch);
    content.addEventListener("change", invalidateSearch);
    content.addEventListener("click", (event) => {
        if (!event.target.closest(".set-search-hit")) invalidateSearch();
    });
    search.oninput = () => {
        stopPreviews();
        stopMicCheck();
        stopCameraTest();
        cancelHotkeyCapture();
        const q = search.value.trim().toLowerCase();
        if (!q) {
            renderPage(document.querySelector(".settings-nav-item.active")?.dataset.page || "application");
            return;
        }
        if (!searchIndex || searchLanguage !== currentLanguage()) {
            searchIndex = [];
            searchLanguage = currentLanguage();
            for (const p of PAGES) {
                const pageEl = PAGE_BUILDERS[p.id]();
                pageEl.querySelectorAll(".set-row, .set-subhead, .set-hint, button").forEach((r) => {
                    const label = (r.querySelector(".set-label")?.textContent || r.textContent || "").toLowerCase().trim();
                    searchIndex.push({ page: p.id, label });
                });
            }
        }
        const hits = searchIndex.filter(hit => hit.label.includes(q));
        summary.hidden = false;
        summary.textContent = hits.length > 40
            ? t("settings.searchLimited", { shown: 40, total: hits.length })
            : t(hits.length === 1 ? "settings.searchOne" : "settings.searchCount", { count: hits.length });
        content.innerHTML = "";
        if (hits.length === 0) {
            const empty = document.createElement("div");
            empty.className = "empty-state";
            empty.textContent = t("settings.no.matching.settings");
            content.appendChild(empty);
            return;
        }
        for (const h of hits.slice(0, 40)) {
            const row = document.createElement("button");
            row.type = "button";
            row.className = "set-search-hit";
            row.innerHTML = `<span class="mono set-search-page"></span><span class="set-search-label"></span>`;
            row.querySelector(".set-search-page").textContent = t(PAGES.find(p => p.id === h.page).label);
            row.querySelector(".set-search-label").textContent = h.label;
            row.onclick = () => {
                search.value = "";
                renderPage(h.page);
                content.querySelectorAll(".set-row").forEach((r) => {
                    if ((r.querySelector(".set-label")?.textContent || r.textContent || "").toLowerCase().includes(h.label.slice(0, 20))) {
                        r.classList.add("set-hit");
                        r.scrollIntoView({ block: "center" });
                    }
                });
            };
            content.appendChild(row);
        }
    };

    const nav = overlay.querySelector(".settings-nav");
    for (const p of PAGES) {
        const item = document.createElement("button");
        item.type = "button";
        item.className = "settings-nav-item";
        item.dataset.page = p.id;
        item.id = `settings-page-${p.id}`;
        item.setAttribute("role", "tab");
        item.setAttribute("aria-controls", "settings-content");
        item.setAttribute("aria-selected", "false");
        item.tabIndex = -1;
        item.innerHTML = `<span class="nav-icon" aria-hidden="true">${icon(p.icon)}</span><span>${t(p.label)}</span>`;
        item.onclick = () => renderPage(p.id);
        item.addEventListener("keydown", (event) => {
            if (event.key !== "ArrowUp" && event.key !== "ArrowDown") return;
            event.preventDefault();
            const items = [...nav.querySelectorAll(".settings-nav-item")];
            const next = wrappedIndex(items.indexOf(item), items.length, event.key === "ArrowDown" ? 1 : -1);
            items[next].focus();
            items[next].click();
        });
        nav.appendChild(item);
    }

    let saving = false;
    const saveStatus = overlay.querySelector(".settings-save-status");
    const applyAll = async () => {
        if (saving) return false;
        saving = true;
        const snapshot = structuredClone(draft);
        const previous = V().state.settings;
        const whisperConfig = (s) => JSON.stringify([!!s?.whisper_active, s?.whisper_clients || [], s?.whisper_channels || []]);
        const whisperChanged = whisperConfig(previous) !== whisperConfig(snapshot);
        const previousLanguage = currentLanguage();
        const serverGeneration = V().state.serverGeneration;
        const focused = document.activeElement;
        overlay.setAttribute("aria-busy", "true");
        for (const button of overlay.querySelectorAll(".settings-footer button")) button.disabled = true;
        search.disabled = true;
        nav.inert = true;
        content.inert = true;
        saveStatus.hidden = false;
        saveStatus.classList.remove("warn");
        saveStatus.textContent = t("common.saving");
        try {
            await commit(snapshot);
            if (!overlay.isConnected) return false;
            if (previousLanguage !== currentLanguage()) {
                translateDialog(overlay);
                if (search.value) search.oninput();
                else renderPage(overlay.querySelector(".settings-nav-item.active")?.dataset.page || "application");
            }
            // Local preferences also save while disconnected. A live whisper
            // update belongs only to the server where this save began.
            if (whisperChanged && V().state.myClientID && serverGeneration === V().state.serverGeneration) {
                const error = await window.go.main.App.WhisperSet(
                    snapshot.whisper_active ? snapshot.whisper_clients || [] : [],
                    snapshot.whisper_active ? snapshot.whisper_channels || [] : [],
                    !!snapshot.whisper_active,
                );
                if (error) throw new Error(error);
            }
            saveStatus.textContent = t("settings.saved");
            return true;
        } catch (error) {
            if (overlay.isConnected) {
                saveStatus.textContent = error instanceof SavedAudioSettingsError
                    ? error.message
                    : t("menu.saveFailed", { error: error.message || String(error) });
                saveStatus.classList.add("warn");
            }
            return false;
        } finally {
            saving = false;
            overlay.removeAttribute("aria-busy");
            for (const button of overlay.querySelectorAll(".settings-footer button")) button.disabled = false;
            search.disabled = false;
            nav.inert = false;
            content.inert = false;
            if (overlay.isConnected && focused?.isConnected) focused.focus();
        }
    };

    overlay.querySelector("#set-ok").onclick = async () => {
        if (await applyAll()) closeDialog(overlay);
    };
    const cancel = () => closeDialog(overlay, "cancel");
    overlay.querySelector("#set-cancel").onclick = cancel;
    overlay.querySelector("#set-apply").onclick = applyAll;
    overlay.onclick = (e) => { if (e.target === overlay) cancel(); };

    translateDialog(overlay);
    mountDialog(overlay, {
        onCancel: () => !saving,
        onClose: () => {
            window.removeEventListener("noxa-audio-status", refreshAudioStatus);
            window.removeEventListener("noxa-audio-preview-blocked", previewBlocked);
            stopPreviews();
            stopMicCheck();
            stopCameraTest();
            cancelHotkeyCapture();
            revertLivePreview();
        },
    });
    renderPage(pageId);
}

export function initSettingsUI() {
    window.__noxa.openSettings = openSettings;
    // (301) track registration errors for the hotkey map rows.
    window.runtime.EventsOn("hotkey_status", (st) => {
        if (st.error) hkErrors.set(st.action, st.error);
        else if (st.registered) hkErrors.delete(st.action);
    });
}
