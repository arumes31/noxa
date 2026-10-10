import test from "node:test";
import assert from "node:assert/strict";
import { voiceHistoryModel } from "../src/voice-history.js";

const at = 1791300000000;
const point = (seconds, values = {}) => ({ observed_at: at + seconds * 1000, ...values });

test("history orders and bounds actual timestamps without mutating caller data", () => {
    const history = Array.from({ length: 80 }, (_, n) => point(n * 5, { buffer_ms: n }));
    history.reverse();
    const model = voiceHistoryModel(history, at + 395000);
    assert.equal(model.points.length, 60);
    assert.equal(model.start, at + 100000);
    assert.equal(model.end, at + 395000);
    assert.equal(history[0].observed_at, at + 395000);
    assert.equal(model.ageMS, 0);
});

test("missing values and long collection gaps split paths; zero remains a measured point", () => {
    const model = voiceHistoryModel([
        point(0, { buffer_ms: 0, loss_percent: null }), point(5, { buffer_ms: 40, loss_percent: 0 }),
        point(10, { buffer_ms: null }), point(15, { buffer_ms: 80 }), point(40, { buffer_ms: 120 }),
    ], at + 45000);
    const buffer = model.series.find(series => series.key === "buffer_ms");
    assert.deepEqual(buffer.segments.map(segment => segment.map(p => p.value)), [[0, 40], [80], [120]]);
    assert.equal(model.series.find(series => series.key === "loss_percent").segments[0][0].value, 0);
    assert.equal(model.ageMS, 5000);
    assert.equal(model.stale, false);
});

test("stale time uses the server observation clock and does not extend measurements", () => {
    const model = voiceHistoryModel([point(0, { buffer_ms: 30 }), point(5, { buffer_ms: 60 })], at + 30000);
    assert.equal(model.ageMS, 25000);
    assert.equal(model.stale, true);
    assert.equal(model.series[0].segments[0].at(-1).at, at + 5000);
    assert.equal(model.end, at + 30000);
});

test("invalid and expired data do not become healthy zero values", () => {
    const model = voiceHistoryModel([point(-400, { buffer_ms: 1 }), { observed_at: NaN },
        point(0, { buffer_ms: -1, loss_percent: 101, concealment_percent: Infinity }),
        point(5, { buffer_ms: null, loss_percent: null, concealment_percent: null }),
    ], at + 5000);
    assert.equal(model.points.length, 2);
    assert.ok(model.series.every(series => series.segments.length === 0));
    assert.equal(voiceHistoryModel([], at).points.length, 0);
});

test("all percentage charts share a bounded explicit scale; single samples remain visible", () => {
    const model = voiceHistoryModel([point(0, { buffer_ms: 420, loss_percent: 1, concealment_percent: 4 })], at);
    assert.equal(model.series[0].maximum, 500);
    assert.equal(model.series[1].maximum, model.series[2].maximum);
    assert.equal(model.series[1].maximum, 5);
    assert.ok(model.series.every(series => series.segments[0].length === 1));
    assert.equal(model.end - model.start, 30000);
});
