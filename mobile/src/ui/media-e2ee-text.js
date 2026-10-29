// media-e2ee-text.js — one localized explanation for every encrypted-media
// state and failure (Task 2): the voice status line, join failures and
// runtime alerts use the same reason codes (core/media-e2ee/media-session.js).
import { t, addStrings } from "../i18n.js";
import { mediaErrorReason } from "../core/media-e2ee/media-session.js";

addStrings({
    en: {
        "e2ee.media.keying": "Setting up end-to-end encryption…",
        "e2ee.media.encrypted": "End-to-end encrypted · keys held by {count} device(s): {names}",
        "e2ee.media.mismatch": "Warning: {names} sees different channel members than you. Someone may be receiving keys you cannot see.",
        "e2ee.reason.invisible": "Encrypted voice is unavailable while you are invisible. Become visible to join voice.",
        "e2ee.reason.channel_full": "This encrypted voice channel is full (at most 64 participants).",
        "e2ee.media.tileAccount": "Encryption keys for this participant came from the verified account {account}. Frames prove channel membership, not which member sent them.",
        "e2ee.media.waiting": "{count} device(s) are still receiving the new keys and cannot hear or see you yet",
        "e2ee.media.tileKeys": "keys from {account} · not proof of who is speaking",
        "e2ee.media.alone": "End-to-end encrypted · nobody else is here yet",
        "e2ee.media.partial": "End-to-end encrypted · {failed} device(s) could not receive the keys and cannot hear or see you",
        "e2ee.media.limits": "The server still sees who is in the channel and when and how much data is sent. Participants can record what they receive. Frames prove channel membership, not which member sent them: with the server's help, any participant could forge another participant's audio or video.",
        "e2ee.reason.server_outdated": "This server does not support end-to-end encrypted voice yet. The server needs an update.",
        "e2ee.reason.client_outdated": "Voice on this server is end-to-end encrypted. Update noXa to join.",
        "e2ee.reason.keyless_guest": "Voice is end-to-end encrypted. Guest sessions without a key identity cannot join voice.",
        "e2ee.reason.no_device": "This session has no secure device yet, so encryption keys cannot be exchanged.",
        "e2ee.reason.secure_unavailable": "Secure messaging is not active on this device, so encryption keys cannot be exchanged.",
        "e2ee.reason.unsupported_runtime": "This browser cannot encrypt voice and video. Use a current Chrome, Edge, Firefox or Safari.",
        "e2ee.reason.worker": "The encryption worker could not start. Voice stays off.",
        "e2ee.reason.unavailable": "End-to-end encrypted voice is unavailable on this server right now.",
        "e2ee.reason.codec": "This video format cannot be end-to-end encrypted yet.",
        "e2ee.reason.recording": "Server recording is off because voice is end-to-end encrypted. Local recording still works.",
        "e2ee.reason.whisper_unavailable": "Whisper is unavailable while voice is end-to-end encrypted.",
        "e2ee.reason.preview": "Stream previews are made on your device while media is end-to-end encrypted.",
        "e2ee.reason.closed": "The encrypted voice session ended.",
    },
    de: {
        "e2ee.media.keying": "Ende-zu-Ende-Verschlüsselung wird eingerichtet…",
        "e2ee.media.encrypted": "Ende-zu-Ende-verschlüsselt · Schlüssel bei {count} Gerät(en): {names}",
        "e2ee.media.mismatch": "Warnung: {names} sieht andere Kanalmitglieder als du. Möglicherweise erhält jemand Schlüssel, den du nicht siehst.",
        "e2ee.reason.invisible": "Verschlüsselte Sprache ist nicht verfügbar, solange du unsichtbar bist. Werde sichtbar, um beizutreten.",
        "e2ee.reason.channel_full": "Dieser verschlüsselte Sprachkanal ist voll (höchstens 64 Teilnehmende).",
        "e2ee.media.tileAccount": "Die Schlüssel dieser Person stammen vom verifizierten Konto {account}. Frames belegen die Kanalmitgliedschaft, nicht welches Mitglied sie sendete.",
        "e2ee.media.waiting": "{count} Gerät(e) erhalten die neuen Schlüssel noch und hören/sehen dich noch nicht",
        "e2ee.media.tileKeys": "Schlüssel von {account} · kein Beweis, wer spricht",
        "e2ee.media.alone": "Ende-zu-Ende-verschlüsselt · noch niemand sonst hier",
        "e2ee.media.partial": "Ende-zu-Ende-verschlüsselt · {failed} Gerät(e) konnten die Schlüssel nicht erhalten und hören/sehen dich nicht",
        "e2ee.media.limits": "Der Server sieht weiterhin, wer im Kanal ist und wann wie viele Daten fließen. Teilnehmende können Empfangenes aufzeichnen. Frames belegen die Kanalmitgliedschaft, nicht welches Mitglied sie sendete: mit Hilfe des Servers könnte jede teilnehmende Person Ton oder Video einer anderen fälschen.",
        "e2ee.reason.server_outdated": "Dieser Server unterstützt noch keine Ende-zu-Ende-verschlüsselte Sprache. Der Server braucht ein Update.",
        "e2ee.reason.client_outdated": "Sprache ist auf diesem Server Ende-zu-Ende-verschlüsselt. Aktualisiere noXa, um beizutreten.",
        "e2ee.reason.keyless_guest": "Sprache ist Ende-zu-Ende-verschlüsselt. Gastsitzungen ohne Schlüsselidentität können nicht beitreten.",
        "e2ee.reason.no_device": "Diese Sitzung hat noch kein sicheres Gerät; Schlüssel können nicht ausgetauscht werden.",
        "e2ee.reason.secure_unavailable": "Sichere Nachrichten sind auf diesem Gerät nicht aktiv; Schlüssel können nicht ausgetauscht werden.",
        "e2ee.reason.unsupported_runtime": "Dieser Browser kann Sprache und Video nicht verschlüsseln. Nutze einen aktuellen Chrome, Edge, Firefox oder Safari.",
        "e2ee.reason.worker": "Der Verschlüsselungs-Worker konnte nicht starten. Sprache bleibt aus.",
        "e2ee.reason.unavailable": "Ende-zu-Ende-verschlüsselte Sprache ist auf diesem Server gerade nicht verfügbar.",
        "e2ee.reason.codec": "Dieses Videoformat kann noch nicht Ende-zu-Ende-verschlüsselt werden.",
        "e2ee.reason.recording": "Serveraufnahme ist aus, weil Sprache Ende-zu-Ende-verschlüsselt ist. Lokale Aufnahme funktioniert weiter.",
        "e2ee.reason.whisper_unavailable": "Flüstern ist nicht verfügbar, solange Sprache Ende-zu-Ende-verschlüsselt ist.",
        "e2ee.reason.preview": "Stream-Vorschauen entstehen auf deinem Gerät, solange Medien Ende-zu-Ende-verschlüsselt sind.",
        "e2ee.reason.closed": "Die verschlüsselte Sprachsitzung wurde beendet.",
    },
});

/** Localized text for an encrypted-media failure, or the error's own message. */
export function mediaErrorText(error) {
    const reason = mediaErrorReason(error);
    return reason ? t(`e2ee.reason.${reason}`) : String(error?.message || error || "");
}

/** An account in the app's fingerprint form: 4-character blocks, the first
 * six (24 characters) on a tile, the full account in the explanation. */
export function accountBlocks(account, blocks = 6) {
    const groups = String(account || "").replace(/\s+/g, "").match(/.{1,4}/g) || [];
    return groups.slice(0, blocks).join(" ") + (groups.length > blocks ? " …" : "");
}

/** Visible label (key source, with its caveat) and full explanation of a
 * tile's verified key account (null when none). Review 2b P3-3. */
export function tileAccount(account) {
    if (!account) return null;
    return { label: t("e2ee.media.tileKeys", { account: accountBlocks(account) }), title: t("e2ee.media.tileAccount", { account: accountBlocks(account, Infinity) }) };
}

/** The voice panel's encryption status line ("" when voice is off). */
export function mediaStatusText(status) {
    switch (status?.state) {
        case "keying": return t("e2ee.media.keying");
        case "encrypted": return withWarning(status, !status.recipients ? t("e2ee.media.alone") : status.keyHolders?.length ? t("e2ee.media.encrypted", { count: status.keyHolders.length, names: holderNames(status) }) : t("e2ee.media.keying"));
        case "partial": return withWarning(status, t("e2ee.media.partial", { failed: status.failed?.length || 0 }));
        default: return "";
    }
}

// Who holds my current keys: every device, by nickname with the verified
// account (never truncated: the list is the defence against hidden members).
function holderNames(status) {
    return (status.keyHolders || []).map(h => (h.nickname && h.nickname !== h.principal ? `${h.nickname} (${h.principal})` : h.principal)).join(", ");
}

function withWarning(status, text) {
    if (status.waiting) text = `${text} · ${t("e2ee.media.waiting", { count: status.waiting })}`;
    const mismatch = status.membershipMismatch || [];
    return mismatch.length ? `${text} · ${t("e2ee.media.mismatch", { names: mismatch.map(m => m.principal).join(", ") })}` : text;
}
