import { test, expect } from "./fixtures.js";

test("personal voice volume remains effective through the enabled limiter", async ({ page }, testInfo) => {
    await page.route("**/__voice_volume__", route => route.fulfill({ contentType: "text/html", body: "<!doctype html><title>Voice volume</title>" }));
    await page.goto("/__voice_volume__");
    const levels = await page.evaluate(async () => {
        const { makeLimiter, registerUserChain, unregisterUserChain } = await import("/src/audio.js");
        const render = async (percent, amplitude = 0.03) => {
            window.__noxa = { state: { settings: { user_volumes: { speaker: percent } } } };
            const context = new OfflineAudioContext(1, 48000, 48000);
            const buffer = context.createBuffer(1, 48000, 48000), data = buffer.getChannelData(0);
            for (let i = 0; i < data.length; i++) data[i] = amplitude * Math.sin(2 * Math.PI * 440 * i / 48000);
            const source = context.createBufferSource(); source.buffer = buffer;
            const gain = context.createGain();
            registerUserChain("speaker", gain, context.createGain());
            const limiter = makeLimiter(context);
            source.connect(gain); gain.connect(limiter); limiter.connect(context.destination); source.start();
            const samples = (await context.startRendering()).getChannelData(0).subarray(12000);
            unregisterUserChain("speaker", gain);
            return { rms: Math.sqrt(samples.reduce((sum, sample) => sum + sample * sample, 0) / samples.length),
                peak: samples.reduce((max, sample) => Math.max(max, Math.abs(sample)), 0) };
        };
        return { muted: await render(0), quiet: await render(50), normal: await render(100), boosted: await render(200), overload: await render(200, 2) };
    });
    await testInfo.attach("measured-voice-levels", { body: JSON.stringify(levels, null, 2), contentType: "application/json" });
    expect(levels.muted.peak).toBe(0);
    expect(levels.quiet.rms / levels.normal.rms).toBeCloseTo(0.25, 1);
    expect(levels.boosted.rms / levels.normal.rms).toBeCloseTo(10, 1);
    expect(levels.overload.peak).toBeLessThan(1);
});

test("personal volume controls every active track of an identity and survives one session leaving", async ({ page }) => {
    await page.route("**/__voice_volume__", route => route.fulfill({ contentType: "text/html", body: "<!doctype html><title>Multiple voice sessions</title>" }));
    await page.goto("/__voice_volume__");
    const result = await page.evaluate(async () => {
        window.__noxa = { state: { settings: { user_volumes: {} } } };
        const audio = await import("/src/audio.js");
        const context = new OfflineAudioContext(1, 48000, 48000);
        const first = context.createGain(), second = context.createGain();
        audio.registerUserChain("same-user", first, context.createGain());
        audio.registerUserChain("same-user", second, context.createGain());
        audio.previewUserVolume("same-user", 0, "test");
        const muted = [first.gain.value, second.gain.value];
        audio.unregisterUserChain("same-user", first);
        audio.previewUserVolume("same-user", 200, "test");
        const boosted = second.gain.value;
        audio.clearUserVolumePreview("same-user", "test");
        const restored = second.gain.value;
        audio.unregisterUserChain("same-user", second);
        return { muted, boosted, restored };
    });
    expect(result).toEqual({ muted: [0, 0], boosted: 10, restored: 1 });
});
