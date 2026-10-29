// Drives TestBrowserE2EEChannelMediaThroughSFU: headless Chromium pages that
// run the REAL client modules (desktop channel-media.js + video-negotiation.js,
// PWA voice.js, shared MediaSession/worker) against the Go harness, which
// hosts the actual SFU. Captures are Chromium fake devices plus a canvas
// screen and an oscillator screen-audio track. Prints "E2EE SCENARIO PASSED"
// as its last line on success; any failed check exits non-zero.
import { createRequire } from "node:module";
import { join, resolve } from "node:path";
import { pathToFileURL } from "node:url";

const bridge = process.argv[2];
const frontend = process.env.NOXA_FRONTEND;
const repo = resolve(frontend, "../..");
const require = createRequire(join(frontend, "package.json"));
const { chromium } = require("@playwright/test");
const { createServer } = await import(pathToFileURL(require.resolve("vite")).href);
const longMinutes = Number(process.env.NOXA_E2EE_LONG_MINUTES || 0);

const log = (...args) => console.log(new Date().toISOString().slice(11, 19), ...args);
async function poll(label, fn, { timeout = 30000, every = 300 } = {}) {
    const deadline = Date.now() + timeout;
    let last;
    for (;;) {
        try { last = await fn(); if (last?.ok) return last; } catch (error) { last = { error: String(error) }; }
        if (Date.now() > deadline) {
            let diag = "";
            try { diag = JSON.stringify(Object.fromEntries(await Promise.all(Object.keys(pages).filter(k => k !== "X").map(async k => [k, await report(k)])))).slice(0, 6000); } catch (e) { diag = String(e); }
            diag += " TAP " + JSON.stringify(await api("/tap"));
            throw new Error(`${label}: timed out; last ${JSON.stringify(last)?.slice(0, 1500)}
${diag}`);
        }
        await new Promise(r => setTimeout(r, every));
    }
}
const api = async (path, body) => (await fetch(bridge + path, { method: body ? "POST" : "GET", body: body ? JSON.stringify(body) : undefined })).json();

const vite = await createServer({ root: frontend, configFile: join(frontend, "vite.config.js"), logLevel: "error",
    // Pages reach the harness through a same-origin proxy (Chromium blocks
    // cross-origin loopback fetches).
    server: { port: 0, host: "127.0.0.1", fs: { allow: [repo] }, proxy: { "/bridge": { target: bridge, rewrite: p => p.replace(/^\/bridge/, "") } } } });
await vite.listen();
const origin = `http://127.0.0.1:${vite.httpServer.address().port}`;
const browser = await chromium.launch({ headless: true,
    args: ["--use-fake-ui-for-media-stream", "--use-fake-device-for-media-stream", "--autoplay-policy=no-user-gesture-required"] });
const mobileFS = "/@fs/" + repo.replace(/\\/g, "/").replace(/^\//, "") + "/mobile/src/core";

// Shared in-page helpers: bridge calls, fake screen + screen audio, metrics.
const common = (id, transformAPI) => `
const BRIDGE = "/bridge", ID = ${JSON.stringify(id)};
${transformAPI === "streams" ? "delete window.RTCRtpScriptTransform;" : ""}
${transformAPI === "none" ? "delete window.RTCRtpScriptTransform; delete RTCRtpSender.prototype.createEncodedStreams; delete RTCRtpReceiver.prototype.createEncodedStreams;" : ""}
window.post = async (path, body) => { const r = await fetch(BRIDGE + path, { method: "POST", body: JSON.stringify(body) }); if (!r.ok) throw new Error(path + ": " + await r.text()); return r.json(); };
window.offers = 0;
window.fakeScreen = () => {
  const canvas = document.createElement("canvas"); canvas.width = 640; canvas.height = 360; const ctx = canvas.getContext("2d"); let n = 0;
  window.__paint = setInterval(() => { ctx.fillStyle = "hsl(" + (n++ * 7 % 360) + ",70%,50%)"; ctx.fillRect(0, 0, 640, 360); ctx.fillStyle = "#fff"; ctx.fillText("screen " + ID + " " + n, 20, 40); }, 66);
  const audio = new AudioContext(); const osc = audio.createOscillator(); const dest = audio.createMediaStreamDestination(); osc.frequency.value = 660; osc.connect(dest); osc.start();
  return { video: canvas.captureStream(15).getVideoTracks()[0], audio: dest.stream.getAudioTracks()[0] };
};
window.bindings = new Map(); // mid -> [pid history]
window.metrics = async (pc, binding, stats) => {
  const out = {};
  const inbound = new Map();
  (await pc.getStats()).forEach(s => { if (s.type === "inbound-rtp") inbound.set(s.mid, s); });
  for (const tr of pc.getTransceivers()) {
    const pid = binding(tr.receiver);
    if (!pid) continue;
    const hist = window.bindings.get(tr.mid) || []; if (hist.at(-1) !== pid) hist.push(pid); window.bindings.set(tr.mid, hist);
    const st = await stats(tr.receiver);
    const s = inbound.get(tr.mid);
    out[pid] = { mid: tr.mid, frames: st?.endpoint?.frames || 0, dropped: st?.endpoint?.dropped || {}, decoded: s?.framesDecoded || 0, bytes: s?.bytesReceived || 0 };
  }
  const senders = {};
  for (const tr of pc.getTransceivers()) {
    const st = await stats(tr.sender); if (!st?.endpoint?.pid) continue;
    senders[st.endpoint.pid] = { frames: st.endpoint.frames, dropped: st.endpoint.dropped };
  }
  let sent = 0; const pairs = []; const cands = {}; const all = await pc.getStats(); all.forEach(s => { if (s.type === "outbound-rtp") sent += s.bytesSent || 0; if (s.type === "local-candidate" || s.type === "remote-candidate") cands[s.id] = s.type[0] + ":" + s.candidateType + ":" + s.address + ":" + s.port + ":" + s.protocol; }); all.forEach(s => { if (s.type === "candidate-pair") pairs.push(s.state + " " + cands[s.localCandidateId] + " -> " + cands[s.remoteCandidateId]); }); all.forEach(s => { if (s.type === "transport") pairs.push("transport dtls=" + s.dtlsState + " ice=" + s.iceState); });
  return { receivers: out, senders, bindings: Object.fromEntries(window.bindings), conn: pc.connectionState, ice: pc.iceConnectionState, sent, pairs,
    transceivers: pc.getTransceivers().map(t => ({ mid: t.mid, dir: t.currentDirection, kind: t.receiver.track.kind })) };
};
async function eventLoop(handle) { for (;;) { try { const events = await (await fetch(BRIDGE + "/events?id=" + ID)).json(); for (const e of events) await handle(e); } catch (e) { console.log("events " + e); await new Promise(r => setTimeout(r, 200)); } } }
`;

const desktopPage = (id, transformAPI) => `<!doctype html><meta charset="utf-8"><title>desktop ${id}</title><body><script type="module">
${common(id, transformAPI)}
const state = { pc: null, serverGeneration: 1, activeTabID: "tab", shareAudioSender: null, shareVideoTransceiver: null, clients: [] };
window.__noxa = { state };
window.go = { main: { App: {
  MediaE2EEStatusForTab: async () => ({ supported: true, principal: "u-" + ID, device: "dev-" + ID, client_id: ID }),
  SendMediaKeyForTab: async (_tab, req) => post("/control", { ID, To: req.device, Body: { kind: req.kind, scope: req.scope, epoch: req.epoch, expires_at: req.expires_at, data: req.data } }),
  WebRTCOfferSecureForTab: async (_tab, sdp, slots, version) => { if (version !== "noxa-media-e2ee-v1") throw new Error("undeclared"); window.offers++; return (await post("/offer", { ID, SDP: sdp, Slots: slots })).sdp; },
  WebRTCAnswerForTab: async (_tab, sdp) => post("/answer", { ID, SDP: sdp }),
} } };
const { startChannelMedia, channelMedia, receiveMediaKey, stopChannelMedia } = await import("/src/channel-media.js");
const { renegotiate, answerRemoteOffer, queuePeerNegotiation } = await import("/src/video-negotiation.js");
await post("/register", { ID, Principal: "u-" + ID, Device: "dev-" + ID });
eventLoop(async e => {
  if (e.type === "offer" && state.pc) await answerRemoteOffer(state.pc, 1, e.sdp).catch(err => console.log("answer " + err));
  // As main.js does: remote candidates queue behind the negotiation.
  else if (e.type === "candidate" && state.pc) { const pc = state.pc; void queuePeerNegotiation(pc, async () => { if (state.pc === pc && pc.remoteDescription) await pc.addIceCandidate({ candidate: e.candidate, sdpMid: e.mid, sdpMLineIndex: e.index }); else console.log("candidate dropped"); }).catch(err => console.log("candidate " + err)); }
  else if (e.type === "roster") { state.clients = e.clients; channelMedia()?.setRoster(e.clients); }
  else if (e.type === "control") receiveMediaKey({ sender: e.sender, sender_device: e.senderDevice, ...e.body });
});
let mic = null;
window.e2ee = {
  async join(channel = 1) {
    const media = await startChannelMedia({ tabID: "tab", channelID: channel, roster: state.clients, onError: err => console.log("media error " + err) });
    const pc = media.protectPeer(new RTCPeerConnection(media.peerConfig()));
    state.pc = pc;
    pc.onicecandidate = ev => { if (ev.candidate) post("/candidate", { ID, Candidate: ev.candidate.candidate, Mid: ev.candidate.sdpMid || "", Index: ev.candidate.sdpMLineIndex || 0 }); };
    mic = await navigator.mediaDevices.getUserMedia({ audio: true });
    pc.addTransceiver(mic.getAudioTracks()[0], { direction: "sendrecv", streams: [mic] });
    await post("/join", { ID, Channel: channel });
    await renegotiate(pc, 1);
    return media.api || channelMedia()?.media?.api;
  },
  async camera() {
    const cam = await navigator.mediaDevices.getUserMedia({ video: { width: 640, height: 360 } });
    state.pc.addTransceiver(cam.getVideoTracks()[0], { direction: "sendonly", streams: [cam] });
    await renegotiate(state.pc, 1);
    await post("/publish", { ID, Slot: "cam", Active: true });
    channelMedia().publicationsChanged();
  },
  async screen() {
    const { video, audio } = fakeScreen();
    const stream = new MediaStream([video, audio]);
    state.shareVideoTransceiver = state.pc.addTransceiver(video, { direction: "sendonly", streams: [stream] });
    state.shareAudioSender = state.pc.addTransceiver(audio, { direction: "sendonly", streams: [stream] }).sender;
    await renegotiate(state.pc, 1);
    await post("/publish", { ID, Slot: "screen", Active: true });
    channelMedia().publicationsChanged();
  },
  rekey() { channelMedia().publicationsChanged(); },
  // Transforms belong to the sender: replaceTrack keeps encryption.
  async replaceMic() { const s = await navigator.mediaDevices.getUserMedia({ audio: true }); const tr = state.pc.getTransceivers().find(t => t.sender.track?.kind === "audio" && t !== state.shareAudioSender); await tr.sender.replaceTrack(s.getAudioTracks()[0]); },
  async restartCamera() { const cam = await navigator.mediaDevices.getUserMedia({ video: { width: 640, height: 360 } }); const tr = state.pc.getTransceivers().find(t => t.sender.track?.kind === "video" && t !== state.shareVideoTransceiver); tr.sender.track.stop(); await tr.sender.replaceTrack(cam.getVideoTracks()[0]); },
  status() { return channelMedia()?.status() || { state: "off" }; },
  epoch() { return channelMedia()?.engine?.activeEpoch || 0; },
  async leave() { stopChannelMedia(); state.pc?.close(); state.pc = null; await post("/close", { ID }); },
  report() { return metrics(state.pc, r => channelMedia()?.media?.boundPid(r) || "", e => channelMedia().stats(e)); },
  offers() { return window.offers; },
};
window.ready = true;
</script>`;

const pwaPage = (id, transformAPI) => `<!doctype html><meta charset="utf-8"><title>pwa ${id}</title><body><script type="module">
${common(id, transformAPI)}
const { Voice } = await import("${mobileFS}/voice.js");
const { Msg } = await import("${mobileFS}/frame.js");
let roster = [];
const handlers = {};
const session = Object.assign(new EventTarget(), {
  clientID: ID, deviceID: "dev-" + ID, uniqueID: "u-" + ID, capabilities: new Set(), mediaLimits: null, iceServers: [],
  relayServers: async () => [], onRelayRenewal: () => () => {}, clients: () => roster, self: () => roster.find(c => c.client_id === ID) || null, channels: () => [{ id: 1, clients: roster }, { id: 2, clients: roster }],
  conn: { open: true, on(type, fn) { handlers[type] = fn; return () => {}; },
    send(type, data) {
      if (type === Msg.WebRTCAnswer) post("/answer", { ID, SDP: data.sdp });
      else if (type === Msg.ICECandidate) post("/candidate", { ID, Candidate: data.candidate, Mid: data.sdp_mid, Index: data.sdp_mline_index });
    },
    async request(type, data) {
      if (type === Msg.JoinChannel) { if (data.channel_id) await post("/join", { ID }); return { channel_id: data.channel_id }; }
      if (type === Msg.WebRTCOffer) { if (data.media_e2ee !== "noxa-media-e2ee-v1") throw new Error("undeclared"); window.offers++; return { sdp: (await post("/offer", { ID, SDP: data.sdp, Slots: data.tracks })).sdp }; }
      if (type === Msg.VideoStreamControl) { const r = await post("/publish", { ID, Slot: data.slot, Active: data.active }); return { action: "publish", generation: String(r.generation) }; }
      return {};
    } },
});
await post("/register", { ID, Principal: "u-" + ID, Device: "dev-" + ID });
const adapter = { status: async () => ({ supported: true, principal: "u-" + ID, device: "dev-" + ID, clientID: ID }),
  send: async c => post("/control", { ID, To: c.device, Body: c.body }) };
const voice = new Voice(session, null, { media: adapter });
window.voice = voice;
eventLoop(async e => {
  if (e.type === "offer") handlers[Msg.WebRTCOffer]?.({ sdp: e.sdp });
  else if (e.type === "candidate") console.log("pwa candidate rd=" + !!voice.pc?.remoteDescription), handlers[Msg.ICECandidate]?.({ candidate: e.candidate, sdp_mid: e.mid, sdp_mline_index: e.index });
  else if (e.type === "roster") { roster = e.clients; session.dispatchEvent(new Event("snapshot")); }
  else if (e.type === "control") voice.receiveMediaKey({ sender: e.sender, senderDevice: e.senderDevice, ...e.body, expiresAt: e.body.expires_at });
});
window.e2ee = {
  async join() { await voice.join(1); return voice.mediaStatus?.api; },
  async camera() { await voice.startCamera(); },
  async flip() { try { await voice.flipCamera(); return "flipped"; } catch (e) { return "flip unavailable: " + e.message; } },
  async tryJoin() { try { await voice.join(1); return "joined"; } catch (error) { return String(error?.reason || error?.message || error); } },
  status() { return voice.mediaStatus; },
  epoch() { return voice.mediaStatus?.epoch || 0; },
  async leave() { await voice.leave(); await post("/close", { ID }); },
  report() { return metrics(voice.pc, r => voice.mediaBinding(r), e => voice.mediaStats(e)); },
  offers() { return window.offers; },
  pc() { return !!voice.pc; },
};
window.ready = true;
</script>`;

const pages = {};
async function open(id, kind, transformAPI) {
    // No permission grant: like the desktop listener test, Chromium keeps
    // mDNS host candidates, which reach the loopback SFU on every platform.
    const context = await browser.newContext();
    const page = await context.newPage();
    page.on("console", m => { const text = m.text(); if (!/^\[vite\]/.test(text)) log(`[${id}] ${text}`); });
    page.on("pageerror", e => log(`[${id} error] ${e}`));
    const path = `/__e2ee__/${id}`;
    await page.route(origin + path, route => route.fulfill({ contentType: "text/html", body: kind === "desktop" ? desktopPage(id, transformAPI) : pwaPage(id, transformAPI) }));
    await page.goto(origin + path);
    await page.waitForFunction(() => window.ready === true, null, { timeout: 60000 });
    pages[id] = page;
    return page;
}
const call = (id, fn, ...args) => pages[id].evaluate(([fn, args]) => window.e2ee[fn](...args), [fn, args]);
const report = id => call(id, "report");
const frames = async (viewer, pid) => (await report(viewer)).receivers[pid]?.frames || 0;
async function growing(viewer, pid, label, { min = 20, video = false } = {}) {
    const start = await frames(viewer, pid);
    return poll(label, async () => {
        const r = (await report(viewer)).receivers[pid];
        return { ok: !!r && r.frames - start >= min && (!video || r.decoded > 0), r };
    }, { timeout: 40000 });
}

try {
    // 0. A runtime without encoded transforms never creates a peer or offers.
    await open("X", "pwa", "none");
    const refused = await call("X", "tryJoin");
    if (refused !== "unsupported_runtime" || await call("X", "pc") || await call("X", "offers") !== 0) throw new Error(`unsupported runtime: ${refused}`);
    await api("/leave", { ID: "X" });
    log("unsupported runtime refused explicitly, no peer, no offer");

    // 1. Desktop A (RTCRtpScriptTransform) and PWA B (createEncodedStreams).
    await open("A", "desktop", "script");
    await open("B", "pwa", "streams");
    log("A api", await call("A", "join"), "B api", await call("B", "join"));
    await new Promise(r => setTimeout(r, 2500));
    for (const id of ["A", "B"]) { const r = await report(id); log(id, "ice", r.conn, r.ice, JSON.stringify(r.pairs)); }
    await call("A", "camera");
    await call("A", "screen");
    await call("B", "camera");
    for (const [viewer, publisher, slot] of [["B", "A", "cam"], ["B", "A", "screen"], ["A", "B", "cam"]]) await api("/watch", { ID: viewer, Publisher: publisher, Slot: slot });
    await growing("B", "A|mic", "B hears A mic (desktop script -> pwa streams)");
    await growing("B", "A|screenaudio", "B hears A screen audio");
    await growing("B", "A|cam", "B sees A camera", { video: true, min: 10 });
    await growing("B", "A|screen", "B sees A screen", { video: true, min: 10 });
    await growing("A", "B|mic", "A hears B mic (pwa streams -> desktop script)");
    await growing("A", "B|cam", "A sees B camera", { video: true, min: 10 });
    for (const id of ["A", "B"]) {
        const st = await call(id, "status");
        if (st.state !== "encrypted") throw new Error(`${id} status ${JSON.stringify(st)}`);
        const senders = (await report(id)).senders;
        for (const [pid, s] of Object.entries(senders)) if ((s.dropped.unencrypted || 0) > 0) throw new Error(`${id} ${pid} produced unencrypted output`);
    }
    log("phase 1: desktop<->pwa mic/camera/screen/screen-audio decrypted through the SFU");

    // 1b. replaceTrack (new microphone), an ended + restarted camera capture
    // and a PWA camera flip keep encryption through the SFU.
    await call("A", "replaceMic");
    await growing("B", "A|mic", "B hears A after replaceTrack of the microphone");
    await call("A", "restartCamera");
    await growing("B", "A|cam", "B sees A after the camera capture restarted", { video: true, min: 10 });
    const flipped = await call("B", "flip");
    if (flipped !== "flipped") throw new Error(`camera flip failed: ${flipped}`);
    await growing("A", "B|cam", "A sees B after the camera flip", { video: true, min: 10 });
    for (const id of ["A", "B"]) for (const [pid, s] of Object.entries((await report(id)).senders)) if ((s.dropped.unencrypted || 0) > 0) throw new Error(`${id} ${pid} produced unencrypted output after track changes`);
    log("phase 1b: replaceTrack, capture restart and camera flip stayed encrypted");

    // 2. A joiner (desktop C, createEncodedStreams) gets only new epochs.
    const epochBefore = await call("A", "epoch");
    await open("C", "desktop", "streams");
    await call("C", "join");
    await api("/watch", { ID: "C", Publisher: "A", Slot: "cam" });
    await growing("C", "A|mic", "joiner C hears A");
    await growing("C", "A|cam", "joiner C sees A camera", { video: true, min: 10 });
    const toC = (await api("/controls")).filter(c => c.to === "dev-C");
    if (!toC.length || toC.some(c => c.epoch <= epochBefore)) throw new Error(`joiner received an earlier epoch: ${JSON.stringify(toC)} before ${epochBefore}`);
    log("phase 2: joiner received only epochs after", epochBefore);

    // 3. A device removed from A's membership but still served by the SFU
    // cannot decrypt A's new epoch; B keeps hearing A.
    const hideAt = Date.now();
    await api("/hide", { ID: "A", Hide: "C" });
    await poll("A rekeys without C", async () => ({ ok: (await call("A", "epoch")) > (toC.at(-1)?.epoch || 0) && (await call("A", "status")).recipients === 1 }));
    await new Promise(r => setTimeout(r, 4000));
    const cut = await report("C");
    await new Promise(r => setTimeout(r, 3000));
    const after = await report("C");
    const gained = (after.receivers["A|mic"]?.frames || 0) - (cut.receivers["A|mic"]?.frames || 0);
    const nokey = (after.receivers["A|mic"]?.dropped?.nokey || 0) - (cut.receivers["A|mic"]?.dropped?.nokey || 0);
    const lateToC = (await api("/controls")).filter(c => c.to === "dev-C" && c.from === "dev-A" && c.epoch > (toC.at(-1)?.epoch || 0));
    const bytesGained = (after.receivers["A|mic"]?.bytes || 0) - (cut.receivers["A|mic"]?.bytes || 0);
    // The SFU still delivers A's packets to C (bytes grow), but without the
    // new key not one frame decrypts: the media is really encrypted.
    if (bytesGained <= 0) throw new Error("removed device received no packets; the decrypt check would be vacuous");
    if (gained > 0 || nokey < 20 || lateToC.length) throw new Error(`removed device still decrypts: gained ${gained} nokey ${nokey} late ${JSON.stringify(lateToC)}`);
    await growing("B", "A|mic", "B still hears A after the removal rekey");
    log(`phase 3: removed-but-served device decrypted 0 new frames (${nokey} dropped without key) ${Date.now() - hideAt}ms`);

    // 4. C leaves; A reconnects with a fresh session; B decrypts again.
    await call("C", "leave");
    await api("/leave", { ID: "C" });
    const beforeReconnect = await call("A", "epoch");
    await call("A", "leave");
    await call("A", "join");
    await api("/watch", { ID: "B", Publisher: "A", Slot: "cam" }).catch(() => {});
    await poll("reconnect starts a newer epoch", async () => ({ ok: (await call("A", "epoch")) > beforeReconnect }));
    await growing("B", "A|mic", "B hears A after A reconnects");
    log("phase 4: leave + reconnect re-keyed, media resumed");

    // 5. Same MID, new publisher: PWA D (script) joins after C left; B's
    // receivers rebind to D (never outputting C) and decrypt D.
    await open("D", "pwa", "script");
    await call("D", "join");
    await growing("B", "D|mic", "B hears new publisher D");
    const bindings = (await report("B")).bindings;
    const reused = Object.entries(bindings).filter(([, h]) => h.at(-1)?.startsWith("D|") && h.some(pid => !pid.startsWith("D|")));
    if (!reused.length) throw new Error(`no receiver MID was reused for the new publisher: ${JSON.stringify(bindings)}`);
    log("phase 5: same MID rebound to new publisher D:", JSON.stringify(Object.fromEntries(reused)));

    // 6. Repeated rekeys while media flows (publication changes / renewal).
    const nokeyBefore = (await report("B")).receivers["A|mic"]?.dropped?.nokey || 0;
    for (let i = 0; i < 3; i++) {
        const e = await call("A", "epoch");
        await call("A", "rekey");
        await poll(`rekey ${i}`, async () => ({ ok: (await call("A", "epoch")) > e }));
        await growing("B", "A|mic", `B hears A after rekey ${i}`, { min: 10 });
    }
    const nokeyRekeys = ((await report("B")).receivers["A|mic"]?.dropped?.nokey || 0) - nokeyBefore;
    if (nokeyRekeys > 25) throw new Error(`rekeys interrupted decryption: ${nokeyRekeys} frames without key`);
    log(`phase 6: three rekeys under load, ${nokeyRekeys} frames without key during transitions`);

    // 6b. 10% SRTP loss on the SFU egress (every 10th packet): decryption continues, nothing
    // unauthenticated or replayed is output.
    await api("/loss", { Active: true });
    const beforeLoss = (await report("B")).receivers["A|mic"];
    await growing("B", "A|mic", "B hears A under 10% loss", { min: 50 });
    await api("/loss", { Active: false });
    const afterLoss = (await report("B")).receivers["A|mic"];
    log("phase 6b: under loss", JSON.stringify({ frames: afterLoss.frames - beforeLoss.frames, dropped: afterLoss.dropped }));

    // 6c. Echo test channel: my own media comes back and decrypts with my
    // own epoch key only (no control, no plaintext exception).
    await open("E", "desktop", "script");
    await call("E", "join", 2);
    await growing("E", "E|mic", "encrypted self-echo decrypts");
    const echoControls = (await api("/controls")).filter(c => c.from === "dev-E");
    if (echoControls.length) throw new Error(`self-echo sent keys: ${JSON.stringify(echoControls)}`);
    log("phase 6c: encrypted self-echo decrypted with the sender's own key, no key control sent");

    // 7. Optional long session beyond the old 10-minute hard close.
    if (longMinutes > 0) {
        const until = Date.now() + longMinutes * 60000;
        let last = await frames("B", "A|mic");
        const epochs = new Set();
        while (Date.now() < until) {
            await new Promise(r => setTimeout(r, 30000));
            const now = await frames("B", "A|mic");
            epochs.add(await call("A", "epoch"));
            if (now - last < 100) throw new Error(`long session stalled at ${new Date().toISOString()}: ${now - last}`);
            log(`long session: +${now - last} frames, epochs seen ${epochs.size}`);
            last = now;
        }
        log(`phase 7: ${longMinutes} min continuous decrypted media across ${epochs.size} epochs`);
    }

    log("tap", JSON.stringify(await api("/tap")));
    log("E2EE SCENARIO PASSED");
    await browser.close();
    await vite.close();
    process.exit(0);
} catch (error) {
    log("FAILED", error?.stack || String(error));
    await browser.close().catch(() => {});
    await vite.close().catch(() => {});
    process.exit(1);
}
