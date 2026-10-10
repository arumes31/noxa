import { t } from "./i18n.js";
import { escapeHTML } from "./markdown.js";
import { voicePlaybackHealth } from "./voice-diagnostics.js";
import { voiceHistoryModel } from "./voice-history.js";

const esc = value => escapeHTML(String(value ?? ""));
const metric = (value, suffix = "") => Number.isFinite(value) ? `${value.toFixed(1)}${suffix}` : "—";
const row = (label, value) => `<div class="ci-label">${esc(label)}</div><div class="ci-val">${esc(value)}</div>`;
const age = sample => t(sample.stale ? "diagnostics.stale" : "diagnostics.fresh", { seconds: Math.max(0, Math.round((sample.age_ms || 0) / 1000)) });

function historyHTML(diagnostic) {
    const model = voiceHistoryModel(diagnostic.history, diagnostic.observed_at);
    if (!model.points.length) return "";
    const clock = at => new Date(at).toLocaleTimeString();
    const x = at => (2 + (at - model.start) / (model.end - model.start) * 316).toFixed(2);
    const charts = model.series.map(series => {
        const label = t(`diagnostics.${series.label}`), range = `0–${series.maximum} ${series.unit}`;
        const y = value => (64 - value / series.maximum * 60).toFixed(2);
        const lines = series.segments.map(segment => `<polyline points="${segment.map(point => `${x(point.at)},${y(point.value)}`).join(" ")}"/>`).join("");
        const dots = series.segments.flat().map(point => `<circle cx="${x(point.at)}" cy="${y(point.value)}" r="2"><title>${esc(`${clock(point.at)} · ${metric(point.value, " " + series.unit)}`)}</title></circle>`).join("");
        const description = t("diagnostics.historyChart", { label, range, start: clock(model.start), end: clock(model.end) });
        return `<figure class="ci-history-chart" data-metric="${series.key}"><figcaption><strong>${esc(label)}</strong><span>${esc(range)}</span></figcaption>
            <svg viewBox="0 0 320 68" preserveAspectRatio="none" role="img" aria-label="${esc(description)}"><path class="ci-history-grid" d="M2 4H318M2 34H318M2 64H318"/>${lines}${dots}</svg>
            ${dots ? "" : `<p class="ci-history-empty">${esc(t("diagnostics.historyNoMeasurements"))}</p>`}</figure>`;
    }).join("");
    const freshness = model.ageMS === null ? t("diagnostics.historyAgeUnknown") : t(model.stale ? "diagnostics.stale" : "diagnostics.fresh", { seconds: Math.round(model.ageMS / 1000) });
    return `<details data-role="voice-history"><summary>${esc(t("diagnostics.history"))}</summary><p class="ci-muted">${esc(t("diagnostics.historyMeaning"))}</p>
        <p class="ci-history-age ${model.stale ? "ci-diagnostic-stale" : "ci-muted"}">${esc(freshness)}</p>
        <div class="ci-history-charts">${charts}</div><div class="ci-history-times" aria-hidden="true"><span>${esc(clock(model.start))}</span><span>${esc(clock(model.end))}</span></div>
        <p class="ci-muted ci-history-hint">${esc(t("diagnostics.historyGaps"))}</p>
        <div class="ci-history-scroll" tabindex="0" role="region" aria-label="${esc(t("diagnostics.historyTable"))}"><table><caption>${esc(t("diagnostics.historyTable"))}</caption><thead><tr>${["historyTime", "historyBuffer", "historyLoss", "historyDiscard", "historyConcealment"].map(key => `<th scope="col">${esc(t(`diagnostics.${key}`))}</th>`).join("")}</tr></thead><tbody>${model.points.slice().reverse().map(point => `<tr><td>${esc(clock(point.observed_at))}</td><td>${esc(metric(point.buffer_ms, " ms"))}</td><td>${esc(metric(point.loss_percent, "%"))}</td><td>${esc(metric(point.discard_percent, "%"))}</td><td>${esc(metric(point.concealment_percent, "%"))}</td></tr>`).join("")}</tbody></table></div></details>`;
}

// Only the owner/administrators receive this optional field. Clear the section
// when permission or connection changes, including failed refreshes.
export function renderReceiverDiagnostics(overlay, diagnostic, clients = []) {
    let section = overlay.querySelector('[data-role="receiver-diagnostics"]');
    if (!diagnostic) { section?.remove(); return; }
    if (!section) {
        section = document.createElement("details");
        section.dataset.role = "receiver-diagnostics";
        section.className = "ci-receiver-diagnostics";
        section.innerHTML = `<summary>${esc(t("diagnostics.receiver"))}</summary><div class="ci-diagnostic-content"></div>`;
        overlay.querySelector(".dlg-buttons").before(section);
    }
    const name = id => clients.find(client => client.client_id === id)?.nickname || id || t("desktop.unknown");
    const card = (heading, body) => `<section class="ci-diagnostic-track"><strong>${esc(heading)}</strong><div class="ci-grid">${body}</div></section>`;
    const sample = diagnostic.client_report;
    const parts = [`<p>${esc(t("diagnostics.direction", { name: diagnostic.nickname || diagnostic.client_id }))}</p>`];
    if (sample) {
        const report = sample.report;
        const health = voicePlaybackHealth(report, sample.age_ms);
        parts.push(`<p data-role="voice-health" data-quality="${esc(health.quality)}">${esc(t("diagnostics.healthBadge", { quality: t(`diagnostics.health.${health.quality}`) }))}</p>`);
        if (report.transport) {
            const route = report.transport;
            parts.push(`<div class="ci-grid">${row(t("diagnostics.transport"), `${route.protocol.toUpperCase()} · ${route.local_candidate} → ${route.remote_candidate}${route.local_candidate === "relay" ? ` · TURN ${route.relay_protocol}` : ""}`)}</div><p class="ci-muted">${esc(t("diagnostics.transportMeaning"))}</p>`);
        }
        if (report.truncated) parts.push(`<p class="ci-muted">${esc(t("diagnostics.truncated"))}</p>`);
        parts.push(`<p class="${sample.stale ? "ci-diagnostic-stale" : "ci-muted"}">${esc(age(sample))}</p>`);
        parts.push(`<div class="ci-grid">${row(t("diagnostics.output"), `${report.output_state} · ${metric(report.output_latency_ms, " ms")}`)}${row(t("diagnostics.processing"), t("diagnostics.processingValue", {
            volume: report.volume, limiter: t(report.voice_limiter ? "diagnostics.on" : "diagnostics.off"), normalization: t(report.gain_normalize ? "diagnostics.on" : "diagnostics.off"),
        }))}</div>`);
        parts.push(`<p class="ci-muted">${esc(t("diagnostics.bufferMeaning"))}</p>`);
        for (const track of (report.tracks || []).slice(0, 64)) {
            parts.push(card(name(track.publisher_id),
                row(t("diagnostics.interval"), metric(Number.isFinite(track.sample_ms) ? track.sample_ms / 1000 : null, " s")) +
                row(t("diagnostics.lossJitter"), `${metric(track.loss_percent, "%")} / ${metric(track.jitter_ms, " ms")}`) +
                row(t("diagnostics.discarded"), metric(track.discard_percent, "%")) +
                row(t("diagnostics.bufferDelays"), `${metric(track.buffer_ms, " ms")} / ${metric(track.buffer_target_ms, " ms")} / ${metric(track.buffer_minimum_ms, " ms")}`) +
                row(t("diagnostics.concealmentParts"), `${metric(track.concealment_percent, "%")} / ${metric(track.non_silent_concealment_percent, "%")} / ${metric(track.silent_concealment_percent, "%")}`) +
                row(t("diagnostics.timeStretch"), `${metric(track.acceleration_percent, "%")} / ${metric(track.deceleration_percent, "%")}`)));
        }
        if (!report.tracks?.length) parts.push(`<p class="ci-muted">${esc(t("diagnostics.noTracks"))}</p>`);
    } else parts.push(`<p class="ci-muted">${esc(t("diagnostics.missing"))}</p>`);
    if (diagnostic.history?.length) {
        parts.push(historyHTML(diagnostic));
    }
    if (diagnostic.paths?.length) {
        parts.push(`<h4>${esc(t("diagnostics.path"))}</h4><p class="ci-muted">${esc(t("diagnostics.pathMeaning"))}</p>`);
        for (const path of diagnostic.paths.slice(0, 64)) {
            const input = path.ingress;
            parts.push(card(`${name(path.publisher_id)} · ${path.slot}`, row(t("diagnostics.senderRate"), path.sender ? `${metric(path.sender.packets_per_second, " packets/s")} · ${metric(Number.isFinite(path.sender.bitrate_bps) ? path.sender.bitrate_bps / 1000 : null, " kbit/s")} · ${metric(Number.isFinite(path.sender_age_ms) ? path.sender_age_ms / 1000 : null, " s")}` : t("diagnostics.unmatched")) +
                row(t("diagnostics.ingressRate"), input && !input.stale ? `${metric(input.packets_per_second, " packets/s")} · ${metric(input.jitter_ms, " ms")}` : t("diagnostics.unmatched")) +
                row(t("diagnostics.ingressGap"), input && !input.stale ? `${metric(input.max_gap_ms, " ms")} · ${input.burst_packets}` : "—") +
                row(t("diagnostics.receiverStage"), path.receiver ? `${metric(path.receiver.buffer_ms, " ms")} · ${metric(path.receiver.non_silent_concealment_percent, "%")}` : t("diagnostics.unmatched")) +
                row(t("diagnostics.receiverFeedback"), path.feedback ? `${metric(path.feedback.fraction_lost * 100, "%")} · ${metric(path.feedback.jitter_ms, " ms")}` : t("diagnostics.unmatched"))));
        }
    }
    parts.push(`<h4>${esc(t("diagnostics.serverFeedback"))}</h4>`);
    const feedback = diagnostic.transport?.receiver_reports || [];
    for (const track of feedback.slice(0, 64)) {
        parts.push(card(`${name(track.publisher_id)} · ${track.slot}`, row(age(track), `${metric(track.fraction_lost * 100, "%")} / ${metric(track.jitter_ms, " ms")}`)));
    }
    if (!feedback.length) parts.push(`<p class="ci-muted">${esc(t("diagnostics.noFeedback"))}</p>`);
    const historyOpen = !!section.querySelector('[data-role="voice-history"]')?.open;
    const previousTable = section.querySelector(".ci-history-scroll");
    const tablePosition = previousTable && { top: previousTable.scrollTop, left: previousTable.scrollLeft, focused: previousTable.ownerDocument?.activeElement === previousTable };
    section.querySelector(".ci-diagnostic-content").innerHTML = parts.join("");
    const history = section.querySelector('[data-role="voice-history"]');
    if (history) history.open = historyOpen;
    const table = section.querySelector(".ci-history-scroll");
    if (table && tablePosition) {
        table.scrollTop = tablePosition.top; table.scrollLeft = tablePosition.left;
        if (tablePosition.focused) table.focus({ preventScroll: true });
    }
}
