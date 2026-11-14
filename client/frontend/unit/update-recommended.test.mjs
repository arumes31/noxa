import test from 'node:test';
import assert from 'node:assert/strict';
import { createRecommendedUpdate } from '../src/update-recommended.js';

function harness({ session = { connected: true, update_recommended: '0.6.3' }, info = { available: true, version: '0.6.3' },
    settings = { updates_auto_install: true }, downloadError = '', busy = false, afterDownload = null, restartError = null, phase = 'ready' } = {}) {
    const calls = [];
    const current = { settings };
    const check = createRecommendedUpdate({
        app: () => ({
            SessionInfoForTab: async tab => { calls.push(['session', tab]); return session; },
            CheckForUpdate: async () => { calls.push(['check']); return info; },
            GetUpdatePhase: async () => phase,
        }),
        settings: () => current.settings,
        busy: () => busy,
        notify: (key, values) => calls.push(['notify', key, values]),
        download: async got => {
            calls.push(['download', got.version]);
            if (afterDownload) current.settings = afterDownload;
            return downloadError;
        },
        restart: async () => {
            calls.push(['restart']);
            if (restartError) throw new Error(restartError);
            return true;
        },
        unstage: () => calls.push(['unstage']),
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

test('auto-install turned off during the download keeps the staged update for a manual restart', async () => {
    const { check, calls } = harness({ afterDownload: { updates_auto_install: false } });
    assert.equal(await check('tab-1'), false);
    assert.deepEqual(calls.at(-1), ['notify', 'updater.softUpdateReady', { version: '0.6.3' }]);
    assert.ok(!calls.some(call => call[0] === 'restart' || call[0] === 'unstage'));
});

test('a restart rolled back to the old version unstages it so a later check can download again', async () => {
    const restored = harness({ restartError: 'launch failed', phase: 'restored' });
    assert.equal(await restored.check('tab-1'), false);
    assert.deepEqual(restored.calls.slice(-2), [['notify', 'updater.restartFailed', { error: new Error('launch failed') }], ['unstage']]);
    const kept = harness({ restartError: 'launch failed', phase: 'ready' });
    assert.equal(await kept.check('tab-1'), false);
    assert.ok(!kept.calls.some(call => call[0] === 'unstage'), 'a staged update that was not rolled back stays staged');
});
