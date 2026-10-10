import { t } from "./i18n.js";
import { mediaScopeIsCurrent } from "./media-controls.js";
import { copyToClipboard } from "./clipboard.js";
import { formatBitrate } from "./connection-stats.js";
import { collectVideoSenders, primaryVideoSender, videoSenderEncodingSettings } from "./video-sender-stats.js";
import { publicationUploadActive } from "./stream-publication.js";
import { summarizeStreamHealth, renderStreamHealth } from "./stream-health.js";
import "./share-status.css";
import "./stream-health.css";

let session = null;
export function refreshShareStatus() { if (session) render(session); }
const current = s => session === s && mediaScopeIsCurrent(s.scope) && window.__noxa.state.pc === s.pc &&
    window.__noxa.state.shareStream === s.stream && s.track.readyState !== "ended";

// Only this sender's outbound report describes what is transmitted. Capture
// dimensions and the selected preset are never presented as sent dimensions.
export function summarizeShare(report, previous, source, previousSource) {
    return primaryVideoSender(collectVideoSenders(report, previous, source ? [source] : [], previousSource ? [previousSource] : []));
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
    const s = { ...options, root, track: options.stream.getVideoTracks()[0], viewers: null, sample: null, startedAt: performance.now() };
    session = s;
    root.innerHTML = `<div class="sharing-heading"><strong class="sharing-source"></strong><div class="sharing-actions"><button type="button" class="sharing-quality"></button><button type="button" class="sharing-change"></button><button type="button" class="sharing-stop danger"></button></div></div>
        <p class="sharing-meta"></p><p class="sharing-warning" role="status" hidden></p><div class="stream-health" data-direction="sender"></div>
        <details class="sharing-preview"><summary></summary><video muted playsinline></video></details>
        <details class="sharing-diagnostics"><summary></summary><p class="sharing-diagnostics-note"></p><dl></dl><p class="sharing-layers"></p><button type="button" class="sharing-copy"></button></details>`;
    root.hidden = false;
    root.querySelector(".sharing-change").onclick = () => { if (current(s)) s.change(); };
    root.querySelector(".sharing-quality").onclick = () => { if (current(s)) s.changeQuality(); };
    root.querySelector(".sharing-stop").onclick = () => { if (current(s)) s.stop(); };
    root.querySelector(".sharing-copy").onclick = () => {
        if (!current(s) || !s.layers?.length) return;
        void copyToClipboard(JSON.stringify({ sampled_at: s.sampledAt, direction: "sent", slot: "screen", video_senders: s.layers,
            note: "Measured RTP payload counters. Requested and capture settings are not measured frame rates. Null means unavailable." }, null, 2), { isCurrent: () => current(s) });
    };
    const details = root.querySelector(".sharing-preview"), video = root.querySelector("video");
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
    s.root.querySelector(".sharing-preview summary").textContent = t("share.preview");
    s.root.querySelector("video").setAttribute("aria-label", t("share.preview"));
    const busy = !!window.__noxa.state.shareStarting || !!window.__noxa.state.shareStopping;
    for (const [selector, key] of [[".sharing-quality", "share.changeQuality"], [".sharing-change", "share.change"], [".sharing-stop", "voice.stopShare"]]) {
        const button = s.root.querySelector(selector); button.textContent = t(key);
        button.disabled = busy || (selector !== ".sharing-stop" && !!window.__noxa.state.shareQualityUpdating);
    }
    const sample = s.sample;
    const waiting = publicationUploadActive("screen", s.track) === false;
    const dimensions = sample?.width > 0 && sample?.height > 0 ? `${sample.width} × ${sample.height}` : "—";
    const fps = Number.isFinite(sample?.sent_fps) ? `${Math.round(sample.sent_fps)} fps` : "— fps";
    const audio = s.stream.getAudioTracks().some(track => track.readyState === "live" && track.enabled);
    const viewers = s.viewers === null ? t("share.viewersUnknown") : s.viewers === 0 ? t("share.noViewers") : t("share.viewers", { count: s.viewers });
    const audioText = t(!audio ? "share.audioOff" : s.audioMode === "application" ? "share.applicationOn" : "share.audioOn");
    s.root.querySelector(".sharing-meta").textContent = `${waiting ? t("share.waitingViewers") : t("share.sending", { dimensions, fps })} · ${audioText} · ${viewers}`;
    const reason = waiting ? "" : s.reduction();
    const limits = window.__noxa.state.mediaLimits;
    const capped = (s.preset.original && limits?.video_max_width > 0) || (limits?.video_max_width > 0 && limits.video_max_width < s.preset.width) ||
        (limits?.video_max_height > 0 && limits.video_max_height < s.preset.height) ||
        limits?.video_max_bitrate > 0;
    const warning = s.root.querySelector(".sharing-warning");
    warning.textContent = reason ? t(reason) : capped ? t(s.preset.original ? "share.originalServerLimit" : "share.serverLimit") : "";
    warning.hidden = !warning.textContent;
    renderStreamHealth(s.root.querySelector(".stream-health"), summarizeStreamHealth({ sender: sample,
        senderAgeMS: s.measuredAt == null ? null : performance.now() - s.measuredAt, screen: true, waiting,
        starting: !s.statsFailed && performance.now() - s.startedAt < 5000 && !sample?.sample_ms,
    }));
    s.root.querySelector(".sharing-meta").title = t(s.preset.original ? "share.selectedOriginal" : "share.selected", {
        width: s.preset.width, height: s.preset.height, fps: s.preset.fps });
    renderDetails(s);
}

function renderDetails(s) {
    const details = s.root.querySelector(".sharing-diagnostics"), sample = s.sample;
    details.querySelector("summary").textContent = t("share.diagnostics");
    details.querySelector(".sharing-diagnostics-note").textContent = t("share.diagnosticsNote");
    const display = (value, suffix = "") => Number.isFinite(value) ? `${value.toFixed(1)}${suffix}` : "—";
    const rows = [["share.requestedFPS", display(sample?.requested_fps ?? s.preset.fps, " fps")],
        ["share.settingsFPS", display(sample?.settings_fps, " fps")], ["share.captureFPS", display(sample?.capture_fps, " fps")],
        ["share.encodedFPS", display(sample?.encoded_fps, " fps")], ["share.sentFPS", display(sample?.sent_fps, " fps")],
        ["share.reportedFPS", display(sample?.reported_fps, " fps")], ["share.sendBitrate", formatBitrate(sample?.bitrate_bps)],
        ["share.targetBitrate", formatBitrate(sample?.target_bitrate_bps)],
        ["share.transportBudget", formatBitrate(sample?.available_outgoing_bitrate_bps)],
        ["share.transportRTT", display(sample?.transport_rtt_ms, " ms")],
        ["share.remoteRTT", display(sample?.remote_rtt_ms, " ms")],
        ["share.remoteLoss", display(Number.isFinite(sample?.remote_fraction_lost) ? sample.remote_fraction_lost * 100 : null, "%")],
        ["share.encodingCap", formatBitrate(sample?.encoding_max_bitrate_bps)],
        ["share.encodingActive", t(sample?.encoding_active === true ? "streams.reportedYes" : sample?.encoding_active === false ? "streams.reportedNo" : "streams.notReported")],
        ["share.bandwidthLimited", display(sample?.bandwidth_limited_ms, " ms")], ["share.cpuLimited", display(sample?.cpu_limited_ms, " ms")],
        ["share.retryBitrate", `${formatBitrate(sample?.retransmit_bitrate_bps)} · ${display(sample?.retransmit_percent, "%")}`],
        ["share.keyframes", display(sample?.key_frames_delta)], ["share.frameBytes", display(sample?.frame_bytes, " B")],
        ["share.encodeTime", display(sample?.encode_ms, " ms")], ["share.sendDelay", display(sample?.send_delay_ms, " ms")],
        ["share.sampleWindow", display(sample?.sample_ms ? sample.sample_ms / 1000 : null, " s")],
        ["share.encoder", sample ? `${sample.codec} · ${sample.encoder_implementation}` : "—"],
        ["share.encoderReason", sample?.quality_reason || "—"],
        ["share.powerEfficient", t(sample?.power_efficient === true ? "streams.reportedYes" : sample?.power_efficient === false ? "streams.reportedNo" : "streams.notReported")]];
    const list = details.querySelector("dl");
    list.replaceChildren(...rows.flatMap(([key, value]) => {
        const label = document.createElement("dt"), text = document.createElement("dd");
        label.textContent = t(key); text.textContent = value; return [label, text];
    }));
    details.querySelector(".sharing-layers").textContent = (s.layers || []).map(row =>
        `${row.rid || "—"}: ${row.width ?? "—"} × ${row.height ?? "—"} · ${display(row.sent_fps, " fps")} · ${formatBitrate(row.bitrate_bps)}`).join(" | ");
    const copy = details.querySelector(".sharing-copy");
    copy.textContent = t("share.copyDiagnostics"); copy.disabled = !s.layers?.length;
}

async function poll(s) {
    if (!current(s)) { if (session === s) stopShareStatus(); return; }
    try {
        const sender = window.__noxa.state.shareVideoTransceiver?.sender;
        const source = { slot: "screen", generation: s.generation, trackID: s.track.id, senderScoped: true,
            requestedFPS: s.preset.fps, settingsFPS: s.track.getSettings().frameRate, encodings: videoSenderEncodingSettings(sender) };
        let deadline;
        const report = sender?.getStats ? await Promise.race([sender.getStats(), new Promise((_, reject) => {
            deadline = setTimeout(() => reject(new Error("stats timeout")), 10000);
        })]).finally(() => clearTimeout(deadline)) : null;
        if (!current(s)) return;
        s.layers = report ? collectVideoSenders(report, s.previous, [source], s.previousSource ? [s.previousSource] : []) : [];
        s.sample = primaryVideoSender(s.layers);
        s.previous = report; s.previousSource = source; s.sampledAt = Date.now(); s.measuredAt = performance.now(); s.statsFailed = false;
        if (current(s)) render(s);
    } catch { if (current(s)) { s.sample = null; s.layers = []; s.previous = null; s.statsFailed = true; render(s); } }
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
