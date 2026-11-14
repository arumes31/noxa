import assert from "node:assert/strict";
import { test } from "node:test";

import { openExternalURL } from "../src/external-url.js";

test("links open through the validating native binding only", async () => {
    const calls = [];
    globalThis.window = { go: { main: { App: { OpenExternalURL: async url => { calls.push(url); return url.startsWith("https://") ? "" : "refused"; } } } } };
    await openExternalURL("https://example.com/");
    await assert.rejects(openExternalURL("file:///C:/x.exe"), /refused/);
    assert.deepEqual(calls, ["https://example.com/", "file:///C:/x.exe"]);
    delete globalThis.window;
});
