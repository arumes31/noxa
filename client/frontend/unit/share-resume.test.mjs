import assert from 'node:assert/strict';
import test from 'node:test';
import { createShareResume } from '../src/share-resume.js';

function fixture() {
    let now = 1, expiry, stopped = 0, studioStopped = 0;
    const track = new EventTarget(); Object.assign(track, { readyState: 'live', enabled: true });
    const audio = { readyState: 'live', enabled: false };
    const stream = { getTracks: () => [track, audio], getVideoTracks: () => [track] };
    const scope = { tabID: 'original', channelID: 42, uid: 'account' };
    const options = { preset: 'hd', surface: 'window', audioMode: 'none', studio: { onEnded: () => studioStopped++ } };
    const controller = createShareResume({ stop: value => { assert.equal(value, stream); stopped++; track.readyState = 'ended'; },
        now: () => now, schedule: fn => { expiry = fn; }, cancelTimer() {} });
    controller.remember(stream, options, scope);
    return { controller, stream, track, audio, scope, options, stopped: () => stopped, studioStopped: () => studioStopped, expire: () => { now += 120001; expiry(); } };
}

test('reconnect retains the exact capture and restores prior track enablement once', () => {
    const f = fixture(); assert.equal(f.controller.pause(f.stream), f.stream);
    assert.equal(f.track.enabled, false); assert.equal(f.audio.enabled, false); assert.equal(f.stopped(), 0);
    const resumed = f.controller.take(f.scope);
    assert.equal(resumed.stream, f.stream); assert.equal(resumed.options, f.options);
    assert.equal(f.track.enabled, true); assert.equal(f.audio.enabled, false);
    assert.equal(f.controller.take(f.scope), null); assert.equal(f.stopped(), 0);
});

test('tab, channel and account changes stop retained capture instead of republishing', () => {
    for (const key of ['tabID', 'channelID', 'uid']) {
        const f = fixture(); f.controller.pause(f.stream);
        assert.equal(f.controller.take({ ...f.scope, [key]: 'different' }), null);
        assert.equal(f.stopped(), 1); assert.equal(f.studioStopped(), 1);
    }
});

test('manual cancellation, expiration and source ending release capture exactly once', () => {
    for (const reason of ['cancel', 'expire', 'ended']) {
        const f = fixture(); f.controller.pause(f.stream);
        if (reason === 'cancel') f.controller.cancel();
        if (reason === 'expire') f.expire();
        if (reason === 'ended') { f.track.readyState = 'ended'; f.track.dispatchEvent(new Event('ended')); }
        f.controller.cancel(); assert.equal(f.controller.take(f.scope), null);
        assert.equal(f.stopped(), 1); assert.equal(f.studioStopped(), 1);
    }
});

test('unknown, stopped and partially starting captures cannot be resumed', () => {
    const f = fixture(); assert.equal(f.controller.pause({}), null);
    f.track.readyState = 'ended'; assert.equal(f.controller.pause(f.stream), null);
    assert.equal(f.controller.settings(f.stream), null);
});
