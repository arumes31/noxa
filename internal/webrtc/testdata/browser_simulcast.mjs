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
                WebRTCOfferForTab: async (_tab, _sdp, tracks) => {
                    await window.gather(pc);
                    return (await window.request("offer", { id, sdp: pc.localDescription.sdp, tracks })).sdp;
                },
                VideoStreamControlForTab: async (_tab, body) => body.action === "publish"
                    ? window.request("publication", { id, ...body }) : {},
            } } };
            Object.defineProperty(navigator.mediaDevices, "getDisplayMedia", { value: async () => canvas.captureStream(fps) });
            const { createVideoPublication } = await import("/src/video-publication.js");
            const publication = createVideoPublication({ policy: { lowBandwidth: false } });
            (await import("/src/modal.js")).initModalSystem();
            document.getElementById("voice-screen").onclick = () => publication.shareToggle();
        }, { id, width, height, fps });
        await page.locator("#voice-screen").click();
        await page.locator(".sh-preset").selectOption(fps === 60 ? "hdMotion" : "original");
        await page.getByRole("button", { name: "Start sharing", exact: true }).click();
        await expect(page.locator("#sharing-status")).toBeVisible();
        await expect.poll(() => page.evaluate(() => window.__noxa.state.pc.connectionState), { timeout: 15000 }).toBe("connected");
        publishers.push(page);
    }
    const viewer = await fixture();
    await viewer.evaluate(async () => {
        const pc = new RTCPeerConnection({ iceServers: [] });
        window.receiver = pc;
        pc.ontrack = ({ track }) => {
            if (track.kind !== "video") return;
            const video = document.createElement("video");
            video.muted = true; video.autoplay = true; video.playsInline = true;
            video.srcObject = new MediaStream([track]);
            document.body.append(video);
        };
        await pc.setRemoteDescription(await window.request("viewer", { id: "viewer" }));
        await pc.setLocalDescription(await pc.createAnswer());
        await window.gather(pc);
        await window.request("answer", { id: "viewer", sdp: pc.localDescription.sdp });
    });
    const stages = [];
    for (const [a, b] of [["high", "high"], ["low", "high"], ["mid", "low"], ["high", "high"]]) {
        await viewer.evaluate(async ({ a, b }) => {
            await window.request("quality", { id: "a", quality: a });
            await window.request("quality", { id: "b", quality: b });
        }, { a, b });
        const dimensions = { high: [width, height], mid: [width / 2, height / 2], low: [width / 4, height / 4] };
        let received;
        await expect.poll(async () => {
            received = await viewer.evaluate(async () => [...(await window.receiver.getStats()).values()]
                .filter(row => row.type === "inbound-rtp" && row.kind === "video" && row.framesDecoded > 0)
                .map(row => ({ id: row.trackIdentifier, width: row.frameWidth, height: row.frameHeight, fps: row.framesPerSecond, decoded: row.framesDecoded })));
            return [a, b].every((quality, index) => {
                const row = received.find(row => row.id === `${index === 0 ? "a" : "b"}|screen`);
                return row && row.width === dimensions[quality][0] && row.height === dimensions[quality][1] && row.fps >= fps * 0.6 && row.fps <= fps + 5;
            });
        }, { timeout: 15000, message: `decoded independent qualities ${a}/${b}` }).toBe(true);
        stages.push({ a, b, received });
    }
    const sent = [];
    for (const page of publishers) {
        const layers = await page.evaluate(async () => [...(await window.__noxa.state.shareVideoTransceiver.sender.getStats()).values()]
            .filter(row => row.type === "outbound-rtp" && row.kind === "video" && row.framesEncoded > 0)
            .map(row => ({ rid: row.rid, width: row.frameWidth, height: row.frameHeight, encoded: row.framesEncoded, fps: row.framesPerSecond }))
            .sort((a, b) => a.width - b.width));
        assert.deepEqual(layers.map(row => [row.rid, row.width, row.height]), [["q", width / 4, height / 4], ["h", width / 2, height / 2], ["f", width, height]]);
        sent.push(layers);
    }
    assert.deepEqual(errors, []);
    console.log(JSON.stringify({ sent, stages }, null, 2));
} catch (error) {
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
