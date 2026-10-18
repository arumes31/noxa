import { t } from "./i18n.js";
import { videoConstraints } from "./media-limits.js";

const presets = {
    balanced: { width: 1280, height: 720, fps: 30 },
    text: { width: 1920, height: 1080, fps: 15 },
    motion: { width: 1280, height: 720, fps: 60 },
    hd: { width: 1920, height: 1080, fps: 30 },
    hdMotion: { width: 1920, height: 1080, fps: 60 },
    qhd: { width: 2560, height: 1440, fps: 30 },
    uhd: { width: 3840, height: 2160, fps: 30 },
    original: { width: 0, height: 0, fps: 30, original: true },
};

export function shareQuality(preset, custom = {}, bitrateCeiling = 0) {
    const ceiling = bitrateCeiling ?? 0;
    if (![0, 2000000, 4000000, 6000000, 8000000].includes(ceiling)) return null;
    const budget = ceiling ? { bitrateCeiling: ceiling } : {};
    if (preset !== "custom") return { ...(presets[preset] || presets.balanced), ...budget };
    const { width, height, fps } = custom;
    if (![width, height].every(n => Number.isInteger(n) && n >= 160 && n <= 8192) || ![15, 30, 60].includes(fps)) return null;
    return { width, height, fps, ...budget };
}

export function screenShareConstraints(profile, limits) {
    const bounded = limits?.video_max_width > 0 && limits?.video_max_height > 0;
    if (profile.original) {
        // Empty dictionaries also clear old size caps when a server relaxes its limits.
        return { width: bounded ? { max: limits.video_max_width } : {},
            height: bounded ? { max: limits.video_max_height } : {}, frameRate: { ideal: profile.fps, max: profile.fps } };
    }
    const constraints = videoConstraints(profile.width, profile.height, profile.fps, limits);
    constraints.width.max = constraints.width.ideal;
    constraints.height.max = constraints.height.ideal;
    constraints.frameRate.max = profile.fps;
    return constraints;
}

export const SHARE_PRESETS = ["balanced", "text", "motion", "hd", "hdMotion", "qhd", "uhd", "original", "custom"];
export const SHARE_BITRATES = [0, 2000000, 4000000, 6000000, 8000000];
export const DEFAULT_SHARE_PRESET = "hd";

// presetSpec is the short technical line shown next to a preset name.
export function presetSpec(preset, custom = {}) {
    if (preset === "custom") return `${custom.width}×${custom.height} · ${custom.fps} fps`;
    const profile = presets[preset] || presets[DEFAULT_SHARE_PRESET];
    if (profile.original) return t("share.specSource", { fps: profile.fps });
    return `${profile.height}p · ${profile.fps} fps`;
}

export const bitrateLabel = bitrate => bitrate ? `${bitrate / 1000000} Mbit/s` : t("share.bitrateAuto");

// shareSummary is the preset name and one line of numbers for the share bar.
export function shareSummary({ preset, custom = {}, bitrateCeiling = 0 }) {
    return { name: t(`share.preset.${SHARE_PRESETS.includes(preset) ? preset : DEFAULT_SHARE_PRESET}`),
        spec: `${presetSpec(preset, custom)} · ${bitrateLabel(bitrateCeiling)}` };
}

export function createShareQualityControls(id, { preset = DEFAULT_SHARE_PRESET, custom = {}, bitrateCeiling = 0 } = {}) {
    const element = document.createElement("div");
    element.className = "share-quality-controls";
    element.innerHTML = `
        <fieldset class="sh-preset sq-presets" aria-describedby="${id}-help ${id}-budget ${id}-warning">
            <legend class="dlg-label">${t("polish.preset")}</legend>
        </fieldset>
        <div class="share-custom" hidden>
            <div><label for="${id}-width">${t("share.width")}</label><input id="${id}-width" class="dlg-input sh-width" type="number" min="160" max="8192" step="1" required /></div>
            <div><label for="${id}-height">${t("share.height")}</label><input id="${id}-height" class="dlg-input sh-height" type="number" min="160" max="8192" step="1" required /></div>
            <div><label for="${id}-fps">${t("share.frameRate")}</label><select id="${id}-fps" class="dlg-input sh-fps" required><option value="15">15 fps</option><option value="30">30 fps</option><option value="60">60 fps</option></select></div>
        </div>
        <fieldset class="sh-bitrate sq-bitrates" aria-describedby="${id}-budget">
            <legend class="dlg-label">${t("share.bitrateCeiling")}</legend>
        </fieldset>
        <p class="set-hint share-quality-help" id="${id}-help"></p>
        <p class="set-hint share-budget" id="${id}-budget" role="status"></p>
        <p class="set-hint warn share-quality-warning" id="${id}-warning" role="status" hidden></p>`;
    const presetGroup = element.querySelector(".sh-preset"), bitrateGroup = element.querySelector(".sh-bitrate");
    for (const value of SHARE_PRESETS) {
        const option = document.createElement("label");
        option.className = "sq-option";
        option.innerHTML = `<input type="radio" name="${id}-preset" value="${value}" /><span class="sq-name"></span><span class="sq-spec"></span>`;
        option.querySelector(".sq-name").textContent = t(`share.preset.${value}`);
        presetGroup.appendChild(option);
    }
    for (const value of SHARE_BITRATES) {
        const option = document.createElement("label");
        option.className = "sq-segment";
        option.innerHTML = `<input type="radio" name="${id}-bitrate" value="${value}" /><span></span>`;
        option.querySelector("span").textContent = value ? String(value / 1000000) : t("share.bitrateAuto");
        if (value) option.title = bitrateLabel(value);
        bitrateGroup.appendChild(option);
    }
    const radio = (group, value) => group.querySelector(`input[value="${value}"]`);
    (radio(presetGroup, preset) || radio(presetGroup, DEFAULT_SHARE_PRESET)).checked = true;
    (radio(bitrateGroup, bitrateCeiling ?? 0) || radio(bitrateGroup, 0)).checked = true;
    const width = element.querySelector(".sh-width"), height = element.querySelector(".sh-height"), fps = element.querySelector(".sh-fps");
    width.value = custom.width ?? 2560; height.value = custom.height ?? 1440; fps.value = custom.fps ?? 30;
    const checked = group => group.querySelector("input:checked")?.value;
    const selection = () => ({ preset: checked(presetGroup), custom: { width: Number(width.value), height: Number(height.value), fps: Number(fps.value) }, bitrateCeiling: Number(checked(bitrateGroup)) });
    const listeners = new Set();
    const update = () => {
        const selected = selection(), profile = shareQuality(selected.preset, selected.custom, selected.bitrateCeiling);
        const isCustom = selected.preset === "custom";
        for (const option of presetGroup.querySelectorAll(".sq-option")) {
            const input = option.querySelector("input");
            option.querySelector(".sq-spec").textContent = presetSpec(input.value, selected.custom);
            option.classList.toggle("selected", input.checked);
        }
        for (const option of bitrateGroup.querySelectorAll(".sq-segment")) option.classList.toggle("selected", option.querySelector("input").checked);
        element.querySelector(".share-custom").hidden = !isCustom;
        width.disabled = height.disabled = fps.disabled = !isCustom;
        element.querySelector(".share-quality-help").textContent = t(selected.preset === "original" ? "share.originalHelp" : isCustom ? "share.customHelp" : "share.resolutionHelp");
        element.querySelector(".share-budget").textContent = profile ? selected.bitrateCeiling ? t("share.selectedTraffic", { bitrate: selected.bitrateCeiling / 1000000 }) : t("share.traffic") : "";
        const warning = element.querySelector(".share-quality-warning");
        warning.hidden = !profile || !(profile.original || profile.width > 1920 || profile.height > 1080 || profile.fps > 30);
        warning.textContent = warning.hidden ? "" : t("share.performanceWarning");
        for (const listener of listeners) listener(selected, profile);
    };
    presetGroup.onchange = bitrateGroup.onchange = update;
    for (const control of [width, height, fps]) control.oninput = control.onchange = update;
    update();
    return {
        element,
        read: () => {
            if (checked(presetGroup) === "custom" && ![width, height, fps].every(control => control.reportValidity())) return null;
            return selection();
        },
        selection,
        // onChange runs now and after every edit, so callers can mirror the choice.
        onChange: listener => {
            listeners.add(listener);
            const selected = selection();
            listener(selected, shareQuality(selected.preset, selected.custom, selected.bitrateCeiling));
        },
    };
}
