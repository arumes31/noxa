import { expect, test } from "./fixtures.js";

// Encrypted channel media (Task 2) in a real Chromium worker on BOTH transform
// APIs, including cross-API pairs: frames are encrypted by the pinned LiveKit
// FrameCryptor behind the noXa wrapper; a receiver without the key decodes
// nothing; epochs rotate without interruption; a retired epoch stops output.
for (const [senderAPI, receiverAPI] of [["script", "script"], ["streams", "streams"], ["script", "streams"], ["streams", "script"]]) {
    test(`encrypted VP8 + Opus loopback: ${senderAPI} sender -> ${receiverAPI} receiver`, async ({ page }) => {
        await page.route("**/__media_e2ee__", route => route.fulfill({ contentType: "text/html", body: "<!doctype html><title>Encrypted media</title>" }));
        await page.goto("/__media_e2ee__");
        await page.evaluate(async ([senderAPI, receiverAPI]) => {
            const { MediaE2EE } = await import("/src/media-e2ee.js");
            const errors = window.__errors = [];
            const tx = new MediaE2EE({ api: senderAPI, onError: e => errors.push(String(e)) });
            const rx = new MediaE2EE({ api: receiverAPI, onError: e => errors.push(String(e)) });
            await Promise.all([tx.ready, rx.ready]);
            window.__sessions = [tx, rx];
            const canvas = document.createElement("canvas"); canvas.width = 160; canvas.height = 90;
            const ctx = canvas.getContext("2d"); let n = 0;
            window.__paint = setInterval(() => { ctx.fillStyle = ++n % 2 ? "#1fd288" : "#4455dd"; ctx.fillRect(0, 0, 160, 90); }, 50);
            const audio = new AudioContext(); const osc = audio.createOscillator(); const dest = audio.createMediaStreamDestination(); osc.connect(dest); osc.start();
            const stream = new MediaStream([canvas.captureStream(15).getVideoTracks()[0], dest.stream.getAudioTracks()[0]]);
            window.__capture = stream;
            const source = tx.protectPeer(new RTCPeerConnection(tx.peerConfig()));
            const sink = rx.protectPeer(new RTCPeerConnection(rx.peerConfig()));
            window.__pcs = [source, sink];
            source.onicecandidate = e => { if (e.candidate) void sink.addIceCandidate(e.candidate); };
            sink.onicecandidate = e => { if (e.candidate) void source.addIceCandidate(e.candidate); };
            const video = source.addTransceiver(stream.getVideoTracks()[0], { direction: "sendonly", streams: [stream] });
            video.setCodecPreferences(RTCRtpSender.getCapabilities("video").codecs.filter(c => /vp8|rtx/i.test(c.mimeType)));
            const mic = source.addTransceiver(stream.getAudioTracks()[0], { direction: "sendonly", streams: [stream] });
            await tx.bind(video.sender, "self|cam"); await tx.bind(mic.sender, "self|mic");
            window.__keys = { cam: crypto.getRandomValues(new Uint8Array(32)), mic: crypto.getRandomValues(new Uint8Array(32)) };
            await tx.setKey("self|cam", 100, 0, window.__keys.cam); await tx.setKey("self|mic", 100, 0, window.__keys.mic);
            await tx.activate("self|cam", 100); await tx.activate("self|mic", 100);
            sink.ontrack = event => {
                if (event.track.kind !== "video") return;
                const el = document.createElement("video"); el.autoplay = true; el.muted = true; el.srcObject = new MediaStream([event.track]); document.body.append(el);
            };
            await source.setLocalDescription(await source.createOffer()); await sink.setRemoteDescription(source.localDescription);
            window.__receivers = sink.getTransceivers().map(t => t.receiver);
            for (const r of window.__receivers) await rx.bind(r, r.track.kind === "video" ? "pub|cam" : "pub|mic");
            await sink.setLocalDescription(await sink.createAnswer()); await source.setRemoteDescription(sink.localDescription);
        }, [senderAPI, receiverAPI]);
        await expect.poll(() => page.evaluate(() => window.__pcs.map(pc => pc.connectionState))).toEqual(["connected", "connected"]);
        const stats = () => page.evaluate(async () => {
            const [tx, rx] = window.__sessions;
            const out = { width: document.querySelector("video")?.videoWidth || 0 };
            for (const r of window.__receivers) out[r.track.kind] = (await rx.stats(r)).endpoint;
            out.sent = (await tx.stats()).encrypted; out.unencrypted = (await tx.stats()).dropped.unencrypted;
            return out;
        });
        // Packets arrive, but nothing decrypts or decodes without the keys.
        await expect.poll(async () => (await stats()).video?.dropped?.nokey || 0).toBeGreaterThan(5);
        expect((await stats()).width).toBe(0);
        expect((await stats()).audio.frames).toBe(0);
        await page.evaluate(async () => { const rx = window.__sessions[1]; await rx.setKey("pub|cam", 100, 0, window.__keys.cam); await rx.setKey("pub|mic", 100, 0, window.__keys.mic); });
        await expect.poll(async () => (await stats()).width, { timeout: 10000 }).toBe(160);
        await expect.poll(async () => (await stats()).audio.frames).toBeGreaterThan(20);
        // Rotate to a new epoch at the next key slot: decryption continues.
        await page.evaluate(async () => {
            const [tx, rx] = window.__sessions;
            const cam = crypto.getRandomValues(new Uint8Array(32)), mic = crypto.getRandomValues(new Uint8Array(32));
            await rx.setKey("pub|cam", 101, 1, cam); await rx.setKey("pub|mic", 101, 1, mic);
            await tx.setKey("self|cam", 101, 1, cam); await tx.setKey("self|mic", 101, 1, mic);
            await tx.activate("self|cam", 101); await tx.activate("self|mic", 101);
            await tx.retire("self|cam", 100); await tx.retire("self|mic", 100);
            window.__afterRotate = (await rx.stats(window.__receivers.find(r => r.track.kind === "audio"))).endpoint.frames;
        });
        await expect.poll(async () => (await stats()).audio.frames - await page.evaluate(() => window.__afterRotate)).toBeGreaterThan(20);
        // Retiring the receiver's current epoch stops all output for good.
        await page.evaluate(async () => { const rx = window.__sessions[1]; await rx.retire("pub|mic", 101); window.__retiredAt = (await rx.stats(window.__receivers.find(r => r.track.kind === "audio"))).endpoint.frames; });
        await page.waitForTimeout(1000);
        expect((await stats()).audio.frames).toBe(await page.evaluate(() => window.__retiredAt));
        const reinstall = await page.evaluate(async () => { try { await window.__sessions[1].setKey("pub|mic", 101, 1, new Uint8Array(32)); return "installed"; } catch (e) { return String(e.message); } });
        expect(reinstall).toMatch(/retired/);
        const final = await stats();
        expect(final.sent).toBeGreaterThan(50);
        expect(final.unencrypted || 0).toBe(0);
        expect(await page.evaluate(() => window.__errors)).toEqual([]);
        await page.evaluate(() => { window.__sessions.forEach(s => s.close()); window.__pcs.forEach(pc => pc.close()); window.__capture.getTracks().forEach(t => t.stop()); clearInterval(window.__paint); });
    });
}

test("a browser without encoded transforms refuses encrypted media explicitly", async ({ page }) => {
    await page.route("**/__media_e2ee__", route => route.fulfill({ contentType: "text/html", body: "<!doctype html><title>Encrypted media</title>" }));
    await page.goto("/__media_e2ee__");
    const result = await page.evaluate(async () => {
        delete window.RTCRtpScriptTransform;
        delete RTCRtpSender.prototype.createEncodedStreams;
        const { MediaE2EE } = await import("/src/media-e2ee.js");
        try { new MediaE2EE(); return "created"; } catch (error) { return `${error.name}:${error.reason}`; }
    });
    expect(result).toBe("MediaE2EEUnsupported:transform");
});
