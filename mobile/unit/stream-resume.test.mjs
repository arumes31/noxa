import test from "node:test";
import assert from "node:assert/strict";
import { createResumeWatches, RESUME_WATCH_MS } from "../src/core/stream-resume.js";

test("a resume watch is taken once, only for its source and before it expires", () => {
    let now = 1000;
    const watches = createResumeWatches(() => now);
    assert.equal(watches.note("c-pub", "screen"), true);
    assert.equal(watches.note("c-pub", "microphone"), false);
    assert.equal(watches.note("", "cam"), false);
    assert.equal(watches.take("c-pub|cam"), false);
    assert.equal(watches.take("c-pub|screen"), true);
    assert.equal(watches.take("c-pub|screen"), false, "single use");
    watches.note("c-late", "cam");
    now += RESUME_WATCH_MS + 1;
    assert.equal(watches.take("c-late|cam"), false, "expired");
});

test("resume watches stay bounded", () => {
    const watches = createResumeWatches(() => 0);
    for (let i = 0; i < 100; i++) watches.note(`c-${i}`, "cam");
    assert.equal(watches.take("c-0|cam"), false);
    assert.equal(watches.take("c-99|cam"), true);
});
