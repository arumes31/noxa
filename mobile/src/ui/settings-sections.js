// settings-sections.js — the built-in Settings sections: Appearance, Chat,
// Language, Data and About. Rows go through settings-registry.js, so the
// search box on the You screen finds them like any other feature's rows.
import { app } from "../app.js";
import { formatBytes } from "../core/channel-files.js";
import { ACCENTS, clampScale, FONT_SCALE_MAX, FONT_SCALE_MIN, normalizeHex } from "../core/appearance.js";
import { all as allSettings, get, reset, set } from "../core/settings.js";
import { addStrings, languageSetting, setLanguage, t } from "../i18n.js";
import { h, sheet, toast } from "./dom.js";
import { registerSettings } from "./settings-registry.js";
import { choiceSetting, infoRow, toggleSetting } from "./settings-rows.js";
import "./settings.css";

addStrings({
    en: {
        "settings.search": "Search settings",
        "settings.noMatch": "No setting matches “{query}”.",
        "settings.section.appearance": "Appearance",
        "settings.section.chat": "Chat",
        "settings.section.notifications": "Notifications",
        "settings.section.voice": "Voice",
        "settings.section.dm": "Direct messages",
        "settings.section.language": "Language",
        "settings.section.data": "Data & storage",
        "settings.section.about": "About",
        "settings.section.account": "Account",
        "settings.theme": "Theme",
        "settings.theme.midnight": "Midnight",
        "settings.theme.arctic": "Arctic night",
        "settings.theme.light": "Light",
        "settings.theme.contrast": "High contrast",
        "settings.theme.system": "Follow the phone",
        "settings.accent": "Accent colour",
        "settings.accentDefault": "Theme default",
        "settings.accentSwatch": "Accent {color}",
        "settings.accentCustom": "Custom colour (hex)",
        "settings.accentInvalid": "Enter a colour like #34d9ef.",
        "settings.fontScale": "Text size",
        "settings.fontScaleValue": "{percent} %",
        "settings.compact": "Compact mode",
        "settings.compactHint": "Tighter lists and message lines.",
        "settings.reduceMotion": "Reduce motion",
        "settings.reduceMotionHint": "Turn off animations, in addition to the phone's setting.",
        "settings.timestamps": "Message times",
        "settings.timestamps.absolute": "Clock time",
        "settings.timestamps.relative": "Relative (5 min ago)",
        "settings.timestamps.off": "Hidden",
        "settings.inlineImages": "Show images inline",
        "settings.inlineImagesHint": "Open image attachments in the chat without tapping.",
        "settings.linkPreviews": "Link previews",
        "settings.linkPreviewsHint": "Show link cards in channels. Previews are disabled in private messages and groups.",
        "settings.joinLeave": "Show join and leave lines",
        "settings.language": "Interface language",
        "settings.language.system": "Follow the phone",
        "settings.language.en": "English",
        "settings.language.de": "Deutsch",
        "settings.languageReload": "Reload to switch the language?",
        "settings.languageReloadBody": "The new language is used after the app reloads.",
        "settings.reloadNow": "Reload now",
        "settings.later": "Later",
        "settings.storage": "Storage used",
        "settings.storageValue": "{used} of {quota}",
        "settings.storageUnknown": "This browser does not report storage use.",
        "settings.clearImages": "Clear cached images",
        "settings.clearImagesDone": "Cached images cleared.",
        "settings.exportData": "Export local data",
        "settings.exportDataHint": "Settings and reminders of this phone as a JSON file. Keys and messages are not included.",
        "settings.deleteData": "Delete local data",
        "settings.deleteDataHint": "Resets this phone's settings and caches. You stay signed in; use Sign out to remove the device.",
        "settings.deleteDataConfirm": "Delete this phone's settings and cached data? The app reloads afterwards.",
        "settings.delete": "Delete",
        "settings.version": "Version",
        "settings.server": "Server",
        "settings.licences": "Licences",
        "settings.licencesHint": "Open-source notices",
        "settings.whatsNew": "What's new",
        "settings.whatsNewHint": "Release notes",
        "settings.checkUpdate": "Check for updates",
        "settings.upToDate": "noXa is up to date.",
        "settings.updateReady": "A new version is ready.",
        "settings.updateNow": "Update now",
        "settings.diagnostics": "Diagnostics",
        "settings.diagnosticsHint": "Ping, connection test, voice and stream stats, logs",
        "settings.open": "Open",
    },
    de: {
        "settings.search": "Einstellungen durchsuchen",
        "settings.noMatch": "Keine Einstellung passt zu „{query}“.",
        "settings.section.appearance": "Darstellung",
        "settings.section.chat": "Chat",
        "settings.section.notifications": "Benachrichtigungen",
        "settings.section.voice": "Sprache & Audio",
        "settings.section.dm": "Direktnachrichten",
        "settings.section.language": "Sprache",
        "settings.section.data": "Daten & Speicher",
        "settings.section.about": "Über",
        "settings.section.account": "Konto",
        "settings.theme": "Design",
        "settings.theme.midnight": "Mitternacht",
        "settings.theme.arctic": "Polarnacht",
        "settings.theme.light": "Hell",
        "settings.theme.contrast": "Hoher Kontrast",
        "settings.theme.system": "Wie das Telefon",
        "settings.accent": "Akzentfarbe",
        "settings.accentDefault": "Standard des Designs",
        "settings.accentSwatch": "Akzent {color}",
        "settings.accentCustom": "Eigene Farbe (Hex)",
        "settings.accentInvalid": "Gib eine Farbe wie #34d9ef ein.",
        "settings.fontScale": "Textgröße",
        "settings.fontScaleValue": "{percent} %",
        "settings.compact": "Kompakter Modus",
        "settings.compactHint": "Engere Listen und Nachrichtenzeilen.",
        "settings.reduceMotion": "Bewegung reduzieren",
        "settings.reduceMotionHint": "Animationen ausschalten, zusätzlich zur Telefoneinstellung.",
        "settings.timestamps": "Nachrichtenzeit",
        "settings.timestamps.absolute": "Uhrzeit",
        "settings.timestamps.relative": "Relativ (vor 5 Min.)",
        "settings.timestamps.off": "Ausgeblendet",
        "settings.inlineImages": "Bilder direkt anzeigen",
        "settings.inlineImagesHint": "Bildanhänge im Chat ohne Antippen öffnen.",
        "settings.linkPreviews": "Linkvorschauen",
        "settings.linkPreviewsHint": "Linkkarten in Kanälen zeigen. In privaten Nachrichten und Gruppen sind Vorschauen deaktiviert.",
        "settings.joinLeave": "Beitritte und Abgänge anzeigen",
        "settings.language": "Sprache der Oberfläche",
        "settings.language.system": "Wie das Telefon",
        "settings.language.en": "English",
        "settings.language.de": "Deutsch",
        "settings.languageReload": "Neu laden, um die Sprache zu wechseln?",
        "settings.languageReloadBody": "Die neue Sprache gilt, sobald die App neu geladen ist.",
        "settings.reloadNow": "Jetzt neu laden",
        "settings.later": "Später",
        "settings.storage": "Belegter Speicher",
        "settings.storageValue": "{used} von {quota}",
        "settings.storageUnknown": "Dieser Browser meldet keine Speichernutzung.",
        "settings.clearImages": "Zwischengespeicherte Bilder löschen",
        "settings.clearImagesDone": "Zwischengespeicherte Bilder gelöscht.",
        "settings.exportData": "Lokale Daten exportieren",
        "settings.exportDataHint": "Einstellungen und Erinnerungen dieses Telefons als JSON-Datei. Schlüssel und Nachrichten sind nicht enthalten.",
        "settings.deleteData": "Lokale Daten löschen",
        "settings.deleteDataHint": "Setzt Einstellungen und Zwischenspeicher dieses Telefons zurück. Du bleibst angemeldet; zum Entfernen des Geräts nutze Abmelden.",
        "settings.deleteDataConfirm": "Einstellungen und zwischengespeicherte Daten dieses Telefons löschen? Die App lädt danach neu.",
        "settings.delete": "Löschen",
        "settings.version": "Version",
        "settings.server": "Server",
        "settings.licences": "Lizenzen",
        "settings.licencesHint": "Open-Source-Hinweise",
        "settings.whatsNew": "Neuigkeiten",
        "settings.whatsNewHint": "Versionshinweise",
        "settings.checkUpdate": "Nach Updates suchen",
        "settings.upToDate": "noXa ist aktuell.",
        "settings.updateReady": "Eine neue Version ist bereit.",
        "settings.updateNow": "Jetzt aktualisieren",
        "settings.diagnostics": "Diagnose",
        "settings.diagnosticsHint": "Ping, Verbindungstest, Sprach- und Streamwerte, Protokolle",
        "settings.open": "Öffnen",
    },
});

const REPO = "https://github.com/arumes31/noxa";
const SHELL_CACHE = /^noxa-shell-/;


// --- Appearance -------------------------------------------------------------------

function accentRow() {
    const current = normalizeHex(get("accent"));
    const custom = h("input", { class: "input settings-hex", type: "text", inputmode: "text", maxlength: "7", spellcheck: "false", autocomplete: "off",
        placeholder: "#34d9ef", value: current, "aria-label": t("settings.accentCustom") });
    const error = h("p", { class: "error", role: "status" });
    const swatches = h("div", { class: "settings-swatches", role: "group", "aria-label": t("settings.accent") });
    const draw = () => {
        const value = normalizeHex(get("accent"));
        swatches.replaceChildren(
            h("button", { type: "button", class: "settings-swatch settings-swatch-default", "aria-pressed": String(!value), "aria-label": t("settings.accentDefault"), title: t("settings.accentDefault"),
                onclick: () => { set({ accent: "" }); custom.value = ""; draw(); } }),
            ...ACCENTS.map(color => h("button", { type: "button", class: "settings-swatch", style: `--swatch:${color}`, "aria-pressed": String(value === color),
                "aria-label": t("settings.accentSwatch", { color }), title: color,
                onclick: () => { set({ accent: color }); custom.value = color; error.textContent = ""; draw(); } })));
    };
    custom.onchange = () => {
        const value = normalizeHex(custom.value);
        if (custom.value.trim() && !value) { error.textContent = t("settings.accentInvalid"); return; }
        error.textContent = "";
        custom.value = value;
        set({ accent: value });
        draw();
    };
    draw();
    return h("div", { class: "settings-block" }, h("span", { class: "row-title" }, t("settings.accent")), swatches, custom, error);
}

function fontScaleRow() {
    const value = h("output", { class: "row-meta" });
    const range = h("input", { type: "range", class: "settings-range", min: String(FONT_SCALE_MIN), max: String(FONT_SCALE_MAX), step: "0.05", value: String(clampScale(get("fontScale"))), "aria-label": t("settings.fontScale") });
    const show = () => { value.textContent = t("settings.fontScaleValue", { percent: Math.round(clampScale(range.value) * 100) }); };
    range.oninput = () => { show(); set({ fontScale: clampScale(range.value) }); };
    show();
    return h("div", { class: "settings-block" }, h("span", { class: "settings-line" }, h("span", { class: "row-title" }, t("settings.fontScale")), value), range);
}

registerSettings("appearance", [
    { key: "theme", label: t("settings.theme"), keywords: "dark light contrast design farbe", render: () => choiceSetting("theme", t("settings.theme"),
        ["midnight", "arctic", "light", "contrast", "system"].map(name => [name, t(`settings.theme.${name}`)])) },
    { key: "accent", label: t("settings.accent"), keywords: "color colour", render: accentRow },
    { key: "fontScale", label: t("settings.fontScale"), keywords: "font size", render: fontScaleRow },
    { key: "compact", label: t("settings.compact"), keywords: "density", render: () => toggleSetting("compact", t("settings.compact"), t("settings.compactHint")) },
    { key: "reduceMotion", label: t("settings.reduceMotion"), keywords: "animation", render: () => toggleSetting("reduceMotion", t("settings.reduceMotion"), t("settings.reduceMotionHint")) },
], { title: t("settings.section.appearance") });

// --- Chat display -----------------------------------------------------------------

registerSettings("chat", [
    { key: "timestamps", label: t("settings.timestamps"), keywords: "time clock", render: () => choiceSetting("timestamps", t("settings.timestamps"),
        ["absolute", "relative", "off"].map(name => [name, t(`settings.timestamps.${name}`)])) },
    { key: "inlineImages", label: t("settings.inlineImages"), keywords: "pictures", render: () => toggleSetting("inlineImages", t("settings.inlineImages"), t("settings.inlineImagesHint")) },
    { key: "linkPreviews", label: t("settings.linkPreviews"), keywords: "url embed", render: () => toggleSetting("linkPreviews", t("settings.linkPreviews"), t("settings.linkPreviewsHint")) },
    { key: "showJoinLeave", label: t("settings.joinLeave"), keywords: "join leave", render: () => toggleSetting("showJoinLeave", t("settings.joinLeave")) },
], { title: t("settings.section.chat") });

// --- Language ---------------------------------------------------------------------

registerSettings("language", [
    { key: "language", label: t("settings.language"), keywords: "language sprache english deutsch", render: () => choiceSetting("language", t("settings.language"),
        [["", t("settings.language.system")], ["en", t("settings.language.en")], ["de", t("settings.language.de")]], {
            value: languageSetting(),
            onChange: async code => {
                setLanguage(code);
                const choice = await sheet(t("settings.languageReload"), t("settings.languageReloadBody"), [
                    { label: t("settings.reloadNow"), value: true, primary: true },
                    { label: t("settings.later"), value: false },
                ]);
                if (choice) location.reload();
            },
        }) },
], { title: t("settings.section.language") });

// --- Data -------------------------------------------------------------------------

function storageRow() {
    const detail = h("span", { class: "row-sub" }, "…");
    const row = infoRow(t("settings.storage"), null);
    row.querySelector(".row-main").append(detail);
    (async () => {
        try {
            const estimate = await navigator.storage?.estimate?.();
            if (!estimate) { detail.textContent = t("settings.storageUnknown"); return; }
            detail.textContent = t("settings.storageValue", { used: formatBytes(estimate.usage || 0), quota: formatBytes(estimate.quota || 0) });
        } catch {
            detail.textContent = t("settings.storageUnknown");
        }
    })();
    return row;
}

/** Media caches other modules keep: Cache Storage beyond the app shell, plus an event for in-memory caches. */
export async function clearImageCaches() {
    if (globalThis.caches) for (const name of await caches.keys()) if (!SHELL_CACHE.test(name)) await caches.delete(name);
    app.dispatchEvent(new Event("clear-media-cache"));
}

function localKeys() {
    const keys = [];
    try { for (let i = 0; i < localStorage.length; i++) if (localStorage.key(i)?.startsWith("noxa.")) keys.push(localStorage.key(i)); } catch { /* blocked */ }
    return keys;
}

function exportLocalData() {
    const stored = {};
    for (const key of localKeys()) {
        try { stored[key] = JSON.parse(localStorage.getItem(key)); } catch { stored[key] = localStorage.getItem(key); }
    }
    const data = { app: "noXa", exported_at: new Date().toISOString(), version: app.info?.version || "", server: app.info?.server_name || "", settings: allSettings(), storage: stored };
    const url = URL.createObjectURL(new Blob([JSON.stringify(data, null, 2)], { type: "application/json" }));
    const link = h("a", { href: url, download: `noxa-local-data-${new Date().toISOString().slice(0, 10)}.json` });
    document.body.append(link);
    link.click();
    link.remove();
    setTimeout(() => URL.revokeObjectURL(url), 60000);
}

async function deleteLocalData() {
    const ok = await sheet(t("settings.deleteData"), t("settings.deleteDataConfirm"), [
        { label: t("settings.delete"), value: true, danger: true },
        { label: t("settings.later"), value: false },
    ]);
    if (!ok) return;
    reset();
    for (const key of localKeys()) { try { localStorage.removeItem(key); } catch { /* blocked */ } }
    await clearImageCaches().catch(() => {});
    location.reload();
}

registerSettings("data", [
    { key: "storage", label: t("settings.storage"), keywords: "quota space", render: storageRow },
    { key: "clearImages", label: t("settings.clearImages"), keywords: "cache", render: () => infoRow(t("settings.clearImages"), null,
        h("button", { class: "btn", type: "button", onclick: async () => {
            try { await clearImageCaches(); toast(t("settings.clearImagesDone")); } catch (error) { toast(t("common.error", { error: error.message }), "warn"); }
        } }, t("settings.clearImages"))) },
    { key: "exportData", label: t("settings.exportData"), keywords: "backup json", render: () => infoRow(t("settings.exportData"), t("settings.exportDataHint"),
        h("button", { class: "btn", type: "button", onclick: exportLocalData }, t("settings.exportData"))) },
    { key: "deleteData", label: t("settings.deleteData"), keywords: "reset", render: () => infoRow(t("settings.deleteData"), t("settings.deleteDataHint"),
        h("button", { class: "btn btn-danger", type: "button", onclick: deleteLocalData }, t("settings.delete"))) },
], { title: t("settings.section.data") });

// --- About ------------------------------------------------------------------------

/** Whether the server has a newer app: a waiting service worker or a changed version. */
export async function checkForUpdate() {
    const registration = await navigator.serviceWorker?.getRegistration("/app/").catch(() => null);
    if (registration) {
        await registration.update().catch(() => {});
        if (registration.waiting || registration.installing) return { ready: true, registration };
    }
    try {
        const response = await fetch("/api/v1/web/info", { cache: "no-store" });
        const info = response.ok ? await response.json() : null;
        if (info?.version && app.info?.version && info.version !== app.info.version) return { ready: true, registration };
    } catch { /* offline: nothing newer is known */ }
    return { ready: false, registration };
}

function updateRow() {
    const status = h("span", { class: "row-sub settings-wrap", role: "status" });
    const button = h("button", { class: "btn", type: "button" }, t("settings.checkUpdate"));
    button.onclick = async () => {
        button.disabled = true;
        status.textContent = "…";
        const { ready, registration } = await checkForUpdate();
        button.disabled = false;
        status.textContent = ready ? t("settings.updateReady") : t("settings.upToDate");
        if (ready) {
            button.textContent = t("settings.updateNow");
            button.onclick = () => {
                registration?.waiting?.postMessage("activate-update");
                location.reload();
            };
        }
    };
    const row = infoRow(t("settings.checkUpdate"), null, button);
    row.querySelector(".row-main").append(status);
    return row;
}

function licencesRow() {
    const link = h("a", { class: "btn", href: `${REPO}/blob/main/LICENSE`, target: "_blank", rel: "noopener" }, t("settings.open"));
    // The audio notices ship with builds that include the sound pack.
    fetch("/app/noxa-audio-licenses.txt", { method: "HEAD" }).then(response => {
        if (response.ok && !/text\/html/.test(response.headers.get("content-type") || "")) link.href = "/app/noxa-audio-licenses.txt";
    }).catch(() => {});
    return infoRow(t("settings.licences"), t("settings.licencesHint"), link);
}

registerSettings("about", [
    { key: "version", label: t("settings.version"), render: () => infoRow(t("settings.version"), app.info?.version || "–") },
    { key: "server", label: t("settings.server"), render: () => infoRow(t("settings.server"), app.info?.server_name || location.host) },
    { key: "whatsNew", label: t("settings.whatsNew"), keywords: "changelog release", render: () => infoRow(t("settings.whatsNew"), t("settings.whatsNewHint"),
        h("a", { class: "btn", href: `${REPO}/releases`, target: "_blank", rel: "noopener" }, t("settings.open"))) },
    { key: "licences", label: t("settings.licences"), keywords: "license open source", render: licencesRow },
    { key: "update", label: t("settings.checkUpdate"), keywords: "update version", render: updateRow },
    { key: "diagnostics", label: t("settings.diagnostics"), keywords: "ping logs network", render: () => infoRow(t("settings.diagnostics"), t("settings.diagnosticsHint"),
        h("a", { class: "btn", href: "#/diagnostics" }, t("settings.open"))) },
], { title: t("settings.section.about") });
