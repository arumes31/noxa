import { test, expect } from "./fixtures.js";

test.beforeEach(async ({ page }) => {
    await page.route("**/voice-idle-test", route => route.fulfill({ contentType: "text/html", body: `<!doctype html><html lang="en"><head><link rel="stylesheet" href="/src/style.css"></head><body><div id="conn-pill" class="up"></div><footer><span id="voice-latency"></span></footer></body></html>` }));
    await page.goto("/voice-idle-test");
});

test("quiet playback has a neutral explanation and still shows real faults", async ({ page }, testInfo) => {
    await page.evaluate(async () => {
        const { createConnectionQuality } = await import("/src/connection-quality.js");
        window.go = { main: { App: { GetClientInfoForTab: async () => ({ ping_ms: 12 }) } } };
        const peer = { connectionState: "connected" };
        const state = { activeTabID: "one", serverGeneration: 1, myChannelID: 7, myClientID: "me", pc: peer };
        const report = { connection_state: "connected", output_state: "running", tracks: [{ sample_ms: 5000,
            audio_active: false, loss_percent: 0, discard_percent: 0, non_silent_concealment_percent: 8, buffer_ms: 400 }] };
        state.voiceTelemetry = { at: Date.now(), peer, scope: JSON.stringify(["one", 1, 7]), report };
        window.__idleReport = report;
        const quality = createConnectionQuality({ $: id => document.getElementById(id), state });
        window.__stopIdle = quality.stopQualitySampler;
        quality.startQualitySampler();
    });
    const badge = page.locator("#voice-playback-quality");
    await expect(badge).toHaveText("Voice: idle");
    await expect(badge).toHaveAttribute("data-quality", "idle");
    await badge.focus();
    await expect(page.getByRole("tooltip")).toContainText("No recent audible speech");
    await page.screenshot({ path: testInfo.outputPath("voice-idle.png") });
    await page.evaluate(() => { window.__idleReport.tracks[0].audio_active = true; });
    await expect(badge).toHaveText("Voice: poor");
    await page.evaluate(() => { window.__idleReport.tracks[0].audio_active = false; window.__idleReport.output_state = "suspended"; });
    await expect(badge).toHaveText("Voice: poor");
    await expect(page.getByRole("tooltip")).toContainText("Audio output is suspended");
    await page.evaluate(() => window.__stopIdle());
});

test("real Opus reception distinguishes a silent interval from an earlier tone", async ({ page }) => {
    test.setTimeout(45000);
    try {
        await page.evaluate(async () => {
            const { createRemoteAudioSource, createAudioLevelSampler } = await import("/src/audio.js");
            const { collectVoiceTelemetry } = await import("/src/voice-diagnostics.js");
            const sender = new RTCPeerConnection(), receiver = new RTCPeerConnection();
            const context = new AudioContext();
            const oscillator = context.createOscillator(), gain = context.createGain(), destination = context.createMediaStreamDestination();
            const state = { pc: receiver, activeTabID: "test", serverGeneration: 1, myChannelID: 7, clients: [], trackUsers: new Map() };
            window.__voicePeers = { sender, receiver, context, oscillator, gain, destination, state };
            window.__collectVoice = async () => {
                const stats = await receiver.getStats();
                return collectVoiceTelemetry(state, stats, window.__voiceBefore).tracks[0];
            };
            gain.gain.value = 0.2;
            oscillator.connect(gain).connect(destination);
            oscillator.start();
            await context.resume();
            sender.onicecandidate = event => { if (event.candidate) void receiver.addIceCandidate(event.candidate); };
            receiver.onicecandidate = event => { if (event.candidate) void sender.addIceCandidate(event.candidate); };
            receiver.ontrack = event => {
                // Exercise the actual muted media element + WebAudio path.
                // A zero output gain keeps the synthetic test tone inaudible.
                const source = createRemoteAudioSource(context, event.track);
                const sink = context.createGain();
                sink.gain.value = 0;
                source.src.connect(sink).connect(context.destination);
                window.__silentReceiveSink = { ...source, sink, sample: createAudioLevelSampler(context, source.src) };
                state.trackUsers.set(event.track.id, { client_id: "c-tone", track_id: "c-tone" });
            };
            sender.addTrack(destination.stream.getAudioTracks()[0]);
            await sender.setLocalDescription(await sender.createOffer());
            await receiver.setRemoteDescription(sender.localDescription);
            await receiver.setLocalDescription(await receiver.createAnswer());
            await sender.setRemoteDescription(receiver.localDescription);
        });
        await expect.poll(() => page.evaluate(() => window.__voicePeers.receiver.connectionState)).toBe("connected");
        // Independently establish decoded Opus tone. Chromium can report zero
        // RTP energy on this playback path, which must not imply silence.
        await expect.poll(() => page.evaluate(() => window.__silentReceiveSink?.sample())).toBeGreaterThan(0.05);
        await page.evaluate(async () => { window.__voiceBefore = await window.__voicePeers.receiver.getStats(); });
        await expect.poll(() => page.evaluate(async () => (await window.__collectVoice())?.sample_ms)).toBeGreaterThan(0);
        const unknown = await page.evaluate(async () => {
            const { collectVoiceTelemetry } = await import("/src/voice-diagnostics.js");
            const { receiver, state } = window.__voicePeers;
            const stats = await receiver.getStats();
            const incoming = [...stats.values()].find(row => row.type === "inbound-rtp" && row.kind === "audio");
            const previous = window.__voiceBefore.get(incoming.id);
            const energy = incoming.totalAudioEnergy - previous.totalAudioEnergy;
            const duration = incoming.totalSamplesDuration - previous.totalSamplesDuration;
            const track = collectVoiceTelemetry(state, stats, window.__voiceBefore).tracks[0];
            return { active: track.audio_active, codec: track.codec, expected: duration > 0 && energy / duration > 0.000001 ? true : null };
        });
        expect(unknown.codec).toBe("audio/opus");
        expect(unknown.active).toBe(unknown.expected);
        await page.evaluate(() => { window.__voicePeers.state.clients = [{ client_id: "c-tone", is_speaking: true }]; });
        await expect.poll(() => page.evaluate(async () => (await window.__collectVoice()).audio_active)).toBe(true);
        await page.evaluate(async () => {
            const { noteVoiceActivity } = await import("/src/voice-diagnostics.js");
            const { gain, state } = window.__voicePeers;
            gain.gain.value = 0;
            noteVoiceActivity(state, "c-tone");
            state.clients[0].is_speaking = false;
        });
        await expect.poll(() => page.evaluate(() => window.__silentReceiveSink.sample())).toBeLessThan(0.0001);
        // The earlier phrase remains active in the old interval even though
        // the current decoded samples and server speaking flag are now quiet.
        expect(await page.evaluate(async () => (await window.__collectVoice()).audio_active)).toBe(true);
        await page.evaluate(async () => { window.__voiceBefore = await window.__voicePeers.receiver.getStats(); });
        await expect.poll(() => page.evaluate(async () => (await window.__collectVoice()).audio_active),
            { intervals: [500], timeout: 10000 }).toBe(false);
    } finally {
        await page.evaluate(async () => {
            const peers = window.__voicePeers;
            if (!peers) return;
            const sink = window.__silentReceiveSink;
            if (sink) { sink.playback.pause(); sink.playback.srcObject = null; sink.src.disconnect(); sink.sink.disconnect(); }
            peers.sender.onicecandidate = peers.receiver.onicecandidate = null;
            peers.sender.close(); peers.receiver.close(); peers.oscillator.stop();
            peers.destination.stream.getTracks().forEach(track => track.stop());
            await peers.context.close();
        });
    }
});
