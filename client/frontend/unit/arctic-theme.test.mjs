import { describe, it } from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";

const css = await readFile(new URL("../src/arctic-night.css", import.meta.url), "utf8");
const html = await readFile(new URL("../index.html", import.meta.url), "utf8");
const source = css.replace(/\/\*[\s\S]*?\*\//g, "");

// selectorsOf returns every selector list that opens a style rule, skipping
// @-rule preludes and keyframe stops.
function selectorsOf(text) {
    const selectors = [];
    const pattern = /([^{}]+)\{/g;
    for (const match of text.matchAll(pattern)) {
        const prelude = match[1].trim();
        if (!prelude || prelude.startsWith("@") || /^(from|to|\d+%)(\s*,\s*(from|to|\d+%))*$/.test(prelude)) continue;
        selectors.push(prelude);
    }
    return selectors;
}

describe("Arctic Night theme", () => {
    it("is the default theme before settings load", () => {
        assert.match(html, /<html lang="en" data-theme="arctic">/);
        assert.ok(html.indexOf("/src/arctic-night.css") > html.indexOf("/src/workspace.css"));
    });

    it("scopes every rule so classic, light and high-contrast themes are unchanged", () => {
        for (const prelude of selectorsOf(source)) {
            for (const selector of prelude.split(/,(?![^(]*\))/).map(part => part.trim())) {
                if (selector === "#topbar") continue; // layout-neutral wrapper (display: contents)
                assert.ok(selector.startsWith(':root[data-theme="arctic"]'), `unscoped selector: ${selector}`);
            }
        }
    });

    it("leaves the gaming/fullscreen overlay and the speaking avatar alone", () => {
        for (const prelude of selectorsOf(source)) {
            // :not(...) exclusions only keep theme rules away from those elements.
            const targets = prelude.replace(/:not\([^)]*\)/g, "");
            assert.doesNotMatch(targets, /gaming-overlay|overlay-(position|sample|animation|color)|speaking-avatar|vtile-fallback|:fullscreen/, prelude);
        }
        assert.doesNotMatch(source, /@keyframes\s+speak|talk-pulse|speaking-avatar-light/);
    });

    it("has no continuous or background animation and loads no remote fonts", () => {
        assert.doesNotMatch(source, /infinite/);
        assert.doesNotMatch(source, /url\(\s*["']?https?:/);
        assert.doesNotMatch(source, /fonts\.(googleapis|gstatic)\.com/);
        for (const match of source.matchAll(/animation:\s*([^;]+);/g)) {
            assert.match(match[1], /^arc-msg-in\b/, `unexpected animation: ${match[1]}`);
        }
    });

    it("gates its motion behind reduced-motion preferences", () => {
        const motion = source.slice(source.indexOf("@keyframes arc-msg-in"));
        assert.match(motion, /@media \(prefers-reduced-motion: no-preference\)\s*\{\s*:root\[data-theme="arctic"\]:not\(\[data-reduce-motion="1"\]\)/);
    });
});
