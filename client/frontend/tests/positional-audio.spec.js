import { expect, test } from "@playwright/test";

test("real Web Audio pans voice, preserves mute, and bypasses stale or disabled positions", async ({ page }) => {
    await page.route("**/__spatial_test__", route => route.fulfill({ contentType: "text/html", body: "<!doctype html><title>Spatial audio test</title>" }));
    await page.goto("/__spatial_test__");
    const results = await page.evaluate(async () => {
        const { SpatialVoice } = await import("/src/positional-audio.js");
        const render = async (x, enabled, muted, stale = false) => {
            const ctx = new OfflineAudioContext(2, 24000, 48000);
            const tone = ctx.createOscillator(); tone.frequency.value = 400;
            const mute = ctx.createGain(); mute.gain.value = muted ? 0 : 1;
            tone.connect(mute);
            const spatial = new SpatialVoice(ctx);
            spatial.attach("peer", mute, ctx.destination);
            spatial.local({ x: 0, y: 0, z: 0, context: "map", forward: [0, 0, -1], up: [0, 1, 0] }, 1000);
            spatial.remote("peer", { x, y: 0, z: 0, context: "map" }, 1000);
            spatial.update(enabled, stale ? 5000 : 1000);
            tone.start();
            const audio = await Promise.race([ctx.startRendering(), new Promise((_, reject) => setTimeout(() => reject(new Error(`audio render timed out: x=${x} enabled=${enabled} muted=${muted} stale=${stale}`)), 4000))]);
            return [0, 1].map(channel => audio.getChannelData(channel).slice(12000).reduce((sum, sample) => sum + sample * sample, 0));
        };
        return { left: await render(-4, true, false), right: await render(4, true, false), off: await render(4, false, false), muted: await render(4, true, true), stale: await render(4, true, false, true) };
    });
    expect(results.left[0]).toBeGreaterThan(results.left[1] * 1.2);
    expect(results.right[1]).toBeGreaterThan(results.right[0] * 1.2);
    expect(results.off[0]).toBeGreaterThan(0);
    expect(results.off[0]).toBeCloseTo(results.off[1], 4);
    expect(results.stale[0]).toBeCloseTo(results.stale[1], 4);
    expect(results.muted).toEqual([0, 0]);
});
