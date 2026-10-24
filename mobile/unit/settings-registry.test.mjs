import assert from "node:assert/strict";
import { test } from "node:test";

import { filterSettings, onSettingsRegistered, registerSettings, rowMatches, settingsSections, unregisterSettings } from "../src/ui/settings-registry.js";

const row = (key, label, keywords = "") => ({ key, label, keywords, render: () => null });

test("sections keep the fixed order, unknown ones follow in registration order", () => {
    registerSettings("zeta", [row("z1", "Zeta thing")], { title: "Zeta" });
    registerSettings("about", [row("version", "Version")], { title: "About" });
    registerSettings("appearance", [row("theme", "Theme")], { title: "Appearance" });
    registerSettings("alpha", [row("a1", "Alpha thing")]);
    registerSettings("voice", [row("echo", "Echo cancellation", "aec")], { title: "Voice" });
    const ids = settingsSections().map(s => s.id);
    assert.deepEqual(ids.filter(id => ["appearance", "voice", "about", "zeta", "alpha"].includes(id)), ["appearance", "voice", "about", "zeta", "alpha"]);
    for (const id of ["zeta", "about", "appearance", "alpha", "voice"]) unregisterSettings(id);
});

test("registering again appends rows and replaces rows with the same key", () => {
    registerSettings("chat", [row("a", "First"), row("b", "Second")], { title: "Chat" });
    registerSettings("chat", [row("b", "Second, replaced"), row("c", "Third")]);
    const chat = settingsSections().find(s => s.id === "chat");
    assert.equal(chat.title, "Chat");
    assert.deepEqual(chat.rows.map(r => r.label), ["First", "Second, replaced", "Third"]);
    unregisterSettings("chat", "a");
    assert.deepEqual(settingsSections().find(s => s.id === "chat").rows.map(r => r.key), ["b", "c"]);
    unregisterSettings("chat");
});

test("rows need a key and render()", () => {
    assert.throws(() => registerSettings("x", [{ key: "k", label: "no render" }]));
    assert.throws(() => registerSettings("", []));
    unregisterSettings("x");
});

test("search matches every word in label, keywords or section title", () => {
    assert.equal(rowMatches(row("t", "Theme"), "the"), true);
    assert.equal(rowMatches(row("t", "Theme"), "THEME dark"), false);
    assert.equal(rowMatches(row("t", "Theme", "dark light"), "theme dark"), true);
    assert.equal(rowMatches(row("t", "Theme"), "appearance", "Appearance"), true);
    assert.equal(rowMatches(row("t", "Theme"), "   "), true);

    registerSettings("appearance", [row("theme", "Theme", "dark"), row("font", "Text size")], { title: "Appearance" });
    registerSettings("data", [row("storage", "Storage used")], { title: "Data" });
    const found = filterSettings("size");
    assert.deepEqual(found.map(s => s.id), ["appearance"]);
    assert.deepEqual(found[0].rows.map(r => r.key), ["font"]);
    assert.equal(filterSettings("nothing-like-this").length, 0);
    assert.ok(filterSettings("").length >= 2);
    unregisterSettings("appearance");
    unregisterSettings("data");
});

test("listeners hear registrations", () => {
    const seen = [];
    const stop = onSettingsRegistered(id => seen.push(id));
    registerSettings("dm", [row("x", "X")]);
    stop();
    registerSettings("dm", [row("y", "Y")]);
    assert.deepEqual(seen, ["dm"]);
    unregisterSettings("dm");
});
