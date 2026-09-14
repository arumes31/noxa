import { closeDialog, isCurrentServerDialog, mountServerDialog } from "./modal.js";
import { icon } from "./icons.js";
import { formatBytes, formatDuration, measured, summarizeMedia } from "./connection-stats.js";

const V = () => window.__voicx;
let currentOverlay = null;

export function openServerInfo() {
    if (!V().state.myClientID) return V().toast("Connect to a server to view its information.", "warn");
    if (currentOverlay && isCurrentServerDialog(currentOverlay)) {
        currentOverlay.focus();
        return;
    }
    const overlay = document.createElement("div");
    overlay.className = "dlg-overlay";
    const field = (key) => `<span data-stat="${key}">—</span>`;
    overlay.innerHTML = `
        <div class="dlg stats-page server-info">
            <div class="pm-head"><h3>Server information</h3>
                <button class="icon-btn stats-close" aria-label="Close server information">${icon("close")}</button></div>
            <div class="server-info-body">
                <dl class="server-info-details">
                    <dt>Name</dt><dd>${field("name")}</dd>
                    <dt>Address</dt><dd class="server-info-address">${field("address")}<button class="server-copy" title="Copy server address" aria-label="Copy server address">Copy</button></dd>
                    <dt>Version</dt><dd>${field("version")}</dd>
                    <dt>Platform</dt><dd>${field("platform")}</dd>
                    <dt>Uptime</dt><dd>${field("uptime")}</dd>
                    <dt>Users / capacity</dt><dd>${field("users")}</dd>
                    <dt>Channels</dt><dd>${field("channels")}</dd>
                </dl>
                <section class="server-info-connection" aria-labelledby="server-info-connection-title">
                    <h4 id="server-info-connection-title">Your connection</h4>
                    <div class="server-info-quality">
                        <div><span>Server latency</span><strong>${field("ping")}</strong></div>
                        <div><span>Incoming audio loss</span><strong>${field("loss")}</strong></div>
                        <div><span>Audio jitter · highest</span><strong>${field("jitter")}</strong></div>
                    </div>
                    <p class="server-info-session">Connected for ${field("connected")}</p>
                    <table class="server-info-traffic">
                        <caption>Traffic for this connection</caption>
                        <thead><tr><th scope="col">Transferred</th><th scope="col">In</th><th scope="col">Out</th></tr></thead>
                        <tbody>
                            <tr><th scope="row">Control data</th><td>${field("control-in")}</td><td>${field("control-out")}</td></tr>
                            <tr><th scope="row">Media data</th><td>${field("media-in")}</td><td>${field("media-out")}</td></tr>
                            <tr><th scope="row">Media packets</th><td>${field("packets-in")}</td><td>${field("packets-out")}</td></tr>
                            <tr><th scope="row">Media / second</th><td>${field("rate-in")}</td><td>${field("rate-out")}</td></tr>
                        </tbody>
                    </table>
                    <p class="server-info-note">In = received · Out = sent. Data excludes protocol headers. Audio loss is cumulative for current streams; — means unavailable.</p>
                    <details class="server-info-history"><summary>Recent latency &amp; audio loss</summary>
                        <div class="stats-label">Server latency · ms · last 60 seconds</div><canvas class="stats-rtt" width="560" height="80" role="img" aria-label="Recent server latency"></canvas>
                        <div class="stats-label">Incoming audio loss · % · last 60 seconds</div><canvas class="stats-loss" width="560" height="60" role="img" aria-label="Recent incoming audio loss"></canvas>
                    </details>
                </section>
                <p class="server-info-error" role="status" hidden></p>
            </div>
            <div class="server-info-footer"><span>Updates every 2 seconds while visible</span><button class="dlg-ok">Close</button></div>
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
    copy.onclick = async () => {
        try {
            if (await window.runtime.ClipboardSetText(address) === false) throw new Error("Clipboard unavailable");
            if (isCurrentServerDialog(overlay)) V().toast("Server address copied.");
        } catch {
            if (isCurrentServerDialog(overlay)) V().toast("Could not copy the server address.", "warn");
        }
    };

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
        drawChart(overlay.querySelector(".stats-rtt"), history, "ping", "Server latency", "ms");
        drawChart(overlay.querySelector(".stats-loss"), history, "loss", "Incoming audio loss", "%");
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
                Promise.resolve().then(() => app.GetClientInfo(state.myClientID)),
                Promise.resolve().then(() => refreshServer ? app.ServerInfo() : server),
                Promise.resolve().then(() => pc?.getStats()),
            ]);
            busy = false;
            if (!isCurrentServerDialog(overlay)) return;
            if (!visible()) { schedule(); return; }
            const errors = [];
            if (serverResult.status === "fulfilled" && serverResult.value?.name) {
                server = serverResult.value;
                if (refreshServer) serverAt = Date.now();
            } else errors.push("Server details unavailable.");
            if (server) {
                set("name", server.name);
                set("version", server.version || "—");
                set("platform", server.platform || "Unavailable on this server");
                set("uptime", formatDuration(measured(server.uptime_seconds) ? server.uptime_seconds + (Date.now() - serverAt) / 1000 : null));
                set("users", `${server.clients_online ?? "—"} / ${server.max_clients ?? "—"}`);
                set("channels", server.channels_online);
            }
            const info = infoResult.status === "fulfilled" ? infoResult.value : null;
            if (!info || typeof info !== "object") errors.push("Connection statistics unavailable. Retrying…");
            const ping = measured(info?.ping_ms) ? info.ping_ms : null;
            set("ping", ping === null ? "—" : `${Math.round(ping)} ms`);
            set("connected", formatDuration(measured(info?.connected_at) && info.connected_at > 0 ? Math.max(0, Date.now() / 1000 - info.connected_at) : null));
            // The server counts bytes_in as uploads and bytes_out as downloads.
            set("control-in", formatBytes(info?.bytes_out));
            set("control-out", formatBytes(info?.bytes_in));
            const media = summarizeMedia(mediaResult.status === "fulfilled" && pc === state.pc ? mediaResult.value : null, pc === previousPC ? previous : null);
            previous = media;
            previousPC = pc;
            if (mediaResult.status === "rejected" && pc === state.pc) errors.push("Media statistics unavailable. Retrying…");
            set("loss", measured(media.loss) ? `${media.loss.toFixed(2)} %` : "—");
            set("jitter", measured(media.jitter) ? `${media.jitter.toFixed(1)} ms` : "—");
            for (const [key, value] of [["media-in", media.inBytes], ["media-out", media.outBytes]]) set(key, formatBytes(value));
            for (const [key, value] of [["packets-in", media.inPackets], ["packets-out", media.outPackets]]) set(key, measured(value) ? value.toLocaleString() : "—");
            for (const [key, value] of [["rate-in", media.inRate], ["rate-out", media.outRate]]) set(key, measured(value) ? `${formatBytes(value)}/s` : "—");
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
    const text = `${label}: ${measured(latest) ? `${latest.toFixed(1)} ${unit}` : "unavailable"}`;
    canvas.setAttribute("aria-label", text);
    context.fillStyle = styles.color;
    context.font = "12px sans-serif";
    context.fillText(text, 8, 16);
}
