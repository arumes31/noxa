// The PWA carries a byte-identical copy of the desktop's encrypted channel
// media modules (Task 2) and the same pinned maintained worker library, so
// desktop and PWA always speak the same frame, key and control format.
import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

const FILES = ["media-e2ee-core.js", "media-e2ee.js", "media-e2ee.worker.js", "media-keys.js", "media-replay.js", "media-session.js"];
const here = new URL("../src/core/media-e2ee/", import.meta.url);
const desktop = new URL("../../client/frontend/src/", import.meta.url);

test("PWA media E2EE modules are identical to the desktop's", () => {
    for (const file of FILES) assert.equal(readFileSync(new URL(file, here), "utf8"), readFileSync(new URL(file, desktop), "utf8"), file);
});

test("both clients pin the same maintained LiveKit worker version", () => {
    const mobile = JSON.parse(readFileSync(new URL("../package.json", import.meta.url), "utf8")).dependencies["livekit-client"];
    const frontend = JSON.parse(readFileSync(new URL("../../client/frontend/package.json", import.meta.url), "utf8")).dependencies["livekit-client"];
    assert.equal(mobile, "2.23.0");
    assert.equal(frontend, mobile);
});
