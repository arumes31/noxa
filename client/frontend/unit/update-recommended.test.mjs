import test from 'node:test';
import assert from 'node:assert/strict';
import { createRecommendedUpdate } from '../src/update-recommended.js';

function harness({ session = { connected: true, update_recommended: '0.6.3' }, info = { available: true, version: '0.6.3' },
    settings = { updates_auto_install: true }, downloadError = '', busy = false } = {}) {
    const calls = [];
    const check = createRecommendedUpdate({
        app: () => ({
            SessionInfoForTab: async tab => { calls.push(['session', tab]); return session; },
            CheckForUpdate: async () => { calls.push(['check']); return info; },
        }),
        settings: () => settings,
        busy: () => busy,
        notify: (key, values) => calls.push(['notify', key, values]),
        download: async got => { calls.push(['download', got.version]); return downloadError; },
        restart: async () => { calls.push(['restart']); return true; },
    });
    return { check, calls };
}

test('a recommended update downloads in the background with a notice and restarts right away', async () => {
    const { check, calls } = harness();
    assert.equal(await check('tab-1'), true);
    assert.deepEqual(calls, [['session', 'tab-1'], ['check'], ['notify', 'updater.softUpdating', { version: '0.6.3' }], ['download', '0.6.3'], ['restart']]);
});

test('no recommendation, no newer release, auto-install off or a busy updater never update', async () => {
    for (const options of [{ session: { connected: true } }, { info: { available: false } }, { settings: { updates_auto_install: false } },
        { settings: null }, { busy: true }, { session: { connected: false, update_recommended: '0.6.3' } }]) {
        const { check, calls } = harness(options);
        assert.equal(await check('tab-1'), false, JSON.stringify(options));
        assert.ok(!calls.some(call => call[0] === 'download' || call[0] === 'restart'), JSON.stringify(options));
    }
});

test('a failed download reports once and keeps the running version', async () => {
    const { check, calls } = harness({ downloadError: 'checksum mismatch' });
    assert.equal(await check('tab-1'), false);
    assert.deepEqual(calls.at(-1), ['notify', 'updater.softUpdateFailed', { error: 'checksum mismatch' }]);
    assert.ok(!calls.some(call => call[0] === 'restart'));
});
