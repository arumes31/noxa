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

export function createShareQualityControls(id, { preset = "balanced", custom = {}, bitrateCeiling = 0 } = {}) {
    const element = document.createElement("div");
    element.className = "share-quality-controls";
    element.innerHTML = `
        <label class="dlg-label" for="${id}">${t("polish.preset")}</label>
        <select class="dlg-input sh-preset" id="${id}" aria-describedby="${id}-help ${id}-budget ${id}-warning"></select>
        <div class="share-custom" hidden>
            <div><label for="${id}-width">${t("share.width")}</label><input id="${id}-width" class="dlg-input sh-width" type="number" min="160" max="8192" step="1" required /></div>
            <div><label for="${id}-height">${t("share.height")}</label><input id="${id}-height" class="dlg-input sh-height" type="number" min="160" max="8192" step="1" required /></div>
            <div><label for="${id}-fps">${t("share.frameRate")}</label><select id="${id}-fps" class="dlg-input sh-fps" required><option value="15">15 fps</option><option value="30">30 fps</option><option value="60">60 fps</option></select></div>
        </div>
        <label class="dlg-label" for="${id}-bitrate">${t("share.bitrateCeiling")}</label>
        <select class="dlg-input sh-bitrate" id="${id}-bitrate" aria-describedby="${id}-bitrate-help">
            <option value="0">${t("share.bitrateAutomatic")}</option>
            <option value="2000000">2 Mbit/s</option><option value="4000000">4 Mbit/s</option>
            <option value="6000000">6 Mbit/s</option><option value="8000000">8 Mbit/s</option>
        </select>
        <p class="set-hint" id="${id}-bitrate-help">${t("share.bitrateHelp")}</p>
        <p class="set-hint share-quality-help" id="${id}-help"></p>
        <p class="set-hint share-budget" id="${id}-budget" role="status"></p>
        <p class="set-hint warn share-quality-warning" id="${id}-warning" role="status" hidden></p>`;
    const select = element.querySelector(".sh-preset");
    for (const [value, key] of [
        ["balanced", "polish.presetBalanced"], ["text", "polish.presetText"], ["motion", "polish.presetMotion"],
        ["hd", "share.hd"], ["hdMotion", "share.hdMotion"], ["qhd", "share.qhd"], ["uhd", "share.uhd"],
        ["original", "share.original"], ["custom", "share.custom"],
    ]) {
        const option = document.createElement("option");
        option.value = value; option.textContent = t(key); select.appendChild(option);
    }
    select.value = preset;
    if (!select.value) select.value = "balanced";
    const width = element.querySelector(".sh-width"), height = element.querySelector(".sh-height"), fps = element.querySelector(".sh-fps");
    width.value = custom.width ?? 2560; height.value = custom.height ?? 1440; fps.value = custom.fps ?? 30;
    const bitrate = element.querySelector(".sh-bitrate");
    bitrate.value = String(bitrateCeiling ?? 0);
    if (!bitrate.value) bitrate.value = "0";
    const selection = () => ({ preset: select.value, custom: { width: Number(width.value), height: Number(height.value), fps: Number(fps.value) }, bitrateCeiling: Number(bitrate.value) });
    const update = () => {
        const selected = selection(), profile = shareQuality(selected.preset, selected.custom, selected.bitrateCeiling);
        const isCustom = selected.preset === "custom";
        element.querySelector(".share-custom").hidden = !isCustom;
        width.disabled = height.disabled = fps.disabled = !isCustom;
        element.querySelector(".share-quality-help").textContent = t(selected.preset === "original" ? "share.originalHelp" : isCustom ? "share.customHelp" : "share.resolutionHelp");
        element.querySelector(".share-budget").textContent = profile ? selected.bitrateCeiling ? t("share.selectedTraffic", { bitrate: selected.bitrateCeiling / 1000000 }) : t("share.traffic") : "";
        const warning = element.querySelector(".share-quality-warning");
        warning.hidden = !profile || !(profile.original || profile.width > 1920 || profile.height > 1080 || profile.fps > 30);
        warning.textContent = warning.hidden ? "" : t("share.performanceWarning");
    };
    select.onchange = bitrate.onchange = update;
    for (const control of [width, height, fps]) control.oninput = update;
    update();
    return { element, read: () => {
        if (select.value === "custom" && ![width, height, fps].every(control => control.reportValidity())) return null;
        return selection();
    } };
}
