// Drives TestBrowserStudioOutputThroughSFU: desktop page A runs the real
// Studio modules and publishes the program through the real encrypted screen
// publication (video.js startStudioShare -> startShare, channel-media.js,
// video-negotiation.js, Task 2 worker); desktop page B receives, decrypts and
// samples decoded pixels. Captures are synthetic colored canvases. Prints
// "STUDIO SCENARIO PASSED" as its last line on success.
import { createRequire } from "node:module";
import { join, resolve } from "node:path";
import { pathToFileURL } from "node:url";

const bridge = process.argv[2];
const frontend = process.env.NOXA_FRONTEND;
const repo = resolve(frontend, "../..");
const require = createRequire(join(frontend, "package.json"));
const { chromium } = require("@playwright/test");
const { createServer } = await import(pathToFileURL(require.resolve("vite")).href);

const log = (...args) => console.log(new Date().toISOString().slice(11, 19), ...args);
const api = async (path, body) => (await fetch(bridge + path, { method: body ? "POST" : "GET", body: body ? JSON.stringify(body) : undefined })).json();
async function poll(label, fn, { timeout = 30000, every = 250 } = {}) {
    const deadline = Date.now() + timeout;
    let last;
    for (;;) {
        try { last = await fn(); if (last?.ok) return last; } catch (error) { last = { error: String(error) }; }
        if (Date.now() > deadline) throw new Error(`${label}: timed out; last ${JSON.stringify(last)?.slice(0, 1500)} TAP ${JSON.stringify(await api("/tap"))}`);
        await new Promise(r => setTimeout(r, every));
    }
}

const vite = await createServer({ root: frontend, configFile: join(frontend, "vite.config.js"), logLevel: "error",
    server: { port: 0, host: "127.0.0.1", fs: { allow: [repo] }, proxy: { "/bridge": { target: bridge, rewrite: p => p.replace(/^\/bridge/, "") } } } });
await vite.listen();
const origin = `http://127.0.0.1:${vite.httpServer.address().port}`;
const browser = await chromium.launch({ headless: true, args: ["--use-fake-ui-for-media-stream", "--use-fake-device-for-media-stream", "--autoplay-policy=no-user-gesture-required"] });

const page = (id, transformAPI) => `<!doctype html><meta charset="utf-8"><title>studio ${id}</title><body>
<button id="voice-screen"></button><button id="voice-video"></button><video id="local-video"></video><div id="mic-status"></div><button id="ptt-btn"></button><section id="sharing-status" hidden></section>
<script type="module">
const BRIDGE = "/bridge", ID = ${JSON.stringify(id)};
${transformAPI === "streams" ? "delete window.RTCRtpScriptTransform;" : ""}
window.post = async (path, body) => { const r = await fetch(BRIDGE + path, { method: "POST", body: JSON.stringify(body) }); if (!r.ok) throw new Error(path + ": " + await r.text()); return r.json(); };
const state = { pc: null, serverGeneration: 1, activeTabID: "tab", myClientID: ID, myChannelID: 1, clients: [], settings: {}, localStream: null, mediaLimits: null, shareAudioSender: null, shareVideoTransceiver: null };
window.__noxa = { state, $: id => document.getElementById(id), sysMsg: m => console.log("sys " + m), toast: m => console.log("toast " + m) };
window.go = { main: { App: {
  MediaE2EEStatusForTab: async () => ({ supported: true, principal: "u-" + ID, device: "dev-" + ID, client_id: ID }),
  SendMediaKeyForTab: async (_tab, req) => post("/control", { ID, To: req.device, Body: { kind: req.kind, scope: req.scope, epoch: req.epoch, expires_at: req.expires_at, data: req.data } }),
  WebRTCOfferSecureForTab: async (_tab, sdp, slots, version) => { if (version !== "noxa-media-e2ee-v1") throw new Error("undeclared"); window.offers = (window.offers || 0) + 1; return (await post("/offer", { ID, SDP: sdp, Slots: slots })).sdp; },
  WebRTCAnswerForTab: async (_tab, sdp) => post("/answer", { ID, SDP: sdp }),
  VideoStreamControlForTab: async (_tab, msg) => {
    if (msg.action === "publish") { const r = await post("/publish", { ID, Slot: msg.slot, Active: msg.active }); return { ...msg, generation: String(r.generation), streams: [] }; }
    if (msg.action === "keyframe") return post("/keyframe", { ID, Publisher: msg.publisher_id, Slot: msg.slot });
    return { ...msg, streams: [] };
  },
} } };
const { startChannelMedia, channelMedia, receiveMediaKey } = await import("/src/channel-media.js");
const { renegotiate, answerRemoteOffer, queuePeerNegotiation } = await import("/src/video-negotiation.js");
await post("/register", { ID, Principal: "u-" + ID, Device: "dev-" + ID });
(async () => { for (;;) { try { for (const e of await (await fetch(BRIDGE + "/events?id=" + ID)).json()) {
  if (e.type === "offer" && state.pc) await answerRemoteOffer(state.pc, 1, e.sdp).catch(err => console.log("answer " + err));
  else if (e.type === "candidate" && state.pc) { const pc = state.pc; void queuePeerNegotiation(pc, async () => { if (state.pc === pc && pc.remoteDescription) await pc.addIceCandidate({ candidate: e.candidate, sdpMid: e.mid, sdpMLineIndex: e.index }); }).catch(() => {}); }
  else if (e.type === "roster") { state.clients = e.clients; channelMedia()?.setRoster(e.clients); }
  else if (e.type === "control") receiveMediaKey({ sender: e.sender, sender_device: e.senderDevice, ...e.body });
} } catch (e) { await new Promise(r => setTimeout(r, 200)); } } })();
window.colored = rgb => {
  const canvas = document.createElement("canvas"); canvas.width = 320; canvas.height = 180; const ctx = canvas.getContext("2d");
  setInterval(() => { ctx.fillStyle = "rgb(" + rgb.join(",") + ")"; ctx.fillRect(0, 0, 320, 180); }, 50);
  ctx.fillStyle = "rgb(" + rgb.join(",") + ")"; ctx.fillRect(0, 0, 320, 180);
  return canvas.captureStream(30);
};
window.join = async () => {
  const media = await startChannelMedia({ tabID: "tab", channelID: 1, roster: state.clients, onError: err => console.log("media error " + err) });
  const pc = media.protectPeer(new RTCPeerConnection(media.peerConfig()));
  state.pc = pc;
  pc.onicecandidate = ev => { if (ev.candidate) post("/candidate", { ID, Candidate: ev.candidate.candidate, Mid: ev.candidate.sdpMid || "", Index: ev.candidate.sdpMLineIndex || 0 }); };
  const mic = await navigator.mediaDevices.getUserMedia({ audio: true });
  state.localStream = mic;
  pc.addTransceiver(mic.getAudioTracks()[0], { direction: "sendrecv", streams: [mic] });
  await post("/join", { ID, Channel: 1 });
  await renegotiate(pc, 1);
};
window.ready = true;
</script>`;

const pages = {};
async function open(id, api) {
    const context = await browser.newContext();
    const p = await context.newPage();
    p.on("console", m => { const text = m.text(); if (!/^\[vite\]/.test(text)) log(`[${id}] ${text}`); });
    p.on("pageerror", e => log(`[${id} error] ${e}`));
    const path = `/__studio__/${id}`;
    await p.route(origin + path, route => route.fulfill({ contentType: "text/html", body: page(id, api) }));
    await p.goto(origin + path);
    await p.waitForFunction(() => window.ready === true, null, { timeout: 60000 });
    pages[id] = p;
    return p;
}

// Receiver: decoded pixel color at the center of A's screen publication.
const sample = () => pages.B.evaluate(async () => {
    const media = (await import("/src/channel-media.js")).channelMedia();
    const pc = window.__noxa.state.pc;
    const transceiver = pc.getTransceivers().find(t => media.media.boundPid(t.receiver) === "A|screen");
    if (!transceiver) return { ok: false, why: "no A|screen receiver" };
    // As remote-media.js does: present only after the presentation gate saw
    // the first authenticated (decrypted) frame.
    if (media.media.authenticated(transceiver.receiver) !== true) return { ok: false, why: "not authenticated yet" };
    transceiver.receiver.track.enabled = true;
    if (!window.__view || window.__view.track !== transceiver.receiver.track) {
        const video = document.createElement("video"); video.muted = true; video.autoplay = true; video.playsInline = true;
        video.srcObject = new MediaStream([transceiver.receiver.track]); document.body.append(video); await video.play().catch(() => {});
        window.__view = { track: transceiver.receiver.track, video, canvas: document.createElement("canvas") };
    }
    const { video, canvas } = window.__view;
    if (video.readyState < 2 || !video.videoWidth) return { ok: false, why: "no decoded frame yet" };
    canvas.width = video.videoWidth; canvas.height = video.videoHeight;
    const ctx = canvas.getContext("2d", { willReadFrequently: true }); ctx.drawImage(video, 0, 0);
    const d = ctx.getImageData(Math.floor(canvas.width / 2), Math.floor(canvas.height / 2), 1, 1).data;
    const stats = await media.stats(transceiver.receiver);
    return { ok: true, rgb: [d[0], d[1], d[2]], size: [video.videoWidth, video.videoHeight], frames: stats?.endpoint?.frames || 0 };
});
const isRed = ([r, g, b]) => r > 150 && g < 90 && b < 90;
const isBlue = ([r, g, b]) => b > 150 && r < 90 && g < 110;
const isNeutral = ([r, g, b]) => r < 50 && g < 50 && b < 60;
async function seen(label, test, timeout = 20000) {
    return poll(label, async () => { const s = await sample(); return { ok: s.ok && test(s.rgb), s }; }, { timeout, every: 100 });
}

// Receiver: RMS of A's decrypted screen-audio publication (presented only
// after the gate authenticated it) and its decrypted frame count.
const listen = () => pages.B.evaluate(async () => {
    const media = (await import("/src/channel-media.js")).channelMedia();
    const pc = window.__noxa.state.pc;
    const transceiver = pc.getTransceivers().find(t => media.media.boundPid(t.receiver) === "A|screenaudio");
    if (!transceiver) return { ok: false, why: "no A|screenaudio receiver" };
    if (media.media.authenticated(transceiver.receiver) !== true) return { ok: false, why: "not authenticated yet" };
    transceiver.receiver.track.enabled = true;
    if (!window.__ear || window.__ear.track !== transceiver.receiver.track) {
        // Chromium feeds remote WebRTC audio to Web Audio only while a media element plays it.
        const element = new Audio(); element.muted = true; element.srcObject = new MediaStream([transceiver.receiver.track]); element.play().catch(() => {});
        const context = new AudioContext(); const analyser = context.createAnalyser();
        context.createMediaStreamSource(new MediaStream([transceiver.receiver.track])).connect(analyser);
        window.__ear = { track: transceiver.receiver.track, element, context, analyser, data: new Float32Array(analyser.fftSize) };
        await context.resume().catch(() => {});
    }
    const { analyser, data } = window.__ear;
    let rms = 0;
    for (let i = 0; i < 4; i++) {
        await new Promise(r => setTimeout(r, 50));
        analyser.getFloatTimeDomainData(data);
        rms = Math.max(rms, Math.sqrt(data.reduce((sum, v) => sum + v * v, 0) / data.length));
    }
    const stats = await media.stats(transceiver.receiver);
    return { ok: true, rms, frames: stats?.endpoint?.frames || 0 };
});
const A = selector => pages.A.locator(selector);

try {
    await open("A", "script");
    await open("B", "streams");
    await pages.A.evaluate(() => window.join());
    await pages.B.evaluate(() => window.join());
    await new Promise(r => setTimeout(r, 2000));

    // A drives the real Studio panel. Captures: colored canvases, with a 440 Hz
    // tone as "system audio" when audio is requested.
    await pages.A.evaluate(async () => {
        window.colors = [];
        window.captures = [];
        Object.defineProperty(navigator.mediaDevices, "getDisplayMedia", { configurable: true, value: async options => {
            const stream = window.colored(window.colors.shift());
            if (options.audio) {
                const context = new AudioContext(); const osc = context.createOscillator(); const dest = context.createMediaStreamDestination();
                osc.frequency.value = 440; osc.connect(dest); osc.start(); stream.addTrack(dest.stream.getAudioTracks()[0]);
            }
            window.captures.push(stream);
            return stream;
        } });
        (await import("/src/modal.js")).initModalSystem();
        window.studioUI = await import("/src/studio-ui.js");
        window.video = await import("/src/video.js");
        window.studioUI.openStudio();
    });
    const capture = async (rgb, audio) => {
        await pages.A.evaluate(c => window.colors.push(c), rgb);
        await A(".studio-capture-audio").selectOption(audio);
        const before = await pages.A.evaluate(() => window.studioUI.studioDiagnostics().captures.length);
        await A(".studio-add-display").click();
        await poll("capture added", async () => ({ ok: await pages.A.evaluate(n => window.studioUI.studioDiagnostics().captures.length === n + 1, before) }));
    };
    await capture([230, 20, 20], "none");                      // scene 1: red, no audio
    await A(".studio-transition").selectOption("cut");
    await A(".studio-take").click();
    await pages.A.evaluate(() => {
        window.replacements = 0;
        const replace = RTCRtpSender.prototype.replaceTrack;
        RTCRtpSender.prototype.replaceTrack = function (...args) { window.replacements++; return replace.apply(this, args); };
    });
    await A(".studio-golive").click();
    await poll("A is live", async () => ({ ok: (await A(".studio-air").textContent()) === "Live" }));
    const published = await pages.A.evaluate(() => { window.replacementsAtLive = window.replacements; window.publishedTrack = window.__noxa.state.shareVideoTransceiver.sender.track; return window.studioUI.studioDiagnostics(); });
    log("A live through studio-ui", JSON.stringify(published));
    // Prepare scene 2 in the draft: blue WITH system audio (enabled on the draft source).
    await A(".studio-add-scene").click();
    await capture([20, 40, 230], "system");
    await api("/watch", { ID: "B", Publisher: "A", Slot: "screen" });
    const first = await seen("B decrypts and decodes the Studio program (red)", isRed, 40000);
    log("phase 1: receiver decoded the encrypted Studio program", JSON.stringify(first.s));

    // No draft leak: neither the blue picture nor the draft capture's audio.
    const quiet = await poll("B receives the (silent) Studio audio publication", async () => {
        const l = await listen(); return { ok: l.ok && l.frames > 20, l };
    }, { timeout: 30000 });
    let loudest = 0;
    for (let i = 0; i < 20; i++) {
        const s = await sample();
        if (s.ok && isBlue(s.rgb)) throw new Error("draft preview leaked to the receiver before Take: " + JSON.stringify(s));
        const l = await listen();
        if (l.ok) loudest = Math.max(loudest, l.rms);
    }
    const framesQuiet = (await listen()).frames;
    if (loudest > 0.005) throw new Error(`draft capture audio leaked to the receiver before Take: rms ${loudest}`);
    if (framesQuiet - quiet.l.frames < 50) throw new Error("audio frames did not flow; the silence check would be vacuous");
    const routesBefore = (await pages.A.evaluate(() => window.studioUI.studioDiagnostics())).routes;
    if (routesBefore.length) throw new Error("draft audio routed before Take: " + JSON.stringify(routesBefore));
    log(`phase 2: ~3 s, draft picture (blue) and draft audio (max rms ${loudest.toFixed(4)}, ${framesQuiet - quiet.l.frames} decrypted audio frames) never reached the receiver before Take`);

    // Take (cut) through the panel: picture AND audio go on air together.
    await A(".studio-take").click();
    await seen("B sees blue after Take (cut)", isBlue);
    const loud = await poll("B hears the scene's audio after Take", async () => { const l = await listen(); return { ok: l.ok && l.rms > 0.02, l }; });
    log("phase 3: Take (cut) put picture and audio on air", JSON.stringify(loud.l));

    // Crossfade back to scene 1 over 1500 ms: blended frames, then red; the
    // outgoing scene's audio stops at Take.
    await A(".studio-transition").selectOption("fade");
    await A(".studio-duration").fill("1500");
    await A('.studio-scene[data-scene="sc1"]').click();
    await poll("scene 1 selected", async () => ({ ok: (await pages.A.evaluate(() => window.studioUI.studioDiagnostics().scene)) === "sc1" }));
    await A(".studio-take").click();
    let mixed = 0;
    const until = Date.now() + 1800;
    while (Date.now() < until) { const s = await sample(); if (s.ok && s.rgb[0] > 70 && s.rgb[2] > 70) mixed++; await new Promise(r => setTimeout(r, 40)); }
    await seen("B sees red after the crossfade", isRed);
    if (!mixed) throw new Error("receiver never decoded a crossfade frame");
    await poll("audio stops with the Take", async () => { const l = await listen(); return { ok: l.ok && l.rms < 0.005, l }; });
    log(`phase 4: crossfade reached the receiver (${mixed} blended samples), settled on red; scene audio stopped at Take`);

    const same = await pages.A.evaluate(() => ({ track: window.__noxa.state.shareVideoTransceiver.sender.track === window.publishedTrack && window.publishedTrack.readyState === "live", replacements: window.replacements - window.replacementsAtLive }));
    if (!same.track || same.replacements !== 0) throw new Error("the published Studio track was replaced: " + JSON.stringify(same));
    log("phase 5: Takes and the crossfade kept the published track (0 replaceTrack calls)");

    // Revoked source: the live red capture ends; the receiver sees neutral.
    await pages.A.evaluate(() => { const track = window.captures[0].getVideoTracks()[0]; track.stop(); track.dispatchEvent(new Event("ended")); });
    await seen("B sees neutral output after the live source was revoked", isNeutral);
    log("phase 6: revoked live source neutral at the receiver");

    const dropped = await pages.A.evaluate(async () => {
        const media = (await import("/src/channel-media.js")).channelMedia();
        const out = {};
        for (const t of window.__noxa.state.pc.getTransceivers()) { const st = await media.stats(t.sender); if (st?.endpoint?.pid) out[st.endpoint.pid] = st.endpoint; }
        return out;
    });
    if (!dropped["self|screen"] || dropped["self|screen"].frames < 30) throw new Error("studio screen sender encrypted too few frames: " + JSON.stringify(dropped));
    for (const [pid, s] of Object.entries(dropped)) if ((s.dropped?.unencrypted || 0) > 0) throw new Error(`${pid} produced unencrypted output`);
    log("phase 7: sender encrypted", JSON.stringify(Object.fromEntries(Object.entries(dropped).map(([k, v]) => [k, v.frames]))));

    // Stop from the panel: publication and every capture end.
    await A(".studio-stop").click();
    const stopped = await poll("Stop ended the publication", async () => {
        const r = await pages.A.evaluate(() => ({ sharing: window.__noxa.state.screenSharing, published: window.publishedTrack.readyState,
            captures: window.captures.flatMap(s => s.getTracks()).map(t => t.readyState), diagnostics: window.studioUI.studioDiagnostics() }));
        return { ok: !r.sharing && r.published === "ended" && r.captures.every(state => state === "ended") && !r.diagnostics.live, r };
    });
    log("phase 8: Stop ended the publication and every capture", JSON.stringify(stopped.r.diagnostics));

    log("tap", JSON.stringify(await api("/tap")));
    log("STUDIO SCENARIO PASSED");
    await browser.close();
    await vite.close();
    process.exit(0);
} catch (error) {
    log("FAILED", error?.stack || String(error));
    await browser.close().catch(() => {});
    await vite.close().catch(() => {});
    process.exit(1);
}
