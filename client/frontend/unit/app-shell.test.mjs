import { describe, it } from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { formatStreamStats, QUICK_SWITCH_SETTINGS, serverInitial, textChannels } from "../src/shell-model.js";
import { shellEnglish, shellGerman } from "../src/shell-messages.js";

const css = (await readFile(new URL("../src/arctic-shell.css", import.meta.url), "utf8")).replace(/\/\*[\s\S]*?\*\//g, "");
const html = await readFile(new URL("../index.html", import.meta.url), "utf8");

describe("app shell model", () => {
    it("lists real text channels and voice-chat links in stable order", () => {
        const channels = [
            { ChannelID: 1, Name: "Lobby" },
            { ChannelID: 5, Name: "clips", TextOnly: true, OrderIndex: 3 },
            { ChannelID: 3, Name: "general", TextOnly: true, OrderIndex: 0 },
            { ChannelID: 4, Name: "lobby", TextOnly: true, OrderIndex: 0 },
        ];
        assert.deepEqual(textChannels(channels).map((channel) => channel.Name), ["Lobby", "general", "lobby", "clips"]);
        assert.deepEqual(textChannels([{ ChannelID: 1, Name: "legacy" }]).map(channel => channel.Name), ["legacy"]);
        assert.deepEqual(textChannels(), []);
    });

    it("keeps linked chats in voice-tree order across nested sibling ranks", () => {
        const channels = [
            { ChannelID: 20, Name: "Private", OrderIndex: 0 },
            { ChannelID: 1, Name: "Lobby", SystemKind: "home", OrderIndex: 99 },
            { ChannelID: 8, Name: "Public2", ParentID: 1, OrderIndex: 2 },
            { ChannelID: 7, Name: "Public1", ParentID: 1, OrderIndex: 1 },
            { ChannelID: 22, Name: "Private2", ParentID: 20, OrderIndex: 2 },
            { ChannelID: 21, Name: "Private1", ParentID: 20, OrderIndex: 1 },
            { ChannelID: 30, Name: "general", TextOnly: true, OrderIndex: 0 },
        ];
        const original = channels.map(channel => ({ ...channel }));
        assert.deepEqual(textChannels(channels).map(channel => channel.Name), ["Public1", "Public2", "Private", "Private1", "Private2", "general"]);
        assert.deepEqual(channels, original);
    });

    it("promotes voice descendants of text-only parents exactly like the voice tree", () => {
        const channels = [
            { ChannelID: 1, Name: "Voice" },
            { ChannelID: 2, Name: "Text parent", ParentID: 1, TextOnly: true },
            { ChannelID: 3, Name: "Nested voice", ParentID: 2 },
            { ChannelID: 4, Name: "Next voice", OrderIndex: 1 },
        ];
        assert.deepEqual(textChannels(channels).map(channel => channel.ChannelID), [1, 3, 4, 2]);
    });

    it("formats the voice card stream summary", () => {
        assert.equal(formatStreamStats({ height: 1080, fps: 59.7, bitrate: 6_200_000 }), "1080p60 · 6.2 Mbps");
        assert.equal(formatStreamStats({ height: 720, fps: 30, bitrate: 850_000 }), "720p30 · 850 kbps");
        assert.equal(formatStreamStats({ height: 1440, fps: 0, bitrate: null }), "1440p");
        assert.equal(formatStreamStats({ height: 0, fps: NaN, bitrate: 0 }), "");
        assert.equal(formatStreamStats(null), "");
    });

    it("derives the server badge letter", () => {
        assert.equal(serverInitial("wowcraft.pw"), "W");
        assert.equal(serverInitial("  #raid"), "R");
        assert.equal(serverInitial("über"), "Ü");
        assert.equal(serverInitial(""), "");
    });

    it("keeps a well-formed fallback list of settings pages", () => {
        // The settings dialog's own settingsPages() list wins when present.
        const ids = QUICK_SWITCH_SETTINGS.map(([page]) => page);
        assert.equal(new Set(ids).size, ids.length);
        for (const [, key] of QUICK_SWITCH_SETTINGS) assert.match(key, /^settings\.[a-z]+$/);
    });

    it("carries every shell string in English and German", () => {
        assert.deepEqual(Object.keys(shellGerman).sort(), Object.keys(shellEnglish).sort());
        for (const [key, value] of Object.entries(shellGerman)) assert.ok(value.trim(), key);
    });
});

describe("app shell markup and styles", () => {
    it("links the shell stylesheet after the theme", () => {
        assert.ok(html.indexOf("/src/arctic-shell.css") > html.indexOf("/src/arctic-night.css"));
    });

    it("keeps the ping, the search and the voice controls reachable with names", () => {
        assert.match(html, /<button id="quick-switch-open"[^>]*aria-label="Search or jump to"/);
        assert.match(html, /<button id="voice-latency"[^>]*aria-haspopup="dialog"/);
        for (const id of ["voice-mute", "voice-deafen", "voice-screen", "voice-video", "voice-leave-channel"]) assert.match(html, new RegExp(`id="${id}"`));
        assert.match(html, /id="text-channels" role="list"/);
        assert.match(html, /<aside id="sidebar" aria-labelledby="channels-heading">/);
    });

    it("has no continuous motion and leaves the overlay and speaking avatar alone", () => {
        assert.doesNotMatch(css, /infinite|@keyframes|animation\s*:/);
        assert.doesNotMatch(css, /gaming-overlay|speaking-avatar|speak-pulse|:fullscreen/);
    });
});
