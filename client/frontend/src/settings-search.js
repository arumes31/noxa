// settings-search.js — pure matching for the settings dialog search (261/262).
//
// The dialog indexes every rendered row (label, help text, page, section,
// saved-preference key) and this module adds the vocabulary users actually
// type: feature keywords and problem descriptions in German and English
// ("Mikrofon zu leise", "nobody hears me", "Benachrichtigung kommt nicht").
// Matching is case- and diacritic-insensitive, token based, synonym aware
// and scored across label, help, keywords and problem phrases. Results carry
// the page, section and setting key so the dialog can navigate and highlight
// the control (settings-ui.js).
//
// No DOM, no i18n: labels arrive from the caller already translated.

// Keywords and problem phrases per saved preference key. Both languages are
// always indexed, so a German user may type English terms and vice versa.
export const SETTINGS_SEARCH_TERMS = Object.freeze({
    // General
    language: { de: ["Sprache", "Deutsch", "Englisch", "Sprache umstellen", "falsche Sprache"], en: ["language", "German", "English", "wrong language", "switch language"] },
    auto_away_minutes: { de: ["abwesend", "AFK", "automatisch abwesend", "Status wechselt von selbst"], en: ["away", "AFK", "idle", "auto away", "status changes by itself"] },
    auto_away_message: { de: ["Abwesenheitsnachricht", "Statusnachricht"], en: ["away message", "status message"] },
    always_on_top: { de: ["immer im Vordergrund", "Fenster oben halten", "über anderen Fenstern"], en: ["always on top", "keep window above", "window stays in front"] },
    close_to_tray: { de: ["Tray", "Infobereich", "Fenster verschwindet beim Schließen", "schließen beendet nicht", "läuft im Hintergrund weiter"], en: ["tray", "close hides window", "closing does not quit", "keeps running in background"] },
    minimize_to_tray: { de: ["minimieren Tray", "Fenster weg beim Minimieren", "Taskleiste"], en: ["minimize to tray", "window gone when minimized", "taskbar"] },
    updates_auto_install: { de: ["Update", "Aktualisierung installieren", "neue Version automatisch"], en: ["update", "install updates automatically", "new version"] },
    updates_auto_check: { de: ["nach Updates suchen", "Updateprüfung", "Aktualisierung"], en: ["check for updates", "update check", "upgrade"] },
    updates_check_on_reconnect: { de: ["Update beim Verbinden", "Aktualisierung prüfen"], en: ["update on reconnect", "check updates when connecting"] },
    download_folder: { de: ["Downloadordner", "Speicherort", "wo werden Dateien gespeichert", "Dateien landen falsch", "Ordner ändern"], en: ["download folder", "save location", "where are files saved", "change folder", "downloads"] },
    // Audio
    activation_mode: { de: ["Push-to-Talk", "Sprachaktivierung", "Taste zum Sprechen", "Mikrofon dauernd an", "Mikrofon geht nicht an", "Sendemodus"], en: ["push to talk", "PTT", "voice activation", "mic always on", "mic does not activate", "transmit mode"] },
    capture_device_id: { de: ["Mikrofon", "Mikro", "Eingabegerät", "falsches Mikrofon", "niemand hört mich", "Headset wird nicht erkannt", "kein Mikrofon"], en: ["microphone", "mic", "input device", "wrong mic", "nobody hears me", "headset not detected", "no microphone"] },
    vad_threshold: { de: ["Mikrofon zu leise", "Mikrofon zu empfindlich", "niemand hört mich", "Sprache wird abgeschnitten", "Schwelle", "Empfindlichkeit", "Mikrofon schaltet nicht", "sendet ständig"], en: ["mic too quiet", "mic too sensitive", "nobody hears me", "voice cuts out", "threshold", "sensitivity", "mic does not trigger", "transmits constantly"] },
    echo_cancellation: { de: ["Echo", "ich höre mich selbst", "Rückkopplung", "Hall", "andere hören sich selbst"], en: ["echo", "I hear myself", "feedback", "reverb", "others hear themselves"] },
    noise_suppression: { de: ["Rauschen", "Hintergrundgeräusche", "Tastatur hörbar", "Lüfter hörbar", "Störgeräusche"], en: ["noise", "background noise", "keyboard clicks", "fan noise", "hiss", "static"] },
    ptt_release_delay_ms: { de: ["Wort abgeschnitten", "Ende abgeschnitten", "Loslassverzögerung", "letzte Silbe fehlt"], en: ["words cut off", "release delay", "clipped end", "last syllable missing"] },
    playback_device_id: { de: ["Ausgabegerät", "Lautsprecher", "Kopfhörer", "ich höre nichts", "kein Ton", "falsches Ausgabegerät"], en: ["output device", "speakers", "headphones", "I hear nothing", "no sound", "wrong output device"] },
    volume: { de: ["Lautstärke", "andere zu leise", "andere zu laut", "Wiedergabelautstärke", "Ton zu leise"], en: ["volume", "others too quiet", "others too loud", "playback volume", "sound too quiet"] },
    voice_limiter: { de: ["übersteuert", "knackt", "verzerrt", "zu laut", "Limiter", "Begrenzer"], en: ["clipping", "crackles", "distorted", "too loud", "limiter"] },
    gain_normalize: { de: ["zu leise", "zu laut", "Lautstärke schwankt", "Verstärkung", "Normalisierung", "Pegel angleichen"], en: ["too quiet", "too loud", "volume varies", "gain", "normalize", "level matching", "boost"] },
    positional_audio: { de: ["räumlicher Klang", "Richtung hören", "3D Audio", "Stereo Position"], en: ["positional audio", "spatial sound", "3D audio", "stereo position"] },
    // Streams
    camera_device_id: { de: ["Kamera", "Webcam", "kein Bild", "Kamera schwarz", "falsche Kamera"], en: ["camera", "webcam", "no video", "black screen", "wrong camera"] },
    camera_background: { de: ["Hintergrund unscharf", "Weichzeichnen", "Hintergrundeffekt", "virtueller Hintergrund"], en: ["blur background", "background effect", "virtual background"] },
    camera_background_scene: { de: ["Hintergrundszene", "Hintergrundbild"], en: ["background scene", "background image"] },
    camera_background_image: { de: ["eigenes Hintergrundbild", "Bild als Hintergrund"], en: ["custom background image", "own background picture"] },
    camera_blur_strength: { de: ["Unschärfe", "Weichzeichner Stärke"], en: ["blur strength", "blur amount"] },
    camera_mirror_preview: { de: ["gespiegelt", "Spiegelung", "Vorschau seitenverkehrt"], en: ["mirrored", "mirror preview", "flipped"] },
    camera_fps: { de: ["ruckelt", "Bildrate", "Kamera stockt", "FPS"], en: ["stutter", "frame rate", "choppy video", "FPS"] },
    // Overlay
    gaming_overlay: { de: ["Overlay", "Einblendung im Spiel", "wer spricht im Spiel", "Overlay fehlt"], en: ["overlay", "in-game overlay", "who is talking in game", "overlay missing"] },
    gaming_overlay_monitor: { de: ["Overlay Monitor", "falscher Bildschirm"], en: ["overlay monitor", "wrong screen"] },
    gaming_overlay_position: { de: ["Overlay Position", "Overlay verschieben", "Ecke"], en: ["overlay position", "move overlay", "corner"] },
    gaming_overlay_opacity: { de: ["Overlay durchsichtig", "Overlay Deckkraft"], en: ["overlay opacity", "overlay transparency"] },
    gaming_overlay_scale: { de: ["Overlay zu groß", "Overlay zu klein", "Overlay Größe"], en: ["overlay too big", "overlay too small", "overlay size"] },
    gaming_overlay_only_speaking: { de: ["nur Sprechende zeigen"], en: ["only show speakers", "show only who talks"] },
    gaming_overlay_auto_hide: { de: ["Overlay ausblenden", "Overlay verschwindet"], en: ["hide overlay", "overlay disappears"] },
    gaming_overlay_chat_peek: { de: ["Chat im Spiel", "Nachrichten im Overlay"], en: ["chat in game", "messages in overlay"] },
    // Hotkeys
    hotkey_ptt: { de: ["Sprechtaste", "Push-to-Talk Taste", "Hotkey funktioniert nicht", "Tastenkombination", "Shortcut"], en: ["talk key", "push to talk key", "hotkey not working", "keyboard shortcut", "key binding"] },
    hotkey_mute: { de: ["Stummtaste", "Mikrofon per Taste stumm", "Tastenkombination stumm"], en: ["mute key", "mute hotkey", "toggle mic key"] },
    hotkey_deafen: { de: ["Taub schalten", "alles stumm Taste"], en: ["deafen key", "silence everything key"] },
    hotkey_profiles: { de: ["Hotkey Profil", "Tasten pro Server"], en: ["hotkey profile", "keys per server"] },
    // Appearance
    theme: { de: ["Design", "dunkel", "hell", "Farbschema", "Dark Mode", "zu hell"], en: ["theme", "dark mode", "light mode", "colour scheme", "too bright"] },
    accent_color: { de: ["Akzentfarbe", "Farbe ändern", "Highlightfarbe"], en: ["accent colour", "accent color", "change colour", "highlight colour"] },
    ui_font: { de: ["Schrift", "Schriftart", "andere Schrift"], en: ["font", "typeface", "different font"] },
    ui_font_size: { de: ["Schrift zu klein", "Schrift zu groß", "zu groß", "zu klein", "Schriftgröße", "Zoom", "Text größer", "schlecht lesbar"], en: ["text too small", "font too big", "too big", "too small", "font size", "zoom", "larger text", "hard to read"] },
    compact_mode: { de: ["kompakt", "dichter", "zu viel Platz", "weniger Abstand"], en: ["compact", "dense", "too much space", "less spacing"] },
    reduce_motion: { de: ["Animationen aus", "flimmert", "Bewegung reduzieren", "weniger Animation"], en: ["disable animations", "flicker", "reduce motion", "less animation"] },
    window_opacity: { de: ["durchsichtig", "Transparenz", "Fenster Deckkraft"], en: ["transparent", "opacity", "window transparency"] },
    user_css: { de: ["eigenes CSS", "Aussehen anpassen", "Stylesheet"], en: ["custom CSS", "custom styles", "stylesheet"] },
    // Privacy
    identity_key_protection: { de: ["Schlüsselschutz", "Identität", "Passwort für Schlüssel"], en: ["key protection", "identity", "key password"] },
    allow_plaintext: { de: ["unverschlüsselt", "Klartext", "Verschlüsselung", "unsichere Verbindung"], en: ["unencrypted", "plaintext", "encryption", "insecure connection"] },
    hide_chat_activity: { de: ["tippt gerade", "Lesebestätigung", "gelesen Anzeige", "Aktivität verbergen"], en: ["typing indicator", "read receipts", "seen status", "hide activity"] },
    log_channel_chat: { de: ["Chatverlauf speichern", "Protokoll", "Chat Log", "Verlauf fehlt"], en: ["save chat history", "chat log", "history missing"] },
    log_private_chat: { de: ["private Nachrichten speichern", "DM Verlauf"], en: ["save private messages", "DM history"] },
    log_server_chat: { de: ["Serverchat speichern"], en: ["save server chat"] },
    // Chat
    link_previews: { de: ["Linkvorschau", "YouTube", "Vorschaubild", "Vorschau blockiert", "keine Vorschau"], en: ["link preview", "YouTube", "thumbnail", "preview blocked", "no preview"] },
    chat_timestamps: { de: ["Uhrzeit anzeigen", "Zeitstempel", "wann geschrieben"], en: ["show time", "timestamps", "when was it sent"] },
    chat_density: { de: ["Nachrichten enger", "Zeilenabstand Chat"], en: ["message density", "tighter messages"] },
    chat_layout: { de: ["Chat Layout", "Nachrichten Anordnung"], en: ["chat layout", "message arrangement"] },
    chat_font_size: { de: ["Chat Schrift", "Nachrichten zu klein", "Chat zu groß"], en: ["chat text size", "messages too small", "chat too big"] },
    sys_join_leave: { de: ["Systemnachrichten", "beigetreten Zeile", "Beitritt anzeigen"], en: ["system messages", "join lines", "show joins"] },
    sys_kick: { de: ["Kick Nachricht", "Rauswurf anzeigen"], en: ["kick message", "show kicks"] },
    chat_max_lines: { de: ["Verlauf Länge", "alte Nachrichten verschwinden"], en: ["history length", "old messages disappear"] },
    keywords: { de: ["Stichwort", "Erwähnung", "Hervorhebung", "Schlüsselwort", "Highlight"], en: ["keyword", "mention", "highlight", "watch word"] },
    chat_undo_seconds: { de: ["Nachricht zurückholen", "Rückgängig senden", "Senden abbrechen"], en: ["undo send", "take back message", "cancel sending"] },
    // Notifications
    play_sounds: { de: ["Ton aus", "Töne stumm", "Sounds", "keine Töne", "Klänge abschalten"], en: ["sounds off", "mute sounds", "sound effects", "no sounds", "turn off sounds"] },
    sound_volume: { de: ["Töne zu laut", "Töne zu leise", "Soundlautstärke"], en: ["sounds too loud", "sounds too quiet", "sound volume"] },
    effects_enabled: { de: ["Soundeffekte", "Effekte"], en: ["sound effects", "effects"] },
    duck_effects_while_speaking: { de: ["Töne leiser beim Sprechen", "Ducking"], en: ["duck sounds", "sounds quieter while talking", "ducking"] },
    spoken_messages: { de: ["Sprachausgabe", "Vorlesen", "Stimme liest vor", "Text zu Sprache"], en: ["text to speech", "read aloud", "spoken messages", "voice reads"] },
    speech_volume: { de: ["Sprachausgabe Lautstärke", "Vorlesen zu laut"], en: ["speech volume", "read aloud too loud"] },
    speech_language: { de: ["Sprachausgabe Sprache"], en: ["speech language"] },
    event_sounds: { de: ["Ereignistöne", "Ton bei Beitritt", "Ton pro Ereignis"], en: ["event sounds", "join sound", "sound per event"] },
    chat_notification_level: { de: ["Benachrichtigung kommt nicht", "keine Benachrichtigung", "zu viele Benachrichtigungen", "Benachrichtigungen", "Hinweis bei Nachricht"], en: ["notification not showing", "no notifications", "too many notifications", "notifications", "alert on message"] },
    notify_matrix: { de: ["Benachrichtigung pro Ereignis", "Benachrichtigungsmatrix", "Popup", "Desktop Benachrichtigung"], en: ["notification per event", "notification matrix", "popup", "desktop notification"] },
    dnd_enabled: { de: ["Nicht stören", "Ruhezeiten", "nachts keine Benachrichtigungen", "stumm schalten"], en: ["do not disturb", "quiet hours", "no notifications at night", "silence"] },
    dnd_from: { de: ["Ruhezeit Beginn"], en: ["quiet hours start"] },
    dnd_to: { de: ["Ruhezeit Ende"], en: ["quiet hours end"] },
    notify_join_leave: { de: ["Hinweis bei Beitritt", "jemand kommt", "jemand geht"], en: ["someone joins", "someone leaves", "join notification"] },
    notify_connection: { de: ["Verbindungshinweis", "Hinweis bei Verbindung"], en: ["connection notification", "connected alert"] },
    warn_muted_talking: { de: ["stumm aber ich rede", "Warnung stumm", "rede ins stumme Mikro"], en: ["talking while muted", "muted warning", "speaking into muted mic"] },
    warn_empty_channel: { de: ["leerer Kanal Warnung", "allein im Kanal"], en: ["empty channel warning", "alone in channel"] },
    // Whisper
    whisper_active: { de: ["Flüstern", "nur bestimmte hören mich", "Flüsterliste"], en: ["whisper", "only some hear me", "whisper list"] },
    whisper_clients: { de: ["Flüstern an Personen"], en: ["whisper to people"] },
    whisper_channels: { de: ["Flüstern an Kanäle"], en: ["whisper to channels"] },
    whisper_reply_hotkey: { de: ["Flüstern antworten Taste"], en: ["whisper reply key"] },
    // Connection
    reconnect_on_loss: { de: ["Verbindung bricht ab", "verbindet nicht neu", "Reconnect", "Verbindungsabbruch", "automatisch verbinden"], en: ["connection drops", "does not reconnect", "reconnect", "disconnects", "auto reconnect"] },
});

// Synonym groups: any token in a group matches any other (after
// normalisation). Keep entries normalised (lowercase, ae/oe/ue/ss spelling).
const SYNONYM_GROUPS = [
    ["mikrofon", "mikro", "mic", "microphone", "headset", "eingabe", "input"],
    ["lautstaerke", "volume", "pegel", "level"],
    ["leise", "quiet", "low", "faint"],
    ["laut", "loud"],
    ["ton", "sound", "sounds", "audio", "klang", "klaenge", "toene"],
    ["benachrichtigung", "benachrichtigungen", "notification", "notifications", "alert", "alerts", "hinweis", "popup"],
    ["schrift", "schriftart", "schriftgroesse", "font", "text", "typeface"],
    ["gross", "groesse", "grosse", "groesser", "big", "bigger", "large", "larger", "size"],
    ["klein", "kleiner", "small", "smaller", "tiny"],
    ["kamera", "camera", "webcam", "video"],
    ["echo", "hall", "feedback", "rueckkopplung"],
    ["rauschen", "noise", "hiss", "static", "geraeusche", "stoergeraeusche"],
    ["taste", "tasten", "key", "keys", "hotkey", "hotkeys", "shortcut", "tastenkombination", "binding"],
    ["verbindung", "connection", "reconnect", "verbinden", "disconnect", "disconnects", "verbindungsabbruch"],
    ["dunkel", "dark"],
    ["hell", "light", "bright"],
    ["aus", "off", "deaktivieren", "disable", "ausschalten", "stumm", "mute", "muted"],
    ["update", "updates", "aktualisierung", "aktualisieren", "upgrade"],
    ["sprache", "language"],
    ["vorschau", "preview", "thumbnail", "vorschaubild"],
    ["hoeren", "hoert", "hear", "hears", "hearing"],
    ["niemand", "nobody", "keiner", "noone"],
    ["abgeschnitten", "cut", "clipped", "cuts"],
    ["ordner", "folder", "directory", "verzeichnis"],
    ["fenster", "window"],
    ["hintergrund", "background"],
    ["ruckelt", "stutter", "choppy", "laggy", "stockt"],
    ["flimmert", "flicker", "animation", "animationen", "animations", "motion", "bewegung"],
    ["durchsichtig", "transparent", "transparenz", "opacity", "deckkraft"],
    ["farbe", "colour", "color", "farben"],
    ["uhrzeit", "zeit", "time", "timestamp", "zeitstempel", "timestamps"],
    ["speichern", "save", "saved", "log", "protokoll", "verlauf", "history"],
    ["stoeren", "disturb", "dnd", "ruhezeit", "ruhezeiten", "quiet"],
    ["fluestern", "whisper"],
    ["kommt", "arrives", "showing", "erscheint", "appears"],
    ["nicht", "not", "kein", "keine", "no", "nothing", "nichts", "doesnt", "dont"],
];

const SYNONYMS = new Map();
for (const group of SYNONYM_GROUPS) {
    for (const word of group) SYNONYMS.set(word, group);
}

// Function words that carry no meaning for matching. "nicht"/"no" stay
// meaningful (they describe problems), so they are synonyms, not stopwords.
const STOPWORDS = new Set(["zu", "ich", "mich", "mir", "mein", "meine", "der", "die", "das", "ist", "und", "im", "in", "am", "beim", "bei", "von", "fuer", "mit", "the", "a", "an", "is", "are", "my", "me", "i", "to", "too", "of", "and", "it", "its", "on", "for", "with", "when", "wenn", "wird", "werden", "sich"]);

const FIELD_WEIGHTS = Object.freeze({ label: 5, problems: 4, keywords: 3, help: 2, section: 1, page: 1 });

// normalizeSearchText lowercases, maps German umlauts to their ae/oe/ue/ss
// spelling and strips remaining diacritics, so "Größe", "groesse" and
// "GROSSE" index identically to a query typed either way.
export function normalizeSearchText(value) {
    return String(value ?? "")
        .toLowerCase()
        .replace(/ä/g, "ae").replace(/ö/g, "oe").replace(/ü/g, "ue").replace(/ß/g, "ss")
        .normalize("NFD").replace(/[̀-ͯ]/g, "")
        .replace(/[^\p{L}\p{N}]+/gu, " ")
        .trim();
}

export function tokenizeSearchText(value) {
    return normalizeSearchText(value).split(" ").filter(token => token && !STOPWORDS.has(token));
}

function expand(token) {
    const group = SYNONYMS.get(token);
    return group ? new Set(group) : new Set([token]);
}

function termsFor(key, terms) {
    const entry = key && terms ? terms[key] : null;
    if (!entry) return { keywords: [], problems: [] };
    const all = [...(entry.de || []), ...(entry.en || [])];
    // Multi-word entries describe problems; single words are plain keywords.
    return {
        problems: all.filter(phrase => /\s/.test(phrase.trim())),
        keywords: all.filter(phrase => !/\s/.test(phrase.trim())),
    };
}

// buildSettingsSearchIndex pre-normalises dialog entries. Each entry is
// { page, pageLabel, section, key, label, help }; keys without vocabulary
// still index by label/help. Returns a new array; input is not mutated.
export function buildSettingsSearchIndex(entries, { terms = SETTINGS_SEARCH_TERMS } = {}) {
    const index = [];
    for (const entry of entries || []) {
        if (!entry || typeof entry.label !== "string" || !entry.label.trim()) continue;
        const vocabulary = termsFor(entry.key, terms);
        const fields = {
            label: [normalizeSearchText(entry.label)],
            help: entry.help ? [normalizeSearchText(entry.help)] : [],
            keywords: vocabulary.keywords.map(normalizeSearchText),
            problems: vocabulary.problems.map(normalizeSearchText),
            section: entry.sectionLabel ? [normalizeSearchText(entry.sectionLabel)] : [],
            page: entry.pageLabel ? [normalizeSearchText(entry.pageLabel)] : [],
        };
        const tokens = {};
        for (const [name, values] of Object.entries(fields)) {
            tokens[name] = new Set(values.flatMap(value => value.split(" ")).filter(Boolean));
        }
        index.push({
            page: entry.page, pageLabel: entry.pageLabel || "", section: entry.section || "", key: entry.key || "",
            label: entry.label, help: entry.help || "", phrases: [...vocabulary.problems, ...vocabulary.keywords],
            fields, tokens,
        });
    }
    return index;
}

function tokenScore(variants, entry) {
    let best = 0;
    for (const [field, weight] of Object.entries(FIELD_WEIGHTS)) {
        const words = entry.tokens[field];
        if (!words?.size) continue;
        for (const variant of variants) {
            if (words.has(variant)) { best = Math.max(best, weight); continue; }
            if (variant.length >= 3) {
                for (const word of words) {
                    if (word.startsWith(variant) || (variant.length >= 5 && variant.startsWith(word) && word.length >= 4)) {
                        best = Math.max(best, weight * 0.6);
                        break;
                    }
                }
            }
        }
    }
    return best;
}

// searchSettings scores every index entry against the query. A result lists
// { page, section, key, label, score, phrase } where phrase is the keyword
// or problem description that matched best (for display). Entries must
// match more than half of the meaningful query tokens; matching all of them
// earns a bonus, as does containing the whole query as a phrase.
export function searchSettings(index, query, { limit = 40 } = {}) {
    const normalized = normalizeSearchText(query);
    if (!normalized) return [];
    // Tier 1: the query appears literally in a setting label. These are the
    // precise hits a user typing a known name expects ("Overlay opacity" is
    // one setting, not every row mentioning the overlay). The problem-phrase
    // search below only answers when no label contains the query.
    const literal = [];
    for (const entry of index || []) {
        const labels = entry.fields?.label || [];
        const position = labels.findIndex(text => text && text.includes(normalized));
        if (position < 0) continue;
        const text = labels[position];
        const score = text === normalized ? 100 : 10;
        literal.push({ page: entry.page, pageLabel: entry.pageLabel, section: entry.section, key: entry.key, label: entry.label, score, phrase: "" });
    }
    if (literal.length) {
        // Broad label queries preserve the familiar settings-page order.
        literal.sort((a, b) => b.score - a.score);
        return literal.slice(0, limit);
    }
    const needles = tokenizeSearchText(query);
    // A query made only of function words ("zu", "the") would match every
    // problem phrase by substring; it finds nothing until a real term follows.
    if (!needles.length) return [];
    // Every real term has to hit somewhere: a query that shares only one of
    // four words with a setting is noise, not a match ("nothing matches this value").
    const required = needles.length;
    const results = [];
    for (const entry of index || []) {
        let score = 0;
        let matched = 0;
        for (const token of needles) {
            const value = tokenScore(expand(token), entry);
            if (value > 0) { matched++; score += value; }
        }
        let phrase = "";
        let wholePhrase = false;
        for (const field of ["label", "help", "problems", "keywords"]) {
            for (const [position, text] of entry.fields[field].entries()) {
                if (text && (text.includes(normalized) || (normalized.length >= 6 && normalized.includes(text) && text.length >= 4))) {
                    score += FIELD_WEIGHTS[field] * 2;
                    wholePhrase = true;
                    if (!phrase && field !== "label" && field !== "help") {
                        phrase = entry.phrases[field === "problems" ? position : entry.fields.problems.length + position] || "";
                    }
                }
            }
        }
        if (matched < required && !wholePhrase) continue;
        if (matched === needles.length && needles.length > 1) score *= 1.5;
        if (!phrase) phrase = bestPhrase(entry, needles);
        results.push({ page: entry.page, pageLabel: entry.pageLabel, section: entry.section, key: entry.key, label: entry.label, score: Math.round(score * 100) / 100, phrase });
    }
    results.sort((a, b) => b.score - a.score || a.label.localeCompare(b.label));
    return results.slice(0, limit);
}

// bestPhrase picks the vocabulary entry sharing the most tokens with the
// query so the result row can show why it matched ("Mikrofon zu leise").
function bestPhrase(entry, needles) {
    const wanted = new Set(needles.flatMap(token => [...expand(token)]));
    let best = "";
    let bestHits = 0;
    const normalizedPhrases = [...entry.fields.problems, ...entry.fields.keywords];
    normalizedPhrases.forEach((text, position) => {
        const hits = text.split(" ").filter(word => wanted.has(word)).length;
        if (hits > bestHits) { bestHits = hits; best = entry.phrases[position] || ""; }
    });
    return normalizeSearchText(best) === normalizeSearchText(entry.label) ? "" : best;
}
