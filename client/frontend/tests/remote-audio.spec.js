import { expect, test } from "@playwright/test";

// Use real WebRTC receivers: a synthetic local MediaStream bypasses the
// Chromium decoder-start issue and can pass while remote callers stay silent.
test("remote WebRTC audio reaches the processing graph", async ({ page }) => {
    await page.route("**/audio-check", route => route.fulfill({ contentType: "text/html", body: "<!doctype html><body>Audio check</body>" }));
    await page.goto("/audio-check");
    await page.locator("body").click({ position: { x: 5, y: 5 } });
    await page.evaluate(async () => {
        const { createRemoteAudioSource } = await import("/src/audio.js");
        const sender = new RTCPeerConnection();
        const receiver = new RTCPeerConnection();
        sender.onicecandidate = e => { if (e.candidate) void receiver.addIceCandidate(e.candidate); };
        receiver.onicecandidate = e => { if (e.candidate) void sender.addIceCandidate(e.candidate); };
        const context = new AudioContext();
        // Drive the graph without playing the synthetic test tone on hardware.
        await context.setSinkId({ type: "none" });
        await context.resume();
        const oscillator = context.createOscillator();
        const volume = context.createGain();
        volume.gain.value = 0.05;
        const stream = context.createMediaStreamDestination();
        oscillator.connect(volume).connect(stream);
        oscillator.start();
        sender.addTrack(stream.stream.getAudioTracks()[0], stream.stream);
        window.__remoteAudioCheck = { context, sender, receiver, oscillator };
        receiver.ontrack = ({ track }) => {
            const source = createRemoteAudioSource(context, track);
            const analyser = context.createAnalyser();
            source.src.connect(analyser).connect(context.destination);
            Object.assign(window.__remoteAudioCheck, { ...source, analyser });
        };
        const offer = await sender.createOffer();
        await sender.setLocalDescription(offer);
        await receiver.setRemoteDescription(offer);
        const answer = await receiver.createAnswer();
        await receiver.setLocalDescription(answer);
        await sender.setRemoteDescription(answer);
    });
    await expect.poll(() => page.evaluate(() => {
        const analyser = window.__remoteAudioCheck.analyser;
        if (!analyser) return 0;
        const samples = new Float32Array(analyser.fftSize);
        analyser.getFloatTimeDomainData(samples);
        return Math.sqrt(samples.reduce((sum, value) => sum + value * value, 0) / samples.length);
    })).toBeGreaterThan(0.01);
    expect(await page.evaluate(() => window.__remoteAudioCheck.playback.muted)).toBe(true);
    await page.evaluate(async () => {
        const { sender, receiver, context, oscillator, playback, src } = window.__remoteAudioCheck;
        playback.pause();
        playback.srcObject = null;
        src.disconnect();
        oscillator.stop();
        sender.close();
        receiver.close();
        await context.close();
    });
});
