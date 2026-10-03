import { escapeHTML as escapeTranslation } from "./markdown.js";
import { closeDialog, isCurrentServerDialog, mountServerDialog } from "./modal.js";
import { icon } from "./icons.js";
import { copyToClipboard } from "./clipboard.js";
import { formatBytes, formatBitrate, formatDuration, measured, summarizeMedia, summarizeVideoProcessing } from "./connection-stats.js";
import { t } from "./i18n.js";

const V = () => window.__noxa;
let currentOverlay = null;

export function openServerInfo() {
    if (!V().state.myClientID) return V().toast(t("desktop.connect.to.a.server.to.view.its.information"), "warn");
    if (currentOverlay && isCurrentServerDialog(currentOverlay)) {
        currentOverlay.focus();
        return;
    }
    const { activeTabID: tabID, myClientID: clientID } = V().state;
    const overlay = document.createElement("div");
    overlay.className = "dlg-overlay";
    const field = (key) => `<span data-stat="${key}">—</span>`;
    overlay.innerHTML = `
        <div class="dlg stats-page server-info">
            <div class="pm-head"><h3>${escapeTranslation(t("desktop.server.information"))}</h3>
                <button class="icon-btn stats-close" aria-label="${escapeTranslation(t("desktop.close.server.information"))}">${icon("close")}</button></div>
            <div class="server-info-body">
                <dl class="server-info-details">
                    <dt>${escapeTranslation(t("desktop.name"))}</dt><dd>${field("name")}</dd>
                    <dt>${escapeTranslation(t("desktop.address"))}</dt><dd class="server-info-address">${field("address")}<button class="server-copy" title="${escapeTranslation(t("desktop.copy.server.address"))}" aria-label="${escapeTranslation(t("desktop.copy.server.address"))}">${escapeTranslation(t("desktop.copy.e21f93"))}</button></dd>
                    <dt>${escapeTranslation(t("desktop.version"))}</dt><dd>${field("version")}</dd>
                    <dt>${escapeTranslation(t("desktop.platform"))}</dt><dd>${field("platform")}</dd>
                    <dt>${escapeTranslation(t("desktop.uptime"))}</dt><dd>${field("uptime")}</dd>
                    <dt>${escapeTranslation(t("desktop.users.capacity"))}</dt><dd>${field("users")}</dd>
                    <dt>${escapeTranslation(t("desktop.channels"))}</dt><dd>${field("channels")}</dd>
                </dl>
                <section class="server-info-connection" aria-labelledby="server-info-connection-title">
                    <h4 id="server-info-connection-title">${escapeTranslation(t("desktop.your.connection"))}</h4>
                    <div class="server-info-quality">
                        <div><span>${escapeTranslation(t("desktop.server.latency"))}</span><strong>${field("ping")}</strong></div>
                        <div><span>${escapeTranslation(t("desktop.incoming.audio.loss"))}</span><strong>${field("loss")}</strong></div>
                        <div><span>${escapeTranslation(t("desktop.audio.jitter.highest"))}</span><strong>${field("jitter")}</strong></div>
                    </div>
                    <p class="server-info-session">${escapeTranslation(t("desktop.connected.for"))} ${field("connected")}</p>
                    <table class="server-info-traffic">
                        <caption>${escapeTranslation(t("desktop.traffic.for.this.connection"))}</caption>
                        <thead><tr><th scope="col">${escapeTranslation(t("desktop.transferred"))}</th><th scope="col">${escapeTranslation(t("desktop.in"))}</th><th scope="col">${escapeTranslation(t("desktop.out"))}</th></tr></thead>
                        <tbody>
                            <tr><th scope="row">${escapeTranslation(t("desktop.control.data"))}</th><td>${field("control-in")}</td><td>${field("control-out")}</td></tr>
                            <tr><th scope="row">${escapeTranslation(t("desktop.media.data"))}</th><td>${field("media-in")}</td><td>${field("media-out")}</td></tr>
                            <tr><th scope="row">${escapeTranslation(t("desktop.media.packets"))}</th><td>${field("packets-in")}</td><td>${field("packets-out")}</td></tr>
                            <tr><th scope="row">${escapeTranslation(t("desktop.media.second"))}</th><td>${field("rate-in")}</td><td>${field("rate-out")}</td></tr>
                        </tbody>
                    </table>
                    <p class="server-info-note">${escapeTranslation(t("desktop.in.received.out.sent.data.excludes.protocol.headers.audio.loss"))}</p>
                    <details class="server-info-processing"><summary>${t("streams.processing")}</summary>
                        <dl class="server-info-details">
                            <dt>${t("streams.encoding")}</dt><dd data-video-processors="encoders">—</dd>
                            <dt>${t("streams.decoding")}</dt><dd data-video-processors="decoders">—</dd>
                        </dl>
                        <p class="server-info-note">${t("streams.processingNote")}</p>
                    </details>
                    <details class="server-info-history"><summary>${escapeTranslation(t("desktop.recent.latency.audio.loss"))}</summary>
                        <div class="stats-label">${escapeTranslation(t("desktop.server.latency.ms.last.60.seconds"))}</div><canvas class="stats-rtt" width="560" height="80" role="img" aria-label="${escapeTranslation(t("desktop.recent.server.latency"))}"></canvas>
                        <div class="stats-label">${escapeTranslation(t("desktop.incoming.audio.loss.last.60.seconds"))}</div><canvas class="stats-loss" width="560" height="60" role="img" aria-label="${escapeTranslation(t("desktop.recent.incoming.audio.loss"))}"></canvas>
                    </details>
                </section>
                <p class="server-info-error" role="status" hidden></p>
            </div>
            <div class="server-info-footer"><span>${escapeTranslation(t("desktop.updates.every.2.seconds.while.visible"))}</span><button class="dlg-ok">${escapeTranslation(t("desktop.close"))}</button></div>
        </div>`;
    currentOverlay = overlay;
    const elements = new Map([...overlay.querySelectorAll("[data-stat]")].map((el) => [el.dataset.stat, el]));
    const set = (key, value) => {
        const text = String(value ?? "—");
        const el = elements.get(key);
        if (el.textContent !== text) el.textContent = text;
    };
    const address = V().state.lastConnect?.addr || "";
    set("address", address || "—");
    const copy = overlay.querySelector(".server-copy");
    copy.disabled = !address;
    copy.onclick = () => copyToClipboard(address, {
        success: t("desktop.server.address.copied.e68604"),
        failure: t("desktop.could.not.copy.the.server.address"),
        isCurrent: () => isCurrentServerDialog(overlay),
    });

    let timer = null;
    let busy = false;
    let server = null;
    let serverAt = 0;
    let previous = null;
    let previousPC = null;
    const history = [];
    const historyElement = overlay.querySelector(".server-info-history");
    const drawHistory = () => {
        if (!historyElement.open) return;
        drawChart(overlay.querySelector(".stats-rtt"), history, "ping", t("desktop.server.latency"), "ms");
        drawChart(overlay.querySelector(".stats-loss"), history, "loss", t("desktop.incoming.audio.loss"), "%");
    };
    historyElement.addEventListener("toggle", drawHistory);
    const visible = () => isCurrentServerDialog(overlay) && !document.hidden && !overlay.inert;
    const clearTimer = () => { clearTimeout(timer); timer = null; };
    const sample = async () => {
        clearTimer();
        if (busy || !isCurrentServerDialog(overlay)) return;
        if (visible()) {
            busy = true;
            const { state } = V();
            const pc = state.pc;
            const app = window.go.main.App;
            const refreshServer = !server || Date.now() - serverAt >= 10000;
            const [infoResult, serverResult, mediaResult] = await Promise.allSettled([
                Promise.resolve().then(() => app.GetClientInfoForTab(tabID, clientID)),
                Promise.resolve().then(() => refreshServer ? app.ServerInfoForTab(tabID) : server),
                Promise.resolve().then(() => pc?.getStats()),
            ]);
            busy = false;
            if (!isCurrentServerDialog(overlay)) return;
            if (!visible()) { schedule(); return; }
            const errors = [];
            if (serverResult.status === "fulfilled" && serverResult.value?.name) {
                server = serverResult.value;
                if (refreshServer) serverAt = Date.now();
            } else errors.push(t("runtime.serverDetailsUnavailable"));
            if (server) {
                set("name", server.name);
                set("version", server.version || "—");
                set("platform", server.platform || t("desktop.unavailable.on.this.server"));
                set("uptime", formatDuration(measured(server.uptime_seconds) ? server.uptime_seconds + (Date.now() - serverAt) / 1000 : null));
                set("users", `${server.clients_online ?? "—"} / ${server.max_clients ?? "—"}`);
                set("channels", server.channels_online);
            }
            const info = infoResult.status === "fulfilled" ? infoResult.value : null;
            if (!info || typeof info !== "object") errors.push(t("runtime.statsUnavailable"));
            const ping = measured(info?.ping_ms) ? info.ping_ms : null;
            set("ping", ping === null ? "—" : `${Math.round(ping)} ms`);
            set("connected", formatDuration(measured(info?.connected_at) && info.connected_at > 0 ? Math.max(0, Date.now() / 1000 - info.connected_at) : null));
            // The server counts bytes_in as uploads and bytes_out as downloads.
            set("control-in", formatBytes(info?.bytes_out));
            set("control-out", formatBytes(info?.bytes_in));
            const report = mediaResult.status === "fulfilled" && pc === state.pc ? mediaResult.value : null;
            const media = summarizeMedia(report, pc === previousPC ? previous : null);
            const processing = summarizeVideoProcessing(report);
            for (const [direction, processors] of Object.entries(processing)) {
                const element = overlay.querySelector(`[data-video-processors="${direction}"]`);
                const labels = [...new Set(processors.map((processor) => t("streams.processor", {
                    codec: processor.codec || t("streams.notReported"),
                    implementation: processor.implementation || t("streams.notReported"),
                    efficiency: t(processor.powerEfficient === true ? "streams.reportedYes" :
                        processor.powerEfficient === false ? "streams.reportedNo" : "streams.notReported"),
                })))];
                element.replaceChildren(...(labels.length ? labels : ["—"]).map((label) => {
                    const row = document.createElement("div");
                    row.textContent = label;
                    return row;
                }));
            }
            previous = media;
            previousPC = pc;
            if (mediaResult.status === "rejected" && pc === state.pc) errors.push(t("runtime.mediaUnavailable"));
            set("loss", measured(media.loss) ? `${media.loss.toFixed(2)} %` : "—");
            set("jitter", measured(media.jitter) ? `${media.jitter.toFixed(1)} ms` : "—");
            for (const [key, value] of [["media-in", media.inBytes], ["media-out", media.outBytes]]) set(key, formatBytes(value));
            for (const [key, value] of [["packets-in", media.inPackets], ["packets-out", media.outPackets]]) set(key, measured(value) ? value.toLocaleString() : "—");
            for (const [key, value] of [["rate-in", media.inRate], ["rate-out", media.outRate]]) set(key, formatBitrate(measured(value) ? value * 8 : null));
            history.push({ at: Date.now(), ping, loss: media.loss });
            while (history.length && history[0].at < Date.now() - 60000) history.shift();
            drawHistory();
            const error = overlay.querySelector(".server-info-error");
            error.textContent = errors.join(" ");
            error.hidden = !errors.length;
        }
        schedule();
    };
    const schedule = () => {
        if (isCurrentServerDialog(overlay) && !document.hidden) timer = setTimeout(sample, 2000);
    };
    const visibilityChanged = () => {
        clearTimer();
        previous = null;
        if (!document.hidden) void sample();
    };
    document.addEventListener("visibilitychange", visibilityChanged);
    const close = () => closeDialog(overlay);
    overlay.querySelector(".stats-close").onclick = close;
    overlay.querySelector(".dlg-ok").onclick = close;
    overlay.onclick = (event) => { if (event.target === overlay) close(); };
    mountServerDialog(overlay, { onClose: () => {
        clearTimer();
        document.removeEventListener("visibilitychange", visibilityChanged);
        if (currentOverlay === overlay) currentOverlay = null;
    } });
    void sample();
}

function drawChart(canvas, history, field, label, unit) {
    const context = canvas.getContext("2d");
    const styles = getComputedStyle(canvas);
    context.clearRect(0, 0, canvas.width, canvas.height);
    context.strokeStyle = styles.getPropertyValue("--accent");
    context.lineWidth = 1.5;
    context.beginPath();
    const max = Math.max(1, ...history.map((r) => r[field] || 0));
    let gap = true;
    for (const row of history) {
        if (!measured(row[field])) { gap = true; continue; }
        const x = (row.at - Date.now() + 60000) / 60000 * canvas.width;
        const y = canvas.height - row[field] / max * (canvas.height - 6) - 3;
        if (gap) context.moveTo(x, y);
        else context.lineTo(x, y);
        gap = false;
    }
    context.stroke();
    const latest = history.at(-1)?.[field];
    const text = measured(latest) ? `${label}: ${latest.toFixed(1)} ${unit}` : t("runtime.chartUnavailable", { label });
    canvas.setAttribute("aria-label", text);
    context.fillStyle = styles.color;
    context.font = "12px sans-serif";
    context.fillText(text, 8, 16);
}
