import { t } from "./i18n.js";
import { escapeHTML } from "./markdown.js";

const esc = value => escapeHTML(String(value ?? ""));
const metric = (value, suffix = "") => Number.isFinite(value) ? `${value.toFixed(1)}${suffix}` : "—";
const row = (label, value) => `<div class="ci-label">${esc(label)}</div><div class="ci-val">${esc(value)}</div>`;
const age = sample => t(sample.stale ? "diagnostics.stale" : "diagnostics.fresh", { seconds: Math.max(0, Math.round((sample.age_ms || 0) / 1000)) });

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
    parts.push(`<h4>${esc(t("diagnostics.serverFeedback"))}</h4>`);
    const feedback = diagnostic.transport?.receiver_reports || [];
    for (const track of feedback.slice(0, 64)) {
        parts.push(card(`${name(track.publisher_id)} · ${track.slot}`, row(age(track), `${metric(track.fraction_lost * 100, "%")} / ${metric(track.jitter_ms, " ms")}`)));
    }
    if (!feedback.length) parts.push(`<p class="ci-muted">${esc(t("diagnostics.noFeedback"))}</p>`);
    section.querySelector(".ci-diagnostic-content").innerHTML = parts.join("");
}
