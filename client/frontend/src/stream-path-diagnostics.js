import { t } from "./i18n.js";
import { formatBitrate } from "./connection-stats.js";

const fresh = (value, extraAgeMS) => value && !value.stale && Number.isFinite(value.age_ms) && value.age_ms >= 0 && value.age_ms + extraAgeMS <= 15000;
const format = (value, suffix, digits = 1) => Number.isFinite(value) && value >= 0 ? `${value.toFixed(digits)}${suffix}` : "—";
const seconds = value => Number.isFinite(value) ? format(value / 1000, " s") : "—";

// These stages have independent clocks/windows. RTP timestamps observed by the
// SFU do not establish that every packet of a frame arrived or was decoded.
export function streamPathMeasurements(remote, extraAgeMS = 0, quality = "high") {
    const layers = remote?.layers || [];
    const source = remote?.forwarding?.source_ssrc;
    const rid = { high: "f", mid: "h", low: "q" }[quality];
    const layer = source ? layers.find(item => item.ssrc === source)
        : layers.find(item => item.rid === rid) || (layers.length === 1 ? layers[0] : null);
    const report = remote?.sender_report;
    const sender = layer && fresh(report, extraAgeMS)
        ? report.rows?.find(row => row.ssrc === layer.ssrc && row.generation === remote.generation && row.slot === remote.slot) : null;
    return {
        sender: sender || null,
        ingress: fresh(layer?.ingress, extraAgeMS) ? layer.ingress : null,
        forwarding: layer && fresh(remote?.forwarding?.stage, extraAgeMS) ? remote.forwarding.stage : null,
        senderAgeMS: report ? report.age_ms + extraAgeMS : null,
        senderStale: !!report && !fresh(report, extraAgeMS),
    };
}

export function renderStreamPath(element, remote, receiver, { extraAgeMS = 0, quality = "high", status = "", error = "" } = {}) {
    const view = streamPathMeasurements(remote, extraAgeMS, quality);
    const sender = view.sender;
    const rows = [
        ["capture", format(sender?.capture_fps, " fps")],
        ["encoded", format(sender?.encoded_fps, " fps")],
        ["sent", format(sender?.sent_fps, " fps")],
        ["ingress", format(view.ingress?.fps, " fps")],
        ["forwarded", format(view.forwarding?.fps, " fps")],
        ["receivedFrames", format(receiver?.receivedFPS, " fps")],
        ["decoded", format(receiver?.fps, " fps")],
        ["requestedFPS", format(sender?.requested_fps, " fps")],
        ["settingsFPS", format(sender?.settings_fps, " fps")],
        ["reportedFPS", format(sender?.reported_fps, " fps")],
        ["senderBitrate", formatBitrate(sender?.bitrate_bps)],
        ["targetBitrate", formatBitrate(sender?.target_bitrate_bps)],
        ["transportBudget", formatBitrate(sender?.available_outgoing_bitrate_bps)],
        ["transportRTT", format(sender?.transport_rtt_ms, " ms")],
        ["remoteRTT", format(sender?.remote_rtt_ms, " ms")],
        ["remoteLoss", format(Number.isFinite(sender?.remote_fraction_lost) ? sender.remote_fraction_lost * 100 : null, "%")],
        ["encodingCap", formatBitrate(sender?.encoding_max_bitrate_bps)],
        ["encodingActive", t(sender?.encoding_active === true ? "streams.reportedYes" : sender?.encoding_active === false ? "streams.reportedNo" : "streams.notReported")],
        ["bandwidthLimited", format(sender?.bandwidth_limited_ms, " ms")], ["cpuLimited", format(sender?.cpu_limited_ms, " ms")],
        ["repairBitrate", `${formatBitrate(sender?.retransmit_bitrate_bps)} (${format(sender?.retransmit_percent, "%")})`],
        ["frameBytes", Number.isFinite(sender?.frame_bytes) ? format(sender.frame_bytes / 1024, " KiB") : "—"],
        ["keyFrames", format(sender?.key_frames_delta, "", 0)],
        ["ingressBitrate", formatBitrate(view.ingress?.bitrate_bps)],
        ["forwardedBitrate", formatBitrate(view.forwarding?.bitrate_bps)],
        ["encodeTime", format(sender?.encode_ms, " ms")],
        ["sendDelay", format(sender?.send_delay_ms, " ms")],
        ["decodeTime", format(receiver?.decodeMS, " ms")],
        ["buffer", format(receiver?.bufferMS, " ms")],
        ["loss", format(receiver?.lossPercent, "%", 2)],
        ["dropped", format(receiver?.droppedFPS, " fps")],
        ["feedback", `${format(receiver?.nacks, "", 0)} / ${format(receiver?.plis, "", 0)}`],
        ["encoder", sender ? `${sender.codec || "—"} · ${sender.encoder_implementation || "—"}` : "—"],
        ["decoder", receiver?.decoder || "—"],
        ["encoderEfficient", t(sender?.power_efficient === true ? "streams.reportedYes" : sender?.power_efficient === false ? "streams.reportedNo" : "streams.notReported")],
        ["decoderEfficient", t(receiver?.powerEfficientDecoder === true ? "streams.reportedYes" : receiver?.powerEfficientDecoder === false ? "streams.reportedNo" : "streams.notReported")],
    ];
    const list = document.createElement("dl"); list.className = "vtile-stage-grid";
    for (const [key, value] of rows) {
        const label = document.createElement("dt"), data = document.createElement("dd");
        label.textContent = t(`wins.path.${key}`); data.textContent = value; data.dataset.stage = key;
        list.append(label, data);
    }
    const note = document.createElement("p"); note.className = "vtile-path-note";
    note.textContent = t("wins.path.windows", {
        sender: seconds(sender?.sample_ms), server: seconds(view.ingress?.sample_ms),
        receiver: seconds(receiver?.sampleMS), age: seconds(view.senderAgeMS),
    });
    const explanation = document.createElement("p"); explanation.textContent = t("wins.path.note");
    const condition = document.createElement("p"); condition.className = "vtile-path-status";
    condition.textContent = error ? t("wins.path.failed", { error }) : view.senderStale ? t("wins.path.stale")
        : status === "loading" ? t("wins.path.loading") : status === "unsupported" ? t("wins.path.unsupported")
            : remote && !sender ? t("wins.path.senderUnavailable") : "";
    element.replaceChildren(list, note, explanation, condition);
}
