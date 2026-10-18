// share-picker.js — the share-screen dialog view (P02): source tabs, a large
// live preview of the source picked in the system picker (with an area
// selector for Region), and a bar whose quality and audio buttons open small
// panels. Capture and publication stay in video-publication.js.
import { t } from "./i18n.js";
import { createShareQualityControls, shareSummary } from "./screen-share-quality.js";
import { normalizeRegion, regionLargeEnough } from "./share-region.js";

const icon = path => `<svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false">${path}</svg>`;
const ICONS = {
    monitor: icon('<rect x="3" y="4" width="18" height="13" rx="2"/><path d="M8 21h8M12 17v4"/>'),
    window: icon('<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M3 9h18"/>'),
    region: icon('<path d="M4 8V4h4M16 4h4v4M20 16v4h-4M8 20H4v-4"/><rect x="8" y="8" width="8" height="8" rx="1"/>'),
    choose: icon('<rect x="3" y="4" width="18" height="13" rx="2"/><path d="M12 8v5M9.5 10.5h5M8 21h8"/>'),
    chevron: icon('<path d="M6 15l6-6 6 6"/>'),
    audio: icon('<path d="M11 5L6 9H3v6h3l5 4z"/><path d="M15.5 8.5a5 5 0 0 1 0 7M18.5 5.5a9 9 0 0 1 0 13"/>'),
    muted: icon('<path d="M11 5L6 9H3v6h3l5 4z"/><path d="M22 9l-6 6M16 9l6 6"/>'),
};
const AUDIO = [["none", "share.audioNone"], ["application", "share.audioApplication"], ["system", "share.audioSystem"]];
export const DEFAULT_REGION = Object.freeze({ x: 0.2, y: 0.2, width: 0.6, height: 0.6 });

// contentBox is where a contain-fitted video actually paints inside its box.
export function contentBox(boxWidth, boxHeight, videoWidth, videoHeight) {
    if (!(boxWidth > 0 && boxHeight > 0 && videoWidth > 0 && videoHeight > 0)) return { left: 0, top: 0, width: boxWidth || 0, height: boxHeight || 0 };
    const scale = Math.min(boxWidth / videoWidth, boxHeight / videoHeight);
    const width = videoWidth * scale, height = videoHeight * scale;
    return { left: (boxWidth - width) / 2, top: (boxHeight - height) / 2, width, height };
}

// moveRegion shifts and/or resizes a region by fractions, keeping it inside.
export function moveRegion(region, dx, dy, dw = 0, dh = 0) {
    const width = Math.min(1, Math.max(0.05, region.width + dw)), height = Math.min(1, Math.max(0.05, region.height + dh));
    return { x: Math.min(1 - width, Math.max(0, region.x + dx)), y: Math.min(1 - height, Math.max(0, region.y + dy)), width, height };
}

export function createSharePicker({ id, preset, custom, bitrateCeiling, surface = "monitor", audio = "none", regionSupported = true }) {
    const dialog = document.createElement("div");
    dialog.className = "dlg share-dlg share-picker";
    const sourceName = `${id}-source`;
    dialog.innerHTML = `
        <div class="sp-head">
            <h3>${t("polish.shareTitle")}</h3>
            <button type="button" class="sp-close" aria-label="${t("share.close")}">${icon('<path d="M6 6l12 12M18 6L6 18"/>')}</button>
        </div>
        <fieldset class="share-sources sp-tabs">
            <legend class="sr-only">${t("polish.source")}</legend>
            ${["monitor", "window", "region"].map(value => `
            <label class="sp-tab"${value === "region" ? ` title="${t(regionSupported ? "polish.cropHelp" : "polish.cropUnavailable")}"` : ""}>
                <input type="radio" name="${sourceName}" value="${value}" ${value === "region" && !regionSupported ? "disabled" : ""} />
                ${ICONS[value]}<span>${t(`polish.${value}`)}</span>
            </label>`).join("")}
        </fieldset>
        <div class="sp-stage">
            <div class="sp-empty">
                <button type="button" class="sh-choose sp-choose" aria-describedby="${id}-choose-help">${ICONS.choose}<span>${t("share.chooseSource")}</span></button>
                <p class="set-hint sp-choose-help" id="${id}-choose-help"></p>
            </div>
            <div class="sp-preview" hidden>
                <video class="sh-preview" autoplay muted playsinline aria-label="${t("share.selectionPreview")}"></video>
                <div class="sp-region" hidden tabindex="0" role="group" aria-label="${t("share.regionArea")}" aria-describedby="${id}-region-help">
                    <div class="sp-region-box"><span class="sp-region-handle" aria-hidden="true"></span></div>
                </div>
                <div class="sp-preview-meta">
                    <span class="sh-source-label sp-source-label"></span>
                    <button type="button" class="sp-change">${t("share.changeSource")}</button>
                </div>
            </div>
        </div>
        <p class="set-hint sp-region-help" id="${id}-region-help" hidden></p>
        <div class="sp-notes"></div>
        <p class="sh-error" role="alert"></p>
        <div class="sp-bar">
            <div class="sp-anchor">
                <button type="button" class="sp-toggle sp-quality-toggle" aria-expanded="false" aria-controls="${id}-quality-panel">
                    <span class="sp-toggle-text"><span class="sp-q-name"></span><span class="sp-q-spec"></span></span>${ICONS.chevron}
                </button>
                <div class="sp-panel sp-quality-panel" id="${id}-quality-panel" hidden></div>
            </div>
            <div class="sp-anchor">
                <button type="button" class="sp-toggle sp-audio-toggle" aria-expanded="false" aria-controls="${id}-audio-panel">
                    <span class="sp-audio-icon"></span><span class="sp-audio-text"></span>${ICONS.chevron}
                </button>
                <div class="sp-panel sp-audio-panel" id="${id}-audio-panel" hidden>
                    <fieldset class="sh-audio sq-presets" aria-describedby="${id}-audio-help">
                        <legend class="dlg-label">${t("share.audio")}</legend>
                        ${AUDIO.map(([value, key]) => `<label class="sq-option"><input type="radio" name="${id}-audio" value="${value}" /><span class="sq-name">${t(key)}</span></label>`).join("")}
                    </fieldset>
                    <p class="set-hint" id="${id}-audio-help">${t("share.applicationHelp")}</p>
                </div>
            </div>
            <div class="dlg-buttons">
                <button type="button" class="dlg-cancel">${t("polish.cancel")}</button>
                <button type="button" class="dlg-ok primary">${t("voice.startShare")}</button>
            </div>
        </div>`;
    const $ = selector => dialog.querySelector(selector);
    const quality = createShareQualityControls(`${id}-quality`, { preset, custom, bitrateCeiling });
    $(".sp-quality-panel").appendChild(quality.element);

    // ---- bar panels
    const anchors = [...dialog.querySelectorAll(".sp-anchor")].map(anchor => ({
        toggle: anchor.querySelector(".sp-toggle"), panel: anchor.querySelector(".sp-panel"), anchor }));
    const closePanels = (except = null) => {
        for (const entry of anchors) if (entry !== except && !entry.panel.hidden) {
            entry.panel.hidden = true;
            entry.toggle.setAttribute("aria-expanded", "false");
        }
        if (!anchors.some(entry => !entry.panel.hidden)) window.removeEventListener("keydown", escape, true);
    };
    const escape = event => {
        if (event.key !== "Escape" || !dialog.isConnected) return;
        const open = anchors.find(entry => !entry.panel.hidden);
        if (!open) return;
        // Runs before the modal stack's own handler: Escape closes the panel only.
        event.preventDefault();
        event.stopImmediatePropagation();
        closePanels();
        open.toggle.focus();
    };
    for (const entry of anchors) {
        entry.toggle.onclick = () => {
            const opening = entry.panel.hidden;
            closePanels(entry);
            entry.panel.hidden = !opening;
            entry.toggle.setAttribute("aria-expanded", String(opening));
            if (!opening) return;
            window.addEventListener("keydown", escape, true);
            (entry.panel.querySelector("input:checked:not(:disabled)") || entry.panel.querySelector("input:not(:disabled), select"))?.focus();
        };
    }
    dialog.addEventListener("pointerdown", event => {
        if (!anchors.some(entry => entry.anchor.contains(event.target))) closePanels();
    });
    quality.onChange(selection => {
        const summary = shareSummary(selection);
        $(".sp-q-name").textContent = summary.name;
        $(".sp-q-spec").textContent = summary.spec;
    });

    // ---- source tabs
    const selectedSurface = () => dialog.querySelector(`input[name="${sourceName}"]:checked`)?.value || "monitor";
    (dialog.querySelector(`input[name="${sourceName}"][value="${surface}"]:not(:disabled)`) ||
        dialog.querySelector(`input[name="${sourceName}"][value="monitor"]`)).checked = true;

    // ---- audio
    const audioInput = value => dialog.querySelector(`input[name="${id}-audio"][value="${value}"]`);
    const audioValue = () => dialog.querySelector(`input[name="${id}-audio"]:checked`)?.value || "none";
    const syncAudioLabel = () => {
        const value = audioValue();
        for (const option of $(".sh-audio").querySelectorAll(".sq-option")) option.classList.toggle("selected", option.querySelector("input").checked);
        $(".sp-audio-icon").innerHTML = value === "none" ? ICONS.muted : ICONS.audio;
        $(".sp-audio-text").textContent = t(AUDIO.find(([key]) => key === value)[1]);
        $(".sp-audio-toggle").setAttribute("aria-label", `${t("share.audio")}: ${$(".sp-audio-text").textContent}`);
    };
    const setAudio = value => { (audioInput(value) || audioInput("none")).checked = true; syncAudioLabel(); };
    setAudio(audio);
    $(".sh-audio").addEventListener("change", syncAudioLabel);

    // ---- preview and region
    const video = $(".sh-preview"), layer = $(".sp-region"), box = $(".sp-region-box"), regionHelp = $(".sp-region-help");
    let region = null, sourceSize = { width: 0, height: 0 };
    const regionMode = () => selectedSurface() === "region" && !$(".sp-preview").hidden;
    const placeLayer = () => {
        const rect = contentBox(video.clientWidth, video.clientHeight, video.videoWidth, video.videoHeight);
        Object.assign(layer.style, { left: `${video.offsetLeft + rect.left}px`, top: `${video.offsetTop + rect.top}px`, width: `${rect.width}px`, height: `${rect.height}px` });
    };
    const drawRegion = () => {
        layer.hidden = !regionMode();
        regionHelp.hidden = layer.hidden;
        if (layer.hidden || !region) return;
        placeLayer();
        Object.assign(box.style, { left: `${region.x * 100}%`, top: `${region.y * 100}%`, width: `${region.width * 100}%`, height: `${region.height * 100}%` });
        const width = Math.round(region.width * sourceSize.width), height = Math.round(region.height * sourceSize.height);
        regionHelp.textContent = t("share.regionHelp", { width, height });
    };
    const resize = new ResizeObserver(() => drawRegion());
    resize.observe(video);
    const changeListeners = new Set();
    const notify = () => { for (const listener of changeListeners) listener(); };
    // The capture's settings can lack a size; the first decoded frame has one.
    video.addEventListener("loadedmetadata", () => {
        sourceSize = { width: video.videoWidth, height: video.videoHeight };
        drawRegion();
        notify();
    });
    const point = event => {
        const rect = layer.getBoundingClientRect();
        return { x: (event.clientX - rect.left) / (rect.width || 1), y: (event.clientY - rect.top) / (rect.height || 1) };
    };
    const setRegion = next => { region = next; drawRegion(); notify(); };
    layer.addEventListener("pointerdown", event => {
        if (event.button !== 0 || !region) return;
        event.preventDefault();
        layer.setPointerCapture?.(event.pointerId);
        layer.focus();
        const start = point(event), before = region;
        const mode = event.target.classList.contains("sp-region-handle") ? "resize" : event.target === box ? "move" : "draw";
        const move = moveEvent => {
            const now = point(moveEvent);
            if (mode === "move") setRegion(moveRegion(before, now.x - start.x, now.y - start.y));
            else if (mode === "resize") setRegion(normalizeRegion({ x: before.x, y: before.y }, now));
            else setRegion(normalizeRegion(start, now));
        };
        const up = () => {
            layer.removeEventListener("pointermove", move);
            layer.removeEventListener("pointerup", up);
            layer.removeEventListener("pointercancel", up);
            if (!regionLargeEnough(region, sourceSize.width, sourceSize.height)) setRegion(before);
        };
        layer.addEventListener("pointermove", move);
        layer.addEventListener("pointerup", up);
        layer.addEventListener("pointercancel", up);
    });
    layer.addEventListener("keydown", event => {
        const step = event.shiftKey ? 0 : 0.02, grow = event.shiftKey ? 0.02 : 0;
        const keys = { ArrowLeft: [-step, 0, -grow, 0], ArrowRight: [step, 0, grow, 0], ArrowUp: [0, -step, 0, -grow], ArrowDown: [0, step, 0, grow] };
        if (!keys[event.key] || !region) return;
        event.preventDefault();
        const next = moveRegion(region, ...keys[event.key]);
        if (regionLargeEnough(next, sourceSize.width, sourceSize.height)) setRegion(next);
    });

    const syncChooseHelp = () => {
        $(".sp-choose-help").textContent = t(selectedSurface() === "region" ? "share.chooseRegionHelp" : "share.chooseHelp");
    };
    syncChooseHelp();
    dialog.querySelector(".sp-tabs").addEventListener("change", () => { syncChooseHelp(); drawRegion(); });

    return {
        element: dialog,
        quality,
        $,
        sourceName,
        surface: selectedSurface,
        audio: audioValue,
        setAudio,
        setApplicationAudio(enabled) { audioInput("application").disabled = !enabled; },
        region: () => regionMode() && regionLargeEnough(region, sourceSize.width, sourceSize.height) ? { ...region } : null,
        onRegionChange: listener => changeListeners.add(listener),
        showPreview(stream, label) {
            const settings = stream.getVideoTracks()[0]?.getSettings?.() || {};
            sourceSize = { width: settings.width || 0, height: settings.height || 0 };
            region = { ...DEFAULT_REGION };
            video.srcObject = stream;
            $(".sp-source-label").textContent = label;
            $(".sp-empty").hidden = true;
            $(".sp-preview").hidden = false;
            void video.play().catch(() => {});
            drawRegion();
        },
        clearPreview() {
            video.srcObject = null;
            region = null;
            $(".sp-preview").hidden = true;
            $(".sp-empty").hidden = false;
            drawRegion();
        },
        closePanels,
        dispose() { resize.disconnect(); closePanels(); window.removeEventListener("keydown", escape, true); },
    };
}
