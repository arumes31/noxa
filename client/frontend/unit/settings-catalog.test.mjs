import assert from "node:assert/strict";
import { describe, it } from "node:test";
import { readFileSync } from "node:fs";
import {
    LEGACY_SETTINGS_PAGES, RESET_PRESERVES, SETTINGS_CATEGORIES, SETTINGS_OUTSIDE_DIALOG, SETTINGS_PAGE_IDS,
    pageForSetting, resetPageDraft, resolveSettingsTarget,
} from "../src/settings-catalog.js";
import { settingsShellEnglish, settingsShellGerman } from "../src/settings-shell-messages.js";

const read = path => readFileSync(new URL(path, import.meta.url), "utf8");

// JSON names of every field of the Go Settings struct.
function goSettingsFields() {
    const source = read("../../settings.go");
    const body = source.slice(source.indexOf("type Settings struct {"));
    const struct = body.slice(0, body.indexOf("\n}\n"));
    return [...struct.matchAll(/json:"([a-z0-9_]+)/g)].map(match => match[1]);
}

// Dialog sources that read or write saved preferences.
const UI_SOURCES = ["settings-ui.js", "gaming-overlay-settings.js", "security-settings.js", "mic-check-ui.js"]
    .map(file => read(`../src/${file}`)).join("\n");

describe("settings catalogue", () => {
    it("puts every saved preference on exactly one page or names its owner outside the dialog", () => {
        const fields = goSettingsFields();
        assert.ok(fields.length > 100, "parsed the Go Settings struct");
        const owners = new Map();
        for (const category of SETTINGS_CATEGORIES) {
            for (const key of category.keys) {
                assert.ok(!owners.has(key), `${key} is on both ${owners.get(key)} and ${category.id}`);
                owners.set(key, category.id);
            }
        }
        for (const key of Object.keys(SETTINGS_OUTSIDE_DIALOG)) {
            assert.ok(!owners.has(key), `${key} is both outside the dialog and on ${owners.get(key)}`);
        }
        const missing = fields.filter(key => !owners.has(key) && !Object.hasOwn(SETTINGS_OUTSIDE_DIALOG, key));
        assert.deepEqual(missing, [], "saved preferences without a settings page");
        const unknown = [...owners.keys(), ...Object.keys(SETTINGS_OUTSIDE_DIALOG)].filter(key => !fields.includes(key));
        assert.deepEqual(unknown, [], "catalogue keys the Go settings do not have");
    });

    it("keeps every page key reachable from a control in the dialog sources", () => {
        for (const category of SETTINGS_CATEGORIES) {
            for (const key of category.keys) {
                const pattern = new RegExp(`(\\.${key}\\b|["']${key}["'])`);
                assert.match(UI_SOURCES, pattern, `${category.id}: no control edits ${key}`);
            }
        }
    });

    it("registers a builder, label and description for every page in both languages", () => {
        const ui = read("../src/settings-ui.js");
        const builders = ui.slice(ui.indexOf("const PAGE_BUILDERS = {"));
        const ids = [...builders.slice(0, builders.indexOf("};")).matchAll(/^\s+(\w+):/gm)].map(match => match[1]);
        assert.deepEqual(ids.sort(), [...SETTINGS_PAGE_IDS].sort());
        for (const id of SETTINGS_PAGE_IDS) {
            for (const key of [`settings.cat.${id}`, `settings.cat.${id}.desc`]) {
                assert.ok(settingsShellEnglish[key], `missing English ${key}`);
                assert.ok(settingsShellGerman[key], `missing German ${key}`);
            }
        }
        assert.deepEqual(SETTINGS_CATEGORIES.filter(c => c.rail === "main").map(c => c.id),
            ["general", "audio", "streams", "overlay", "hotkeys", "appearance", "privacy"]);
    });
});

describe("settings deep links", () => {
    it("maps every pre-redesign page id to a current page", () => {
        const legacy = ["application", "capture", "camera", "playback", "overlay", "hotkeys", "whisper", "downloads", "chat", "security", "server", "notifications"];
        for (const id of legacy) {
            const { page } = resolveSettingsTarget(id);
            assert.ok(SETTINGS_PAGE_IDS.includes(page), `${id} -> ${page}`);
        }
        assert.deepEqual(resolveSettingsTarget("playback"), { page: "audio", section: "output", setting: "" });
        assert.deepEqual(resolveSettingsTarget("server"), { page: "connection", section: "server", setting: "" });
        assert.deepEqual(resolveSettingsTarget("security"), { page: "privacy", section: "", setting: "" });
        for (const [id, target] of Object.entries(LEGACY_SETTINGS_PAGES)) assert.equal(resolveSettingsTarget(id).page, target.page);
    });

    it("accepts page/section strings, option objects and bare settings", () => {
        assert.deepEqual(resolveSettingsTarget("audio/output"), { page: "audio", section: "output", setting: "" });
        assert.deepEqual(resolveSettingsTarget("overlay", { setting: "gaming_overlay_game_ui" }),
            { page: "overlay", section: "", setting: "gaming_overlay_game_ui" });
        assert.deepEqual(resolveSettingsTarget({ setting: "chat_density" }), { page: "chat", section: "", setting: "chat_density" });
        assert.deepEqual(resolveSettingsTarget({ page: "capture", section: "input" }), { page: "audio", section: "input", setting: "" });
        assert.equal(resolveSettingsTarget("nope").page, "general");
        assert.equal(resolveSettingsTarget(undefined).page, "general");
        assert.equal(resolveSettingsTarget("", { setting: "hotkey_ptt" }).page, "hotkeys");
        assert.equal(pageForSetting("gaming_overlay_avoid_zones"), "overlay");
        assert.equal(pageForSetting("bookmarks"), "");
    });
});

describe("reset to default", () => {
    const defaults = { gaming_overlay_opacity: 88, gaming_overlay_game_ui: "none", gaming_overlay_avoid_zones: true, gaming_overlay_position: "center-left",
        hotkey_ptt: "", hotkey_mute: "Ctrl+M", volume: 100 };

    it("restores only the page's own keys and reports what changed", () => {
        const draft = { gaming_overlay_opacity: 40, gaming_overlay_game_ui: "mmo", gaming_overlay_position: "custom", gaming_overlay_x: 50, volume: 150 };
        const changed = resetPageDraft(draft, defaults, "overlay");
        assert.deepEqual(changed.sort(), ["gaming_overlay_avoid_zones", "gaming_overlay_game_ui", "gaming_overlay_opacity", "gaming_overlay_position", "gaming_overlay_x"].sort());
        assert.equal(draft.gaming_overlay_opacity, 88);
        assert.equal(draft.gaming_overlay_position, "center-left");
        assert.equal("gaming_overlay_x" in draft, false, "keys absent from the defaults are removed");
        assert.equal(draft.volume, 150, "other pages are untouched");
        assert.deepEqual(resetPageDraft(draft, defaults, "overlay"), []);
    });

    it("keeps curated lists such as hotkey profiles and whisper targets", () => {
        const draft = { hotkey_ptt: "F1", hotkey_profiles: { raid: { ptt: "F2" } }, whisper_active: true, whisper_clients: ["a"], whisper_channels: [1] };
        resetPageDraft(draft, { ...defaults, whisper_active: false }, "hotkeys");
        resetPageDraft(draft, { whisper_active: false }, "whisper");
        assert.equal(draft.hotkey_ptt, "");
        assert.deepEqual(draft.hotkey_profiles, { raid: { ptt: "F2" } });
        assert.equal(draft.whisper_active, false);
        assert.deepEqual(draft.whisper_clients, ["a"]);
        for (const key of RESET_PRESERVES) assert.ok(pageForSetting(key), `${key} belongs to a page`);
    });

    it("ignores unknown pages and missing defaults", () => {
        const draft = { volume: 150 };
        assert.deepEqual(resetPageDraft(draft, defaults, "nope"), []);
        assert.deepEqual(resetPageDraft(draft, "", "audio"), []);
        assert.deepEqual(resetPageDraft(draft, null, "audio"), []);
        assert.equal(draft.volume, 150);
    });

    it("copies default values instead of sharing them", () => {
        const shared = { event_sounds: { mention: true } };
        const draft = {};
        resetPageDraft(draft, shared, "notifications");
        draft.event_sounds.mention = false;
        assert.equal(shared.event_sounds.mention, true);
    });
});

describe("arctic-settings.css", () => {
    const css = read("../src/arctic-settings.css").replace(/\/\*[\s\S]*?\*\//g, "");
    it("scopes every rule to the settings dialog", () => {
        for (const match of css.matchAll(/([^{}]+)\{/g)) {
            const prelude = match[1].trim();
            if (!prelude || prelude.startsWith("@")) continue;
            for (const selector of prelude.split(/,(?![^(]*\))/).map(part => part.trim())) {
                assert.match(selector, /#settings-overlay\b/, `unscoped selector: ${selector}`);
            }
        }
    });
    it("has no animation and leaves the overlay sample and speaking avatar alone", () => {
        assert.doesNotMatch(css, /animation|@keyframes|infinite/);
        for (const match of css.matchAll(/([^{}]+)\{/g)) {
            assert.doesNotMatch(match[1].replace(/:not\([^)]*\)/g, ""), /overlay-sample|overlay-position-sample|overlay-animation|speaking-avatar/, match[1]);
        }
        assert.match(css, /@media \(prefers-reduced-motion: no-preference\)/, "switch transitions are motion-gated");
    });
});
