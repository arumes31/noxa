import { test, expect } from "./fixtures.js";

test("personal voice volume remains effective through the enabled limiter", async ({ page }, testInfo) => {
    await page.route("**/__voice_volume__", route => route.fulfill({ contentType: "text/html", body: "<!doctype html><title>Voice volume</title>" }));
    await page.goto("/__voice_volume__");
    const levels = await page.evaluate(async () => {
        const { makeLimiter, getUserVolume } = await import("/src/audio.js");
        const render = async (percent, amplitude = 0.3) => {
            window.__noxa = { state: { settings: { user_volumes: { speaker: percent } } } };
            const context = new OfflineAudioContext(1, 48000, 48000);
            const buffer = context.createBuffer(1, 48000, 48000), data = buffer.getChannelData(0);
            for (let i = 0; i < data.length; i++) data[i] = amplitude * Math.sin(2 * Math.PI * 440 * i / 48000);
            const source = context.createBufferSource(); source.buffer = buffer;
            const gain = context.createGain(); gain.gain.value = getUserVolume("speaker");
            const limiter = makeLimiter(context);
            source.connect(gain); gain.connect(limiter); limiter.connect(context.destination); source.start();
            const samples = (await context.startRendering()).getChannelData(0).subarray(12000);
            return { rms: Math.sqrt(samples.reduce((sum, sample) => sum + sample * sample, 0) / samples.length),
                peak: samples.reduce((max, sample) => Math.max(max, Math.abs(sample)), 0) };
        };
        return { muted: await render(0), quiet: await render(50), normal: await render(100), boosted: await render(200), overload: await render(200, 2) };
    });
    await testInfo.attach("measured-voice-levels", { body: JSON.stringify(levels, null, 2), contentType: "application/json" });
    expect(levels.muted.peak).toBe(0);
    expect(levels.quiet.rms / levels.normal.rms).toBeCloseTo(0.5, 1);
    expect(levels.boosted.rms / levels.normal.rms).toBeCloseTo(2, 1);
    expect(levels.overload.peak).toBeLessThan(1);
});
