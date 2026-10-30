import { t } from "./i18n.js";
import { openResumedShare } from "./video.js";
import { SHARE_PRESETS, shareQuality } from "./screen-share-quality.js";

// Update handoff is a reminder, not permission to capture after a restart.
export function offerUpdateShareResume(result) {
    const settings = result?.stream;
    if (!settings || !result.tab_id || !(result.stream_channel_id > 0) ||
        !["monitor", "window", "region", "studio"].includes(settings.surface) || !SHARE_PRESETS.includes(settings.preset) ||
        !["none", "application", "system"].includes(settings.audioMode) || !shareQuality(settings.preset, settings.custom, settings.bitrateCeiling)) return;
    const parent = document.getElementById("voice-bar");
    if (!parent) return;
    const prompt = document.createElement("div");
    prompt.className = "share-resume-prompt";
    const resume = document.createElement("button"), dismiss = document.createElement("button");
    resume.textContent = t("share.resumeAfterUpdate");
    resume.title = t("share.resumeChooseSource");
    dismiss.textContent = "×"; dismiss.setAttribute("aria-label", t("common.close"));
    prompt.append(resume, dismiss); parent.prepend(prompt);
    const state = () => window.__noxa?.state;
    const current = () => state()?.activeTabID === result.tab_id && state()?.myChannelID === result.stream_channel_id;
    const close = () => { clearInterval(timer); prompt.remove(); };
    const timer = setInterval(() => {
        if (!prompt.isConnected || state()?.activeTabID !== result.tab_id || (state()?.myChannelID > 0 && !current())) close();
        resume.disabled = !current() || !state()?.pc;
    }, 250);
    resume.onclick = () => { if (!current() || !state()?.pc) return; close(); openResumedShare(settings); };
    dismiss.onclick = close;
}
