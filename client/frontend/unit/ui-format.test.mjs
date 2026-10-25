import assert from "node:assert/strict";
import test from "node:test";
import { setLanguage, t } from "../src/i18n.js";
import { channelPath, formatTransferETA, titleWhenTruncated } from "../src/ui-format.js";
import { quickWinEnglish, quickWinGerman } from "../src/quick-win-messages.js";

test("transfer ETA handles unknown values and minute/hour boundaries in both languages", () => {
    for (const language of ["en", "de"]) {
        setLanguage(language);
        assert.equal(formatTransferETA(NaN), "");
        assert.equal(formatTransferETA(Infinity), "");
        assert.equal(formatTransferETA(-1), "");
        assert.equal(formatTransferETA(0), t("eta.soon"));
        assert.equal(formatTransferETA(59), t("eta.soon"));
        assert.equal(formatTransferETA(60), t("eta.minute"));
        assert.equal(formatTransferETA(61), t("eta.minutes", { count: 2 }));
        assert.equal(formatTransferETA(3600), t("eta.hour"));
        assert.equal(formatTransferETA(3601), t("eta.hours", { count: 2 }));
    }
    setLanguage("en");
});

test("UI translation catalogs keep matching keys and placeholders", () => {
    assert.deepEqual(Object.keys(quickWinEnglish).sort(), Object.keys(quickWinGerman).sort());
    const placeholders = value => [...value.matchAll(/\{(\w+)\}/g)].map(match => match[1]).sort();
    for (const key of Object.keys(quickWinEnglish)) assert.deepEqual(placeholders(quickWinEnglish[key]), placeholders(quickWinGerman[key]), key);
});

function fakeElement(scrollWidth, clientWidth) {
    const listeners = {};
    return { scrollWidth, clientWidth, dataset: {}, title: undefined, listeners,
        addEventListener(type, handler) { listeners[type] = handler; },
        removeAttribute(name) { delete this[name]; } };
}

test("truncation titles appear only once the text is clipped and keep extra tooltip text", () => {
    const clipped = fakeElement(120, 60);
    titleWhenTruncated(clipped, "A very long display name", "Roles: Admin");
    assert.equal(clipped.title, "A very long display name\nRoles: Admin");
    assert.equal(clipped.dataset.fullText, "A very long display name");
    const fits = fakeElement(60, 60);
    titleWhenTruncated(fits, "Short", "Roles: Admin");
    assert.equal(fits.title, "Roles: Admin");
    const plain = fakeElement(60, 60);
    titleWhenTruncated(plain, "Short");
    assert.equal(plain.title, undefined);
    plain.scrollWidth = 90; plain.listeners.mouseenter();
    assert.equal(plain.title, "Short");
    assert.equal(titleWhenTruncated(null, "x"), undefined);
});

test("channel paths walk parent links and stop on broken or cyclic trees", () => {
    const channels = [{ ChannelID: 1, Name: "Lobby" }, { ChannelID: 2, Name: "Games", ParentID: 1 }, { ChannelID: 3, Name: "Chess", ParentID: 2 },
        { ChannelID: 4, Name: "Orphan", ParentID: 99 }, { ChannelID: 5, Name: "Loop", ParentID: 5 }];
    assert.equal(channelPath(channels, 3), "Lobby › Games › Chess");
    assert.equal(channelPath(channels, 1), "Lobby");
    assert.equal(channelPath(channels, 4), "Orphan");
    assert.equal(channelPath(channels, 42), "");
    assert.ok(channelPath(channels, 5).startsWith("Loop"));
});
