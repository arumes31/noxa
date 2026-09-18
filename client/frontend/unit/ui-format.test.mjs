import assert from "node:assert/strict";
import test from "node:test";
import { setLanguage, t } from "../src/i18n.js";
import { formatTransferETA } from "../src/ui-format.js";
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
