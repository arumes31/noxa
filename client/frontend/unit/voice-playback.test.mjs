import assert from 'node:assert/strict';
import test from 'node:test';
import { waveformPeaks, boundedPlaybackHistory } from '../src/voice-playback-state.js';
test('waveforms combine stereo peaks without treating silence as speech', () => {
    assert.deepEqual(waveformPeaks([new Float32Array(8)], 4), [0,0,0,0]);
    assert.deepEqual(waveformPeaks([new Float32Array([0,0,0,0]), new Float32Array([0,.5,0,1])], 2), [.5,1]);
});
test('resume history rejects invalid data and expires and bounds old entries', () => {
    const entries = Array.from({length:120}, (_,i) => ({key:String(i), position:i, speed:1, at:100000+i}));
    entries.push({key:'bad',position:-1,speed:50,at:110000});
    const got = boundedPlaybackHistory(entries, 120000);
    assert.equal(got.length,100); assert.equal(got[0].key,'119');
    assert.equal(boundedPlaybackHistory(entries, 40*86400000).length,0);
});
