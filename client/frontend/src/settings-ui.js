// settings-ui.js — the settings dialog: a centred modal with a category rail
// on the left and grouped rows (label + help left, control right) per page.
// Page ids, their saved keys and deep links live in settings-catalog.js.
import { icon } from "./icons.js";
import { SETTINGS_CATEGORIES, resetPageDraft, resolveSettingsTarget } from "./settings-catalog.js";
import { capturedHotkey, hotkeyLabel } from "./hotkey-input.js";
import { gamingOverlaySettings } from "./gaming-overlay-settings.js";
import { securitySettings } from "./security-settings.js";
import { captureMediaScope, mediaScopeIsCurrent, setWhisperRouting } from "./media-controls.js";
import { currentLanguage, t } from "./i18n.js";
import { copyToClipboard } from "./clipboard.js";
import { createMicCheck } from "./mic-check-ui.js";
import { createNetworkEchoTest } from "./network-echo.js";
import { createConnectionBenchmark } from "./connection-benchmark.js";
import { createVideoCodecCapabilities } from "./video-codec-capabilities-ui.js";
import { percentageInput } from "./percentage-input.js";
import { previewSounds, previewSpeech, speechPreviewLabel, speechEventEnabled, audioStatus, SPEECH_EVENTS, stopPreviews, updateSoundOutput, SOUND_EVENT_GROUPS, testAll } from "./sounds.js";
import { MATRIX_EVENTS, defaultMatrixRow } from "./notifications.js";
import { renderVoiceHints } from "./workspace-ui.js";
import { associateControlLabel, wrappedIndex } from "./a11y.js";
import { createMediaDeviceInventory } from "./media-devices.js";
import { closeDialog, mountDialog } from "./modal.js";
import { captureCamera, prepareCameraBackground, applyCameraPreview } from "./camera-capture.js";
import "./settings-ui.css";
import "./arctic-settings.css";

const V = () => window.__noxa;

const PAGES = SETTINGS_CATEGORIES.map(category => ({
    id: category.id, icon: category.icon, rail: category.rail,
    label: `settings.cat.${category.id}`, description: `settings.cat.${category.id}.desc`,
}));

// Rail glyphs the shared icon set does not have yet (24px outline, same stroke).
const RAIL_ICONS = {
    streams: '<circle cx="12" cy="12" r="2"/><path d="M8.5 8.5a5 5 0 0 0 0 7M15.5 8.5a5 5 0 0 1 0 7M5.6 5.6a9 9 0 0 0 0 12.8M18.4 5.6a9 9 0 0 1 0 12.8"/>',
    layers: '<path d="m12 3 9 5-9 5-9-5z"/><path d="m3 13 9 5 9-5"/>',
    palette: '<path d="M12 3a9 9 0 1 0 0 18c1.1 0 1.6-.8 1.6-1.6 0-.5-.2-.9-.5-1.2-.3-.3-.5-.7-.5-1.2 0-.9.7-1.6 1.6-1.6H16a5 5 0 0 0 5-5C21 6.4 17 3 12 3z"/><circle cx="7.5" cy="11" r="1"/><circle cx="10.5" cy="7.5" r="1"/><circle cx="15" cy="7.5" r="1"/>',
};

function railIcon(name) {
    if (!RAIL_ICONS[name]) return icon(name);
    return `<svg class="ui-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${RAIL_ICONS[name]}</svg>`;
}

let draft = null; // working copy of settings while the dialog is open
let stopCameraTest = () => {};
let stopMicCheck = () => {};

function settings() { return draft; }

class SavedAudioSettingsError extends Error {}
class SavedIdentityProtectionError extends Error {}

async function commit(snapshot) {
    const err = await window.go.main.App.SaveSettings(snapshot);
    if (err) throw new Error(err);
    // (282) the draft was cloned when the dialog opened: re-read the merged
    // truth so Go-owned fields (recents) written meanwhile survive.
    V().state.settings = await window.go.main.App.GetSettings();
    // Keep existing control closures attached to the rebased draft, including
    // when persistence succeeds but applying an audio device subsequently fails.
    for (const key of Object.keys(draft)) delete draft[key];
    Object.assign(draft, structuredClone(V().state.settings));
    window.dispatchEvent(new Event("noxa-camera-preferences-changed"));
    renderVoiceHints();
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
    // SaveSettings applies native effects from the merged, committed settings.
    return true;
}

let timingSerial = 0;

function timingNote(key, controls = []) {
    const note = document.createElement("div");
    note.className = "settings-timing";
    note.id = `settings-timing-${++timingSerial}`;
    note.textContent = t(`settings.timing.${key}`);
    for (const control of controls) {
        control.setAttribute("aria-describedby", [control.getAttribute("aria-describedby"), note.id].filter(Boolean).join(" "));
    }
    return note;
}

let helpSerial = 0;

// row lays out one setting: label (+ optional help and timing note) on the
// left, the control on the right. opts.help adds a one-line description and
// opts.key tags the row with the saved preference it edits, for deep links.
function row(label, control, timing, opts = {}) {
    const el = document.createElement("div");
    el.className = "set-row";
    if (opts.key) el.dataset.setting = opts.key;
    const l = document.createElement("label");
    l.className = "set-label";
    l.textContent = label;
    el.appendChild(l);
    el.appendChild(control);
    if (opts.help) {
        const caption = document.createElement("div");
        caption.className = "set-field-caption";
        const help = document.createElement("div");
        help.className = "set-help";
        help.id = `settings-help-${++helpSerial}`;
        help.textContent = opts.help;
        l.replaceWith(caption);
        caption.append(l, help);
        const described = control.matches?.("input, select, textarea, button") ? [control] : [...control.querySelectorAll("input, select, textarea, button")];
        for (const item of described) item.setAttribute("aria-describedby", [item.getAttribute("aria-describedby"), help.id].filter(Boolean).join(" "));
    }
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
    if (timing) {
        let caption = l.parentElement;
        if (!caption?.classList.contains("set-field-caption")) {
            caption = document.createElement("div");
            caption.className = "set-field-caption";
            l.replaceWith(caption);
            caption.append(l);
        }
        const controls = control.matches?.("input, select, textarea, button") ? [control] : [...control.querySelectorAll("input, select, textarea, button")];
        caption.append(timingNote(timing, controls));
    }
    return el;
}

// checkbox is a native checkbox drawn as a switch (see arctic-settings.css);
// keeping the native role leaves keyboard and screen-reader behaviour intact.
function checkbox(checked, onchange = () => {}) {
    const c = document.createElement("input");
    c.type = "checkbox";
    c.className = "set-switch";
    c.checked = !!checked;
    c.onchange = () => onchange(c.checked);
    return c;
}

// group renders a titled block of rows on a page. id is the deep-link
// section name (openSettings("audio", { section: "output" })).
function group(title, id = "", ...children) {
    const el = document.createElement("section");
    el.className = "set-group";
    if (id) el.dataset.section = id;
    if (title) {
        const heading = document.createElement("h4");
        heading.className = "set-group-title";
        heading.textContent = title;
        el.appendChild(heading);
    }
    el.append(...children.filter(Boolean));
    return el;
}

function page(...groups) {
    const el = document.createElement("div");
    el.className = "set-page";
    el.append(...groups.filter(Boolean));
    return el;
}

function numberInput(value, min, max, onchange = () => {}) {
    const i = document.createElement("input");
    i.type = "number";
    i.min = min; i.max = max; i.value = value;
    i.required = true;
    i.onchange = () => { if (i.checkValidity()) onchange(i.valueAsNumber); };
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
    const deviceType = t(kind === "videoinput" ? "camera.devices" : kind === "audioinput" ? "settings.devices.capture" : "settings.devices.playback");
    const refresh = async (force = false) => {
        currentId = select.value || currentId;
        if (!loaded) select.disabled = true;
        refreshBtn.disabled = true;
        refreshBtn.textContent = t("settings.refreshing");
        wrap.setAttribute("aria-busy", "true");
        status.classList.remove("warn");
        status.textContent = kind === "videoinput" ? t("camera.devicesLoading") : force ? t("settings.refreshing.audio.devices") : t("settings.loading.audio.devices");

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
            if (select.hasAttribute("aria-describedby")) next.setAttribute("aria-describedby", select.getAttribute("aria-describedby"));
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

function selectOf(options, value, onchange) {
    const sel = document.createElement("select");
    for (const [v, label] of options) {
        const option = document.createElement("option");
        option.value = v;
        option.textContent = label;
        sel.appendChild(option);
    }
    sel.value = value;
    sel.onchange = () => onchange(sel.value);
    return sel;
}

function pageGeneral() {
    const s = settings();
    const langSel = selectOf([["system", t("settings.system.default")], ["en", "English"], ["de", "Deutsch"]],
        s.language || "system", v => { s.language = v; });

    const awayMsg = document.createElement("input");
    awayMsg.className = "dlg-input";
    awayMsg.maxLength = 200;
    awayMsg.placeholder = t("settings.auto.away");
    awayMsg.value = s.auto_away_message ?? "";
    awayMsg.onchange = () => { s.auto_away_message = awayMsg.value; };

    // Downloads (formerly its own page).
    const folder = document.createElement("span");
    folder.className = "mono set-folder-path";
    folder.textContent = s.download_folder || t("settings.not.set");
    const change = document.createElement("button");
    change.type = "button";
    change.textContent = t("settings.change");
    change.onclick = async () => {
        const dir = await window.go.main.App.PickDownloadFolder();
        if (dir) {
            s.download_folder = dir;
            folder.textContent = dir;
            change.dispatchEvent(new Event("change", { bubbles: true }));
        }
    };
    const folderWrap = document.createElement("div");
    folderWrap.className = "set-folder";
    folderWrap.append(folder, change);

    return page(
        group(t("settings.group.language"), "language",
            row(t("settings.language"), langSel, "", { key: "language", help: t("settings.language.help") })),
        // Presence (308/390): the idle timer and the status line it publishes.
        group(t("settings.presence"), "presence",
            row(t("settings.auto.away.after.minutes.0.off"), numberInput(s.auto_away_minutes ?? 15, 0, 240, (v) => { s.auto_away_minutes = v; }), "", { key: "auto_away_minutes" }),
            row(t("settings.auto.away.status.message"), awayMsg, "", { key: "auto_away_message", help: t("settings.other.clients.see.this.text.next.to.your.away.icon.while.you.are.idle") })),
        group(t("settings.group.window"), "window",
            row(t("settings.always.on.top"), checkbox(s.always_on_top, (v) => { s.always_on_top = v; }), "", { key: "always_on_top" }),
            row(t("settings.close.to.tray"), checkbox(s.close_to_tray, (v) => { s.close_to_tray = v; }), "", { key: "close_to_tray" }),
            row(t("settings.minimize.to.tray"), checkbox(s.minimize_to_tray, (v) => { s.minimize_to_tray = v; }), "", { key: "minimize_to_tray" })),
        group(t("settings.group.updates"), "updates",
            row(t("settings.install.updates.at.startup"), checkbox(s.updates_auto_install !== false, (v) => { s.updates_auto_install = v; }), "startup", { key: "updates_auto_install", help: t("settings.install.updates.at.startup.help") }),
            row(t("settings.check.for.updates.at.startup"), checkbox(s.updates_auto_check !== false, (v) => { s.updates_auto_check = v; }), "startup", { key: "updates_auto_check" }),
            row(t("settings.check.for.updates.on.reconnect"), checkbox(s.updates_check_on_reconnect !== false, (v) => { s.updates_check_on_reconnect = v; }), "", { key: "updates_check_on_reconnect" })),
        group(t("settings.downloads"), "downloads",
            row(t("settings.download.folder"), folderWrap, "", { key: "download_folder", help: t("settings.downloads.from.the.file.browser.land.here.without.asking.leave.it.unset.to") })),
    );
}

function pageAppearance() {
    const s = settings();
    const themeSel = selectOf([["arctic", t("settings.arctic.default")], ["dark", t("settings.dark.default")], ["light", t("settings.light")], ["hc", t("settings.high.contrast")]],
        s.theme || "arctic", v => { s.theme = v; });
    const accent = document.createElement("input");
    accent.type = "color";
    accent.value = s.accent_color || "#2ee6a8";
    accent.onchange = () => { s.accent_color = accent.value; };
    const fontSel = selectOf([["outfit", "Outfit"], ["sora", "Sora"], ["jetbrains", "JetBrains Mono"]],
        s.ui_font || "outfit", v => { s.ui_font = v; });
    // (292) the floor keeps the window clickable. Applied on release, not per
    // drag frame: the binding persists the settings file on every call.
    const opacity = slider(s.window_opacity || 100, 20, 100, (v) => { s.window_opacity = v; });
    opacity.querySelector("input").addEventListener("change",
        () => window.go.main.App.SetWindowOpacity(s.window_opacity || 100));
    const css = document.createElement("textarea");
    css.className = "dlg-input user-css";
    css.rows = 4;
    css.placeholder = t("settings.custom.css.overrides.e.g.channel.letter.spacing.0.5px");
    css.value = s.user_css || "";
    const themeColors = themeEditor(s, value => { css.value = value; });
    css.onchange = () => { s.user_css = css.value; themeColors.refresh(); };
    return page(
        group(t("settings.window.appearance"), "theme",
            row(t("settings.theme"), themeSel, "", { key: "theme" }),
            row(t("settings.accent.color"), accent, "", { key: "accent_color" }),
            row(t("settings.ui.font"), fontSel, "", { key: "ui_font" }),
            row(t("settings.ui.font.size"), slider(s.ui_font_size || 14, 10, 20, (v) => { s.ui_font_size = v; }), "", { key: "ui_font_size" })),
        group(t("settings.group.layout"), "layout",
            row(t("settings.compact.mode"), checkbox(s.compact_mode, (v) => { s.compact_mode = v; }), "", { key: "compact_mode" }),
            row(t("settings.reduce.motion"), checkbox(s.reduce_motion, (v) => { s.reduce_motion = v; }), "", { key: "reduce_motion" }),
            row(t("settings.window.opacity"), opacity, "appearancePreview", { key: "window_opacity" })),
        group("", "colors", themeColors.root), // (295)
        group(t("settings.group.customCSS"), "css", row(t("settings.user.css"), css, "", { key: "user_css" })),
    );
}

function pageServer({ load = true } = {}) {
    const el = document.createElement("div");
    const generation = V().state.serverGeneration;
    const tabID = V().state.activeTabID;
    const model = V().state.authorizationModel;
    const current = () => el.isConnected && generation === V().state.serverGeneration &&
        tabID === V().state.activeTabID && model === V().state.authorizationModel;
    const status = hint(t("settings.loading.effective.server.configuration"));
    status.setAttribute("role", "status");
    el.appendChild(status);
    // Access comes from the protected server query in both authorization models.
    const form = document.createElement("form");
    const mediaForm = document.createElement("form");
    form.hidden = true;
    mediaForm.hidden = true;
    const runtimeHeading = document.createElement("h4");
    runtimeHeading.className = "set-subhead";
    runtimeHeading.textContent = t("settings.runtime.settings");
    form.appendChild(runtimeHeading);
    const mediaHeading = document.createElement("h4");
    mediaHeading.className = "set-subhead";
    mediaHeading.textContent = t("settings.video.publishing.limits");
    mediaForm.appendChild(mediaHeading);
    el.append(form, mediaForm);
    let busy = false, needsReload = true;
    const addNumber = (owner, label, min, max) => {
        const input = numberInput(0, min, max);
        input.required = true;
        input.step = "1";
        owner.appendChild(row(label, input));
        return input;
    };
    const maxClients = addNumber(form, t("settings.maximum.clients.0.unlimited"), 0, 100000);
    const timeout = addNumber(form, t("settings.connection.timeout.seconds"), 30, 86400);
    const bitrate = addNumber(form, t("settings.default.opus.bitrate.bit.s"), 6000, 510000);
    const fec = checkbox(false);
    const dtx = checkbox(false);
    const stereo = checkbox(false);
    form.appendChild(row(t("settings.default.opus.in.band.fec"), fec));
    form.appendChild(row(t("settings.default.opus.dtx"), dtx));
    form.appendChild(row(t("settings.default.opus.stereo"), stereo));
    form.appendChild(hint(t("settings.codec.defaults.apply.to.newly.created.channels.existing.channels.keep.their")));
    const videoBitrate = addNumber(mediaForm, t("settings.video.bitrate.ceiling"), 0, 100000000);
    const videoWidth = addNumber(mediaForm, t("settings.video.maximum.encoded.width"), 0, 16383);
    const videoHeight = addNumber(mediaForm, t("settings.video.maximum.encoded.height"), 0, 16383);
    mediaForm.appendChild(hint(t("settings.video.limit.help")));
    const applyToForm = (cfg) => {
        maxClients.value = cfg.max_clients;
        timeout.value = cfg.client_timeout_seconds;
        bitrate.value = cfg.opus_bitrate;
        fec.checked = !!cfg.opus_fec;
        dtx.checked = !!cfg.opus_dtx;
        stereo.checked = !!cfg.opus_stereo;
    };
    const applyToMediaForm = (limits) => {
        videoBitrate.value = limits.video_max_bitrate;
        videoWidth.value = limits.video_max_width;
        videoHeight.value = limits.video_max_height;
    };
    const apply = document.createElement("button");
    apply.type = "submit";
    apply.textContent = t("settings.apply.server.configuration");
    const applyMedia = document.createElement("button");
    applyMedia.type = "submit";
    applyMedia.textContent = t("settings.apply.video.limits");
    const reload = document.createElement("button");
    reload.type = "button";
    reload.textContent = t("settings.serverConfig.reload");
    reload.hidden = true;
    el.appendChild(reload);
    const controls = () => {
        for (const owner of [form, mediaForm]) owner.setAttribute("aria-busy", String(busy));
        for (const input of el.querySelectorAll("form input")) input.disabled = busy || needsReload;
        apply.disabled = busy || needsReload;
        applyMedia.disabled = busy || needsReload;
        reload.disabled = busy;
    };
    const stale = () => {
        if (!el.isConnected) return;
        form.hidden = true;
        mediaForm.hidden = true;
        status.textContent = t("settings.serverConfig.changed");
        reload.hidden = false;
        reload.disabled = false;
    };
    form.onsubmit = async event => {
        event.preventDefault();
        if (!current()) { stale(); return; }
        if (busy || needsReload || !form.reportValidity()) return;
        // Read the actual controls so Enter submits the current number even
        // when the browser has not dispatched a change/blur event yet.
        const request = { max_clients: Number(maxClients.value), client_timeout_seconds: Number(timeout.value),
            opus_bitrate: Number(bitrate.value), opus_fec: fec.checked, opus_dtx: dtx.checked, opus_stereo: stereo.checked };
        busy = true;
        controls();
        try {
            const result = await window.go.main.App.SetServerConfigForTab(tabID, request);
            if (!current()) { stale(); return; }
            applyToForm(result);
            status.textContent = t("settings.server.configuration.saved.and.active");
            V().toast(t("settings.server.configuration.updated"));
        } catch (err) {
            if (!current()) { stale(); return; }
            needsReload = true;
            reload.hidden = false;
            status.textContent = t("settings.could.not.save.server.configuration") + err + " " + t("settings.serverConfig.reloadAfterFailure");
            V().toast(status.textContent, "warn");
        } finally {
            busy = false;
            if (current()) controls();
        }
    };
    form.appendChild(row(t("settings.runtime.settings"), apply));
    for (const input of [videoWidth, videoHeight]) {
        input.addEventListener("input", () => videoHeight.setCustomValidity(""));
    }
    mediaForm.onsubmit = async event => {
        event.preventDefault();
        if (!current()) { stale(); return; }
        if (busy || needsReload) return;
        videoHeight.setCustomValidity("");
        const width = Number(videoWidth.value), height = Number(videoHeight.value);
        if ((width === 0) !== (height === 0)) {
            videoHeight.setCustomValidity(t("settings.video.dimensions.paired"));
        }
        if (!mediaForm.reportValidity()) return;
        const request = { video_max_bitrate: Number(videoBitrate.value),
            video_max_width: width, video_max_height: height };
        busy = true;
        controls();
        try {
            const result = await window.go.main.App.SetMediaLimitsForTab(tabID, request);
            if (!current()) { stale(); return; }
            applyToMediaForm(result);
            status.textContent = t("settings.video.limits.saved.and.active");
            V().toast(t("settings.video.limits.updated"));
        } catch (err) {
            if (!current()) { stale(); return; }
            needsReload = true;
            reload.hidden = false;
            status.textContent = t("settings.could.not.save.video.limits") + err + " " + t("settings.serverConfig.reloadAfterFailure");
            V().toast(status.textContent, "warn");
        } finally {
            busy = false;
            if (current()) controls();
        }
    };
    mediaForm.appendChild(row(t("settings.video.publishing.limits"), applyMedia));

    const refresh = async () => {
        if (!current()) { stale(); return; }
        if (busy) return;
        busy = true;
        controls();
        try {
            const cfg = await window.go.main.App.GetServerConfigForTab(tabID);
            const limits = cfg.media_limits_management
                ? await window.go.main.App.GetMediaLimitsForTab(tabID)
                : null;
            if (!current()) { stale(); return; }
            applyToForm(cfg);
            if (limits) applyToMediaForm(limits);
            needsReload = false;
            status.textContent = t("settings.changes.take.effect.immediately.and.are.restored.after.restart");
            form.hidden = false;
            mediaForm.hidden = !limits;
            reload.hidden = true;
        } catch (err) {
            if (!current()) { stale(); return; }
            needsReload = true;
            form.hidden = true;
            mediaForm.hidden = true;
            reload.hidden = false;
            status.textContent = t("settings.could.not.load.server.configuration") + err;
        } finally {
            busy = false;
            if (current()) controls();
        }
    };
    reload.onclick = () => {
        if (!el.isConnected || busy) return;
        // An explicit reload creates a fresh scope on the newly selected server.
        if (!current()) renderPage("connection");
        else void refresh();
    };
    controls();
    // Search builds detached pages solely to index their static labels.
    if (load) queueMicrotask(refresh);
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
function themeEditor(s, onChange) {
    const wrap = document.createElement("div");
    const head = document.createElement("div");
    head.className = "set-subhead";
    head.textContent = t("settings.theme.colors");
    wrap.appendChild(head);
    const grid = document.createElement("div");
    grid.className = "theme-grid";
    let overrides = parseThemeOverrides(s.user_css);
    const apply = () => {
        s.user_css = writeThemeOverrides(s.user_css, overrides);
        onChange(s.user_css);
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
    wrap.append(timingNote("appearancePreview", grid.querySelectorAll("input")), grid);
    const refresh = () => {
        overrides = parseThemeOverrides(s.user_css);
        for (const [i, [name]] of THEME_VARS.entries()) {
            grid.children[i].querySelector("input").value = currentVar(name, overrides);
        }
    };
    const reset = document.createElement("button");
    reset.textContent = t("settings.reset.theme.colors");
    reset.onclick = () => {
        for (const k of Object.keys(overrides)) delete overrides[k];
        apply();
        refresh();
    };
    wrap.appendChild(reset);
    return { root: wrap, refresh };
}

function pageAudio() {
    const s = settings();
    const microphone = createMicCheck(s, {
        onStart: cleanup => { stopMicCheck(); stopMicCheck = cleanup; },
    });
    // Activation mode.
    const modeWrap = document.createElement("div");
    modeWrap.className = "set-modes";
    modeWrap.setAttribute("role", "radiogroup");
    modeWrap.setAttribute("aria-label", t("settings.activation.mode"));
    for (const [id, label] of [["ptt", t("settings.push.to.talk")], ["vad", t("settings.voice.activity.detection")], ["continuous", t("settings.continuous.transmission")]]) {
        const l = document.createElement("label");
        const r = document.createElement("input");
        r.type = "radio";
        r.name = "actmode";
        r.checked = (s.activation_mode || "ptt") === id;
        r.onchange = () => {
            s.activation_mode = id;
            microphone.refresh();
        };
        l.appendChild(r);
        l.appendChild(document.createTextNode(" " + label));
        modeWrap.appendChild(l);
    }
    const modeRow = document.createElement("div");
    modeRow.className = "set-row set-row-block";
    modeRow.dataset.setting = "activation_mode";
    const modeCaption = document.createElement("div");
    modeCaption.className = "set-field-caption";
    const modeLabel = document.createElement("div");
    modeLabel.className = "set-label";
    modeLabel.textContent = t("settings.activation.mode");
    modeCaption.append(modeLabel, timingNote("saved", modeWrap.querySelectorAll("input")));
    modeRow.append(modeCaption, modeWrap);

    const input = group(t("settings.capture"), "input",
        modeRow,
        row(t("settings.capture.device"), devicePicker(
            "audioinput",
            s.capture_device_id,
            (v) => { s.capture_device_id = v; },
            t("settings.capture.devices"),
        ), "saved", { key: "capture_device_id" }),
        timingNote("localPreview"), microphone.root,
        row(t("settings.echo.cancellation"), checkbox(s.echo_cancellation !== false, (v) => { s.echo_cancellation = v; }), "saved", { key: "echo_cancellation" }),
        row(t("settings.noise.suppression"), checkbox(s.noise_suppression !== false, (v) => { s.noise_suppression = v; }), "saved", { key: "noise_suppression" }),
        row(t("settings.ptt.delay"), slider(s.ptt_release_delay_ms || 0, 0, 2000, (v) => { s.ptt_release_delay_ms = v; }), "saved", { key: "ptt_release_delay_ms" }),
        // The music profile overrides these two, so say so where they live.
        hint(t("settings.music.channels.stereo.96.kbit.s.or.more.capture.in.stereo.with.echo.cancell")));
    input.addEventListener("change", () => microphone.refresh());

    const positionPath = document.createElement("button");
    positionPath.type = "button";
    positionPath.textContent = t("settings.positional.copyPath");
    positionPath.onclick = async () => {
        try {
            const path = await window.go.main.App.PositionalInputPath();
            if (path) await copyToClipboard(path);
        } catch (error) { V().toast(String(error), "warn"); }
    };
    const testBtn = document.createElement("button");
    testBtn.type = "button";
    testBtn.textContent = t("settings.play.test.sound");
    testBtn.onclick = () => previewSounds(["own_channel_join"], s);
    const output = group(t("settings.playback"), "output",
        audioStatusPanel(),
        row(t("settings.output.device"), devicePicker(
            "audiooutput",
            s.playback_device_id,
            (v) => { s.playback_device_id = v; },
            t("settings.playback.devices"),
        ), "saved", { key: "playback_device_id" }),
        row(t("settings.voice.volume"), slider(s.volume, 0, 200, (v) => {
            s.volume = v;
            const rv = document.getElementById("remote-video");
            if (rv) V().applyOutputSettings(rv);
        }, true), "saved", { key: "volume" }),
        row(t("settings.voice.limiter.compressor"), checkbox(s.voice_limiter !== false, (v) => { s.voice_limiter = v; }), "reconnect", { key: "voice_limiter" }),
        // (53) each publisher is levelled on its own chain, so a loud speaker no
        // longer sets the gain for a quiet one.
        row(t("settings.per.user.gain.normalization.cap.4x"), checkbox(s.gain_normalize, (v) => { s.gain_normalize = v; }), "reconnect",
            { key: "gain_normalize", help: t("settings.normalization.levels.every.speaker.separately.limiter.and.normalizer.apply") }),
        row(t("settings.positional.enabled"), checkbox(!!s.positional_audio, (v) => { s.positional_audio = v; }), "", { key: "positional_audio", help: t("settings.positional.hint") }),
        row(t("settings.positional.source"), positionPath),
        row(t("settings.test"), testBtn, "localPreview"));
    return page(input, output);
}

function pageConnection({ detached = false } = {}) {
    const s = settings();
    return page(
        group(t("settings.group.connection"), "reconnect",
            row(t("settings.reconnect.on.connection.loss.5.tries"), checkbox(s.reconnect_on_loss, (v) => { s.reconnect_on_loss = v; }), "", { key: "reconnect_on_loss" })),
        group(t("settings.group.diagnostics"), "diagnostics", createNetworkEchoTest(), createConnectionBenchmark()),
        group(t("settings.server"), "server", pageServer({ load: !detached })),
    );
}

function pageStreams() {
    const s = settings();
    const el = document.createElement("div");

    el.appendChild(row(t("camera.device"), devicePicker("videoinput", s.camera_device_id, value => { s.camera_device_id = value; }, t("camera.device")), "camera", { key: "camera_device_id" }));
    const effect = document.createElement("select");
    for (const [value, label] of [["none", "camera.none"], ["blur", "camera.blur"], ["replace", "camera.replace"]]) {
        const option = document.createElement("option"); option.value = value; option.textContent = t(label); effect.appendChild(option);
    }
    effect.value = s.camera_background || "none";
    const scene = document.createElement("select");
    for (const value of ["slate", "warm", "studio", "custom"]) {
        const option = document.createElement("option"); option.value = value; option.textContent = t("camera." + value); scene.appendChild(option);
    }
    scene.value = s.camera_background_scene || "slate";
    scene.onchange = () => { s.camera_background_scene = scene.value; };
    const sceneRow = row(t("camera.scene"), scene, "camera", { key: "camera_background_scene" });
    sceneRow.classList.toggle("hidden", effect.value !== "replace");
    effect.onchange = () => { s.camera_background = effect.value; sceneRow.classList.toggle("hidden", effect.value !== "replace"); };
    el.append(row(t("camera.effect"), effect, "camera", { key: "camera_background", help: t("camera.private") }), sceneRow);
    const imagePicker = document.createElement("input"); imagePicker.type = "file"; imagePicker.accept = "image/png,image/jpeg,image/webp";
    const imageStatus = hint(""); imageStatus.setAttribute("role", "status");
    const removeImage = document.createElement("button"); removeImage.type = "button"; removeImage.textContent = t("camera.removeImage"); removeImage.disabled = !s.camera_background_image;
    let imageRevision = 0;
    imagePicker.onchange = async () => {
        const revision = ++imageRevision; imageStatus.textContent = "";
        if (!imagePicker.files[0]) return;
        try {
            const data = await prepareCameraBackground(imagePicker.files[0]);
            if (!el.isConnected || revision !== imageRevision) return;
            s.camera_background_image = data; s.camera_background_scene = "custom"; s.camera_background = "replace";
            scene.value = "custom"; effect.value = "replace"; sceneRow.classList.remove("hidden"); removeImage.disabled = false;
            el.dispatchEvent(new Event("change", {bubbles:true}));
        } catch (error) { if (el.isConnected && revision === imageRevision) imageStatus.textContent = error.message || String(error); }
    };
    removeImage.onclick = () => { imageRevision++; s.camera_background_image = ""; s.camera_background_scene = "slate"; scene.value = "slate"; imagePicker.value = ""; removeImage.disabled = true; el.dispatchEvent(new Event("change", {bubbles:true})); };
    el.append(row(t("camera.chooseImage"), imagePicker, "camera", { key: "camera_background_image", help: t("camera.imageHint") }), removeImage, imageStatus);
    el.append(row(t("camera.blurStrength"), slider(s.camera_blur_strength ?? 14, 2, 30, value => { s.camera_blur_strength = value; }), "camera", { key: "camera_blur_strength" }));
    const mirror = checkbox(s.camera_mirror_preview !== false, value => { s.camera_mirror_preview = value; applyCameraPreview(preview, s); });
    el.append(row(t("camera.mirror"), mirror, "", { key: "camera_mirror_preview" }));

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
    el.appendChild(row(t("settings.camera.frame.rate"), fpsSelect, "camera", { key: "camera_fps", help: t("settings.camera.is.off.when.joining.turn.it.on.in.the.voice.controls.or.test.it.here") }));
    const cameraTest = document.createElement("div");
    cameraTest.className = "camera-test";
    const cameraBtn = document.createElement("button");
    cameraBtn.type = "button";
    cameraBtn.textContent = t("settings.test.camera");
    const preview = document.createElement("video");
    preview.autoplay = true;
    preview.playsInline = true;
    preview.muted = true;
    applyCameraPreview(preview, s);
    preview.hidden = true;
    preview.style.maxWidth = "100%";
    preview.setAttribute("aria-label", t("settings.camera.test.preview"));
    const cameraStatus = hint("");
    cameraStatus.setAttribute("role", "status");
    let testing = false;
    let testCapture = null;
    let testController = null;
    let revision = 0;
    const stop = () => {
        revision++;
        testing = false;
        testController?.abort();
        testController = null;
        testCapture?.stop();
        testCapture = null;
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
        const request = ++revision;
        testController = new AbortController();
        cameraBtn.disabled = true;
        cameraStatus.textContent = t("camera.loading");
        try {
            const capture = await captureCamera(s, undefined, { signal: testController.signal });
            if (!testing || request !== revision || !cameraTest.isConnected) {
                capture.stop();
                return;
            }
            testCapture = capture;
            capture.stream.getVideoTracks()[0]?.addEventListener("ended", () => {
                if (testCapture !== capture) return;
                stop();
                cameraStatus.textContent = t("camera.ended");
            }, { once: true });
            preview.srcObject = capture.stream;
            preview.hidden = false;
            cameraStatus.textContent = "";
            cameraBtn.textContent = t("settings.stop.camera.test");
        } catch (error) {
            if (testing && request === revision) {
                stop();
                cameraStatus.textContent = t("settings.camera.test.failed") + (error.message || error.name);
            }
        } finally {
            if (request === revision) cameraBtn.disabled = false;
        }
    };
    el.addEventListener("change", event => {
        if (event.target === mirror) return;
        if (!testing) return;
        stop();
        cameraStatus.textContent = t("camera.previewRestart");
    });
    cameraTest.append(timingNote("localPreview", [cameraBtn]), cameraBtn, preview, cameraStatus);
    el.appendChild(cameraTest);
    return page(group(t("settings.camera"), "camera", el),
        group(t("settings.group.codecs"), "codecs", createVideoCodecCapabilities()));
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

// hkErrors tracks the last activation error per action (301 conflict
// detection), fed by hotkey_status events.
const hkErrors = new Map();
let stopActiveHotkeyCapture = null;

function cancelHotkeyCapture() {
    stopActiveHotkeyCapture?.();
    stopActiveHotkeyCapture = null;
}

// hotkeyCapture builds a click-to-rebind button (shared by the map rows).
function hotkeyCapture(initial, oncapture, errorElement) {
    const b = document.createElement("button");
    b.className = "hotkey-capture";
    b.textContent = initial ? hotkeyLabel(initial, currentLanguage()) : t("settings.click.and.press.a.key");
    b.onclick = () => {
        const wasCapturing = b.classList.contains("capturing");
        cancelHotkeyCapture();
        if (wasCapturing) return;
        const previousLabel = b.textContent;
        b.textContent = t("settings.press.keys");
        b.classList.add("capturing");
        const onKey = (e) => {
            e.preventDefault();
            e.stopImmediatePropagation();
            const spec = capturedHotkey(e);
            if (spec === null) return;
            if (!spec) {
                errorElement.textContent = t("settings.hotkey.unavailable", { key: e.key });
                return;
            }
            stopCapture();
            errorElement.textContent = "";
            b.textContent = hotkeyLabel(spec, currentLanguage());
            oncapture(spec);
            b.dispatchEvent(new Event("change", { bubbles: true }));
        };
        const stopCapture = () => {
            window.removeEventListener("keydown", onKey, true);
            b.classList.remove("capturing");
            b.textContent = previousLabel;
            if (stopActiveHotkeyCapture === stopCapture) stopActiveHotkeyCapture = null;
        };
        stopActiveHotkeyCapture = stopCapture;
        // Run before the dialog's Escape/Tab handlers so those keys can bind.
        window.addEventListener("keydown", onKey, true);
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
        cancelHotkeyCapture();
        rowsEl.innerHTML = "";
        const prof = (s.hotkey_profiles || {})[curProfile];
        for (const [action, label, field, profField] of ACTIONS) {
            const override = curProfile !== "default" && prof && prof[profField];
            const spec = curProfile === "default" ? (s[field] || "") : (override || t("settings.default") + (s[field] || t("settings.unbound")) + ")");
            const wrap = document.createElement("div");
            wrap.className = "hk-map-row";
            wrap.dataset.hotkeyAction = action;
            const errEl = document.createElement("span");
            errEl.className = "hk-err warn";
            errEl.setAttribute("role", "status");
            errEl.id = "hotkey-error-" + action;
            const cap = hotkeyCapture(override || s[field] || "", (v) => {
                if (curProfile === "default") {
                    s[field] = v;
                } else {
                    s.hotkey_profiles = s.hotkey_profiles || {};
                    s.hotkey_profiles[curProfile] = s.hotkey_profiles[curProfile] || {};
                    s.hotkey_profiles[curProfile][profField] = v;
                }
                if (/^[A-Z]$/.test(v)) V().toast(t("settings.warning.bare.letter.hotkeys.fire.while.typing"), "warn");
            }, errEl);
            cap.setAttribute("aria-describedby", errEl.id);
            const unbind = document.createElement("button");
            unbind.textContent = "✕";
            unbind.title = curProfile === "default" ? t("settings.unbind") : t("settings.clear.override.fall.back.to.default");
            unbind.setAttribute("aria-label", unbind.title + ": " + label);
            unbind.onclick = () => {
                if (curProfile === "default") s[field] = "";
                else if (prof) prof[profField] = "";
                renderRows();
            };
            if (hkErrors.has(action)) errEl.textContent = hkErrors.get(action);
            wrap.appendChild(row(label, cap, "", { key: field }));
            wrap.appendChild(unbind);
            wrap.appendChild(errEl);
            if (override) wrap.classList.add("hk-override");
            rowsEl.appendChild(wrap);
        }
    };
    renderRows();

    // "Reset to default" in the footer restores every base binding; named
    // profiles are kept (bookmarks refer to them).
    return page(group(t("settings.group.bindings"), "bindings", el),
        group(t("settings.group.keyHelp"), "help",
            hint(t("settings.hotkey.supported")),
            hint(t("settings.hotkey.numpad")),
            hint(t("settings.hotkeys.are.applied.when.you.apply.or.ok.on.windows.they.do.not.reserve.or"))));
}

function pageWhisper() {
    const s = settings();
    const st = V().state;
    const clients = st.clients.filter((c) => c.client_id !== st.myClientID);
    const clientRows = clients.map(c => row(c.nickname || c.unique_id, checkbox(
        (s.whisper_clients || []).includes(c.unique_id),
        (v) => {
            s.whisper_clients = s.whisper_clients || [];
            if (v && !s.whisper_clients.includes(c.unique_id)) s.whisper_clients.push(c.unique_id);
            if (!v) s.whisper_clients = s.whisper_clients.filter((u) => u !== c.unique_id);
        })));
    const channelRows = st.channels.map(ch => row(ch.Name, checkbox(
        (s.whisper_channels || []).includes(ch.ChannelID),
        (v) => {
            s.whisper_channels = s.whisper_channels || [];
            if (v && !s.whisper_channels.includes(ch.ChannelID)) s.whisper_channels.push(ch.ChannelID);
            if (!v) s.whisper_channels = s.whisper_channels.filter((id) => id !== ch.ChannelID);
        })));
    return page(
        group(t("settings.whisper"), "whisper",
            row(t("settings.activate.whisper"), checkbox(s.whisper_active, (v) => { s.whisper_active = v; }), "",
                { key: "whisper_active", help: t("settings.while.active.your.voice.goes.to.the.checked.targets.instead.of.your.channel") }),
            hint(t("settings.applying.also.sends.the.whisper.list.to.the.server.now"))),
        group(t("settings.clients.online.now"), "clients",
            ...(clientRows.length ? clientRows : [hint(t("settings.no.other.clients.online"))])),
        group(t("settings.channels"), "channels", ...channelRows),
    );
}

function pageChat() {
    const s = settings();
    // (126-129) display prefs — applied live via CSS classes on #chat-log.
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
    return page(
        group(t("settings.group.chatDisplay"), "display",
            row(t("settings.timestamps"), selectOf(
                [["absolute", t("settings.absolute")], ["relative", t("settings.relative")], ["off", t("settings.off")]],
                s.chat_timestamps || "absolute", (v) => { s.chat_timestamps = v; }), "", { key: "chat_timestamps" }),
            row(t("settings.density"), selectOf(
                [["comfortable", t("settings.comfortable")], ["compact", t("settings.compact")]],
                s.chat_density || "comfortable", (v) => { s.chat_density = v; }), "", { key: "chat_density" }),
            row(t("settings.layout"), selectOf(
                [["irc", t("settings.irc.lines")], ["bubbles", t("settings.bubbles")]],
                s.chat_layout || "irc", (v) => { s.chat_layout = v; }), "", { key: "chat_layout" }),
            row(t("settings.font.size"), slider(s.chat_font_size || 14, 12, 18, (v) => { s.chat_font_size = v; }), "", { key: "chat_font_size" })),
        // (130) system-message category filters.
        group(t("settings.group.systemLines"), "system",
            row(t("settings.show.join.leave.system.lines"), checkbox(s.sys_join_leave !== false, (v) => { s.sys_join_leave = v; }), "", { key: "sys_join_leave" }),
            row(t("settings.show.kick.system.lines"), checkbox(s.sys_kick !== false, (v) => { s.sys_kick = v; }), "", { key: "sys_kick" })),
        group(t("settings.group.history"), "history",
            row(t("settings.chat.max.lines"), numberInput(s.chat_max_lines, 10, 5000, (v) => { s.chat_max_lines = v; }), "", { key: "chat_max_lines" }),
            row(t("settings.keywords"), kw, "", { key: "keywords" })),
    );
}

function pagePrivacy() {
    const s = settings();
    // (4b) encryption note.
    return page(
        group("", "identity", securitySettings(s)),
        group(t("settings.group.chatLogs"), "logs",
            row(t("settings.log.channel.chats.to.file"), checkbox(s.log_channel_chat, (v) => { s.log_channel_chat = v; }), "", { key: "log_channel_chat" }),
            row(t("settings.log.private.chats.to.file"), checkbox(s.log_private_chat, (v) => { s.log_private_chat = v; }), "", { key: "log_private_chat" }),
            row(t("settings.log.server.global.chats.to.file"), checkbox(s.log_server_chat, (v) => { s.log_server_chat = v; }), "", { key: "log_server_chat" }),
            hint(t("settings.chat.log.config.noxa.chat.log.help.open.log.folder")),
            hint(t("settings.encryption.direct.messages.are.end.to.end.encrypted.the.server.cannot.read"))),
    );
}

function pageNotifications() {
    const s = settings();
    const el = document.createElement("div");
    el.className = "notification-settings";
    const section = (key, className, collapsible = false) => {
        const group = document.createElement(collapsible ? "details" : "section");
        group.className = "notification-section set-group " + className;
        group.dataset.section = key;
        const title = document.createElement(collapsible ? "summary" : "h4");
        if (!collapsible) title.className = "set-group-title";
        title.textContent = t("settings.notifications." + key);
        group.appendChild(title);
        el.appendChild(group);
        return group;
    };
    const output = section("output", "notification-output");
    const effects = section("effects", "notification-effects");
    const speech = section("speech", "notification-speech");
    const delivery = section("delivery", "notification-delivery");
    const quiet = section("quiet", "notification-quiet");
    const eventEffects = section("eventEffects", "notification-event-effects", true);
    output.appendChild(audioStatusPanel());
    output.appendChild(row(t("settings.play.sounds.master"), checkbox(s.play_sounds !== false, v => { s.play_sounds = v; })));
    output.appendChild(row(t("settings.output.device"), devicePicker("audiooutput", s.playback_device_id,
        v => { s.playback_device_id = v; }, t("settings.playback.devices")), "saved"));
    output.appendChild(hint(t("settings.notifications.sameOutput")));

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
    delivery.appendChild(row(t("settings.chat.notification.category"), chatLevel));
    delivery.appendChild(hint(t("settings.per.channel.mute.and.notification.matrix.outputs.still.apply.after.this.cat")));
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
        tr.innerHTML = `<th scope="row">${label}</th>`;
        for (const col of ["toast", "sound", "flash", "native"]) {
            const td = document.createElement("td");
            td.dataset.label = t("settings.matrix." + col);
            td.appendChild(checkbox(rowData[col], (v) => { rowData[col] = v; }));
            td.firstChild.setAttribute("aria-label", label + ": " + t("settings.matrix." + col));
            tr.appendChild(td);
        }
        const td = document.createElement("td");
        td.dataset.label = t("settings.matrix.preview");
        const preview = document.createElement("button");
        preview.type = "button";
        preview.textContent = t("settings.play");
        preview.setAttribute("aria-label", t("settings.preview", { label }));
        preview.onclick = () => previewSounds([event], s);
        td.appendChild(preview);
        tr.appendChild(td);
        tbody.appendChild(tr);
    }
    const matrixWrap = document.createElement("div");
    matrixWrap.className = "notification-matrix-wrap";
    matrixWrap.appendChild(matrix);
    delivery.appendChild(matrixWrap);
    delivery.appendChild(hint(t("settings.pokePopup")));
    delivery.appendChild(hint(t("settings.previews.use.your.sound.volume.and.event.choices.dnd.silences.all.previews")));
    // (347/348) do-not-disturb: toggle + quiet hours schedule.
    quiet.appendChild(row(t("settings.do.not.disturb"), checkbox(s.dnd_enabled, (v) => { s.dnd_enabled = v; })));
    const from = document.createElement("input");
    from.type = "time"; from.className = "dlg-input";
    from.setAttribute("aria-label", t("settings.quiet.from"));
    from.value = s.dnd_from || "";
    from.onchange = () => { s.dnd_from = from.value; };
    const to = document.createElement("input");
    to.type = "time"; to.className = "dlg-input";
    to.setAttribute("aria-label", t("settings.quiet.to"));
    to.value = s.dnd_to || "";
    to.onchange = () => { s.dnd_to = to.value; };
    const hours = document.createElement("div");
    hours.className = "dnd-hours";
    hours.append(from, document.createTextNode(" – "), to);
    quiet.appendChild(row(t("settings.quiet.hours.empty.off"), hours));
    quiet.appendChild(hint(t("settings.dnd.suppresses.toasts.sounds.and.taskbar.flashes.mentions.still.badge.silen")));
    quiet.appendChild(row(t("settings.toasts.for.join.leave"), checkbox(s.notify_join_leave, (v) => { s.notify_join_leave = v; })));
    quiet.appendChild(row(t("settings.toasts.for.connection.events"), checkbox(s.notify_connection, (v) => { s.notify_connection = v; })));
    quiet.appendChild(row(t("settings.warn.when.talking.while.muted"), checkbox(s.warn_muted_talking !== false, (v) => { s.warn_muted_talking = v; })));
    quiet.appendChild(row(t("settings.hint.when.talking.to.an.empty.channel"), checkbox(s.warn_empty_channel !== false, (v) => { s.warn_empty_channel = v; })));

    effects.appendChild(row(t("settings.sound.volume"), slider(s.sound_volume ?? 100, 0, 200, (v) => { s.sound_volume = v; }, true)));
    effects.appendChild(row(t("settings.sound.effects"), checkbox(s.effects_enabled !== false, v => { s.effects_enabled = v; })));
    effects.appendChild(row(t("settings.audio.duck"), checkbox(s.duck_effects_while_speaking, v => { s.duck_effects_while_speaking = v; })));
    effects.appendChild(hint(t("settings.audio.duck.hint")));
    speech.appendChild(row(t("settings.spoken.system.messages"), checkbox(s.spoken_messages !== false, v => { s.spoken_messages = v; })));
    speech.appendChild(row(t("settings.speech.volume"), slider(s.speech_volume ?? 100, 0, 200, v => { s.speech_volume = v; }, true)));
    const speechLocale = document.createElement("select");
    for (const [value, label] of [["interface", t("settings.audio.follow.interface")], ["en", "English"], ["de", "Deutsch"]]) {
        const option = document.createElement("option"); option.value = value; option.textContent = label; speechLocale.appendChild(option);
    }
    speechLocale.value = s.speech_language || "interface";
    speechLocale.onchange = () => { s.speech_language = speechLocale.value; stopPreviews(); renderPage("notifications"); };
    speech.appendChild(row(t("settings.audio.speech.language"), speechLocale));
    speech.appendChild(row(t("settings.speak.connection.problems"), checkbox(s.speech_connection !== false, v => { s.speech_connection = v; })));
    speech.appendChild(row(t("settings.speak.administrative.actions"), checkbox(s.speech_admin !== false, v => { s.speech_admin = v; })));
    speech.appendChild(row(t("settings.speak.removal"), checkbox(s.speech_removal !== false, v => { s.speech_removal = v; })));
    speech.appendChild(row(t("settings.speak.permissions"), checkbox(s.speech_permissions !== false, v => { s.speech_permissions = v; })));
    const speechTest = document.createElement("button");
    speechTest.type = "button";
    speechTest.textContent = t("settings.test.spoken.message");
    speechTest.onclick = () => previewSpeech(s, ["test"], event => {
        status.textContent = event ? t("settings.playing", { label: speechPreviewLabel(event, s) }) : t("settings.preview.finished");
    });
    speech.appendChild(hint(t("settings.notifications.speechReplaces")));
    speech.appendChild(row(t("settings.speak.channel.activity"), checkbox(s.speech_channel !== false, v => { s.speech_channel = v; })));
    speech.appendChild(hint(t("settings.previews.use.unsaved.settings.master.mute.can.be.bypassed.for.previews.dnd")));
    const status = document.createElement("div");
    status.className = "set-hint";
    status.setAttribute("role", "status");
    const onLabel = (label, event, spoken) => {
        status.textContent = label ? t("settings.playing", { label: spoken ? label : t("settings.sound." + event) }) : t("settings.preview.finished");
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
    controls.prepend(speechTest);
    output.append(controls, status);
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
    for (const [category, label] of [["connection", "settings.speak.connection.problems"], ["admin", "settings.speak.administrative.actions"], ["channel", "settings.speak.channel.activity"]]) {
        speechControls.appendChild(speechButton(t("settings.preview", { label: t(label) }), Object.keys(SPEECH_EVENTS).filter(id => SPEECH_EVENTS[id].category === category)));
    }
    const speechEvents = document.createElement("details");
    speechEvents.className = "notification-event-speech";
    const speechSummary = document.createElement("summary");
    speechSummary.textContent = t("settings.notifications.eventSpeech");
    speechEvents.appendChild(speechSummary);
    speech.append(speechControls, speechEvents);
    for (const event of Object.keys(SPEECH_EVENTS)) {
        const label = speechPreviewLabel(event, s);
        const controls = document.createElement("div");
        controls.className = "sound-controls";
        if (event !== "test") controls.appendChild(checkbox(speechEventEnabled(s, event), enabled => {
            s.speech_events ||= {};
            s.speech_events[event] = enabled;
        }));
        const b = speechButton(t("settings.play"), [event]);
        b.setAttribute("aria-label", t("settings.preview", { label }));
        controls.appendChild(b);
        speechEvents.appendChild(row(label, controls));
    }
    for (const group of SOUND_EVENT_GROUPS) {
        const heading = document.createElement("div");
        heading.className = "set-subhead sound-controls";
        const label = document.createElement("span");
        label.textContent = t("settings.soundgroup." + group.key);
        heading.append(label, button(t("settings.preview", { label: currentLanguage() === "en" ? label.textContent.toLowerCase() : label.textContent }), group.events.map(([event]) => event)));
        eventEffects.appendChild(heading);
        for (const [event] of group.events) {
            const label = t("settings.sound." + event);
            const controls = document.createElement("div");
            controls.className = "sound-controls";
            controls.append(checkbox(s.event_sounds?.[event] !== false, (enabled) => {
                s.event_sounds = s.event_sounds || {};
                s.event_sounds[event] = enabled;
            }), button(t("settings.play"), [event]));
            controls.querySelector("button").setAttribute("aria-label", t("settings.preview", { label }));
            eventEffects.appendChild(row(label, controls));
        }
    }
    return el;
}

function pageOverlay() { return gamingOverlaySettings(settings(), { row, checkbox, slider, hint, group, page }); }

const PAGE_BUILDERS = {
    general: pageGeneral,
    audio: pageAudio,
    streams: pageStreams,
    overlay: pageOverlay,
    hotkeys: pageHotkeys,
    appearance: pageAppearance,
    privacy: pagePrivacy,
    chat: pageChat,
    notifications: pageNotifications,
    whisper: pageWhisper,
    connection: pageConnection,
};

function setPageHeader(title, description) {
    const heading = document.getElementById("settings-page-title");
    const desc = document.getElementById("settings-page-desc");
    if (heading) heading.textContent = title;
    if (desc) { desc.textContent = description; desc.hidden = !description; }
}

// revealSettingsTarget scrolls to and focuses a deep-linked section or row.
function revealSettingsTarget(container, { section = "", setting = "" } = {}) {
    const escape = value => globalThis.CSS?.escape ? CSS.escape(value) : value.replace(/["\\]/g, "\\$&");
    const target = (setting && container.querySelector(`[data-setting="${escape(setting)}"]`))
        || (section && container.querySelector(`[data-section="${escape(section)}"]`));
    if (!target) return false;
    for (let parent = target; parent && parent !== container; parent = parent.parentElement) {
        if (parent.tagName === "DETAILS") parent.open = true;
    }
    target.scrollIntoView?.({ block: "start" });
    const selector = "input:not([disabled]):not([type=hidden]), select:not([disabled]), textarea:not([disabled]), button:not([disabled])";
    const control = setting ? target.querySelector(selector) : null;
    if (setting) target.classList.add("set-hit");
    if (control) control.focus({ preventScroll: true });
    else {
        const heading = target.querySelector(".set-group-title, summary") || target;
        if (heading.tabIndex < 0 && !heading.matches("summary")) heading.tabIndex = -1;
        heading.focus({ preventScroll: true });
    }
    return true;
}

function renderPage(id, focus = {}) {
    document.querySelector(".security-page")?.dispose();
    stopPreviews();
    stopMicCheck();
    stopCameraTest();
    cancelHotkeyCapture();
    if (!PAGE_BUILDERS[id]) id = "general";
    document.querySelectorAll(".settings-nav-item").forEach((n) => {
        const active = n.dataset.page === id;
        n.classList.toggle("active", active);
        n.setAttribute("aria-selected", String(active));
        n.tabIndex = active ? 0 : -1;
    });
    const container = document.getElementById("settings-content");
    const summary = document.querySelector(".settings-search-summary");
    if (summary) { summary.textContent = ""; summary.hidden = true; }
    const info = PAGES.find(p => p.id === id);
    setPageHeader(t(info.label), t(info.description));
    container.setAttribute("aria-labelledby", `settings-page-${id}`);
    container.dataset.page = id;
    container.innerHTML = "";
    const built = PAGE_BUILDERS[id]();
    container.appendChild(built);
    container.scrollTop = 0;
    document.querySelector(".settings-footer-extra")?.replaceChildren(...(built.footerActions || []));
    const reset = document.getElementById("set-reset");
    if (reset) reset.hidden = false;
    if (focus.section || focus.setting) revealSettingsTarget(container, focus);
}

function translateDialog(overlay) {
    overlay.querySelector("#settings-title").textContent = t("settings.settings");
    overlay.querySelector(".settings-nav").setAttribute("aria-label", t("settings.settings.sections"));
    overlay.querySelector('label[for="settings-search"]').textContent = t("settings.search.settings");
    overlay.querySelector("#settings-search").placeholder = t("settings.searchPlaceholder");
    for (const [id, key] of [["set-ok", "settings.done"], ["set-cancel", "common.cancel"], ["set-apply", "common.apply"], ["set-reset", "settings.resetPage"]]) {
        overlay.querySelector("#" + id).textContent = t(key);
    }
    overlay.querySelector(".settings-close").setAttribute("aria-label", t("settings.close"));
    overlay.querySelector(".settings-close").title = t("settings.close");
    for (const page of PAGES) {
        overlay.querySelector(`#settings-page-${page.id} span:last-child`).textContent = t(page.label);
    }
    const active = overlay.querySelector(".settings-nav-item.active")?.dataset.page;
    const info = PAGES.find(p => p.id === active);
    if (info) setPageHeader(t(info.label), t(info.description));
}

// openSettings opens the dialog on a page. target is a page id (current or
// pre-redesign, e.g. "capture"), "page/section", or { page, section,
// setting }; options may add section / setting to focus a group or a row.
function openSettings(target = "general", options = {}) {
    if (document.getElementById("settings-overlay")?.getAttribute("aria-busy") === "true") return;
    if (document.getElementById("settings-overlay")?.dataset.identityBusy === "true") return;
    const destination = resolveSettingsTarget(target, options);
    draft = JSON.parse(JSON.stringify(V().state.settings || {}));
    const whisperScope = captureMediaScope();
    const whisperConfig = (s) => JSON.stringify([!!s?.whisper_active, s?.whisper_clients || [], s?.whisper_channels || []]);
    // Local persistence is independent of live routing. Keep failed live changes
    // pending so Apply can retry the same draft after a server rejection.
    let appliedWhisperConfig = whisperConfig(draft);
    deviceInventory.invalidate();

    let overlay = document.getElementById("settings-overlay");
    if (overlay) closeDialog(overlay, "cancel");

    overlay = document.createElement("div");
    overlay.id = "settings-overlay";
    overlay.className = "dlg-overlay settings-modal-overlay";
    overlay.innerHTML = `
        <div class="settings-dialog settings-modal">
            <aside class="settings-rail">
                <h2 id="settings-title" class="settings-rail-title"></h2>
                <label class="sr-only" for="settings-search"></label>
                <input id="settings-search" class="dlg-input settings-search" type="search" placeholder="" autocomplete="off" />
                <div class="settings-nav" role="tablist" aria-orientation="vertical"></div>
            </aside>
            <div class="settings-main">
                <header class="settings-head">
                    <div class="settings-head-text">
                        <h3 id="settings-page-title" class="settings-page-title"></h3>
                        <p id="settings-page-desc" class="settings-page-desc"></p>
                    </div>
                    <button type="button" class="settings-close icon-btn">${icon("close")}</button>
                </header>
                <div class="settings-search-summary set-hint" role="status" hidden></div>
                <div id="settings-content" role="tabpanel" tabindex="-1"></div>
                <div class="settings-footer">
                    <button id="set-reset" type="button" class="settings-reset"></button>
                    <div class="settings-save-status set-hint" role="status" hidden></div>
                    <div class="settings-footer-extra"></div>
                    <button id="set-cancel" type="button"></button>
                    <button id="set-apply" type="button"></button>
                    <button id="set-ok" type="button"></button>
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
            renderPage(document.querySelector(".settings-nav-item.active")?.dataset.page || "general");
            return;
        }
        setPageHeader(t("settings.searchResults"), "");
        overlay.querySelector(".settings-footer-extra").replaceChildren();
        overlay.querySelector("#set-reset").hidden = true;
        if (!searchIndex || searchLanguage !== currentLanguage()) {
            searchIndex = [];
            searchLanguage = currentLanguage();
            for (const p of PAGES) {
                const pageEl = PAGE_BUILDERS[p.id]({ detached: true });
                pageEl.querySelectorAll(".set-row, .set-subhead, .set-group-title, .set-hint, button").forEach((r) => {
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
                const match = [...content.querySelectorAll(".set-row, .set-subhead, .set-group-title, .set-hint, button")].find(r =>
                    (r.querySelector(".set-label")?.textContent || r.textContent || "").toLowerCase().trim() === h.label);
                if (!match) return;
                for (let parent = match.parentElement; parent && parent !== content; parent = parent.parentElement) {
                    if (parent.tagName === "DETAILS") parent.open = true;
                }
                const highlighted = match.closest(".set-row") || match;
                highlighted.classList.add("set-hit");
                highlighted.scrollIntoView({ block: "center" });
                const selector = "input:not([disabled]):not([type=hidden]), select:not([disabled]), textarea:not([disabled]), button:not([disabled])";
                const target = match.matches(selector) ? match : match.querySelector(selector);
                if (target) target.focus({ preventScroll: true });
                else { highlighted.tabIndex = -1; highlighted.focus({ preventScroll: true }); }
            };
            content.appendChild(row);
        }
    };

    const nav = overlay.querySelector(".settings-nav");
    const narrowLayout = window.matchMedia("(max-width: 600px)");
    const updateNavOrientation = () => nav.setAttribute("aria-orientation", narrowLayout.matches ? "horizontal" : "vertical");
    updateNavOrientation();
    narrowLayout.addEventListener("change", updateNavOrientation);
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
        if (p.rail === "more" && !nav.querySelector(".rail-more-start")) item.classList.add("rail-more-start");
        item.innerHTML = `<span class="nav-icon" aria-hidden="true">${railIcon(p.icon)}</span><span>${t(p.label)}</span>`;
        item.onclick = () => { search.value = ""; renderPage(p.id); };
        item.addEventListener("keydown", (event) => {
            if (!["ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
            event.preventDefault();
            const items = [...nav.querySelectorAll(".settings-nav-item")];
            const next = event.key === "Home" ? 0 : event.key === "End" ? items.length - 1
                : wrappedIndex(items.indexOf(item), items.length, ["ArrowDown", "ArrowRight"].includes(event.key) ? 1 : -1);
            items[next].focus();
            items[next].click();
        });
        nav.appendChild(item);
    }

    let saving = false, protectionPending = false;
    const saveStatus = overlay.querySelector(".settings-save-status");
    let savedDraft = JSON.stringify(draft), lastDraft = savedDraft, applyFailure = "";
    saveStatus.hidden = false;
    saveStatus.textContent = t("settings.clean");
    const markChanges = () => queueMicrotask(() => {
        const next = JSON.stringify(draft);
        if (saving || next === lastDraft || !overlay.isConnected) return;
        lastDraft = next;
        saveStatus.classList.toggle("warn", !!applyFailure);
        saveStatus.textContent = applyFailure
            ? (next === savedDraft ? applyFailure : `${t("settings.unsaved")} · ${applyFailure}`)
            : t(next === savedDraft ? "settings.clean" : "settings.unsaved");
    });
    for (const event of ["input", "change", "click"]) content.addEventListener(event, markChanges);
    const applyAll = async () => {
        if (saving || overlay.dataset.identityBusy === "true") return false;
        for (const control of content.querySelectorAll("input,select,textarea")) {
            if (!control.reportValidity()) return false;
        }
        saving = true;
        const snapshot = structuredClone(draft);
        const protectionMode = value => value === "off" ? "off" : "auto";
        protectionPending ||= protectionMode(snapshot.identity_key_protection) !== protectionMode(V().state.settings.identity_key_protection)
            || overlay.querySelector(".settings-nav-item.active")?.dataset.page === "privacy";
        const applyingProtection = protectionPending;
        let persisted = false;
        const nextWhisperConfig = whisperConfig(snapshot);
        // Applying the whisper editor explicitly reapplies routing, including
        // a configuration saved locally but rejected before reopening it.
        const whisperChanged = appliedWhisperConfig !== nextWhisperConfig ||
            overlay.querySelector(".settings-nav-item.active")?.dataset.page === "whisper";
        const previousLanguage = currentLanguage();
        const refreshDraftControls = () => {
            if (!overlay.isConnected) return;
            if (previousLanguage !== currentLanguage()) translateDialog(overlay);
            invalidateSearch();
            if (search.value) search.oninput();
            else renderPage(overlay.querySelector(".settings-nav-item.active")?.dataset.page || "general");
        };
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
            persisted = true;
            if (!overlay.isConnected) return false;
            savedDraft = lastDraft = JSON.stringify(draft);
            if (protectionPending) {
                try { await window.go.main.App.ApplyIdentityProtection(); }
                catch (failure) { throw new SavedIdentityProtectionError(t("security.protectionFailed", { error: failure.message || String(failure) })); }
                protectionPending = false;
            }
            refreshDraftControls();
            // Local preferences also save while disconnected. A live whisper
            // update belongs only to the server where this dialog opened.
            if (whisperChanged && V().state.myClientID && mediaScopeIsCurrent(whisperScope)) {
                const error = await setWhisperRouting({
                    clients: draft.whisper_active ? draft.whisper_clients || [] : [],
                    channels: draft.whisper_active ? draft.whisper_channels || [] : [],
                    active: !!draft.whisper_active,
                }, whisperScope);
                if (error) throw new Error(error);
                if (error === "") {
                    appliedWhisperConfig = whisperConfig(draft);
                    V().state.whisperArmed = false;
                    V().state.whisperTargetUID = "";
                    V().state.whisperPrev = null;
                    V().renderVoiceStatus();
                }
            }
            applyFailure = "";
            saveStatus.textContent = t("settings.applied") + (applyingProtection ? " " + t("security.protectionApplied") : "");
            return true;
        } catch (error) {
            // Persistence already rebased nested draft objects. Rebind controls
            // even if audio application failed, so subsequent edits reach them.
            if (error instanceof SavedAudioSettingsError || error instanceof SavedIdentityProtectionError) {
                savedDraft = lastDraft = JSON.stringify(draft);
                refreshDraftControls();
            }
            if (overlay.isConnected) {
                saveStatus.textContent = error instanceof SavedAudioSettingsError || error instanceof SavedIdentityProtectionError
                    ? error.message
                    : t("menu.saveFailed", { error: error.message || String(error) });
                saveStatus.classList.add("warn");
                if (persisted || error instanceof SavedAudioSettingsError) applyFailure = saveStatus.textContent;
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
    overlay.querySelector(".settings-close").onclick = cancel;
    overlay.onclick = (e) => { if (e.target === overlay) cancel(); };

    // "Reset to default" restores the active page's keys in the draft only;
    // Done/Apply saves them and Cancel discards them like any other edit.
    let defaultsRequest = null;
    overlay.querySelector("#set-reset").onclick = async () => {
        const pageId = overlay.querySelector(".settings-nav-item.active")?.dataset.page;
        if (!pageId || saving || search.value) return;
        defaultsRequest ||= Promise.resolve()
            .then(() => window.go.main.App.DefaultSettingsSnapshot?.())
            .catch(() => null);
        const defaults = await defaultsRequest;
        if (!overlay.isConnected || saving) return;
        saveStatus.hidden = false;
        if (!defaults || typeof defaults !== "object") {
            defaultsRequest = null;
            saveStatus.classList.add("warn");
            saveStatus.textContent = t("settings.resetUnavailable");
            return;
        }
        const changed = resetPageDraft(draft, defaults, pageId);
        if (pageId === "appearance") {
            // Theme swatches and window opacity preview live; show the reset too.
            const style = document.getElementById("user-css");
            if (style) style.textContent = draft.user_css || "";
            window.go.main.App.SetWindowOpacity?.(draft.window_opacity || 100);
        }
        renderPage(pageId);
        markChanges();
        const label = t(PAGES.find(p => p.id === pageId).label);
        queueMicrotask(() => {
            if (!overlay.isConnected) return;
            saveStatus.classList.remove("warn");
            saveStatus.textContent = changed.length ? t("settings.resetDone", { page: label }) : t("settings.resetNothing", { page: label });
        });
    };

    translateDialog(overlay);
    mountDialog(overlay, {
        initialFocus: () => overlay.querySelector(".settings-nav-item.active"),
        onCancel: () => !saving && overlay.dataset.identityBusy !== "true",
        onClose: () => {
            content.querySelector(".security-page")?.dispose();
            narrowLayout.removeEventListener("change", updateNavOrientation);
            window.removeEventListener("noxa-audio-status", refreshAudioStatus);
            window.removeEventListener("noxa-audio-preview-blocked", previewBlocked);
            stopPreviews();
            stopMicCheck();
            stopCameraTest();
            cancelHotkeyCapture();
            revertLivePreview();
        },
    });
    renderPage(destination.page, destination);
}

// settingsPages lists the dialog's pages for launchers such as the quick
// switcher: { id, label, description, keywords } in rail order.
export function settingsPages() {
    return PAGES.map(p => ({
        id: p.id, label: t(p.label), description: t(p.description),
        keywords: SETTINGS_CATEGORIES.find(c => c.id === p.id).keys,
    }));
}

export function initSettingsUI() {
    window.__noxa.openSettings = openSettings;
    window.__noxa.settingsPages = settingsPages;
    // Deep links from UI that does not import this module:
    // window.dispatchEvent(new CustomEvent("noxa-open-settings", { detail: { page: "overlay" } })).
    window.addEventListener("noxa-open-settings", event => {
        openSettings(event.detail || "general");
    });
    // (301) track registration errors for the hotkey map rows.
    window.runtime.EventsOn("hotkey_status", (st) => {
        if (st.error) hkErrors.set(st.action, st.error);
        else hkErrors.delete(st.action);
        for (const row of document.querySelectorAll(".hk-map-row")) {
            if (row.dataset.hotkeyAction === st.action) row.querySelector(".hk-err").textContent = st.error || "";
        }
    });
}
