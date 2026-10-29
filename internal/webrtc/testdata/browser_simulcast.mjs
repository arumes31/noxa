import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { fileURLToPath, pathToFileURL } from "node:url";

const frontend = fileURLToPath(new URL("../../../client/frontend/", import.meta.url));
const require = createRequire(`${frontend}/package.json`);
const { chromium, expect } = require("@playwright/test");
const { createServer } = await import(pathToFileURL(require.resolve("vite")).href);
const endpoint = process.argv[2];
const [width, height, fps] = process.argv.slice(3).map(Number);
const vite = await createServer({ root: frontend, logLevel: "error", server: { host: "127.0.0.1", port: 0 } });
await vite.listen();
const origin = `http://127.0.0.1:${vite.httpServer.address().port}`;
let browser;
const errors = [];
const pages = [];
const samples = [];
try {
    browser = await chromium.launch({ headless: true });
    async function fixture() {
        const context = await browser.newContext();
        context.on("weberror", event => { errors.push(String(event.error())); console.error(event.error()); });
        const page = await context.newPage();
        pages.push(page);
        await page.route("**/simulcast-fixture", route => route.fulfill({ contentType: "text/html", body: `<!doctype html>
            <button id="voice-screen">Share</button><button id="voice-video">Camera</button>
            <video id="local-video"></video><div id="mic-status"></div><button id="ptt-btn">Talk</button>
            <section id="sharing-status" hidden></section>` }));
        await page.goto(`${origin}/simulcast-fixture`);
        await page.exposeFunction("request", async (path, body) => {
            const response = await fetch(`${endpoint}/${path}`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
            if (!response.ok) throw new Error(await response.text());
            return response.json();
        });
        await page.evaluate(() => {
            window.gather = pc => pc.iceGatheringState === "complete" ? Promise.resolve() : new Promise(resolve => {
                const changed = () => { if (pc.iceGatheringState === "complete") { pc.removeEventListener("icegatheringstatechange", changed); resolve(); } };
                pc.addEventListener("icegatheringstatechange", changed);
            });
        });
        return page;
    }
    const publishers = [];
    for (const id of ["a", "b"]) {
        const page = await fixture();
        await page.evaluate(async ({ id, width, height, fps }) => {
            const pc = new RTCPeerConnection({ iceServers: [] });
            const canvas = document.createElement("canvas");
            canvas.width = width; canvas.height = height;
            document.body.append(canvas);
            const ctx = canvas.getContext("2d");
            let frame = 0;
            setInterval(() => {
                ctx.fillStyle = id === "a" ? "#087582" : "#783a99";
                ctx.fillRect(0, 0, canvas.width, canvas.height);
                ctx.fillStyle = "white";
                ctx.fillRect((frame++ * 11) % (width - 80), 100, 80, 80);
                ctx.font = "48px sans-serif"; ctx.fillText(`${id} ${frame}`, 50, 400);
            }, 1000 / fps);
            window.__noxa = { state: { pc, localStream: new MediaStream(), serverGeneration: 1, sessionGeneration: 1,
                activeTabID: id, settings: {}, myChannelID: 1, myClientID: id, clients: [] },
                $: id => document.getElementById(id), sysMsg: message => { throw new Error(message); }, toast: () => {} };
            window.go = { main: { App: {
                SupportsStreamSourceQualityForTab: async () => true,
                WebRTCOfferForTab: async (_tab, _sdp, tracks) => {
                    await window.gather(pc);
                    return (await window.request("offer", { id, sdp: pc.localDescription.sdp, tracks })).sdp;
                },
                // Channel offers always go through the encrypted-media path;
                // this quality harness measures the codec, so a pass-through
                // stand-in session (below) replaces the frame encryption.
                WebRTCOfferSecureForTab: async (_tab, _sdp, tracks) => {
                    await window.gather(pc);
                    return (await window.request("offer", { id, sdp: pc.localDescription.sdp, tracks })).sdp;
                },
                VideoStreamControlForTab: async (_tab, body) => body.action === "publish"
                    ? window.request("publication", { id, ...body }) : {},
            } } };
            Object.defineProperty(navigator.mediaDevices, "getDisplayMedia", { value: async () => canvas.captureStream(fps) });
            (await import("/src/channel-media.js")).useChannelMediaSession({ active: true, identity: { channelID: 1 }, stop() {}, setRoster() {},
                bindSenders: async () => {}, syncReceivers: async () => {}, publicationsChanged() {} });
            const { createVideoPublication } = await import("/src/video-publication.js");
            const publication = createVideoPublication({ policy: { lowBandwidth: false } });
            (await import("/src/modal.js")).initModalSystem();
            document.getElementById("voice-screen").onclick = () => publication.shareToggle();
        }, { id, width, height, fps });
        await page.locator("#voice-screen").click();
        await page.locator(".sp-quality-toggle").click();
        await page.locator(`.sh-preset input[value="${fps === 60 ? "hdMotion" : "original"}"]`).check();
        await page.keyboard.press("Escape");
        await page.getByRole("button", { name: "Choose screen or window…", exact: true }).click();
        await page.getByRole("button", { name: "Start sharing", exact: true }).click();
        await expect(page.locator("#sharing-status")).toBeVisible();
        await expect.poll(() => page.evaluate(() => window.__noxa.state.pc.connectionState), { timeout: 15000 }).toBe("connected");
        publishers.push(page);
    }
    const outbound = page => page.evaluate(async () => {
        const sender = window.__noxa.state.shareVideoTransceiver.sender;
        return { encodings: sender.getParameters().encodings,
            rows: [...(await sender.getStats()).values()].filter(row => row.type === "outbound-rtp" && row.kind === "video")
                .map(row => ({ rid: row.rid || "", width: row.frameWidth, height: row.frameHeight,
                    encoded: row.framesEncoded || 0, sent: row.framesSent || 0, bytes: row.bytesSent || 0, fps: row.framesPerSecond,
                    targetBitrate: row.targetBitrate, reason: row.qualityLimitationReason, encodeSeconds: row.totalEncodeTime })) };
    });
    async function refreshUploads() {
        for (const page of publishers) {
            await page.evaluate(async () => {
                const { publicationSnapshot, reconcilePublicationUploads } = await import("/src/stream-publication.js");
                const snapshot = publicationSnapshot();
                const result = await window.request("catalog", { id: window.__noxa.state.myClientID });
                reconcilePublicationUploads(snapshot, result.streams);
            });
        }
    }
    async function paused(page, stage) {
        await expect.poll(async () => (await outbound(page)).encodings.every(encoding => encoding.active === false),
            { message: `${stage}: encoder stopped` }).toBe(true);
        // Let already queued packets/RTX drain, then compare actual media
        // counters. An inactive flag alone does not prove upload stopped.
        await page.waitForTimeout(1000);
        const before = await outbound(page);
        await page.waitForTimeout(1000);
        const after = await outbound(page);
        assert.equal(after.encodings.length, 1, `${stage}: one encoding`);
        assert.equal(after.rows.reduce((sum, row) => sum + row.sent, 0), before.rows.reduce((sum, row) => sum + row.sent, 0), `${stage}: no sent frames`);
        assert.equal(after.rows.reduce((sum, row) => sum + row.encoded, 0), before.rows.reduce((sum, row) => sum + row.encoded, 0), `${stage}: no encoded frames`);
        assert.ok(after.rows.reduce((sum, row) => sum + row.bytes, 0) - before.rows.reduce((sum, row) => sum + row.bytes, 0) <= 1200, `${stage}: no video payload upload`);
        return after;
    }
    const dormant = [];
    for (const page of publishers) dormant.push(await paused(page, "before first viewer"));
    async function receiver(id) {
        const page = await fixture();
        await page.evaluate(async id => {
            const pc = new RTCPeerConnection({ iceServers: [] });
            window.receiver = pc;
            pc.ontrack = ({ track }) => {
                if (track.kind !== "video") return;
                const video = document.createElement("video");
                video.muted = true; video.autoplay = true; video.playsInline = true;
                video.srcObject = new MediaStream([track]);
                document.body.append(video);
            };
            await pc.setRemoteDescription(await window.request("viewer", { id }));
            await pc.setLocalDescription(await pc.createAnswer());
            await window.gather(pc);
            await window.request("answer", { id, sdp: pc.localDescription.sdp });
        }, id);
        await refreshUploads();
        return page;
    }
    const stages = [];
    const inbound = page => page.evaluate(async () => [...(await window.receiver.getStats()).values()]
        .filter(row => row.type === "inbound-rtp" && row.kind === "video" && row.framesDecoded > 0)
        .map(row => ({ id: row.trackIdentifier, width: row.frameWidth, height: row.frameHeight, fps: row.framesPerSecond, decoded: row.framesDecoded })));
    function matchesSource(received, output, size, id) {
        const rows = output.rows.filter(row => row.encoded > 0);
        if (output.encodings.length !== 1 || output.encodings[0].active !== true || output.encodings[0].rid || rows.length !== 1) return false;
        const source = rows[0], tracks = received.filter(row => row.id === id);
        if (source.rid || tracks.length !== 1) return false;
        const row = tracks[0];
        // Motion-first encoding may reduce size. The SFU must still forward the
        // very same single source rendition to every watching receiver.
        return Number.isInteger(source.width) && Number.isInteger(source.height) && source.width > 0 && source.height > 0 &&
            source.width <= size[0] && source.height <= size[1] && Math.abs(source.width - source.height * size[0] / size[1]) <= 2 &&
            row.width === source.width && row.height === source.height && row.fps >= fps * 0.6 && row.fps <= fps + 5;
    }
    let checkedRejections = false;
    async function decoded(viewers, stage, dimensions = [[width, height], [width, height]]) {
        let received, sent;
        const start = Date.now();
        let sampled = 0;
        const baselineSent = await Promise.all(publishers.map(outbound));
        const baselineReceived = await Promise.all(viewers.map(inbound));
        await expect.poll(async () => {
            // Bracket both viewers with publisher samples so a size transition
            // cannot approve different renditions in sequential viewer checks.
            const before = await Promise.all(publishers.map(outbound));
            received = await Promise.all(viewers.map(inbound));
            sent = await Promise.all(publishers.map(outbound));
            if (Date.now() - sampled >= 2000) {
                sampled = Date.now();
                samples.push({ stage, elapsedMS: sampled - start, received, sent });
            }
            const matches = dimensions.every((size, index) => {
                if (!size) return true;
                const id = `${index === 0 ? "a" : "b"}|screen`;
                const source = sent[index].rows.find(row => row.encoded > 0);
                const earlier = before[index].rows.find(row => row.encoded > 0);
                const baseline = baselineSent[index].rows.find(row => row.encoded > 0);
                return source && earlier && source.width === earlier.width && source.height === earlier.height &&
                    source.sent > (baseline?.sent || 0) && source.encoded > (baseline?.encoded || 0) &&
                    received.every((rows, viewerIndex) => matchesSource(rows, sent[index], size, id) &&
                        rows.find(row => row.id === id).decoded > (baselineReceived[viewerIndex].find(row => row.id === id)?.decoded || 0));
            });
            if (matches && !checkedRejections) {
                const rows = received[0], output = sent[0], size = dimensions[0], id = "a|screen";
                const reject = (change, label) => {
                    const fixture = structuredClone({ rows, output, size });
                    change(fixture);
                    assert.equal(matchesSource(fixture.rows, fixture.output, fixture.size, id), false, label);
                };
                reject(fixture => fixture.rows.find(row => row.id === id).width += 2, "reject a different viewer rendition");
                reject(fixture => fixture.rows.find(row => row.id === id).id = "wrong|screen", "reject a different source identity");
                reject(fixture => fixture.rows.find(row => row.id === id).fps = fps * 0.6 - 1, "retain the FPS floor");
                reject(fixture => fixture.output.encodings[0].active = false, "reject inactive sources");
                reject(fixture => fixture.output.encodings.push({ active: true }), "reject multiple encodings");
                reject(fixture => fixture.output.rows[0].rid = "extra", "reject simulcast renditions");
                reject(fixture => fixture.size = [1, 1], "reject dimensions above the selected ceiling");
                checkedRejections = true;
            }
            return matches;
        }, { timeout: 15000, message: stage }).toBe(true);
        stages.push({ stage, elapsedMS: Date.now() - start, received });
        return { sent, received };
    }
    const viewer = await receiver("viewer");
    await decoded([viewer], "first viewer starts dormant sources");
    const viewer2 = await receiver("viewer2");
    await decoded([viewer, viewer2], "both viewers receive identical source quality");
    // Exercise a reduced source size deterministically, independent of whether
    // Chromium's bandwidth adaptation happens to activate on this CI host.
    async function scaleSource(scale) {
        await publishers[0].evaluate(async scale => {
            const sender = window.__noxa.state.shareVideoTransceiver.sender;
            const parameters = sender.getParameters();
            parameters.encodings[0].scaleResolutionDownBy = scale;
            await sender.setParameters(parameters);
        }, scale);
    }
    await scaleSource(2);
    await decoded([viewer, viewer2], "both viewers receive the reduced source rendition", [[width / 2, height / 2], [width, height]]);
    await scaleSource(1);
    await decoded([viewer, viewer2], "both viewers follow restored source scaling");
    async function watch(viewerID, active) {
        await viewer.evaluate(args => window.request("watch", { id: "a", ...args }), { viewer: viewerID, active });
        await refreshUploads();
    }
    await watch("viewer", false);
    await decoded([viewer2], "one remaining viewer keeps source active");
    assert.equal((await outbound(publishers[0])).encodings[0].active, true);
    await watch("viewer2", false);
    const stopped = await paused(publishers[0], "last viewer stopped");
    await decoded([viewer, viewer2], "independent second source keeps playing", [null, [width, height]]);
    await watch("viewer", true);
    await decoded([viewer], "source resumes after all viewers stopped");
    await watch("viewer2", true);
    await decoded([viewer, viewer2], "second viewer resumes same source");
    // Choose the new sender size through production live quality controls.
    // Every receiver must see the same change, with no second encoding.
    await publishers[0].locator(".sharing-quality").click();
    await publishers[0].locator('.live-share-quality .sh-preset input[value="custom"]').check();
    await publishers[0].locator(".live-share-quality .sh-width").fill(String(width / 2));
    await publishers[0].locator(".live-share-quality .sh-height").fill(String(height / 2));
    await publishers[0].locator(".live-share-quality .sh-fps").selectOption(String(fps));
    await publishers[0].locator(".live-share-quality").locator("..").getByRole("button", { name: "Apply quality", exact: true }).click();
    await expect(publishers[0].locator(".live-share-quality")).toHaveCount(0);
    const capture = await publishers[0].evaluate(() => {
        const track = window.__noxa.state.shareStream.getVideoTracks()[0];
        return { constraints: track.getConstraints(), settings: track.getSettings(), hint: track.contentHint };
    });
    assert.equal(capture.constraints.width.max, width / 2);
    assert.equal(capture.constraints.height.max, height / 2);
    assert.equal(capture.constraints.frameRate.max, fps);
    assert.ok(capture.settings.width > 0 && capture.settings.width <= width / 2);
    assert.ok(capture.settings.height > 0 && capture.settings.height <= height / 2);
    assert.equal(capture.hint, "motion");
    const { sent, received } = await decoded([viewer, viewer2], "sender quality change reaches both viewers", [[width / 2, height / 2], [width, height]]);
    for (const [index, output] of sent.entries()) {
        const divisor = index === 0 ? 2 : 1;
        assert.equal(output.encodings.length, 1);
        assert.ok(matchesSource(received[0], output, [width / divisor, height / divisor], `${index === 0 ? "a" : "b"}|screen`));
    }
    assert.deepEqual(errors, []);
    console.log(JSON.stringify({ dormant, stopped, sent, stages, samples }, null, 2));
} catch (error) {
    console.error("media samples", JSON.stringify(samples));
    for (const page of pages) {
        console.error("media failure stats", await page.evaluate(async () => {
            const pc = window.receiver || window.__noxa?.state.pc;
            return pc ? { state: pc.connectionState, encodings: pc.getSenders().map(sender => sender.getParameters()),
                stats: [...(await pc.getStats()).values()].filter(row => ["inbound-rtp", "outbound-rtp", "codec"].includes(row.type)) } : {};
        }));
    }
    throw error;
} finally {
    try { await browser?.close(); }
    finally { await vite.close(); }
}
