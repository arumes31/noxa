// Loads the BUILT encrypted-media worker of a production bundle in Chromium
// and runs a real VP8 encrypt -> decrypt loopback through it (Task 2 review
// 2a-B P1-1: the dev server and Node tests cannot see an unbundled worker).
// Also checks the emitted license notices cover every library that survived
// in the built worker.
//   node tools/verify-media-worker-build.mjs <dist dir> <package dir with @playwright/test>
import { createRequire } from "node:module";
import { createServer } from "node:http";
import { readFileSync, readdirSync, existsSync } from "node:fs";
import { join, extname, resolve } from "node:path";

export async function verifyMediaWorkerBuild(dist, packageDir) {
    dist = resolve(dist);
    const assets = join(dist, "assets");
    const worker = readdirSync(assets).find(name => /^media-e2ee\.worker-.*\.js$/.test(name));
    if (!worker) throw new Error(`no media-e2ee worker in ${assets}`);
    const source = readFileSync(join(assets, worker), "utf8");
    if (/^\s*import\s+["']livekit-client/m.test(source) || source.length < 20000) throw new Error(`worker ${worker} is not bundled (${source.length} bytes)`);
    const notices = readFileSync(join(dist, "media-e2ee-licenses.txt"), "utf8");
    for (const [marker, name] of [["@bufbuild/protobuf", "@bufbuild/protobuf"], ["loglevel", "loglevel"], ["LKFrameEncryptionKey", "livekit-client"]]) {
        if (source.includes(marker) && !notices.includes(name)) throw new Error(`license notices miss ${name}`);
    }
    if (source.includes("google.protobuf") && !notices.includes("Copyright 2008 Google")) throw new Error("license notices miss the Google protobuf notice");

    const require = createRequire(join(resolve(packageDir), "package.json"));
    const { chromium } = require("@playwright/test");
    const server = createServer((req, res) => {
        const path = decodeURIComponent(req.url.split("?")[0]);
        if (path === "/") { res.setHeader("content-type", "text/html"); res.end("<!doctype html><title>worker build</title>"); return; }
        const file = join(dist, path);
        if (!file.startsWith(dist) || !existsSync(file)) { res.statusCode = 404; res.end(); return; }
        res.setHeader("content-type", extname(file) === ".js" ? "text/javascript" : "application/octet-stream");
        res.end(readFileSync(file));
    }).listen(0, "127.0.0.1");
    await new Promise(r => server.once("listening", r));
    const browser = await chromium.launch({ headless: true });
    try {
        const page = await browser.newPage();
        await page.goto(`http://127.0.0.1:${server.address().port}/`);
        return await page.evaluate(async url => {
            const w = new Worker(url, { type: "module" });
            let seq = 0; const pending = new Map();
            const failed = new Promise((_, reject) => { w.onerror = e => reject(new Error(`worker error: ${e.message || "load failed"}`)); });
            w.onmessage = ({ data }) => { const p = pending.get(data.seq); if (p) { pending.delete(data.seq); data.ok ? p.resolve(data.value) : p.reject(new Error(data.value)); } };
            const rpc = (op, data = {}, transfer = []) => Promise.race([failed, new Promise((resolve, reject) => { const id = ++seq; pending.set(id, { resolve, reject }); w.postMessage({ op, seq: id, ...data }, transfer); })]);
            await rpc("init");
            const canvas = document.createElement("canvas"); canvas.width = 160; canvas.height = 90; const ctx = canvas.getContext("2d"); let n = 0;
            const paint = setInterval(() => { ctx.fillStyle = ++n % 2 ? "#1fd288" : "#4455dd"; ctx.fillRect(0, 0, 160, 90); }, 50);
            const track = canvas.captureStream(15).getVideoTracks()[0];
            const a = new RTCPeerConnection({ encodedInsertableStreams: true }), b = new RTCPeerConnection({ encodedInsertableStreams: true });
            a.onicecandidate = e => e.candidate && b.addIceCandidate(e.candidate); b.onicecandidate = e => e.candidate && a.addIceCandidate(e.candidate);
            const tr = a.addTransceiver(track, { direction: "sendonly" });
            tr.setCodecPreferences(RTCRtpSender.getCapabilities("video").codecs.filter(c => /vp8|rtx/i.test(c.mimeType)));
            const tx = tr.sender.createEncodedStreams();
            await rpc("attach", { id: "tx:build", operation: "encode", kind: "video", readable: tx.readable, writable: tx.writable }, [tx.readable, tx.writable]);
            const key = crypto.getRandomValues(new Uint8Array(32));
            await rpc("bind", { id: "tx:build", pid: "self|cam" });
            await rpc("key", { pid: "self|cam", epoch: 1, kid: 0, key });
            await rpc("activate", { pid: "self|cam", epoch: 1 });
            const video = document.createElement("video"); video.muted = true; video.autoplay = true; document.body.append(video);
            b.ontrack = async e => {
                const rx = e.receiver.createEncodedStreams();
                await rpc("attach", { id: "rx:build", operation: "decode", kind: "video", readable: rx.readable, writable: rx.writable }, [rx.readable, rx.writable]);
                await rpc("bind", { id: "rx:build", pid: "pub|cam" });
                await rpc("key", { pid: "pub|cam", epoch: 1, kid: 0, key });
                video.srcObject = new MediaStream([e.track]);
            };
            await a.setLocalDescription(await a.createOffer()); await b.setRemoteDescription(a.localDescription);
            await b.setLocalDescription(await b.createAnswer()); await a.setRemoteDescription(b.localDescription);
            const deadline = Date.now() + 20000;
            for (;;) {
                const st = await rpc("stats", { id: "rx:build" }).catch(() => null);
                if (st?.endpoint?.frames > 10 && video.videoWidth === 160) {
                    clearInterval(paint); a.close(); b.close(); w.terminate();
                    return { decrypted: st.endpoint.frames, encrypted: st.encrypted, width: video.videoWidth };
                }
                if (Date.now() > deadline) throw new Error(`no decrypted video through the built worker: ${JSON.stringify(st)}`);
                await new Promise(r => setTimeout(r, 200));
            }
        }, `/assets/${worker}`);
    } finally {
        await browser.close();
        server.close();
    }
}

if (import.meta.url === `file://${process.argv[1].replace(/\\/g, "/").replace(/^\/?/, "/")}` || process.argv[1]?.endsWith("verify-media-worker-build.mjs")) {
    const [dist, packageDir] = process.argv.slice(2);
    verifyMediaWorkerBuild(dist, packageDir).then(r => { console.log(`built media worker OK: ${JSON.stringify(r)}`); }, e => { console.error(e.stack || String(e)); process.exit(1); });
}
