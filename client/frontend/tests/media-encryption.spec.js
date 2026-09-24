import { expect, test } from "./fixtures.js";

test("maintained media worker encrypts VP8 and fails closed for a recipient without the key", async ({ page }) => {
    await page.route("**/__encryption_test__", route => route.fulfill({ contentType: "text/html", body: '<!doctype html><title>Encrypted media test</title>' }));
    await page.goto("/__encryption_test__");
    await page.evaluate(async () => {
        const { MediaCryptor } = await import("/src/media-cryptor.js");
        const errors = []; window.__encryptionErrors = errors;
        const sender = new MediaCryptor(error => errors.push(String(error)));
        const receiver = new MediaCryptor(error => errors.push(String(error)));
        window.__cryptors = [sender, receiver];
        await Promise.all([sender.ready, receiver.ready]);
        const material = crypto.getRandomValues(new Uint8Array(32));
        window.__material = material;
        await sender.setKey("alice/camera/epoch-1", material, 0);
        const canvas = document.createElement("canvas"); canvas.width = 160; canvas.height = 90;
        const context = canvas.getContext("2d"); let frame = 0;
        window.__paint = setInterval(() => { context.fillStyle = ++frame % 2 ? "#1fd288" : "#4455dd"; context.fillRect(0, 0, 160, 90); }, 50);
        const stream = canvas.captureStream(15); window.__capture = stream;
        const source = new RTCPeerConnection({ encodedInsertableStreams: true });
        const sink = new RTCPeerConnection({ encodedInsertableStreams: true });
        window.__pcs = [source, sink];
        source.onicecandidate = event => { if (event.candidate) void sink.addIceCandidate(event.candidate); };
        sink.onicecandidate = event => { if (event.candidate) void source.addIceCandidate(event.candidate); };
        const transceiver = source.addTransceiver(stream.getVideoTracks()[0], { direction: "sendonly", streams: [stream] });
        const vp8 = RTCRtpSender.getCapabilities("video").codecs.filter(codec => codec.mimeType.toLowerCase() === "video/vp8");
        transceiver.setCodecPreferences(vp8);
        sender.attachSender(transceiver.sender, "alice/camera/epoch-1", "camera", "vp8");
        sink.ontrack = event => {
            receiver.attachReceiver(event.receiver, "alice/camera/epoch-1", "camera", "vp8");
            const video = document.createElement("video"); video.autoplay = true; video.muted = true; video.srcObject = event.streams[0]; document.body.append(video);
        };
        await source.setLocalDescription(await source.createOffer()); await sink.setRemoteDescription(source.localDescription);
        await sink.setLocalDescription(await sink.createAnswer()); await source.setRemoteDescription(sink.localDescription);
    });
    await expect.poll(() => page.evaluate(() => window.__pcs.map(pc => pc.connectionState))).toEqual(["connected", "connected"]);
    // Keyless receivers get ciphertext packets but no decodable video.
    await expect.poll(() => page.evaluate(async () => [...(await window.__pcs[1].getStats()).values()].filter(s => s.type === "inbound-rtp").reduce((n, s) => n + (s.bytesReceived || 0), 0))).toBeGreaterThan(0);
    expect(await page.locator("video").evaluate(video => video.videoWidth)).toBe(0);
    await page.evaluate(() => window.__cryptors[1].setKey("alice/camera/epoch-1", window.__material, 0));
    await expect.poll(() => page.locator("video").evaluate(video => video.videoWidth), { timeout: 10000 }).toBe(160);
    const preview = await page.evaluate(async () => {
        const plain = new TextEncoder().encode("preview bytes");
        const sealed = await window.__cryptors[0].encryptData("alice/camera/epoch-1", plain);
        const opened = await window.__cryptors[1].decryptData("alice/camera/epoch-1", sealed);
        const bad = { ...sealed, payload: sealed.payload.slice(0) }; bad.payload[0] ^= 1;
        let tampered = false; try { await window.__cryptors[1].decryptData("alice/camera/epoch-1", bad); } catch { tampered = true; }
        return { plain: new TextDecoder().decode(opened), opaque: new TextDecoder().decode(sealed.payload) !== "preview bytes", tampered };
    });
    expect(preview).toEqual({ plain: "preview bytes", opaque: true, tampered: true });
    await page.evaluate(() => { window.__cryptors.forEach(c => c.close()); window.__pcs.forEach(pc => pc.close()); window.__capture.getTracks().forEach(t => t.stop()); clearInterval(window.__paint); });
});
