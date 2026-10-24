import assert from "node:assert/strict";
import { test } from "node:test";

import { accentVars, appearanceFor, applyAppearance, clampScale, hexToRGB, normalizeHex, normalizeTheme, resolveTheme } from "../src/core/appearance.js";

test("themes normalize and system follows the phone", () => {
    assert.equal(normalizeTheme("arctic"), "arctic");
    assert.equal(normalizeTheme("neon"), "midnight");
    assert.equal(resolveTheme("system", true), "light");
    assert.equal(resolveTheme("system", false), "midnight");
    assert.equal(resolveTheme("contrast", true), "contrast");
});

test("hex colours normalize; accent variables derive from them", () => {
    assert.equal(normalizeHex("#ABC"), "#aabbcc");
    assert.equal(normalizeHex("34d9ef"), "#34d9ef");
    assert.equal(normalizeHex("#12345"), "");
    assert.equal(normalizeHex("red"), "");
    assert.deepEqual(hexToRGB("#ff8000"), [255, 128, 0]);
    assert.deepEqual(accentVars("#ff8000"), { "--accent": "#ff8000", "--accent-dim": "rgba(255, 128, 0, 0.14)", "--border-strong": "rgba(255, 128, 0, 0.45)" });
    assert.deepEqual(accentVars(""), {});
});

test("font scale clamps to 0.9–1.3 in 0.05 steps", () => {
    assert.equal(clampScale(2), 1.3);
    assert.equal(clampScale(0.5), 0.9);
    assert.equal(clampScale("1.12"), 1.1);
    assert.equal(clampScale("x"), 1);
});

test("appearance attributes and variables; null removes", () => {
    const a = appearanceFor({ theme: "light", accent: "#f00", fontScale: 1.2, compact: true, reduceMotion: false });
    assert.deepEqual(a.attributes, { "data-theme": "light", "data-compact": "", "data-reduce-motion": null });
    assert.equal(a.vars["--accent"], "#ff0000");
    assert.equal(a.vars["--font-scale"], "1.2");
    const b = appearanceFor({ theme: "bogus", accent: "", fontScale: 1 });
    assert.equal(b.attributes["data-theme"], "midnight");
    assert.equal(b.vars["--accent"], null);
    assert.equal(b.vars["--font-scale"], null);
});

test("applyAppearance writes onto an element", () => {
    const attributes = new Map();
    const style = new Map();
    const element = {
        setAttribute: (k, v) => attributes.set(k, v), removeAttribute: k => attributes.delete(k),
        style: { setProperty: (k, v) => style.set(k, v), removeProperty: k => style.delete(k) },
    };
    applyAppearance(element, { theme: "contrast", accent: "#00ff00", fontScale: 1.3, compact: true, reduceMotion: true });
    assert.equal(attributes.get("data-theme"), "contrast");
    assert.ok(attributes.has("data-reduce-motion"));
    assert.equal(style.get("--accent"), "#00ff00");
    applyAppearance(element, { theme: "midnight", accent: "", fontScale: 1, compact: false, reduceMotion: false });
    assert.equal(attributes.get("data-theme"), "midnight");
    assert.equal(attributes.has("data-compact"), false);
    assert.equal(style.size, 0);
});
