// i18n.js — wave-8c lightweight internationalization (336).
//
// USAGE PATTERN (how to add strings):
//   1. Add the key with BOTH translations to the catalogs below
//      (key = dotted path, e.g. "menu.connections"). en and de must carry
//      the same keys — unit tests enforce parity without putting work on the
//      language-switch hot path.
//   2. In code use t("menu.connections"); with placeholders:
//      t("chat.connectedAs", { nick }) → "Connected as {nick}".
//   3. Settings → Application → Language applies it live (menus and the
//      settings dialog rebuild; static index.html labels are set once at
//      startup). Every settings page uses the same catalogs.

import { settingsEnglish, settingsGerman } from "./settings-messages.js";
import { loginErrorEnglish, loginErrorGerman } from "./login-error-messages.js";
import { streamHealthEnglish, streamHealthGerman } from "./stream-health-messages.js";
import { audioRecoveryEnglish, audioRecoveryGerman } from "./audio-recovery-messages.js";
import { networkEchoEnglish, networkEchoGerman } from "./network-echo-messages.js";
import { runtimeEnglish, runtimeGerman } from "./runtime-messages.js";
import { desktopEnglish, desktopGerman } from "./desktop-messages.js";
import { securityEnglish, securityGerman } from "./security-messages.js";
import { interfaceEnglish, interfaceGerman } from "./interface-messages.js";
import { roleEnglish, roleGerman } from "./role-messages.js";
import { auditEnglish, auditGerman } from "./audit-messages.js";
import { voiceDiagnosticsEnglish, voiceDiagnosticsGerman } from "./voice-diagnostics-messages.js";

import { quickWinEnglish, quickWinGerman } from "./quick-win-messages.js";
import { uiPolishEnglish, uiPolishGerman } from "./ui-polish-messages.js";
import { streamEnglish, streamGerman } from "./stream-messages.js";
import { chatEnglish, chatGerman } from "./chat-messages.js";
import { conversationEnglish, conversationGerman } from "./conversation-messages.js";
import { callEnglish, callGerman } from "./call-messages.js";
import { selectedWinEnglish, selectedWinGerman } from "./selected-win-messages.js";
import { contextEnglish, contextGerman } from "./context-messages.js";
import { micEnglish, micGerman } from "./mic-messages.js";
import { cameraEnglish, cameraGerman } from "./camera-messages.js";
import { callMediaEnglish, callMediaGerman } from "./call-media-messages.js";
import { discussionEnglish, discussionGerman } from "./forum-messages.js";
import { voiceMessageEnglish, voiceMessageGerman } from "./voice-message-messages.js";
import { overlayEnglish, overlayGerman } from "./gaming-overlay-messages.js";
import { communicationExtrasEnglish, communicationExtrasGerman } from "./communication-extras-messages.js";
import { messageToolsEnglish, messageToolsGerman } from "./message-tools-messages.js";
import { webhookEnglish, webhookGerman } from "./webhook-messages.js";

const en = {
    ...loginErrorEnglish, ...streamHealthEnglish,
    ...voiceDiagnosticsEnglish,
    ...desktopEnglish,
    ...runtimeEnglish,
    ...networkEchoEnglish,
    ...audioRecoveryEnglish,
    ...cameraEnglish, ...callMediaEnglish, ...discussionEnglish, ...voiceMessageEnglish, ...overlayEnglish, ...communicationExtrasEnglish, ...messageToolsEnglish, ...webhookEnglish,
    ...contextEnglish,
    ...micEnglish,
    ...selectedWinEnglish,
    ...callEnglish,
    ...conversationEnglish,
    ...chatEnglish,
    ...streamEnglish,
    ...uiPolishEnglish,
    ...auditEnglish,
    ...roleEnglish,
    ...quickWinEnglish,
    ...settingsEnglish,
    ...securityEnglish,
    ...interfaceEnglish,
    "menu.connections": "Connections",
    "menu.bookmarks": "Bookmarks",
    "menu.self": "Self",
    "menu.view": "View",
    "menu.permissions": "Permissions",
    "menu.tools": "Tools",
    "menu.help": "Help",
    "menu.connect": "Connect…",
    "menu.disconnect": "Disconnect",
    "menu.quit": "Quit",
    "menu.settings": "Settings…",
    "menu.compact": "Compact mode",
    "menu.exitCompact": "Exit compact mode",
    "menu.zen": "Zen mode",
    "menu.setStatus": "Set status…",
    "menu.contacts": "Contacts…",
    "menu.setAvatar": "Set avatar…",
    "menu.setServerIcon": "Set server icon…",
    "menu.auditLog": "Audit Log…",
    "menu.bans": "Bans…",
    "menu.debugConsole": "Debug console…",
    "menu.connStats": "Connection stats…",
    "menu.exportLogs": "Export logs…",
    "menu.about": "About noXa",
    "menu.checkUpdates": "Check for updates…",
    "menu.permManager": "Permission Manager…",
    "menu.viewMyPerms": "View my permissions",
    "menu.whisperLists": "Whisper lists…",
    "common.ok": "OK",
    "common.cancel": "Cancel",
    "common.close": "Close",
    "common.save": "Save",
    "common.delete": "Delete",
    "common.create": "Create",
    "common.apply": "Apply",
    "login.optional": "Optional details",
    "login.serverPlaceholder": "Server address",
    "login.displayPlaceholder": "Use account name",
    "login.accountPasswordPlaceholder": "For a registered account",
    "login.rememberPasswords": "Remember passwords for this connection",
    "login.savedPassword": "Saved password",
    "login.passwordProtection": "Protected by your operating system. Uncheck to delete saved passwords.",
    "login.passwordUnavailable": "Protected password storage is unavailable on this device.",
    "login.passwordLookupFailed": "Saved passwords could not be checked. Enter them again.",
    "login.passwordForgetFailed": "Saved passwords could not be deleted. Please try again.",
    "login.connectionChanged": "Connection details changed. Please connect again.",
    "login.optionalHint": "Display name · Passwords",
    "login.identity": "Identity auto-generated — stored locally",
    "login.connecting": "Connecting…",
    "login.emptyRecents": "Your recent servers will appear here.",
    "login.server": "Server",
    "login.nickname": "Account login / guest name",
    "login.displayName": "Display name (optional)",
    "login.displayNameHint": "Visible to everyone. Your account login stays unchanged.",
    "login.serverPassword": "Server password (optional)",
    "login.accountPassword": "Account password (optional)",
    "login.connect": "Connect",
    "login.recentServers": "Recent servers",
    "settings.application": "Application",
    "settings.capture": "Capture",
    "settings.playback": "Playback",
    "settings.hotkeys": "Hotkeys",
    "settings.whisper": "Whisper",
    "settings.downloads": "Downloads",
    "settings.chat": "Chat",
    "settings.security": "Security",
    "settings.server": "Server",
    "settings.notifications": "Notifications",
    "settings.language": "Language",
    "settings.searchPlaceholder": "search settings…",
    "chat.connectedAs": "Connected as {nick}",
    "status.offline": "offline",
    "status.retry": "retry {n}/{max} in {s}s…",
    "notif.title": "Notifications",
    "notif.clearAll": "clear all",
};

const de = {
    ...loginErrorGerman, ...streamHealthGerman,
    ...voiceDiagnosticsGerman,
    ...desktopGerman,
    ...runtimeGerman,
    ...networkEchoGerman,
    ...audioRecoveryGerman,
    ...cameraGerman, ...callMediaGerman, ...discussionGerman, ...voiceMessageGerman, ...overlayGerman, ...communicationExtrasGerman, ...messageToolsGerman, ...webhookGerman,
    ...contextGerman,
    ...micGerman,
    ...selectedWinGerman,
    ...callGerman,
    ...conversationGerman,
    ...chatGerman,
    ...streamGerman,
    ...uiPolishGerman,
    ...auditGerman,
    ...roleGerman,
    ...quickWinGerman,
    ...settingsGerman,
    ...securityGerman,
    ...interfaceGerman,
    "menu.connections": "Verbindungen",
    "menu.bookmarks": "Lesezeichen",
    "menu.self": "Selbst",
    "menu.view": "Ansicht",
    "menu.permissions": "Rechte",
    "menu.tools": "Werkzeuge",
    "menu.help": "Hilfe",
    "menu.connect": "Verbinden…",
    "menu.disconnect": "Trennen",
    "menu.quit": "Beenden",
    "menu.settings": "Einstellungen…",
    "menu.compact": "Kompaktmodus",
    "menu.exitCompact": "Kompaktmodus verlassen",
    "menu.zen": "Zen-Modus",
    "menu.setStatus": "Status setzen…",
    "menu.contacts": "Kontakte…",
    "menu.setAvatar": "Avatar setzen…",
    "menu.setServerIcon": "Server-Icon setzen…",
    "menu.auditLog": "Audit-Log…",
    "menu.bans": "Bans…",
    "menu.debugConsole": "Debug-Konsole…",
    "menu.connStats": "Verbindungsstatistik…",
    "menu.exportLogs": "Logs exportieren…",
    "menu.about": "Über noXa",
    "menu.checkUpdates": "Nach Updates suchen…",
    "menu.permManager": "Rechte-Manager…",
    "menu.viewMyPerms": "Meine Rechte anzeigen",
    "menu.whisperLists": "Flüsterlisten…",
    "common.ok": "OK",
    "common.cancel": "Abbrechen",
    "common.close": "Schließen",
    "common.save": "Speichern",
    "common.delete": "Löschen",
    "common.create": "Erstellen",
    "common.apply": "Anwenden",
    "login.optional": "Optionale Angaben",
    "login.serverPlaceholder": "Serveradresse",
    "login.displayPlaceholder": "Kontonamen verwenden",
    "login.accountPasswordPlaceholder": "Für ein registriertes Konto",
    "login.rememberPasswords": "Passwörter für diese Verbindung merken",
    "login.savedPassword": "Gespeichertes Passwort",
    "login.passwordProtection": "Vom Betriebssystem geschützt. Häkchen entfernen, um gespeicherte Passwörter zu löschen.",
    "login.passwordUnavailable": "Geschütztes Speichern von Passwörtern ist auf diesem Gerät nicht verfügbar.",
    "login.passwordLookupFailed": "Gespeicherte Passwörter konnten nicht geprüft werden. Bitte erneut eingeben.",
    "login.passwordForgetFailed": "Gespeicherte Passwörter konnten nicht gelöscht werden. Bitte erneut versuchen.",
    "login.connectionChanged": "Verbindungsdaten wurden geändert. Bitte erneut verbinden.",
    "login.optionalHint": "Anzeigename · Passwörter",
    "login.identity": "Identität automatisch erstellt — lokal gespeichert",
    "login.connecting": "Verbindung wird hergestellt…",
    "login.emptyRecents": "Deine letzten Server erscheinen hier.",
    "login.server": "Server",
    "login.nickname": "Konto-Login / Gastname",
    "login.displayName": "Anzeigename (optional)",
    "login.displayNameHint": "Für alle sichtbar. Dein Konto-Login bleibt unverändert.",
    "login.serverPassword": "Server-Passwort (optional)",
    "login.accountPassword": "Konto-Passwort (optional)",
    "login.connect": "Verbinden",
    "login.recentServers": "Letzte Server",
    "settings.application": "Anwendung",
    "settings.capture": "Aufnahme",
    "settings.playback": "Wiedergabe",
    "settings.hotkeys": "Tastenkürzel",
    "settings.whisper": "Flüstern",
    "settings.downloads": "Downloads",
    "settings.chat": "Chat",
    "settings.security": "Sicherheit",
    "settings.server": "Server",
    "settings.notifications": "Benachrichtigungen",
    "settings.language": "Sprache",
    "settings.searchPlaceholder": "Einstellungen suchen…",
    "chat.connectedAs": "Verbunden als {nick}",
    "status.offline": "offline",
    "status.retry": "Versuch {n}/{max} in {s}s…",
    "notif.title": "Benachrichtigungen",
    "notif.clearAll": "alle löschen",
};

const catalogs = Object.freeze({
    en: Object.freeze(en),
    de: Object.freeze(de),
});

// catalogParity is a pure, immutable diagnostic for unit tests and release
// checks. Language changes stay allocation-free apart from their own setting.
export function catalogParity() {
    const enKeys = new Set(Object.keys(catalogs.en));
    const deKeys = new Set(Object.keys(catalogs.de));
    return Object.freeze({
        missingFromEnglish: Object.freeze([...deKeys].filter((key) => !enKeys.has(key)).sort()),
        missingFromGerman: Object.freeze([...enKeys].filter((key) => !deKeys.has(key)).sort()),
    });
}

let lang = "en";

// setLanguage applies a language setting ("system" | "en" | "de").
export function setLanguage(l) {
    if (!l || l === "system") {
        l = (navigator.language || "en").toLowerCase().startsWith("de") ? "de" : "en";
    }
    lang = catalogs[l] ? l : "en";
}

export function currentLanguage() {
    return lang;
}

// t translates a key, substituting {placeholders} from vars. Missing keys
// warn once in the debug console and fall back to the key itself.
const warned = new Set();

// interpolate replaces every literal placeholder occurrence without treating
// the key as a regular expression. Translation keys may contain punctuation.
export function interpolate(template, vars) {
    let text = String(template);
    for (const [key, value] of Object.entries(vars || {})) {
        text = text.split("{" + key + "}").join(String(value));
    }
    return text;
}

export function t(key, vars) {
    let s = catalogs[lang][key];
    if (s === undefined) {
        s = catalogs.en[key];
        if (s === undefined) {
            if (!warned.has(key)) {
                warned.add(key);
                console.warn("[i18n] missing key:", key);
            }
            s = key;
        } else if (!warned.has(lang + ":" + key)) {
            warned.add(lang + ":" + key);
            console.warn("[i18n] missing translation", lang, key, "— using en");
        }
    }
    return vars ? interpolate(s, vars) : s;
}

// applyStaticLabels re-labels the static index.html surfaces (login card).
export function applyStaticLabels() {
    for (const element of document.querySelectorAll("[data-login-i18n-placeholder]")) {
        element.setAttribute("placeholder", t(element.dataset.loginI18nPlaceholder));
    }
    for (const element of document.querySelectorAll("[data-login-i18n]")) {
        element.textContent = t(element.dataset.loginI18n);
    }
    for (const element of document.querySelectorAll("[data-login-i18n-aria-label]")) {
        element.setAttribute("aria-label", t(element.dataset.loginI18nAriaLabel));
    }
}
