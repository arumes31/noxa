import assert from "node:assert/strict";
import test from "node:test";
import { createResumeDetector } from "../src/resume-detector.js";

function fixture() {
    let clock = 0, tick = null, cleared = false;
    const resumes = [];
    const stop = createResumeDetector({
        onResume: slept => resumes.push(slept), now: () => clock,
        timers: { setInterval: callback => { tick = callback; return 1; }, clearInterval: () => { cleared = true; } },
    });
    return { advance(ms) { clock += ms; tick(); }, resumes, stop, cleared: () => cleared };
}

test("regular ticks and short stalls are not a resume", () => {
    const f = fixture();
    for (let i = 0; i < 10; i++) f.advance(2000);
    f.advance(9000); // a busy main thread delays one tick
    assert.deepEqual(f.resumes, []);
});

test("a long gap between ticks reports one resume with the time slept", () => {
    const f = fixture();
    f.advance(2000);
    f.advance(2000 + 60000);
    f.advance(2000);
    assert.deepEqual(f.resumes, [60000]);
});

test("stop clears the interval", () => {
    const f = fixture();
    f.stop();
    assert.equal(f.cleared(), true);
});
