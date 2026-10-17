import assert from "node:assert/strict";
import { registerHooks } from "node:module";
import { describe, it } from "node:test";

// The stage module imports its stylesheet and the stream session; neither
// matters for the formatters under test.
const stubs = new Map(Object.entries({
    "arctic-stage.css": "export default '';",
    "stream-controls.js": "export const availableStreams = () => [];",
    "context-menu.js": "export const mountContextMenu = () => {}, closeContextMenu = () => {};",
}).map(([name, source]) => [new URL(`../src/${name}`, import.meta.url).href, source]));
registerHooks({ load(url, context, next) {
    if (stubs.has(url)) return { format: "module", source: stubs.get(url), shortCircuit: true };
    // Stylesheets imported further down (fullscreen HUD, speaking avatar).
    if (url.endsWith(".css")) return { format: "module", source: "export default '';", shortCircuit: true };
    return next(url, context);
} });
const { formatStageRate, stageResolution, stageStats } = await import("../src/stream-stage.js");
const { setLanguage } = await import("../src/i18n.js");

describe("stage stream statistics", () => {
    it("formats the info strip from measured receive stats", () => {
        setLanguage("en");
        assert.equal(stageStats({ width: 1920, height: 1080, fps: 59.7, bitrate: 6_210_000 }), "1080p · 60 FPS · 6.2 Mbps");
        assert.equal(stageStats({ height: 720, fps: 29.9, bitrate: 640_000 }), "720p · 30 FPS · 640 kbps");
        assert.equal(stageStats({ height: 1440, fps: null, bitrate: null }), "1440p", "unmeasured parts are omitted");
        assert.equal(stageStats(null), "");
        setLanguage("de");
        assert.equal(stageStats({ height: 1080, fps: 60, bitrate: 6_200_000 }), "1080p · 60 FPS · 6.2 Mbit/s");
        setLanguage("en");
    });

    it("formats the compact tile resolution and ignores missing samples", () => {
        assert.equal(stageResolution({ height: 1080, fps: 60.2 }), "1080p60");
        assert.equal(stageResolution({ height: 1440 }), "1440p");
        assert.equal(stageResolution({ height: 0, fps: 30 }), "");
        assert.equal(stageResolution(undefined), "");
        assert.equal(formatStageRate(0), "");
        assert.equal(formatStageRate(Number.NaN), "");
        assert.equal(formatStageRate(400), "1 kbps");
    });
});

describe("stage stylesheet guards", async () => {
    const { readFile } = await import("node:fs/promises");
    const css = (await readFile(new URL("../src/arctic-stage.css", import.meta.url), "utf8")).replace(/\/\*[\s\S]*?\*\//g, "");

    it("has no continuous or keyframe animation", () => {
        assert.doesNotMatch(css, /@keyframes|infinite|animation\s*:/);
    });

    it("never restyles native fullscreen tiles or the overlay beyond hiding stage chrome", () => {
        for (const [, prelude, body] of css.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
            if (/gaming-overlay|speaking-avatar|vtile-fallback/.test(prelude)) assert.fail(`touches protected UI: ${prelude.trim()}`);
            for (const selector of prelude.split(/,(?![^(]*\))/).map(part => part.trim()).filter(part => part.includes(":fullscreen"))) {
                const negated = selector.replace(/:not\(:fullscreen\)/g, "");
                if (!negated.includes(":fullscreen")) continue;
                const hidesStageChrome = /^\.vtile:fullscreen :is\(\.stage-info, \.stage-tile-tools, \.stage-tag\)$/.test(selector) && /^\s*display:\s*none;\s*$/.test(body);
                const stylesStageRoot = selector.startsWith(".stream-stage:fullscreen");
                assert.ok(hidesStageChrome || stylesStageRoot, `unexpected :fullscreen rule: ${selector}`);
            }
        }
    });
});
