import { t } from "./i18n.js";
import { mediaScopeIsCurrent } from "./media-controls.js";

let session = null;
export function refreshShareStatus() { if (session) render(session); }
const current = s => session === s && mediaScopeIsCurrent(s.scope) && window.__noxa.state.pc === s.pc &&
    window.__noxa.state.shareStream === s.stream && s.track.readyState !== "ended";

// Only this sender's outbound report describes what is transmitted. Capture
// dimensions and the selected preset are never presented as sent dimensions.
export function summarizeShare(report) {
    const rows = [...report.values()].filter(r => r.type === "outbound-rtp" && (r.kind || r.mediaType) === "video" &&
        r.active !== false && r.framesEncoded > 0);
    if (!rows.length) return null;
    // Sender-scoped getStats includes all simulcast layers. Report the largest
    // active one, rather than claiming simulcast has no measurable dimensions.
    const r = rows.sort((a, b) => (b.frameWidth || 0) * (b.frameHeight || 0) - (a.frameWidth || 0) * (a.frameHeight || 0))[0];
    return { width: r.frameWidth, height: r.frameHeight, fps: r.framesPerSecond, reason: r.qualityLimitationReason };
}

export function updateShareViewers(streams) {
    const s = session;
    if (!s || !current(s)) return;
    const own = streams.find(stream => stream.publisher_id === s.scope.clientID && stream.slot === "screen" && stream.generation === s.generation);
    s.viewers = Number.isSafeInteger(own?.viewer_count) && own.viewer_count >= 0 ? own.viewer_count : null;
    render(s);
}

export function startShareStatus(options) {
    stopShareStatus();
    const root = document.getElementById("sharing-status");
    if (!root) return;
    const s = { ...options, root, track: options.stream.getVideoTracks()[0], viewers: null, sample: null };
    session = s;
    root.innerHTML = `<div class="sharing-heading"><strong class="sharing-source"></strong><div class="sharing-actions"><button type="button" class="sharing-quality"></button><button type="button" class="sharing-change"></button><button type="button" class="sharing-stop danger"></button></div></div>
        <p class="sharing-meta"></p><p class="sharing-warning" role="status" hidden></p>
        <details class="sharing-preview"><summary></summary><video muted playsinline></video></details>`;
    root.hidden = false;
    root.querySelector(".sharing-change").onclick = () => { if (current(s)) s.change(); };
    root.querySelector(".sharing-quality").onclick = () => { if (current(s)) s.changeQuality(); };
    root.querySelector(".sharing-stop").onclick = () => { if (current(s)) s.stop(); };
    const details = root.querySelector("details"), video = root.querySelector("video");
    details.ontoggle = () => {
        if (!current(s)) return;
        if (details.open) { video.srcObject = new MediaStream([s.track]); void video.play().catch(() => {}); }
        else { video.pause(); video.srcObject = null; }
    };
    render(s);
    void poll(s);
}

function render(s) {
    if (!current(s)) return;
    const settings = s.track.getSettings();
    const surface = s.surface === "region" ? "region" : settings.displaySurface || s.surface;
    const kind = t(surface === "window" ? "polish.window" : surface === "browser" ? "share.tab" : surface === "region" ? "polish.region" : "polish.monitor");
    const source = s.track.label ? `${kind} · ${s.track.label}` : kind;
    s.root.setAttribute("aria-label", t("share.status"));
    s.root.querySelector(".sharing-source").textContent = t("share.source", { source });
    s.root.querySelector(".sharing-source").title = source;
    s.root.querySelector("summary").textContent = t("share.preview");
    s.root.querySelector("video").setAttribute("aria-label", t("share.preview"));
    const busy = !!window.__noxa.state.shareStarting || !!window.__noxa.state.shareStopping;
    for (const [selector, key] of [[".sharing-quality", "share.changeQuality"], [".sharing-change", "share.change"], [".sharing-stop", "voice.stopShare"]]) {
        const button = s.root.querySelector(selector); button.textContent = t(key);
        button.disabled = busy || (selector !== ".sharing-stop" && !!window.__noxa.state.shareQualityUpdating);
    }
    const sample = s.sample;
    const dimensions = sample?.width > 0 && sample?.height > 0 ? `${sample.width} × ${sample.height}` : "—";
    const fps = Number.isFinite(sample?.fps) && sample.fps >= 0 ? `${Math.round(sample.fps)} fps` : "— fps";
    const audio = s.stream.getAudioTracks().some(track => track.readyState === "live" && track.enabled);
    const viewers = s.viewers === null ? t("share.viewersUnknown") : s.viewers === 0 ? t("share.noViewers") : t("share.viewers", { count: s.viewers });
    const audioText = t(!audio ? "share.audioOff" : s.audioMode === "application" ? "share.applicationOn" : "share.audioOn");
    s.root.querySelector(".sharing-meta").textContent = `${t("share.sending", { dimensions, fps })} · ${audioText} · ${viewers}`;
    const reason = s.reduction() || (sample?.reason === "cpu" ? "share.cpu" : sample?.reason === "bandwidth" ? "share.bandwidth" :
        sample?.width > 0 && sample.width < (s.preset.original ? settings.width : Math.min(settings.width || s.preset.width, s.preset.width)) ? "share.reduced" : "");
    const limits = window.__noxa.state.mediaLimits;
    const capped = (s.preset.original && limits?.video_max_width > 0) || (limits?.video_max_width > 0 && limits.video_max_width < s.preset.width) ||
        (limits?.video_max_height > 0 && limits.video_max_height < s.preset.height) ||
        limits?.video_max_bitrate > 0;
    const warning = s.root.querySelector(".sharing-warning");
    warning.textContent = reason ? t(reason) : capped ? t(s.preset.original ? "share.originalServerLimit" : "share.serverLimit") : "";
    warning.hidden = !warning.textContent;
    s.root.querySelector(".sharing-meta").title = t(s.preset.original ? "share.selectedOriginal" : "share.selected", {
        width: s.preset.width, height: s.preset.height, fps: s.preset.fps });
}

async function poll(s) {
    if (!current(s)) { if (session === s) stopShareStatus(); return; }
    try {
        const sender = window.__noxa.state.shareVideoTransceiver?.sender;
        s.sample = sender?.getStats ? summarizeShare(await sender.getStats()) : null;
        if (current(s)) render(s);
    } catch { if (current(s)) { s.sample = null; render(s); } }
    finally { if (current(s)) s.timer = setTimeout(() => { void poll(s); }, 2000); }
}

export function stopShareStatus() {
    const s = session;
    session = null;
    if (!s) return;
    clearTimeout(s.timer);
    const video = s.root.querySelector("video");
    if (video) { video.pause(); video.srcObject = null; }
    s.root.hidden = true;
    s.root.replaceChildren();
}

window.addEventListener("noxa-language-changed", () => { if (session) render(session); });
