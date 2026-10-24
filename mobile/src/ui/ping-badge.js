// ping-badge.js — the live connection badge in the Servers header: one
// Msg.Ping round trip every 5 s while a badge is on screen, shown as
// good / fair / poor. It links to Diagnostics.
import { app } from "../app.js";
import { PingMonitor } from "../core/diagnostics.js";
import { addStrings, t } from "../i18n.js";
import { h } from "./dom.js";
import "./diagnostics.css";

addStrings({
    en: {
        "ping.good": "Good", "ping.fair": "Fair", "ping.poor": "Poor", "ping.unknown": "…",
        "ping.label": "Connection {quality}, {ms} ms round trip. Open diagnostics.",
        "ping.labelUnknown": "Measuring the connection. Open diagnostics.",
    },
    de: {
        "ping.good": "Gut", "ping.fair": "Mittel", "ping.poor": "Schlecht", "ping.unknown": "…",
        "ping.label": "Verbindung {quality}, {ms} ms Umlaufzeit. Diagnose öffnen.",
        "ping.labelUnknown": "Verbindung wird gemessen. Diagnose öffnen.",
    },
});

/** The app-wide ping monitor; it follows reconnects through app.session. */
export const pingMonitor = new PingMonitor(() => app.session?.conn);

function draw(badge) {
    const stats = pingMonitor.stats;
    const quality = pingMonitor.quality;
    const ms = stats.last ?? stats.mean;
    badge.dataset.quality = quality;
    badge.replaceChildren(h("span", { class: "ping-dot", "aria-hidden": "true" }),
        h("span", {}, quality === "unknown" ? t("ping.unknown") : `${Math.round(ms ?? 0)} ms`));
    badge.setAttribute("aria-label", quality === "unknown" ? t("ping.labelUnknown") : t("ping.label", { quality: t(`ping.${quality}`), ms: Math.round(ms ?? 0) }));
    badge.title = quality === "unknown" ? "" : t(`ping.${quality}`);
}

/** A badge that pings while it is in the document. */
export function pingBadge() {
    const badge = h("a", { class: "ping-badge", href: "#/diagnostics" });
    draw(badge);
    const stop = pingMonitor.start();
    const onSample = () => {
        if (!badge.isConnected) {
            pingMonitor.removeEventListener("sample", onSample);
            stop();
            return;
        }
        draw(badge);
    };
    pingMonitor.addEventListener("sample", onSample);
    return badge;
}
