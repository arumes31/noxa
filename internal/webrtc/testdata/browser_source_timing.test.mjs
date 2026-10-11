import test from 'node:test';
import assert from 'node:assert/strict';
import { createSourceFrameTiming, drawDurationBoundsMS, rafGapBoundsMS } from './browser_source_timing.mjs';
import { sourceFrameTimingSummary } from './browser_fanout_metrics.mjs';

test('separates missing frame opportunities, throttle skips and completed draws', () => {
    const timing = createSourceFrameTiming('source');
    timing.opportunity(0); timing.completed(.5);
    const baseline = timing.snapshot();
    for (let index = 1; index <= 120; index++) {
        timing.opportunity(index * 1000 / 120);
        if (index % 2) timing.skipped();
        else timing.completed(2);
    }
    const summary = sourceFrameTimingSummary([baseline, timing.snapshot()], 1);
    assert.equal(summary.raf_opportunity_fps, 120);
    assert.equal(summary.throttle_skip_fps, 60);
    assert.equal(summary.completed_draw_fps, 60);
    assert.equal(summary.mean_sync_draw_ms, 2);
    assert.equal(summary.throttle_skip_ratio, .5);
    assert.equal(summary.sync_draw_bins.reduce((a, b) => a + b), 60);
    const slow = createSourceFrameTiming('slower-opportunities');
    const start = slow.snapshot();
    for (let index = 1; index <= 30; index++) { slow.opportunity(index * 1000 / 30); slow.completed(2); }
    const slowSummary = sourceFrameTimingSummary([start, slow.snapshot()], 1);
    assert.equal(slowSummary.raf_opportunity_fps, 30);
    assert.equal(slowSummary.completed_draw_fps, 30);
    assert.equal(slowSummary.throttle_skip_ratio, 0);
    assert.equal(slowSummary.mean_sync_draw_ms, 2, 'identical draw cost can accompany fewer opportunities');
});

test('fixed bins have exclusive bounds and snapshots do not expose mutable counters', () => {
    const timing = createSourceFrameTiming('bins');
    for (const [index, duration] of [0, 1, 2, 4, 8, 16, 33, 100].entries()) {
        timing.opportunity(index * 100); timing.completed(duration);
    }
    const snapshot = timing.snapshot();
    assert.equal(snapshot.sync_draw_bins.length, drawDurationBoundsMS.length);
    assert.equal(snapshot.raf_gap_bins.length, rafGapBoundsMS.length);
    assert.deepEqual(snapshot.sync_draw_bins, [1, 1, 1, 1, 1, 1, 2]);
    assert.equal(snapshot.raf_gap_bins.at(-1), 7);
    assert.equal(snapshot.lifetime_max_sync_draw_ms, 100);
    snapshot.sync_draw_bins[0] = 999;
    assert.equal(timing.snapshot().sync_draw_bins[0], 1);
    timing.opportunity(900); // A failed/unfinished draw must not count as completed.
    assert.equal(timing.snapshot().completed_draws, 8);
});

test('clock resets, absent telemetry and lifetime replacement cannot produce stage rates', () => {
    const timing = createSourceFrameTiming('clock');
    timing.opportunity(100); timing.completed(4);
    const before = timing.snapshot();
    timing.opportunity(50); timing.completed(1);
    const after = timing.snapshot();
    assert.notEqual(before.epoch, after.epoch);
    assert.equal(after.raf_opportunities, 1);
    assert.equal(after.raf_gap_bins.reduce((a, b) => a + b), 0);
    assert.equal(sourceFrameTimingSummary([before, after], 1), null);
    assert.equal(sourceFrameTimingSummary([null, null], 1), null);
    assert.equal(sourceFrameTimingSummary([before], 1), null);
    const idle = sourceFrameTimingSummary([after, after], 1);
    assert.equal(idle.raf_opportunity_fps, 0);
    assert.equal(idle.mean_sync_draw_ms, null);
    assert.equal(idle.throttle_skip_ratio, null);
    assert.equal(sourceFrameTimingSummary([after, { ...after, epoch: 'new-fixture' }], 1), null);
});
