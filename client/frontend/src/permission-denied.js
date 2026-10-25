import { currentLanguage, t } from "./i18n.js";

// (321) Human labels for permission keys. Mirrors internal/authorization/
// capabilities.go; a server-provided capability catalog (snapshot.capabilities)
// takes precedence when it is passed in, so renamed server labels win.
export const PERMISSION_LABELS = Object.freeze({
    administrator: { en: "Administrator", de: "Administrator" },
    view_channel: { en: "View channel", de: "Kanal sehen" },
    read_history: { en: "Read message history", de: "Nachrichtenverlauf lesen" },
    send_messages: { en: "Send messages", de: "Nachrichten senden" },
    mention_everyone: { en: "Mention everyone", de: "Alle erwähnen" },
    manage_messages: { en: "Manage messages", de: "Nachrichten verwalten" },
    bypass_slowmode: { en: "Bypass slowmode", de: "Langsamen Modus umgehen" },
    connect: { en: "Connect to voice", de: "Sprachkanal betreten" },
    speak: { en: "Speak", de: "Sprechen" },
    share_camera: { en: "Share camera", de: "Kamera teilen" },
    share_screen: { en: "Share screen", de: "Bildschirm teilen" },
    whisper: { en: "Whisper", de: "Flüstern" },
    priority_speaker: { en: "Priority speaker", de: "Vorrangiger Sprecher" },
    upload_files: { en: "Upload files", de: "Dateien hochladen" },
    download_files: { en: "Download files", de: "Dateien herunterladen" },
    manage_files: { en: "Manage others' files", de: "Dateien anderer verwalten" },
    move_members: { en: "Move members", de: "Mitglieder verschieben" },
    mute_members: { en: "Mute members", de: "Mitglieder stummschalten" },
    deafen_members: { en: "Deafen members", de: "Mitglieder gehörlos schalten" },
    disconnect_members: { en: "Disconnect members from voice", de: "Mitglieder aus Sprachkanal entfernen" },
    kick_members: { en: "Kick members from server", de: "Mitglieder vom Server entfernen" },
    ban_members: { en: "Ban members", de: "Mitglieder sperren" },
    manage_channels: { en: "Manage channels", de: "Kanäle verwalten" },
    manage_channel_access: { en: "Manage channel access", de: "Kanalzugriff verwalten" },
    invite_others: { en: "Invite others", de: "Andere einladen" },
    create_temporary_channels: { en: "Create temporary channels", de: "Temporäre Kanäle erstellen" },
    bypass_channel_password: { en: "Bypass channel password", de: "Kanalpasswort umgehen" },
    manage_roles: { en: "Manage roles", de: "Rollen verwalten" },
    manage_server: { en: "Manage server", de: "Server verwalten" },
    manage_emoji: { en: "Manage emoji", de: "Emoji verwalten" },
    manage_chat_filters: { en: "Manage chat filters", de: "Chatfilter verwalten" },
    view_audit_log: { en: "View audit log", de: "Prüfprotokoll ansehen" },
    view_connection_info: { en: "View connection statistics and invisible members", de: "Verbindungsstatistik und unsichtbare Mitglieder ansehen" },
    view_remote_addresses: { en: "View remote addresses", de: "Remote-Adressen ansehen" },
    record_channel: { en: "Record channel", de: "Kanal aufnehmen" },
    upload_avatar: { en: "Upload avatar", de: "Avatar hochladen" },
    poke_members: { en: "Poke members", de: "Mitglieder anstupsen" },
});

const KEY_PATTERN = /^[a-z][a-z0-9_]{1,63}$/;

export function permissionLabel(key, { language = currentLanguage(), capabilities = null } = {}) {
    const normalized = String(key || "").trim().toLowerCase();
    if (!KEY_PATTERN.test(normalized)) return "";
    const lang = language === "de" ? "de" : "en";
    const fromServer = Array.isArray(capabilities) ? capabilities.find(c => c?.key === normalized) : null;
    if (fromServer && (fromServer[lang] || fromServer.en)) return fromServer[lang] || fromServer.en;
    const known = PERMISSION_LABELS[normalized];
    if (known) return known[lang];
    // Unknown but well-formed key: readable fallback instead of raw snake_case.
    return normalized.replace(/_/g, " ");
}

// Server texts that are known to denote one specific permission. Messages
// that cover several rights (e.g. the role editor's generic refusal) are not
// listed; callers that know their own action pass a fallback key instead.
const KNOWN_MESSAGES = [
    [/^not a member of this channel\b/, "view_channel"],
    [/^you can only edit your own messages\b/, "manage_messages"],
];

// Extracts the permission key from an error: structured payloads
// ({ permission } / { capability } / { required }), the conventional text
// forms "insufficient permission: <key>", "permission denied: <key>",
// "missing permission <key>", "requires <key>", and known server messages.
export function missingPermission(error, fallbackKey = "") {
    const structured = error && typeof error === "object" ? (error.permission || error.capability || error.required || error.missing_permission) : "";
    if (typeof structured === "string" && KEY_PATTERN.test(structured.trim().toLowerCase())) return structured.trim().toLowerCase();
    const text = String(error?.message || error || "").replace(/^Error: /, "").replace(/^\d+:\s*/, "").trim();
    const lower = text.toLowerCase();
    const match = /(?:insufficient permission|permission denied|missing permission|permission required|requires? (?:the )?permission|requires)[:\s]+["“'„]?([a-z][a-z0-9_]{1,63})["”'“]?(?:\b|$)/.exec(lower);
    if (match && (PERMISSION_LABELS[match[1]] || /_/.test(match[1]))) return match[1];
    for (const [pattern, key] of KNOWN_MESSAGES) if (pattern.test(lower)) return key;
    const normalizedFallback = String(fallbackKey || "").trim().toLowerCase();
    if (normalizedFallback && KEY_PATTERN.test(normalizedFallback) && isPermissionDenial(lower)) return normalizedFallback;
    return "";
}

export function isPermissionDenial(text) {
    const lower = String(text?.message || text || "").toLowerCase();
    return /permission denied|insufficient permission|missing permission|\bforbidden\b|not authori[sz]ed|\bunauthori[sz]ed\b|access denied|you cannot manage|not allowed to|\bdenied\b/.test(lower);
}

// "Aktion nicht möglich: dafür fehlt die Berechtigung „X“." for a known key.
export function deniedActionMessage(key, options = {}) {
    const name = permissionLabel(key, options);
    return name ? t("permission.denied.action", { name }) : t("permission.denied.actionGeneric");
}

// Short form for tooltips on disabled controls whose reason is already known.
export function missingPermissionHint(key, options = {}) {
    const name = permissionLabel(key, options);
    return name ? t("permission.denied.hint", { name }) : "";
}

// Full user-facing explanation for a denied server action, or "" when the
// error is not a permission denial. The raw text is never interpreted as HTML.
export function describeDeniedError(error, fallbackKey = "", options = {}) {
    const key = missingPermission(error, fallbackKey);
    if (key) return deniedActionMessage(key, options);
    return isPermissionDenial(error) ? t("permission.denied.actionGeneric") : "";
}
