import { test, expect } from "./fixtures.js";

test.beforeEach(async ({ page }) => {
    await page.route("**/voice-gate-test", route => route.fulfill({ contentType: "text/html", body: "<!doctype html><body>Microphone test</body>" }));
    await page.goto("/voice-gate-test");
    await page.locator("body").click({ position: { x: 1, y: 1 } });
});

test("audio-thread VAD preserves a quiet word onset while the UI thread is busy", async ({ page }) => {
    const result = await page.evaluate(async () => {
        const { createMicrophoneGate, disposeMicrophoneGate, updateMicrophoneGate } = await import("/src/voice-gate.js");
        const ctx = new AudioContext();
        const source = ctx.createConstantSource(), gain = ctx.createGain();
        const destination = ctx.createMediaStreamDestination();
        source.connect(gain).connect(destination); gain.gain.value = 0; source.start();
        await ctx.resume();
        const raw = destination.stream.getAudioTracks()[0];
        const sent = await createMicrophoneGate(raw, () => ({ settings: { activation_mode: "vad", vad_threshold: 50 } }));
        updateMicrophoneGate(raw);
        const code = `class Recorder extends AudioWorkletProcessor {
            constructor() { super(); this.samples = []; }
            process(inputs) {
                this.samples.push(...(inputs[0][0] || new Float32Array(128)));
                if (this.samples.length >= sampleRate * 1.2) { this.port.postMessage(this.samples); return false; }
                return true;
            }
        } registerProcessor('recorder', Recorder);`;
        const url = URL.createObjectURL(new Blob([code], { type: "text/javascript" }));
        try {
            await ctx.audioWorklet.addModule(url);
            const recorder = new AudioWorkletNode(ctx, "recorder");
            const received = new Promise(resolve => { recorder.port.onmessage = ({ data }) => resolve(data); });
            const input = ctx.createMediaStreamSource(new MediaStream([sent]));
            const silent = ctx.createGain(); silent.gain.value = 0;
            input.connect(recorder).connect(silent).connect(ctx.destination);
            const start = ctx.currentTime + 0.2;
            gain.gain.setValueAtTime(0.04, start);
            gain.gain.setValueAtTime(0.4, start + 0.06);
            gain.gain.setValueAtTime(0, start + 0.3);
            // Deliberately prevent all main-thread VAD polling from running.
            const until = performance.now() + 1000;
            while (performance.now() < until) { /* simulated busy UI */ }
            const samples = await received;
            const loud = samples.findIndex(value => value > 0.2);
            const quiet = samples.slice(0, loud).filter(value => value > 0.025 && value < 0.06).length;
            input.disconnect(); recorder.disconnect(); recorder.port.close(); silent.disconnect();
            return { loud, quietMs: quiet / ctx.sampleRate * 1000 };
        } finally {
            URL.revokeObjectURL(url); disposeMicrophoneGate(raw); raw.stop(); source.stop(); await ctx.close();
        }
    });
    expect(result.loud).toBeGreaterThan(0);
    expect(result.quietMs).toBeGreaterThan(40);
});

test("processed microphones stay private through mute, deafen, replacement and teardown", async ({ page }) => {
    await page.evaluate(async () => {
        const api = await import("/src/voice-gate.js");
        const ctx = new AudioContext(), tone = ctx.createOscillator(), output = ctx.createMediaStreamDestination();
        tone.connect(output); tone.start(); await ctx.resume();
        const raw = output.stream.getAudioTracks()[0];
        const state = { settings: { activation_mode: "vad", vad_threshold: 1 }, muted: false, deafened: false };
        const updates = [];
        const sent = await api.createMicrophoneGate(raw, () => state, (active, _level, track) => updates.push({ active, track }));
        window.__gate = { api, ctx, tone, raw, sent, state, updates };
    });
    expect(await page.evaluate(() => window.__gate.sent.enabled)).toBe(false);
    await page.evaluate(() => { const g = window.__gate; g.api.updateMicrophoneGate(g.raw); });
    await expect.poll(() => page.evaluate(() => window.__gate.updates.some(value => value.active))).toBe(true);
    for (const flag of ["muted", "deafened"]) {
        expect(await page.evaluate(flag => {
            const g = window.__gate; g.state[flag] = true; g.api.updateMicrophoneGate(g.raw);
            return g.sent.enabled;
        }, flag)).toBe(false);
        await page.evaluate(flag => { const g = window.__gate; g.state[flag] = false; g.api.updateMicrophoneGate(g.raw); }, flag);
        await expect.poll(() => page.evaluate(() => window.__gate.sent.enabled)).toBe(true);
    }
    await page.evaluate(async () => {
        const g = window.__gate;
        g.next = g.raw.clone();
        g.nextSent = await g.api.replaceMicrophoneGate(g.raw, g.next);
    });
    expect(await page.evaluate(() => window.__gate.nextSent.enabled)).toBe(false);
    await page.evaluate(() => {
        const g = window.__gate; g.api.disposeMicrophoneGate(g.raw); g.raw.stop();
        g.api.updateMicrophoneGate(g.next);
    });
    await expect.poll(() => page.evaluate(() => {
        const g = window.__gate; return g.updates.some(value => value.active && value.track === g.next);
    })).toBe(true);
    expect(await page.evaluate(async () => {
        const g = window.__gate; g.api.disposeMicrophoneGate(g.next); g.next.stop();
        g.tone.stop(); await g.ctx.close();
        return [g.sent.readyState, g.nextSent.readyState, g.sent.enabled, g.nextSent.enabled];
    })).toEqual(["ended", "ended", false, false]);
});
