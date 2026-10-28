import { test, expect, chooseShareOption } from "./fixtures.js";

// Native control replies and capability evidence are synthetic. The publication
// lifecycle, negotiation callback, RTP sender, encoders and receiver are real.
// A powerEfficient capability stub is not evidence of actual GPU execution.
// Encrypted channel media (Task 2) publishes VP8 only: even a server that
// supports codec refresh never moves an encrypted publication to H.264.
for (const supported of [true, false]) test(`encrypted live publication keeps VP8 ${supported ? "although the server offers H264 refresh" : "on a server without refresh"}`, async ({ page }) => {
    await page.route("**/codec-refresh-fixture", route => route.fulfill({ contentType: "text/html", body: `<!doctype html>
        <button id="voice-screen">Share</button><button id="voice-video">Camera</button>
        <video id="local-video"></video><div id="mic-status"></div><button id="ptt-btn">Talk</button>
        <section id="sharing-status" hidden></section>` }));
    await page.goto("/codec-refresh-fixture");
    await page.evaluate(async supported => {
        const pc = new RTCPeerConnection({ iceServers: [] });
        const receiver = new RTCPeerConnection({ iceServers: [] });
        const gather = peer => peer.iceGatheringState === "complete" ? Promise.resolve() : new Promise(resolve => {
            const changed = () => { if (peer.iceGatheringState === "complete") { peer.removeEventListener("icegatheringstatechange", changed); resolve(); } };
            peer.addEventListener("icegatheringstatechange", changed);
        });
        receiver.ontrack = event => {
            const video = document.createElement("video");
            video.muted = true; video.autoplay = true; video.srcObject = new MediaStream([event.track]);
            document.body.append(video);
        };
        const canvas = document.createElement("canvas"); canvas.width = 640; canvas.height = 360;
        const context = canvas.getContext("2d"); let frame = 0;
        const timer = setInterval(() => {
            context.fillStyle = `hsl(${frame++ % 360} 50% 40%)`; context.fillRect(0, 0, 640, 360);
            context.fillStyle = "white"; context.fillRect(frame % 600, 100, 40, 40);
        }, 33);
        const audio = new AudioContext(), oscillator = audio.createOscillator(), destination = audio.createMediaStreamDestination();
        oscillator.connect(destination); oscillator.start();
        pc.addTransceiver(destination.stream.getAudioTracks()[0], { direction: "sendonly", streams: [destination.stream] });
        const state = { pc, localStream: destination.stream, serverGeneration: 1, sessionGeneration: 1,
            activeTabID: "a", settings: {}, myChannelID: 1, myClientID: "self", clients: [] };
        const fixture = window.codecFixture = { pc, receiver, audio, queries: 0, healthy: false, requests: [], notices: [], selected: "vp8", offers: 0 };
        window.__noxa = { state, $: id => document.getElementById(id), sysMsg: message => fixture.notices.push(message), toast: () => {} };
        Object.defineProperty(navigator.mediaCapabilities, "encodingInfo", { configurable: true, value: async () => {
            fixture.queries++;
            if (!fixture.healthy) throw new Error("transient native capability failure");
            return { supported: true, powerEfficient: true };
        } });
        Object.defineProperty(navigator.mediaDevices, "getDisplayMedia", { configurable: true, value: async () => canvas.captureStream(30) });
        window.go = { main: { App: {
            SupportsStreamSourceQualityForTab: async () => true,
            SupportsStreamCodecForTab: async () => true,
            SupportsStreamCodecRefreshForTab: async () => supported,
            WebRTCOfferForTab: async () => {
                fixture.offers++;
                await gather(pc);
                await receiver.setRemoteDescription(pc.localDescription);
                await receiver.setLocalDescription(await receiver.createAnswer());
                await gather(receiver);
                return receiver.localDescription.sdp;
            },
            VideoStreamControlForTab: async (_tab, body) => {
                if (body.action !== "publish") return {};
                fixture.requests.push(structuredClone(body));
                if (body.generation !== "0" && body.active && body.codecs?.includes("h264")) fixture.selected = "h264";
                return { generation: "42", upload_active: true, codec: fixture.selected };
            },
        } } };
        window.go.main.App.WebRTCOfferSecureForTab = () => window.go.main.App.WebRTCOfferForTab();
        // The in-page receiver has no transform: the session double stands in.
        const channelMedia = await import("/src/channel-media.js");
        channelMedia.useChannelMediaSession({ active: true, identity: { channelID: 1 }, stop() {}, setRoster() {}, bindSenders: async () => {}, syncReceivers: async () => {}, publicationsChanged() {} });
        const { createVideoPublication } = await import("/src/video-publication.js");
        const publication = createVideoPublication({ policy: { lowBandwidth: false } });
        const { publicationSnapshot, reconcilePublicationUploads, stopPublications } = await import("/src/stream-publication.js");
        (await import("/src/modal.js")).initModalSystem();
        document.getElementById("voice-screen").onclick = () => { void audio.resume(); void publication.shareToggle(); };
        fixture.refresh = () => reconcilePublicationUploads(publicationSnapshot(), [{ publisher_id: "self", slot: "screen", generation: "42", upload_active: true, codec: fixture.selected }]);
        fixture.read = async () => {
            const stats = await receiver.getStats();
            const rows = [...stats.values()];
            const audio = rows.find(row => row.type === "inbound-rtp" && row.kind === "audio" && row.packetsReceived > 0);
            const frames = mime => rows.filter(row => row.type === "inbound-rtp" && row.kind === "video" && stats.get(row.codecId)?.mimeType === mime)
                .reduce((sum, row) => sum + (row.framesDecoded || 0), 0);
            return { vp8: frames("video/VP8"), h264: frames("video/H264"), audioId: audio?.id,
                audioPackets: audio?.packetsReceived || 0, audioEmitted: audio?.jitterBufferEmittedCount };
        };
        const originalNow = Date.now;
        fixture.advance = () => { Date.now = () => originalNow() + 31000; fixture.healthy = true; fixture.refresh(); };
        fixture.cleanup = async () => {
            Date.now = originalNow; clearInterval(timer); stopPublications();
            (await import("/src/share-status.js")).stopShareStatus();
            for (const track of [...state.localStream.getTracks(), ...(state.shareStream?.getTracks() || [])]) track.stop();
            pc.close(); receiver.close(); await audio.close();
        };
    }, supported);
    try {
        await page.locator("#voice-screen").click();
        await chooseShareOption(page, "preset", "original");
        await page.getByRole("button", { name: "Choose screen or window…", exact: true }).click();
        await page.getByRole("button", { name: "Start sharing", exact: true }).click();
        await expect.poll(() => page.evaluate(async () => (await codecFixture.read()).vp8)).toBeGreaterThan(20);
        const before = await page.evaluate(async () => {
            const state = __noxa.state;
            codecFixture.original = { pc: state.pc, transceiver: state.shareVideoTransceiver, sender: state.shareVideoTransceiver.sender,
                track: state.shareStream.getVideoTracks()[0], audioSender: state.pc.getSenders().find(sender => sender.track?.kind === "audio") };
            return { ...await codecFixture.read(), queries: codecFixture.queries, offers: codecFixture.offers, requests: codecFixture.requests };
        });
        expect(before.audioPackets).toBeGreaterThan(0);
        expect(before.audioEmitted).toBeGreaterThan(0);
        expect(before.queries).toBe(0); // encrypted media never probes H.264/HEVC hardware
        expect(before.requests).toHaveLength(1);
        expect(before.requests[0]).toMatchObject({ generation: "0", codecs: ["vp8"], quality_mode: "source" });
        await page.evaluate(() => codecFixture.advance());
        await page.evaluate(() => codecFixture.refresh());
        await expect.poll(() => page.evaluate(async () => (await codecFixture.read()).vp8)).toBeGreaterThan(before.vp8 + 20);
        expect(await page.evaluate(async () => (await codecFixture.read()).h264)).toBe(0);
        expect(await page.evaluate(() => ({ requests: codecFixture.requests.length, offers: codecFixture.offers })))
            .toEqual({ requests: 1, offers: before.offers });
        const after = await page.evaluate(async () => {
            const state = __noxa.state, original = codecFixture.original, transceiver = state.shareVideoTransceiver;
            return { ...await codecFixture.read(), same: state.pc === original.pc && transceiver === original.transceiver && transceiver.sender === original.sender &&
                transceiver.sender.track === original.track && state.pc.getSenders().includes(original.audioSender),
            videos: state.pc.getSenders().filter(sender => sender.track?.kind === "video").length,
            encodings: transceiver.sender.getParameters().encodings.length, transceivers: state.pc.getTransceivers().length,
            notices: codecFixture.notices, generation: (await import("/src/stream-publication.js")).publicationSnapshot()[0]?.generation };
        });
        expect(after.same).toBe(true);
        expect(after.videos).toBe(1); expect(after.encodings).toBe(1); expect(after.transceivers).toBe(2);
        expect(after.generation).toBe("42"); expect(after.notices).toEqual([]);
        expect(after.audioId).toBe(before.audioId); expect(after.audioPackets).toBeGreaterThan(before.audioPackets);
        expect(after.audioEmitted).toBeGreaterThan(before.audioEmitted);
    } finally { await page.evaluate(() => window.codecFixture?.cleanup()); }
});
