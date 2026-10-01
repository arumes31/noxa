import test from 'node:test';
import assert from 'node:assert/strict';
import { createVoiceRecording } from '../src/voice-recording.js';

class Recorder {
    static isTypeSupported(type) { return type === 'audio/webm;codecs=opus'; }
    constructor() { this.state = 'inactive'; this.mimeType = 'audio/webm'; }
    start() { this.state = 'recording'; }
    stop() { this.state = 'inactive'; this.ondataavailable?.({ data: new Blob(['audio']) }); this.onstop?.(); }
}
test('recording returns an audio blob and releases its independent capture', async () => {
    let stopped = 0;
    const recorder = createVoiceRecording({ Recorder, capture: async () => ({ getTracks: () => [{ stop() { stopped++; } }] }) });
    await recorder.start();
    const result = await recorder.stop();
    assert.equal(await result.blob.text(), 'audio');
    assert.equal(result.extension, 'weba');
    assert.equal(stopped, 1);
    recorder.dispose();
});
test('closing during permission acquisition stops the late track without recording', async () => {
    let resolve, stopped = 0;
    const recorder = createVoiceRecording({ Recorder, capture: () => new Promise(r => { resolve = r; }) });
    const pending = recorder.start();
    recorder.dispose();
    resolve({ getTracks: () => [{ stop() { stopped++; } }] });
    await pending;
    assert.equal(stopped, 1);
});
test('oversized capture fails and releases the microphone', async () => {
    let stopped = 0;
    const recorder = createVoiceRecording({ Recorder, maxBytes: 2, capture: async () => ({ getTracks: () => [{ stop() { stopped++; } }] }) });
    await recorder.start();
    await assert.rejects(recorder.stop(), /size/i);
    assert.equal(stopped, 1);
});

test('recording continues beyond two minutes and stops at five minutes', async context => {
    context.mock.timers.enable({ apis: ['setTimeout'] });
    let stopped = 0, ready = 0;
    const recorder = createVoiceRecording({
        Recorder,
        capture: async () => ({ getTracks: () => [{ stop() { stopped++; } }] }),
        onReady: () => { ready++; },
    });
    context.after(() => recorder.dispose());
    await recorder.start();
    context.mock.timers.tick(120000);
    assert.equal(stopped, 0);
    assert.equal(ready, 0);
    context.mock.timers.tick(179999);
    assert.equal(stopped, 0);
    context.mock.timers.tick(1);
    assert.equal(stopped, 1);
    assert.equal(ready, 1);
    assert.equal(await (await recorder.stop()).blob.text(), 'audio');
});

test('recording meter uses its capture and releases audio nodes on discard', async context => {
    context.mock.timers.enable({ apis: ['setInterval', 'setTimeout'] });
    let closed = 0, disconnected = 0, stopped = 0;
    const levels = [], connections = [];
    const stream = { getTracks: () => [{ stop() { stopped++; } }] };
    const analyser = { fftSize: 0, getFloatTimeDomainData(samples) { samples.fill(0.1); }, disconnect() { disconnected++; } };
    const recording = createVoiceRecording({ Recorder, capture: async () => stream, onLevel: level => levels.push(level), createContext: () => ({
        createMediaStreamSource(received) { assert.equal(received, stream); return { connect(target) { connections.push(target); }, disconnect() { disconnected++; } }; },
        createAnalyser: () => analyser, resume: async () => {}, close: async () => { closed++; },
    }) });
    await recording.start();
    context.mock.timers.tick(100);
    assert.equal(levels.length, 1);
    assert.equal(levels[0].quality, 'good');
    assert.deepEqual(connections, [analyser]);
    recording.discard();
    context.mock.timers.tick(1000);
    assert.equal(levels.length, 1);
    assert.equal(disconnected, 2);
    assert.equal(closed, 1);
    assert.equal(stopped, 1);
});

test('meter failure leaves recording usable', async () => {
    const levels = [];
    const recording = createVoiceRecording({ Recorder, capture: async () => ({ getTracks: () => [{ stop() {} }] }),
        onLevel: level => levels.push(level), createContext: () => { throw new Error('Unavailable'); } });
    await recording.start();
    assert.deepEqual(levels, [null]);
    assert.equal(await (await recording.stop()).blob.text(), 'audio');
    recording.dispose();
});
