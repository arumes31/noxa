import test from 'node:test';
import assert from 'node:assert/strict';
import { counterDelta, counterRate } from './browser_fanout_metrics.mjs';

test('stage counters reject hidden resets, missing data and replaced RTP or observer identities', () => {
    const rows = [10, 20, 30].map(frames => ({ id: 'rtp', ssrc: 42, epoch: 1, frames }));
    assert.equal(counterDelta(rows, 'frames', ['id', 'ssrc']), 20);
    assert.equal(counterDelta([{ ...rows[0], frames: 0 }, { ...rows[1], frames: 0 }], 'frames', ['epoch']), 0);
    for (const middle of [{ frames: 0 }, { frames: undefined }, { frames: NaN }, { id: 'replacement' }, { ssrc: 43 }, { id: undefined }]) {
        assert.equal(counterDelta([rows[0], { ...rows[1], ...middle }, rows[2]], 'frames', ['id', 'ssrc']), null);
    }
    assert.equal(counterDelta([rows[0], { ...rows[1], epoch: 2 }, rows[2]], 'frames', ['epoch']), null);
    assert.equal(counterDelta([], 'frames'), null);
    assert.equal(counterDelta([rows[0]], 'frames'), null);
});

test('rates use the measured receiver interval and never coerce missing counters to zero', () => {
    assert.equal(counterRate(600, 10), 60);
    assert.equal(counterRate(600, 12), 50, 'a receiver sample interval may differ from the sender interval');
    assert.equal(counterRate(0, 10), 0);
    for (const seconds of [0, -1, NaN, Infinity, null]) assert.equal(counterRate(600, seconds), null);
    for (const delta of [null, undefined, NaN, Infinity, -1]) assert.equal(counterRate(delta, 10), null);
});
