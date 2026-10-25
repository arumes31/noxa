// diagnostics.js — #/diagnostics: live ping and connection quality, a
// connection test (ping jitter over 10 samples, download throughput from
// the app's own assets), voice and watched-stream statistics while in
// voice, and copy / export of a text report with the recent log.
import { app } from "../app.js";
import { measureThroughput, pingQuality, pingSeries, pingStats, pushSeries, sparklinePoints, videoStats, voiceStats } from "../core/diagnostics.js";
import { logText } from "../core/log.js";
import { addStrings, t } from "../i18n.js";
import { h, toast } from "./dom.js";
import { header, nav } from "./common.js";
import { pingMonitor } from "./ping-badge.js";
import "./diagnostics.css";

addStrings({
    en: {
        "diag.title": "Diagnostics",
        "diag.connection": "Connection",
        "diag.ping": "Ping",
        "diag.quality": "Quality",
        "diag.average": "Average (1 min)",
        "diag.jitter": "Jitter",
        "diag.loss": "Lost pings",
        "diag.reconnects": "Reconnects",
        "diag.lastMessage": "Last data received",
        "diag.ago": "{seconds} s ago",
        "diag.state": "State",
        "diag.online": "Connected",
        "diag.offline": "Offline",
        "diag.server": "Server",
        "diag.test": "Connection test",
        "diag.testHint": "Pings the server 10 times and downloads a few hundred KB of the app.",
        "diag.runTest": "Run test",
        "diag.running": "Testing…",
        "diag.testDone": "Test complete",
        "diag.testFailed": "Test failed: {error}",
        "diag.throughput": "Download speed",
        "diag.downloaded": "{size} in {ms} ms",
        "diag.voice": "Voice",
        "diag.voiceIdle": "Join a voice channel to see its statistics.",
        "diag.rtt": "Round trip",
        "diag.packetLoss": "Packet loss",
        "diag.codec": "Codec",
        "diag.bitrateIn": "Receiving",
        "diag.bitrateOut": "Sending",
        "diag.rttChart": "Round trip over the last 60 seconds",
        "diag.lossChart": "Packet loss over the last 60 seconds",
        "diag.streams": "Watched streams",
        "diag.noStreams": "No video is being received.",
        "diag.resolution": "Resolution",
        "diag.fps": "Frame rate",
        "diag.bitrate": "Bitrate",
        "diag.freezes": "Freezes",
        "diag.stream": "Stream {n}",
        "diag.copy": "Copy as text",
        "diag.copied": "Diagnostics copied.",
        "diag.copyFailed": "Copying is not allowed here.",
        "diag.exportLogs": "Export logs",
        "diag.report": "Report",
    },
    de: {
        "diag.title": "Diagnose",
        "diag.connection": "Verbindung",
        "diag.ping": "Ping",
        "diag.quality": "Qualität",
        "diag.average": "Durchschnitt (1 Min.)",
        "diag.jitter": "Jitter",
        "diag.loss": "Verlorene Pings",
        "diag.reconnects": "Neuverbindungen",
        "diag.lastMessage": "Zuletzt Daten empfangen",
        "diag.ago": "vor {seconds} s",
        "diag.state": "Status",
        "diag.online": "Verbunden",
        "diag.offline": "Offline",
        "diag.server": "Server",
        "diag.test": "Verbindungstest",
        "diag.testHint": "Pingt den Server 10-mal und lädt einige hundert KB der App herunter.",
        "diag.runTest": "Test starten",
        "diag.running": "Wird getestet…",
        "diag.testDone": "Test abgeschlossen",
        "diag.testFailed": "Test fehlgeschlagen: {error}",
        "diag.throughput": "Download-Geschwindigkeit",
        "diag.downloaded": "{size} in {ms} ms",
        "diag.voice": "Sprache",
        "diag.voiceIdle": "Tritt einem Sprachkanal bei, um seine Werte zu sehen.",
        "diag.rtt": "Umlaufzeit",
        "diag.packetLoss": "Paketverlust",
        "diag.codec": "Codec",
        "diag.bitrateIn": "Empfang",
        "diag.bitrateOut": "Senden",
        "diag.rttChart": "Umlaufzeit der letzten 60 Sekunden",
        "diag.lossChart": "Paketverlust der letzten 60 Sekunden",
        "diag.streams": "Angesehene Streams",
        "diag.noStreams": "Es wird kein Video empfangen.",
        "diag.resolution": "Auflösung",
        "diag.fps": "Bildrate",
        "diag.bitrate": "Bitrate",
        "diag.freezes": "Standbilder",
        "diag.stream": "Stream {n}",
        "diag.copy": "Als Text kopieren",
        "diag.copied": "Diagnose kopiert.",
        "diag.copyFailed": "Kopieren ist hier nicht erlaubt.",
        "diag.exportLogs": "Protokolle exportieren",
        "diag.report": "Bericht",
    },
});

const ms = value => (value === null || value === undefined ? "–" : `${Math.round(value)} ms`);
const percent = value => (value === null || value === undefined ? "–" : `${value.toFixed(1)} %`);
const kbit = value => (value === null || value === undefined ? "–" : value >= 1000 ? `${(value / 1000).toFixed(2)} Mbit/s` : `${Math.round(value)} kbit/s`);

/** <dl>-like rows: [[label, value, key]]. */
function facts(rows) {
    return h("dl", { class: "diag-facts" }, rows.flatMap(([label, value, key]) => [h("dt", {}, label), h("dd", { "data-diag": key || null }, value)]));
}

function sparkline(series, label) {
    const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    svg.setAttribute("viewBox", "0 0 120 28");
    svg.setAttribute("class", "diag-spark");
    svg.setAttribute("role", "img");
    svg.setAttribute("aria-label", label);
    svg.setAttribute("preserveAspectRatio", "none");
    const line = document.createElementNS("http://www.w3.org/2000/svg", "polyline");
    line.setAttribute("points", sparklinePoints(series));
    svg.append(line);
    return svg;
}

/** Asset URLs of the running app, for the throughput probe. */
function assetURLs() {
    const urls = new Set();
    for (const entry of performance.getEntriesByType?.("resource") || []) {
        const url = new URL(entry.name, location.href);
        if (url.origin === location.origin && url.pathname.startsWith("/app/assets/")) urls.add(url.pathname);
    }
    for (const node of document.querySelectorAll('script[src], link[rel="stylesheet"][href], link[rel="modulepreload"][href]')) {
        const url = new URL(node.src || node.href, location.href);
        if (url.origin === location.origin && url.pathname.startsWith("/app/")) urls.add(url.pathname);
    }
    if (!urls.size) urls.add("/app/");
    return [...urls];
}

// Voice samples survive leaving the screen so the sparkline keeps its minute.
const voiceHistory = { rtt: [], loss: [], snapshot: null, video: null };

export function renderDiagnostics(root) {
    const report = { connection: [], test: [], voice: [], streams: [] };
    const connection = h("div");
    const testResult = h("div", { class: "diag-test", role: "status", "aria-live": "polite" });
    const voice = h("div");
    const streams = h("div");
    let alive = true;

    function drawConnection() {
        const stats = pingMonitor.stats;
        const quality = pingMonitor.quality;
        const conn = app.session?.conn;
        const since = conn?.lastReceivedAt ? Math.max(0, Math.round((Date.now() - conn.lastReceivedAt) / 1000)) : null;
        const rows = [
            [t("diag.state"), app.online && conn?.open ? t("diag.online") : t("diag.offline")],
            [t("diag.server"), app.info?.server_name || location.host],
            [t("diag.ping"), ms(stats.last), "ping"],
            [t("diag.quality"), h("span", { class: "ping-badge", "data-quality": quality }, h("span", { class: "ping-dot", "aria-hidden": "true" }), t(`ping.${quality}`)), "quality"],
            [t("diag.average"), ms(stats.mean)],
            [t("diag.jitter"), ms(stats.jitter)],
            [t("diag.loss"), percent(stats.count ? stats.loss * 100 : null)],
            [t("diag.reconnects"), String(app.reconnects || 0), "reconnects"],
            [t("diag.lastMessage"), since === null ? "–" : t("diag.ago", { seconds: since })],
        ];
        report.connection = rows.map(([label, value]) => [label, typeof value === "string" ? value : t(`ping.${quality}`)]);
        connection.replaceChildren(facts(rows));
    }

    async function runTest(button) {
        button.disabled = true;
        testResult.replaceChildren(h("p", { class: "hint" }, t("diag.running")));
        try {
            const conn = app.session?.conn;
            if (!conn?.open) throw new Error(t("diag.offline"));
            const samples = await pingSeries(conn, 10);
            const stats = pingStats(samples);
            const speed = await measureThroughput(assetURLs());
            if (!alive) return;
            const rows = [
                [t("diag.ping"), ms(stats.mean), "test-ping"],
                [t("diag.jitter"), ms(stats.jitter), "test-jitter"],
                [t("diag.loss"), percent(stats.loss * 100)],
                [t("diag.quality"), t(`ping.${pingQuality(stats.mean, stats.loss, stats.jitter)}`)],
                [t("diag.throughput"), kbit(speed.kbps), "throughput"],
                ["", t("diag.downloaded", { size: `${Math.round(speed.bytes / 1024)} KB`, ms: Math.round(speed.ms) })],
            ];
            report.test = rows;
            testResult.replaceChildren(h("p", { class: "diag-done" }, t("diag.testDone")), facts(rows));
        } catch (error) {
            if (alive) testResult.replaceChildren(h("p", { class: "error" }, t("diag.testFailed", { error: error.message })));
        }
        button.disabled = false;
    }

    async function sampleMedia() {
        const pc = app.voice?.active ? app.voice.pc : null;
        if (!pc) {
            voiceHistory.snapshot = null;
            voiceHistory.video = null;
            report.voice = [];
            report.streams = [];
            voice.replaceChildren(h("p", { class: "hint" }, t("diag.voiceIdle")));
            streams.replaceChildren(h("p", { class: "hint" }, t("diag.noStreams")));
            return;
        }
        const raw = await pc.getStats().catch(() => null);
        if (!alive || !raw) return;
        const audio = voiceStats(raw, voiceHistory.snapshot);
        voiceHistory.snapshot = audio.snapshot;
        pushSeries(voiceHistory.rtt, audio.rtt);
        pushSeries(voiceHistory.loss, audio.packetLoss);
        const rows = [
            [t("diag.rtt"), ms(audio.rtt), "voice-rtt"],
            [t("diag.jitter"), ms(audio.jitter)],
            [t("diag.packetLoss"), percent(audio.packetLoss)],
            [t("diag.codec"), audio.codec || "–"],
            [t("diag.bitrateIn"), kbit(audio.bitrateIn)],
            [t("diag.bitrateOut"), kbit(audio.bitrateOut)],
        ];
        report.voice = rows;
        voice.replaceChildren(facts(rows),
            h("div", { class: "diag-chart" }, h("span", { class: "row-meta" }, t("diag.rtt")), sparkline(voiceHistory.rtt, t("diag.rttChart"))),
            h("div", { class: "diag-chart" }, h("span", { class: "row-meta" }, t("diag.packetLoss")), sparkline(voiceHistory.loss, t("diag.lossChart"))));
        const video = videoStats(raw, voiceHistory.video);
        voiceHistory.video = video.snapshot;
        const active = video.streams.filter(s => s.width || s.bitrate);
        report.streams = active.map((s, i) => [t("diag.stream", { n: i + 1 }), `${s.width}×${s.height} · ${s.fps ?? "–"} fps · ${kbit(s.bitrate)} · ${s.codec || "–"} · ${s.freezes}`]);
        streams.replaceChildren(...(active.length ? active.map((s, i) => h("div", { class: "diag-stream" },
            h("p", { class: "row-title" }, t("diag.stream", { n: i + 1 })),
            facts([
                [t("diag.resolution"), s.width ? `${s.width}×${s.height}` : "–"],
                [t("diag.fps"), s.fps === null ? "–" : String(Math.round(s.fps))],
                [t("diag.bitrate"), kbit(s.bitrate)],
                [t("diag.freezes"), String(s.freezes)],
                [t("diag.codec"), s.codec || "–"],
            ]))) : [h("p", { class: "hint" }, t("diag.noStreams"))]));
    }

    function reportText() {
        const lines = [`noXa diagnostics — ${new Date().toISOString()}`, `version ${app.info?.version || "?"} · ${navigator.userAgent}`, ""];
        const block = (title, rows) => {
            if (!rows.length) return;
            lines.push(`[${title}]`);
            for (const [label, value] of rows) lines.push(label ? `${label}: ${typeof value === "string" ? value : value?.textContent || ""}` : `  ${value}`);
            lines.push("");
        };
        block(t("diag.connection"), report.connection);
        block(t("diag.test"), report.test);
        block(t("diag.voice"), report.voice);
        block(t("diag.streams"), report.streams);
        return lines.join("\n");
    }

    const copy = async () => {
        try { await navigator.clipboard.writeText(reportText()); toast(t("diag.copied")); } catch { toast(t("diag.copyFailed"), "warn"); }
    };
    const exportLogs = () => {
        const text = `${reportText()}\n[log]\n${logText()}\n`;
        const url = URL.createObjectURL(new Blob([text], { type: "text/plain" }));
        const link = h("a", { href: url, download: `noxa-log-${new Date().toISOString().replace(/[:.]/g, "-")}.txt`, hidden: true });
        document.body.append(link);
        link.click();
        link.remove();
        setTimeout(() => URL.revokeObjectURL(url), 60000);
    };

    const testButton = h("button", { class: "btn btn-primary btn-block", type: "button" }, t("diag.runTest"));
    testButton.onclick = () => runTest(testButton);
    root.replaceChildren(h("section", { class: "screen diag-screen" },
        header(t("diag.title"), { back: "#/you" }),
        h("div", { class: "screen-body" },
            h("p", { class: "section-label" }, t("diag.connection")),
            h("div", { class: "card", role: "status", "aria-live": "off" }, connection),
            h("p", { class: "section-label" }, t("diag.test")),
            h("div", { class: "card diag-card" }, h("p", { class: "hint" }, t("diag.testHint")), testButton, testResult),
            h("p", { class: "section-label" }, t("diag.voice")),
            h("div", { class: "card" }, voice),
            h("p", { class: "section-label" }, t("diag.streams")),
            h("div", { class: "card" }, streams),
            h("div", { class: "diag-actions" },
                h("button", { class: "btn", type: "button", onclick: copy }, t("diag.copy")),
                h("button", { class: "btn", type: "button", onclick: exportLogs }, t("diag.exportLogs"))))),
        nav("you"));

    const stopPing = pingMonitor.start();
    const onSample = () => drawConnection();
    pingMonitor.addEventListener("sample", onSample);
    drawConnection();
    sampleMedia();
    const clockTimer = setInterval(drawConnection, 1000);
    const mediaTimer = setInterval(sampleMedia, 1000);
    return () => {
        alive = false;
        stopPing();
        pingMonitor.removeEventListener("sample", onSample);
        clearInterval(clockTimer);
        clearInterval(mediaTimer);
    };
}
