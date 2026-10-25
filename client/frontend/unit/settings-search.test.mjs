import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { readFileSync } from "node:fs";
import { SETTINGS_CATEGORIES, pageForSetting } from "../src/settings-catalog.js";
import {
    SETTINGS_SEARCH_TERMS, buildSettingsSearchIndex, normalizeSearchText, searchSettings, tokenizeSearchText,
} from "../src/settings-search.js";

// Labels as the dialog renders them (English UI); the vocabulary adds the
// German and English problem phrases on top.
const LABELS = {
    language: "Language", auto_away_minutes: "Auto-away after", close_to_tray: "Close to tray", download_folder: "Download folder",
    activation_mode: "Activation", capture_device_id: "Microphone", vad_threshold: "Voice activation threshold",
    echo_cancellation: "Echo cancellation", noise_suppression: "Noise suppression", ptt_release_delay_ms: "Release delay",
    playback_device_id: "Output device", volume: "Output volume", voice_limiter: "Limiter", gain_normalize: "Normalize gain",
    camera_device_id: "Camera", camera_fps: "Frame rate", gaming_overlay: "Enable overlay", hotkey_ptt: "Push-to-talk",
    hotkey_mute: "Toggle mute", theme: "Theme", ui_font: "Interface font", ui_font_size: "Interface font size",
    reduce_motion: "Reduce motion", link_previews: "Link and YouTube previews", chat_timestamps: "Timestamps",
    keywords: "Keyword highlights", play_sounds: "Play sounds", sound_volume: "Sound volume",
    chat_notification_level: "Notify me about", notify_matrix: "Notification matrix", dnd_enabled: "Do not disturb",
    spoken_messages: "Spoken messages", warn_muted_talking: "Warn when talking while muted", whisper_active: "Whisper",
    reconnect_on_loss: "Reconnect automatically", updates_auto_install: "Install updates automatically",
};

const entries = Object.entries(LABELS).map(([key, label]) => ({
    key, label, page: pageForSetting(key), pageLabel: pageForSetting(key), help: key === "vad_threshold" ? "How loud you must be before transmitting." : "",
}));
entries.push({ key: "", label: "Test microphone", page: "audio", pageLabel: "audio" });
const index = buildSettingsSearchIndex(entries);

const keysOf = results => results.map(result => result.key);

describe("settings search normalisation", () => {
    it("is case and diacritic insensitive and keeps ae/oe/ue/ss spelling stable", () => {
        assert.equal(normalizeSearchText("  Schrift GRÖSSE  "), "schrift groesse");
        assert.equal(normalizeSearchText("Lautstärke – zu leise!"), "lautstaerke zu leise");
        assert.equal(normalizeSearchText("Café"), "cafe");
        assert.deepEqual(tokenizeSearchText("Ich höre mich selbst"), ["hoere", "selbst"]);
        assert.deepEqual(tokenizeSearchText(""), []);
        assert.deepEqual(tokenizeSearchText(null), []);
    });

    it("indexes only entries with a label and never mutates the input", () => {
        const input = [{ key: "volume", label: "Output volume", page: "audio" }, { key: "x", label: "", page: "audio" }, null];
        const built = buildSettingsSearchIndex(input);
        assert.equal(built.length, 1);
        assert.equal(built[0].key, "volume");
        assert.ok(built[0].tokens.problems.has("leise"));
        assert.deepEqual(input[0], { key: "volume", label: "Output volume", page: "audio" });
    });

    it("keeps vocabulary keys inside the settings catalogue with both languages", () => {
        const known = new Set(SETTINGS_CATEGORIES.flatMap(category => category.keys));
        for (const [key, terms] of Object.entries(SETTINGS_SEARCH_TERMS)) {
            assert.ok(known.has(key), `${key} is not a settings key`);
            assert.ok(terms.de?.length && terms.en?.length, `${key} needs de and en terms`);
        }
    });
});

describe("settings search problem descriptions", () => {
    const cases = [
        ["Mikrofon zu leise", "vad_threshold"],
        ["mic too quiet", "vad_threshold"],
        ["niemand hört mich", "vad_threshold", "capture_device_id"],
        ["nobody hears me", "vad_threshold", "capture_device_id"],
        ["Echo", "echo_cancellation"],
        ["ich höre mich selbst", "echo_cancellation"],
        ["Benachrichtigung kommt nicht", "chat_notification_level"],
        ["notification not showing", "chat_notification_level"],
        ["Schrift zu groß", "ui_font_size"],
        ["text too small", "ui_font_size"],
        ["Ton aus", "play_sounds"],
        ["sounds off", "play_sounds"],
        ["Hintergrundgeräusche", "noise_suppression"],
        ["keyboard clicks", "noise_suppression"],
        ["Verbindung bricht ab", "reconnect_on_loss"],
        ["connection drops", "reconnect_on_loss"],
        ["Fenster verschwindet beim Schließen", "close_to_tray"],
        ["kamera schwarz", "camera_device_id"],
        ["ruckelt", "camera_fps"],
        ["hotkey funktioniert nicht", "hotkey_ptt"],
        ["nicht stören", "dnd_enabled"],
        ["Vorschau blockiert", "link_previews"],
        ["wo werden dateien gespeichert", "download_folder"],
        ["dark mode", "theme"],
        ["stumm aber ich rede", "warn_muted_talking"],
    ];
    for (const [query, ...expected] of cases) {
        it(`finds ${expected[0]} for "${query}"`, () => {
            const results = searchSettings(index, query);
            assert.ok(results.length > 0, "no results");
            const top = keysOf(results.slice(0, 3));
            assert.ok(expected.some(key => top.includes(key)), `top results ${JSON.stringify(top)} for ${query}`);
            assert.equal(results[0].page, pageForSetting(results[0].key), "result carries the page for navigation");
        });
    }

    it("ranks an exact label above vocabulary-only matches and reports the matched phrase", () => {
        const results = searchSettings(index, "Microphone");
        assert.equal(results[0].key, "capture_device_id");
        const problem = searchSettings(index, "Mikrofon zu leise")[0];
        assert.equal(problem.phrase, "Mikrofon zu leise");
        assert.equal(problem.key, "vad_threshold");
    });

    it("matches synonyms across languages and ignores stopwords", () => {
        assert.ok(keysOf(searchSettings(index, "mikrofon")).includes("capture_device_id"));
        assert.ok(keysOf(searchSettings(index, "lautstaerke")).includes("volume"));
        assert.ok(keysOf(searchSettings(index, "zu")).length === 0, "stopword-only queries find nothing");
        assert.deepEqual(searchSettings(index, "   "), []);
        assert.deepEqual(searchSettings(index, "xyzzy quux"), []);
    });

    it("includes unkeyed rows by label and honours the limit", () => {
        const results = searchSettings(index, "test microphone");
        assert.equal(results[0].label, "Test microphone");
        assert.equal(results[0].key, "");
        assert.equal(searchSettings(index, "sound", { limit: 2 }).length, 2);
    });
});

describe("settings dialog wiring", () => {
    const read = path => readFileSync(new URL(path, import.meta.url), "utf8");
    it("navigates search hits by saved-preference key and flashes the control with a reduced-motion-safe animation", () => {
        const ui = read("../src/settings-ui.js");
        assert.match(ui, /searchSettings\(searchIndex, q/);
        assert.match(ui, /buildSettingsSearchIndex\(entries\)/);
        assert.match(ui, /content\.querySelector\(`\[data-setting="\$\{escape\(h\.key\)\}"\]`\)/);
        assert.match(ui, /flashSettingsHit\(highlighted\)/);
        const css = read("../src/style.css");
        assert.match(css, /@keyframes set-hit-flash/);
        assert.match(css, /@media \(prefers-reduced-motion: no-preference\) \{\s*\.set-hit-flash \{ animation: set-hit-flash/);
    });
});


it("preserves page order for broad German labels but prioritizes exact labels", () => {
    const translated = buildSettingsSearchIndex([
        { key: "volume", page: "audio", label: "Sprachlautstärke" },
        { key: "sound_volume", page: "notifications", label: "Tonlautstärke" },
        { key: "speech_volume", page: "notifications", label: "Lautstärke der Sprachmeldungen" },
    ]);
    assert.equal(searchSettings(translated, "Lautstärke")[0].key, "volume");
    assert.equal(searchSettings(translated, "Tonlautstärke")[0].key, "sound_volume");
});
