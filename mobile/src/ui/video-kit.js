// video-kit.js — pieces shared by the stream stage (streams.js) and the
// call screen (call.js): icons, fullscreen with the iPhone fallback,
// picture-in-picture, the camera sheet (flip, privacy blur, server
// limits) and the video diagnostics sheet.
import * as settings from "../core/settings.js";
import { EFFECTS, QUALITIES, codecCapabilityLine, codecFamilies, limited, watchCodecNames } from "../core/video.js";
import { addStrings, t } from "../i18n.js";
import { h, toast } from "./dom.js";
import "./video.css";

addStrings({
    en: {
        "video.fullscreen": "Fullscreen",
        "video.exitFullscreen": "Exit fullscreen",
        "video.pip": "Picture in picture",
        "video.rotate": "Turn your phone sideways for a bigger picture.",
        "video.fit": "Fit",
        "video.fill": "Fill",
        "video.fitToggle": "Switch between fit and fill",
        "video.flip": "Switch camera",
        "video.flipFront": "Front camera",
        "video.flipBack": "Back camera",
        "video.cameraOptions": "Camera options",
        "video.effect": "Camera effect",
        "video.effect.none": "None",
        "video.effect.blur": "Privacy blur",
        "video.effectHint": "Privacy blur softens the whole picture, including you. It hides your surroundings but is not a person-aware background blur.",
        "video.blurStrength": "Blur strength",
        "video.limits": "This server limits video to {size}{rate}. Your camera is sent within these limits.",
        "video.limitsSize": "{w}×{h}",
        "video.limitsRate": "{mbps} Mbit/s",
        "video.limitsNone": "This server does not limit camera resolution or bitrate.",
        "video.limitsChanged": "The server changed its video limits.",
        "video.quality": "Quality",
        "video.qualityFor": "Quality for {name}",
        "video.quality.auto": "Auto",
        "video.quality.high": "High",
        "video.quality.medium": "Medium",
        "video.quality.low": "Low",
        "video.defaultQuality": "Default stream quality",
        "video.lowBandwidth": "Low bandwidth mode is on: streams use the low layer.",
        "video.qualityUnsupported": "This server picks the stream quality itself.",
        "video.settings": "Video settings",
        "video.diagnostics": "Video diagnostics",
        "video.decode": "Decodes",
        "video.encode": "Encodes",
        "video.watchCodecs": "Requested from the server",
        "video.sfuNote": "The server forwards VP8, H.264 and H.265 only; VP9 and AV1 are used once it can route them.",
        "video.e2ee": "Media encryption",
        "video.e2eeNote": "Channel streams are encrypted between the phone and the server (DTLS-SRTP). Private calls are peer to peer through the relay and encrypted end to end.",
        "video.close": "Close",
        "video.layout": "Layout",
        "video.layoutGrid": "Grid",
        "video.layoutFocus": "Focus",
        "video.swap": "Swap with the large picture",
        "video.muteStream": "Mute stream",
        "video.unmuteStream": "Unmute stream",
        "video.stage": "Streams",
        "video.screenOf": "{name}'s screen",
        "video.cameraOf": "{name}'s camera",
        "video.deafen": "Silence call",
        "video.undeafen": "Hear call",
        "video.shareScreen": "Share screen",
        "video.stopScreen": "Stop sharing",
        "video.waiting": "{name} is calling",
        "video.waitingAccept": "End and accept",
        "video.waitingDecline": "Decline",
        "video.waitingLabel": "Call waiting",
    },
    de: {
        "video.fullscreen": "Vollbild",
        "video.exitFullscreen": "Vollbild beenden",
        "video.pip": "Bild im Bild",
        "video.rotate": "Dreh dein Telefon quer für ein größeres Bild.",
        "video.fit": "Einpassen",
        "video.fill": "Füllen",
        "video.fitToggle": "Zwischen Einpassen und Füllen wechseln",
        "video.flip": "Kamera wechseln",
        "video.flipFront": "Frontkamera",
        "video.flipBack": "Rückkamera",
        "video.cameraOptions": "Kameraoptionen",
        "video.effect": "Kameraeffekt",
        "video.effect.none": "Keiner",
        "video.effect.blur": "Sichtschutz-Unschärfe",
        "video.effectHint": "Die Sichtschutz-Unschärfe macht das ganze Bild unscharf, auch dich. Sie verbirgt deine Umgebung, erkennt aber keine Personen.",
        "video.blurStrength": "Stärke der Unschärfe",
        "video.limits": "Dieser Server begrenzt Video auf {size}{rate}. Deine Kamera wird innerhalb dieser Grenzen gesendet.",
        "video.limitsSize": "{w}×{h}",
        "video.limitsRate": "{mbps} Mbit/s",
        "video.limitsNone": "Dieser Server begrenzt Auflösung und Bitrate der Kamera nicht.",
        "video.limitsChanged": "Der Server hat seine Videogrenzen geändert.",
        "video.quality": "Qualität",
        "video.qualityFor": "Qualität für {name}",
        "video.quality.auto": "Automatisch",
        "video.quality.high": "Hoch",
        "video.quality.medium": "Mittel",
        "video.quality.low": "Niedrig",
        "video.defaultQuality": "Standardqualität für Streams",
        "video.lowBandwidth": "Datensparmodus ist an: Streams nutzen die niedrige Stufe.",
        "video.qualityUnsupported": "Dieser Server wählt die Streamqualität selbst.",
        "video.settings": "Videoeinstellungen",
        "video.diagnostics": "Videodiagnose",
        "video.decode": "Dekodiert",
        "video.encode": "Kodiert",
        "video.watchCodecs": "Beim Server angefragt",
        "video.sfuNote": "Der Server leitet nur VP8, H.264 und H.265 weiter; VP9 und AV1 werden genutzt, sobald er sie unterstützt.",
        "video.e2ee": "Medienverschlüsselung",
        "video.e2eeNote": "Kanal-Streams sind zwischen Telefon und Server verschlüsselt (DTLS-SRTP). Private Anrufe laufen direkt über das Relay und sind Ende-zu-Ende verschlüsselt.",
        "video.close": "Schließen",
        "video.layout": "Anordnung",
        "video.layoutGrid": "Raster",
        "video.layoutFocus": "Fokus",
        "video.swap": "Mit dem großen Bild tauschen",
        "video.muteStream": "Stream stummschalten",
        "video.unmuteStream": "Stream-Ton an",
        "video.stage": "Streams",
        "video.screenOf": "Bildschirm von {name}",
        "video.cameraOf": "Kamera von {name}",
        "video.deafen": "Anruf stumm",
        "video.undeafen": "Anruf hören",
        "video.shareScreen": "Bildschirm teilen",
        "video.stopScreen": "Teilen beenden",
        "video.waiting": "{name} ruft an",
        "video.waitingAccept": "Beenden und annehmen",
        "video.waitingDecline": "Ablehnen",
        "video.waitingLabel": "Anklopfen",
    },
});

// Small inline icons for this feature (same stroke style as icons.js).
const PATHS = {
    fullscreen: "M4 9V4h5M15 4h5v5M20 15v5h-5M9 20H4v-5",
    exitFullscreen: "M9 4v5H4M20 9h-5V4M15 20v-5h5M4 15h5v5",
    pip: "M3 5h18v14H3zM12 12h7v5h-7z",
    flip: "M4 8h11l-3-3M20 16H9l3 3M7 12h.01M17 12h.01",
    settings: "M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM19 12l2-1-1-3-2 .3-1.5-1.5L17 5l-3-1-1 2h-2l-1-2-3 1 .3 2L5.8 7.8 4 7.5 3 10.5l2 1v1l-2 1 1 3 2-.3 1.5 1.5L7 19l3 1 1-2h2l1 2 3-1-.3-2 1.5-1.5 2 .3 1-3-2-1z",
    grid: "M4 4h7v7H4zM13 4h7v7h-7zM4 13h7v7H4zM13 13h7v7h-7z",
    focus: "M3 4h18v11H3zM4 18h4v2H4zM10 18h4v2h-4zM16 18h4v2h-4z",
    swap: "M7 4L3 8l4 4M3 8h14M17 12l4 4-4 4M21 16H7",
    speaker: "M11 5L6 9H3v6h3l5 4zM16 9a4 4 0 0 1 0 6",
    speakerOff: "M11 5L6 9H3v6h3l5 4zM16 9l5 6M21 9l-5 6",
    close: "M6 6l12 12M18 6L6 18",
    fit: "M4 7h16v10H4zM8 10v4M16 10v4",
};

export function videoIcon(name) {
    const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    for (const [key, value] of Object.entries({ viewBox: "0 0 24 24", class: "icon", width: "20", height: "20", fill: "none", stroke: "currentColor",
        "stroke-width": "1.7", "stroke-linecap": "round", "stroke-linejoin": "round", "aria-hidden": "true" })) svg.setAttribute(key, value);
    const path = document.createElementNS("http://www.w3.org/2000/svg", "path");
    path.setAttribute("d", PATHS[name] || "");
    svg.append(path);
    return svg;
}

/** Icon button with an accessible name. */
export function iconButton(name, label, onclick, extra = {}) {
    return h("button", { class: "icon-btn video-btn", type: "button", "aria-label": label, title: label, onclick, ...extra }, videoIcon(name));
}

// --- Fullscreen and picture-in-picture -------------------------------------------

export function fullscreenElement() {
    return document.fullscreenElement || document.webkitFullscreenElement || null;
}

/**
 * Fullscreen for a stage element (Android, desktop). iPhone Safari has no
 * element fullscreen; there the video itself goes fullscreen natively.
 */
export async function enterFullscreen(element, video) {
    const request = element.requestFullscreen || element.webkitRequestFullscreen;
    if (request) {
        await request.call(element, { navigationUI: "hide" });
        // Android lets a fullscreen page turn to landscape on its own.
        await globalThis.screen?.orientation?.lock?.("landscape").catch(() => {});
        return "element";
    }
    if (video?.webkitEnterFullscreen) {
        video.webkitEnterFullscreen();
        return "video";
    }
    throw new Error("fullscreen unavailable");
}

export async function exitFullscreen() {
    globalThis.screen?.orientation?.unlock?.();
    if (!fullscreenElement()) return;
    await (document.exitFullscreen || document.webkitExitFullscreen)?.call(document)?.catch?.(() => {});
}

export function fullscreenSupported() {
    return !!(document.documentElement.requestFullscreen || document.documentElement.webkitRequestFullscreen
        || HTMLVideoElement.prototype.webkitEnterFullscreen);
}

export function pipSupported() {
    return !!document.pictureInPictureEnabled && typeof HTMLVideoElement.prototype.requestPictureInPicture === "function";
}

/** Picture-in-picture for one video, with lock-screen/media metadata. */
export async function togglePictureInPicture(video, { title = "noXa", artist = "" } = {}) {
    if (document.pictureInPictureElement === video) {
        await document.exitPictureInPicture().catch(() => {});
        return false;
    }
    await video.requestPictureInPicture();
    try {
        if (navigator.mediaSession) navigator.mediaSession.metadata = new MediaMetadata({ title, artist, artwork: [{ src: "/app/icons/icon-192.png", sizes: "192x192", type: "image/png" }] });
    } catch { /* metadata is optional */ }
    return true;
}

// --- Sheets ---------------------------------------------------------------------------

/** A bottom sheet with arbitrary content; resolves when closed. */
export function contentSheet(title, content) {
    return new Promise(resolve => {
        const close = () => { overlay.remove(); document.removeEventListener("keydown", onKey); resolve(); };
        const onKey = event => { if (event.key === "Escape") close(); };
        const overlay = h("div", { class: "sheet-overlay", onclick: event => { if (event.target === overlay) close(); } },
            h("div", { class: "sheet video-sheet", role: "dialog", "aria-modal": "true", "aria-label": title },
                h("h2", { class: "sheet-title" }, title),
                content,
                h("div", { class: "sheet-actions" }, h("button", { class: "btn", type: "button", onclick: close }, t("video.close")))));
        document.addEventListener("keydown", onKey);
        document.body.append(overlay);
        overlay.querySelector("select, input, button")?.focus();
    });
}

/** "This server limits video to 1280×720 and 2.5 Mbit/s." */
export function limitsText(limits) {
    if (!limited(limits)) return t("video.limitsNone");
    const size = limits.video_max_width > 0 ? t("video.limitsSize", { w: limits.video_max_width, h: limits.video_max_height }) : "";
    const mbps = limits.video_max_bitrate > 0 ? t("video.limitsRate", { mbps: (limits.video_max_bitrate / 1e6).toFixed(1) }) : "";
    return t("video.limits", { size, rate: size && mbps ? ` · ${mbps}` : mbps });
}

/**
 * Camera options for a running or future camera. `target` is app.voice or
 * app.calls: both have facingMode, flipCamera() and setCameraEffect().
 */
export function cameraSheet(target, session) {
    const hint = h("p", { class: "hint", role: "status" }, limitsText(session?.mediaLimits));
    const onLimits = () => { hint.textContent = limitsText(session.mediaLimits); toast(t("video.limitsChanged")); };
    session?.addEventListener("media-limits", onLimits);
    const strength = h("input", { type: "range", min: "2", max: "30", step: "1", value: String(settings.get("blurStrength")), "aria-label": t("video.blurStrength"),
        onchange: event => {
            settings.set({ blurStrength: Number(event.target.value) });
            target.setCameraEffect?.(settings.get("cameraEffect"), Number(event.target.value)).catch?.(() => {});
        } });
    const strengthField = h("label", { class: "field" }, h("span", {}, t("video.blurStrength")), strength);
    strengthField.hidden = settings.get("cameraEffect") !== "blur";
    const effect = h("select", { class: "input", "aria-label": t("video.effect"), onchange: event => {
        settings.set({ cameraEffect: event.target.value });
        strengthField.hidden = event.target.value !== "blur";
        target.setCameraEffect?.(event.target.value, settings.get("blurStrength")).catch?.(() => {});
    } }, EFFECTS.map(name => h("option", { value: name, selected: settings.get("cameraEffect") === name }, t(`video.effect.${name}`))));
    const flip = h("button", { class: "btn btn-block", type: "button", "data-facing": target.facingMode, onclick: async () => {
        flip.disabled = true;
        try { await target.flipCamera(); } catch (error) { toast(error.message, "warn"); }
        flip.disabled = false;
        flip.dataset.facing = target.facingMode;
        flip.textContent = `${t("video.flip")} · ${t(target.facingMode === "user" ? "video.flipFront" : "video.flipBack")}`;
    } }, `${t("video.flip")} · ${t(target.facingMode === "user" ? "video.flipFront" : "video.flipBack")}`);
    const content = h("div", { class: "video-sheet-body" },
        flip,
        h("label", { class: "field" }, h("span", {}, t("video.effect")), effect),
        h("p", { class: "hint" }, t("video.effectHint")),
        strengthField,
        hint);
    return contentSheet(t("video.cameraOptions"), content).finally(() => session?.removeEventListener("media-limits", onLimits));
}

/** Codec capabilities, what the phone asks the SFU for, encryption, limits. */
export function diagnosticsSheet(session) {
    let receive = [];
    try { receive = globalThis.RTCRtpReceiver?.getCapabilities?.("video")?.codecs || []; } catch { receive = []; }
    const row = (label, value) => h("div", { class: "video-diag-row" }, h("dt", {}, label), h("dd", { class: "mono" }, value));
    const content = h("div", { class: "video-sheet-body" },
        h("dl", { class: "video-diag" },
            row(t("video.decode"), codecCapabilityLine(codecFamilies("RTCRtpReceiver"))),
            row(t("video.encode"), codecCapabilityLine(codecFamilies("RTCRtpSender"))),
            row(t("video.watchCodecs"), watchCodecNames(receive).map(name => name.toUpperCase()).join(", "))),
        h("p", { class: "hint" }, t("video.sfuNote")),
        h("h3", { class: "section-label" }, t("video.e2ee")),
        h("p", { class: "hint" }, t("video.e2eeNote")),
        h("p", { class: "hint" }, limitsText(session?.mediaLimits)));
    return contentSheet(t("video.diagnostics"), content);
}

/** Video settings: default receive quality, then diagnostics. */
export function videoSettingsSheet(voice, session) {
    const select = h("select", { class: "input", "aria-label": t("video.defaultQuality"), onchange: event => {
        settings.set({ receiveQuality: event.target.value });
        voice?.applyQualities?.().catch(() => {});
    } }, QUALITIES.map(q => h("option", { value: q, selected: settings.get("receiveQuality") === q }, t(`video.quality.${q}`))));
    const content = h("div", { class: "video-sheet-body" },
        h("label", { class: "field" }, h("span", {}, t("video.defaultQuality")), select),
        settings.get("lowBandwidth") ? h("p", { class: "hint" }, t("video.lowBandwidth")) : null,
        voice && !voice.streamQualitySupported ? h("p", { class: "hint" }, t("video.qualityUnsupported")) : null,
        h("button", { class: "btn btn-block", type: "button", onclick: () => diagnosticsSheet(session) }, t("video.diagnostics")));
    return contentSheet(t("video.settings"), content);
}
