import { test } from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";

test("only OLED Midnight is bundled and selectable", async () => {
    const html = await readFile(new URL("../index.html", import.meta.url), "utf8");
    const settings = await readFile(new URL("../src/settings-ui.js", import.meta.url), "utf8");
    const css = await readFile(new URL("../src/style.css", import.meta.url), "utf8");
    assert.match(html, /data-theme="midnight"/);
    assert.equal((html.match(/src\/midnight.css/g) || []).length, 1);
    assert.doesNotMatch(html, /arctic/);
    assert.doesNotMatch(settings, /Arctic Night|\["light",|\["hc",|\["dark",/);
    assert.doesNotMatch(css, /data-theme="(?:light|hc|arctic)"/);
});
