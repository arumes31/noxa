import test from 'node:test';
import assert from 'node:assert/strict';
import { createPeriodicUpdateCheck, updateIntervalHours } from '../src/update-periodic.js';

test('defaults to two hours, waits for settings, and skips missed intervals after sleep', async () => {
    let saved, now = 0, calls = 0;
    const tick = createPeriodicUpdateCheck({ settings: () => saved, now: () => now, check: async () => calls++ });
    await tick(); now = 100000000; await tick(); assert.equal(calls, 0);
    saved = {}; await tick(); now += 7199999; await tick(); assert.equal(calls, 0);
    now++; await tick(); assert.equal(calls, 1);
    now += 86400000; await tick(); await tick(); assert.equal(calls, 2);
});

test('opt-out and interval changes reset the timer and invalidate an in-flight offer', async () => {
    let saved = {}, now = 0, calls = 0, permission;
    const tick = createPeriodicUpdateCheck({ settings: () => saved, now: () => now,
        check: async enabled => { calls++; permission = enabled; } });
    await tick(); now = 7200000; await tick(); assert.equal(permission(), true);
    saved.updates_check_periodically = false; await tick(); assert.equal(permission(), false);
    now += 86400000; await tick(); assert.equal(calls, 1);
    saved = { updates_check_interval_hours: 4 }; await tick(); now += 7200000; await tick(); assert.equal(calls, 1);
    now += 7200000; await tick(); assert.equal(calls, 2);
    for (const bad of [0, -1, 3, Infinity, '2', null]) assert.equal(updateIntervalHours(bad), 2);
});

test('slow or failed requests never overlap or retry in a loop', async () => {
    let now = 0, finish, calls = 0;
    const tick = createPeriodicUpdateCheck({ settings: () => ({}), now: () => now,
        check: () => { calls++; return new Promise((_, reject) => { finish = reject; }); } });
    await tick(); now = 7200000; const pending = tick(); now *= 2; await tick(); assert.equal(calls, 1);
    finish(new Error('offline')); await pending;
});
