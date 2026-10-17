import { describe, it } from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";

const css = (await readFile(new URL("../src/arctic-dm.css", import.meta.url), "utf8")).replace(/\/\*[\s\S]*?\*\//g, "");
const view = await readFile(new URL("../src/dm-view.js", import.meta.url), "utf8");

// rules returns [selector list, declarations] for every style rule, including
// rules nested in @media blocks.
function rules(text) {
    const out = [];
    for (const match of text.matchAll(/([^{}@]+)\{([^{}]*)\}/g)) {
        const prelude = match[1].trim();
        if (!prelude || /^(from|to|\d+%)/.test(prelude)) continue;
        out.push([prelude, match[2]]);
    }
    return out;
}
const selectors = prelude => prelude.split(/,(?![^(]*\))/).map(part => part.trim()).filter(Boolean);

describe("Direct Messages stylesheet", () => {
    it("only styles the open Messages view or its own elements", () => {
        for (const [prelude] of rules(css)) {
            for (const selector of selectors(prelude)) {
                assert.match(selector, /^(#center\.dm-view-active\b|\.dm-|#dm-|\.file-chip-meta$)/, `unscoped selector: ${selector}`);
            }
        }
    });

    it("never hides the per-message encryption badge, also in bubble mode", () => {
        const hides = /display:\s*none|visibility:\s*hidden|opacity:\s*0(?![.\d])|font-size:\s*0(?![.\d])|width:\s*0(?![.\d])/;
        for (const [prelude, body] of rules(css)) {
            if (!hides.test(body)) continue;
            for (const selector of selectors(prelude)) {
                const target = selector.split(/[\s>+~]+/).pop();
                assert.doesNotMatch(target, /msg-lock|msg-time|msg-timestamp|^time$|msg-receipt/, `${selector} would hide an encryption badge or its time`);
            }
        }
        // The bubble layout positions the badge's container explicitly.
        assert.match(css, /#chat-log \.msg\.rich \.msg-time\s*\{[^}]*grid-area:\s*time/);
        assert.match(css, /\.group-message > time\s*\{[^}]*grid-area:\s*time/);
    });

    it("keeps refused and sealed messages visibly marked", () => {
        assert.match(css, /#chat-log \.msg\.rich\.missing-key\s*\{[^}]*border-color/);
        assert.match(css, /\.group-message\.missing-key\s*\{[^}]*border-color/);
        assert.match(css, /\.msg-lock\[data-encryption="warning"\]/);
    });

    it("has no animation and leaves the overlay and the speaking avatar alone", () => {
        assert.doesNotMatch(css, /@keyframes|animation|infinite|transition/);
        for (const [prelude] of rules(css)) {
            assert.doesNotMatch(prelude.replace(/:not\([^)]*\)/g, ""), /gaming-overlay|overlay-|speaking-avatar|vtile|:fullscreen/, prelude);
        }
    });

    it("does not load remote resources", () => {
        assert.doesNotMatch(css, /url\(\s*["']?https?:/);
        assert.doesNotMatch(view, /https?:\/\/(?!www\.w3\.org\/2000\/svg)/);
    });

    it("offers a way back to the channel sidebar where it collapses", () => {
        // #chat-head (with #workspace-sidebar-toggle) is hidden in this view.
        assert.match(css, /#center\.dm-view-active > :is\(#chat-head\b/);
        assert.match(view, /iconButton\("speaker", "workspace\.labels\.showChannels"/);
        assert.match(view, /\$\("workspace-sidebar-toggle"\)\?\.click\(\)/);
        assert.match(view, /classList\.add\("dm-show-channels"\)/);
        assert.match(view, /channels\.setAttribute\("aria-controls", "sidebar"\)/);
        assert.match(view, /\[ui\.add, ui\.channels,/, "the toggle's accessible name follows the language");
        assert.match(css, /\.dm-show-channels\s*\{\s*display:\s*none;?\s*\}/);
        assert.match(css, /@media \(max-width: 720px\)\s*\{\s*\.dm-show-channels\s*\{\s*display:\s*inline-grid;?\s*\}/);
    });

    it("never opens a conversation the user did not choose", () => {
        // Opening one sends read receipts; with nothing selected the hint shows.
        const render = view.slice(view.indexOf("function render() {"), view.indexOf("function rowFor("));
        assert.doesNotMatch(render, /select\(/);
        assert.match(view, /t\("dm\.selectHint"\)/);
    });

    it("exposes the documented entry points for the sidebar", () => {
        for (const name of ["openDirectMessages", "closeDirectMessages", "isDirectMessagesOpen", "directMessagesUnread"]) {
            assert.match(view, new RegExp(`export function ${name}\\b`));
        }
        assert.match(view, /noxa-direct-unread/);
        assert.match(view, /noxa-direct-view/);
    });
});
