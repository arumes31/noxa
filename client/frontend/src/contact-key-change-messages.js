// contact-key-change-messages.js — copy for the "contact's signing key
// changed" confirmation (item 241). Plain language: what happened, the
// harmless reasons, the risky case and what to do about it.
export const contactKeyChangeEnglish = {
    "keyChange.title": "Security key of {name} changed",
    "keyChange.what": "The key that {name} uses to sign encrypted messages on {server} is not the one you trusted before. Until you decide, messages and calls with this contact stay blocked.",
    "keyChange.benign": "Usually this is harmless: {name} set up a new device, reinstalled noXa or reset their keys.",
    "keyChange.risky": "It can also mean that someone sits between you and {name} and is trying to read or change your conversation.",
    "keyChange.todo": "Before trusting the new key, compare the new safety number with {name} over a channel you already trust: in person, by phone or on a video call. Trust it only when you both see the same blocks.",
    "keyChange.oldFingerprint": "Previously trusted safety number",
    "keyChange.newFingerprint": "New safety number",
    "keyChange.legend": "Blocks marked “changed” differ from the previously trusted key.",
    "keyChange.changedBlock": "changed",
    "keyChange.unknownFingerprint": "not available",
    "keyChange.later": "Verify later (keep blocking)",
    "keyChange.laterHelp": "Keeping {name} blocked is the safe choice. You will be asked again the next time you message or call.",
    "keyChange.trust": "I compared it, trust the new key",
    "keyChange.expired": "This key change is no longer waiting for a decision. Message or call the contact again to retry.",
};
export const contactKeyChangeGerman = {
    "keyChange.title": "Sicherheitsschlüssel von {name} hat sich geändert",
    "keyChange.what": "Der Schlüssel, mit dem {name} verschlüsselte Nachrichten auf {server} signiert, ist nicht mehr der, dem du bisher vertraut hast. Bis du entscheidest, bleiben Nachrichten und Anrufe mit diesem Kontakt blockiert.",
    "keyChange.benign": "Meistens ist das harmlos: {name} hat ein neues Gerät eingerichtet, noXa neu installiert oder die Schlüssel zurückgesetzt.",
    "keyChange.risky": "Es kann aber auch bedeuten, dass sich jemand zwischen dich und {name} geschaltet hat und eure Unterhaltung mitlesen oder verändern will.",
    "keyChange.todo": "Vergleiche die neue Sicherheitsnummer mit {name} über einen Weg, dem du bereits vertraust: persönlich, am Telefon oder per Videoanruf. Vertraue dem neuen Schlüssel nur, wenn ihr beide dieselben Blöcke seht.",
    "keyChange.oldFingerprint": "Bisher vertraute Sicherheitsnummer",
    "keyChange.newFingerprint": "Neue Sicherheitsnummer",
    "keyChange.legend": "Mit „geändert“ markierte Blöcke unterscheiden sich vom bisher vertrauten Schlüssel.",
    "keyChange.changedBlock": "geändert",
    "keyChange.unknownFingerprint": "nicht verfügbar",
    "keyChange.later": "Später prüfen (blockiert lassen)",
    "keyChange.laterHelp": "{name} blockiert zu lassen ist die sichere Wahl. Beim nächsten Schreiben oder Anrufen wirst du erneut gefragt.",
    "keyChange.trust": "Ich habe verglichen, neuem Schlüssel vertrauen",
    "keyChange.expired": "Diese Schlüsseländerung wartet nicht mehr auf eine Entscheidung. Schreibe oder rufe den Kontakt erneut an, um es noch einmal zu versuchen.",
};
