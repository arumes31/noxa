// Encrypted channel media (Task 2) strings; the PWA carries the same texts
// (mobile/src/ui/media-e2ee-text.js).
export const channelMediaEnglish = {

    "e2ee.media.keying": "Setting up end-to-end encryption…",
    "e2ee.media.encrypted": "End-to-end encrypted · keys delivered to {count} other device(s)",
    "e2ee.media.alone": "End-to-end encrypted · nobody else is here yet",
    "e2ee.media.partial": "End-to-end encrypted · {failed} device(s) could not receive the keys and cannot hear or see you",
    "e2ee.media.limits": "The server still sees who is in the channel and when and how much data is sent. Participants can record what they receive.",
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
    "e2ee.media.voiceOff": "Voice stays off: {reason}",
    "e2ee.media.recordingLocal": "Local recordings contain the decrypted media you receive; the recording indicator stays visible to others.",
};

export const channelMediaGerman = {

    "e2ee.media.keying": "Ende-zu-Ende-Verschlüsselung wird eingerichtet…",
    "e2ee.media.encrypted": "Ende-zu-Ende-verschlüsselt · Schlüssel an {count} weitere(s) Gerät(e) übermittelt",
    "e2ee.media.alone": "Ende-zu-Ende-verschlüsselt · noch niemand sonst hier",
    "e2ee.media.partial": "Ende-zu-Ende-verschlüsselt · {failed} Gerät(e) konnten die Schlüssel nicht erhalten und hören/sehen dich nicht",
    "e2ee.media.limits": "Der Server sieht weiterhin, wer im Kanal ist und wann wie viele Daten fließen. Teilnehmende können Empfangenes aufzeichnen.",
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
    "e2ee.media.voiceOff": "Sprache bleibt aus: {reason}",
    "e2ee.media.recordingLocal": "Lokale Aufnahmen enthalten die entschlüsselten Medien, die du empfängst; die Aufnahmeanzeige bleibt für andere sichtbar.",
};
