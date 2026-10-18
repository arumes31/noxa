import fs from 'node:fs';
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { counterDelta, counterRate, sourceFrameTimingSummary } from './browser_fanout_metrics.mjs';
import { drawDurationBoundsMS, rafGapBoundsMS } from './browser_source_timing.mjs';
const frontend = fileURLToPath(new URL('../../../client/frontend/', import.meta.url));
const require = createRequire(`${frontend}/package.json`);
const { chromium, expect } = require('@playwright/test');
const { createServer } = await import(pathToFileURL(require.resolve('vite')).href);
const endpoint = process.argv[2];
const [width, height, fps] = process.argv.slice(3).map(Number);
const configuration = process.argv[7] ? JSON.parse(process.argv[7]) : { capacity: false, viewers: 3, stageSeconds: 15, slowWriteMS: 0 };
if (configuration.capacity && (![1, 5, 20].includes(configuration.viewers) || !Number.isInteger(configuration.stageSeconds) || configuration.stageSeconds < 10 || configuration.stageSeconds > 60 ||
    !Number.isInteger(configuration.slowWriteMS) || configuration.slowWriteMS < 0 || configuration.slowWriteMS > 20 || (configuration.slowWriteMS && configuration.viewers < 5))) throw new Error('Invalid bounded capacity profile');
const workload = configuration.workload || 'pan';
if (!['pan', 'desktop'].includes(workload) || (workload === 'desktop' && (!configuration.capacity || configuration.stageSeconds !== 60))) throw new Error('Desktop workload requires complete 60-second capacity stages');
const profile = configuration.capacity ? `${workload}-capacity` : 'pan';
const desktopModule = workload === 'desktop' ? fs.readFileSync(new URL('./browser_desktop_workload.mjs', import.meta.url), 'utf8') : null;
const sourceTimingModule = desktopModule ? fs.readFileSync(new URL('./browser_source_timing.mjs', import.meta.url), 'utf8') : null;
if (configuration.codec && (!configuration.capacity || !['h264', 'vp8'].includes(configuration.codec))) throw new Error('Unsupported requested test codec');
const output = process.argv[6];
const vite = await createServer({ root: frontend, logLevel: 'error', server: { host: '127.0.0.1', port: 0, hmr: false } });
await vite.listen();
const origin = `http://127.0.0.1:${vite.httpServer.address().port}`;
let browser;
let browserServer;
let browserClosing;
let failure;
let publisher;
let generation;
const receivers = [];
const errors = [];
const samples = [];
const joins = [];
const impairments = [];
const timeoutSeconds = configuration.capacity ? configuration.viewers * 15 + configuration.stageSeconds * 3 + 60 : 180;
async function bounded(operation, milliseconds, label) {
    let timer;
    try {
        return await Promise.race([operation(), new Promise((_, reject) => { timer = setTimeout(() => reject(new Error(`${label} timed out`)), milliseconds); })]);
    } finally { clearTimeout(timer); }
}
function closeBrowser() {
    return browserClosing ||= (async () => {
        if (!browserServer) return;
        try { await bounded(() => browserServer.close(), 4000, 'browser close'); }
        catch { await bounded(() => browserServer.kill(), 3000, 'owned browser termination'); }
    })();
}
const watchdog = setTimeout(() => {
    failure ||= 'Browser fixture watchdog expired before Go hard deadline';
    // Closing the owned browser interrupts pending page evaluations/polls so
    // the normal finally block can preserve samples and close the local server.
    void closeBrowser().catch(error => errors.push(String(error)));
}, (timeoutSeconds - 20) * 1000);
try {
    browserServer = await chromium.launchServer({ headless: true });
    browser = await chromium.connect(browserServer.wsEndpoint());
    async function fixture() {
        const context = await browser.newContext({ viewport: { width: 1280, height: 800 } });
        context.on('weberror', event => errors.push(String(event.error())));
        const page = await context.newPage();
        if (desktopModule) await page.route('**/fanout-workload.js', route => route.fulfill({ contentType: 'text/javascript', body: desktopModule }));
        if (sourceTimingModule) await page.route('**/fanout-source-timing.js', route => route.fulfill({ contentType: 'text/javascript', body: sourceTimingModule }));
        await page.route('**/fanout-fixture', route => route.fulfill({ contentType: 'text/html', body: `<!doctype html>
            <button id="voice-screen">Share</button><button id="voice-video">Camera</button>
            <video id="local-video"></video><div id="mic-status"></div><button id="ptt-btn">Talk</button>
            <section id="sharing-status" hidden></section>` }));
        await page.goto(`${origin}/fanout-fixture`);
        await page.exposeFunction('request', async (path, body) => {
            const response = await fetch(`${endpoint}/${path}`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body), signal: AbortSignal.timeout(10000) });
            if (!response.ok) throw new Error(await response.text());
            return response.json();
        });
        await page.evaluate(() => {
            window.gather = pc => pc.iceGatheringState === 'complete' ? Promise.resolve() : new Promise(resolve => {
                const changed = () => { if (pc.iceGatheringState === 'complete') { pc.removeEventListener('icegatheringstatechange', changed); resolve(); } };
                pc.addEventListener('icegatheringstatechange', changed);
            });
        });
        return page;
    }
    publisher = await fixture();
    await publisher.evaluate(async ({ width, height, fps, capacity, codec, workload }) => {
        const pc = new RTCPeerConnection({ iceServers: [] });
        if (codec) {
            const { preferPublicationCodec } = await import('/src/video-codecs.js');
            const createOffer = pc.createOffer.bind(pc);
            pc.createOffer = async options => {
                const video = pc.getTransceivers().find(transceiver => transceiver.sender.track?.kind === 'video');
                if (!preferPublicationCodec(video, codec)) throw new Error(`Test codec unavailable: ${codec}`);
                return createOffer(options);
            };
        }
        const canvas = document.createElement('canvas');
        canvas.width = width; canvas.height = height; canvas.style.width = '960px';
        document.body.append(canvas);
        const texture = document.createElement('canvas');
        texture.width = 240; texture.height = texture.width * 9 / 16;
        const textureContext = texture.getContext('2d');
        const pixels = textureContext.createImageData(texture.width, texture.height);
        let random = 180719;
        for (let i = 0; i < pixels.data.length; i += 4) {
            random ^= random << 13; random ^= random >>> 17; random ^= random << 5;
            pixels.data[i] = random & 255; pixels.data[i + 1] = (random >>> 8) & 255;
            pixels.data[i + 2] = (random >>> 16) & 255; pixels.data[i + 3] = 255;
        }
        textureContext.putImageData(pixels, 0, 0);
        const ctx = canvas.getContext('2d'); ctx.imageSmoothingEnabled = false;
        let frame = 0;
        const start = performance.now();
        window.sourceWorkload = { name: workload, width, height, requested_fps: fps, drawn_frames: 0 };
        const sourceState = (now, phase) => Object.assign(window.sourceWorkload, { drawn_frames: frame, elapsed_ms: now - start,
            phase: phase?.name || 'pan', phase_elapsed_ms: phase?.phase_elapsed_ms ?? now - start, cycle: phase?.cycle ?? 0 });
        if (workload === 'desktop') {
            const { drawDesktopScene } = await import('/fanout-workload.js');
            const { createSourceFrameTiming } = await import('/fanout-source-timing.js');
            const timing = window.sourceFrameTiming = createSourceFrameTiming(crypto.randomUUID());
            let last = 0;
            const draw = now => {
                requestAnimationFrame(draw);
                timing.opportunity(now);
                if (last && now - last < 1000 / fps - 1) { timing.skipped(); return; }
                last = now;
                const started = performance.now();
                const phase = drawDesktopScene(ctx, now - start, width, height);
                timing.completed(performance.now() - started);
                frame++;
                sourceState(now, phase);
            };
            requestAnimationFrame(draw);
        } else setInterval(() => {
            frame++;
            const shift = (frame * 13) % width;
            ctx.drawImage(texture, shift, 0, width, height); ctx.drawImage(texture, shift - width, 0, width, height);
            ctx.fillStyle = '#072530'; ctx.fillRect(0, 0, 600, 80);
            ctx.fillStyle = '#40e2fa'; ctx.font = '48px sans-serif'; ctx.fillText(`Synthetic ${frame}`, 20, 60);
            sourceState(performance.now());
        }, 1000 / fps);
        const localStream = new MediaStream();
        window.__noxa = { state: { pc, localStream, serverGeneration: 1, sessionGeneration: 1,
            activeTabID: 'a', settings: {}, myChannelID: 1, myClientID: 'a', clients: [],
            // Use the production cap policy so upload reconciliation preserves
            // this test's 8 Mbps ceiling (including its 15% RTP headroom rule).
            mediaLimits: capacity ? { video_max_bitrate: Math.ceil(8_000_000 / .85) } : undefined },
            $: id => document.getElementById(id), sysMsg: message => { throw new Error(message); }, toast: () => {} };
        window.go = { main: { App: {
            SupportsStreamSourceQualityForTab: async () => true,
            WebRTCOfferForTab: async (_tab, _sdp, tracks) => {
                await window.gather(pc);
                return (await window.request('offer', { id: 'a', sdp: pc.localDescription.sdp, tracks })).sdp;
            },
            VideoStreamControlForTab: async (_tab, body) => body.action === 'publish'
                ? window.request('publication', { id: 'a', ...body }) : {},
        } } };
        Object.defineProperty(navigator.mediaDevices, 'getDisplayMedia', { value: async () => canvas.captureStream(fps) });
        const { createVideoPublication } = await import('/src/video-publication.js');
        const publication = createVideoPublication({ policy: { lowBandwidth: false } });
        (await import('/src/modal.js')).initModalSystem();
        document.getElementById('voice-screen').onclick = () => publication.shareToggle();
    }, { width, height, fps, capacity: configuration.capacity, codec: configuration.codec, workload });
    await publisher.locator('#voice-screen').click();
    await publisher.locator('.sh-preset').selectOption(fps === 60 ? 'hdMotion' : 'original');
    await publisher.getByRole('button', { name: 'Choose screen or window…', exact: true }).click();
    await publisher.getByRole('button', { name: 'Start sharing', exact: true }).click();
    await expect.poll(() => publisher.evaluate(() => window.__noxa.state.pc.connectionState), { timeout: 15000 }).toBe('connected');
    generation = await publisher.evaluate(async () => (await window.request('catalog', { id: 'a' })).streams[0].generation);
    async function refresh() {
        await publisher.evaluate(async () => {
            const { publicationSnapshot, reconcilePublicationUploads } = await import('/src/stream-publication.js');
            const snapshot = publicationSnapshot();
            reconcilePublicationUploads(snapshot, (await window.request('catalog', { id: 'a' })).streams);
        });
    }
    async function receiver(id) {
        const startedAt = Date.now();
        const page = await fixture();
        await page.evaluate(async ({ id, capacity }) => {
            const { observeVideoPresentation } = await import('/src/video-presentation-timing.js');
            const pc = new RTCPeerConnection({ iceServers: [] });
            window.receiver = pc;
            window.presentationObservers = new Map();
            window.presentationCounts = new Map();
            window.presentationEpoch = 0;
            pc.ontrack = ({ track }) => {
                if (track.kind === 'audio') {
                    const audio = document.createElement('audio'); audio.muted = true; audio.autoplay = true;
                    audio.srcObject = new MediaStream([track]); document.body.append(audio); return;
                }
                if (track.kind !== 'video') return;
                const video = [...document.querySelectorAll('video')].find(element => element.srcObject?.getVideoTracks()[0]?.id === track.id) || document.createElement('video');
                window.presentationObservers.get(video)?.dispose();
                video.muted = true; video.autoplay = true; video.playsInline = true; video.style.width = '960px';
                // Reserved camera tracks stay attached, but must not push the
                // actual capacity-test video below the viewport and suppress
                // autoplay/presentation. Legacy pan layout stays unchanged.
                if (capacity && track.id === 'a|screen') Object.assign(video.style, {
                    position: 'fixed', left: '0', top: '0', width: '960px', height: '540px', zIndex: '1', objectFit: 'contain',
                });
                video.srcObject = new MediaStream([track]); document.body.append(video);
                const stream = video.srcObject;
                const callbacks = { count: 0, epoch: ++window.presentationEpoch };
                window.presentationCounts.set(video, callbacks);
                window.presentationObservers.set(video, observeVideoPresentation(video, {
                    track, visibility: document, isCurrent: () => video.srcObject === stream,
                    onFrame: () => {
                        callbacks.count++;
                        if (!capacity || track.id === 'a|screen') window.firstFrameAt ||= Date.now();
                    },
                }));
            };
            window.negotiate = async () => {
                await pc.setRemoteDescription(await window.request('viewer', { id }));
                await pc.setLocalDescription(await pc.createAnswer()); await window.gather(pc);
                await window.request('answer', { id, sdp: pc.localDescription.sdp });
            };
            await window.negotiate();
        }, { id, capacity: configuration.capacity });
        joins.push({ id, startedAt, connectedAt: Date.now() });
        receivers.push({ id, page });
        await refresh();
        if (configuration.codec && receivers.length === 1) {
            // As in browser_codec.mjs, wait for the actual ingress codec before
            // negotiating its receiver track. Upload activation needs the first
            // watch, so refresh the initial reserved output once after it starts.
            await expect.poll(() => publisher.evaluate(() => window.request('codec', { id: 'a' })), { timeout: 15000 }).toBe(`video/${configuration.codec === 'h264' ? 'H264' : 'VP8'}`);
            await page.evaluate(async () => { await window.negotiate(); window.firstFrameAt = null; });
            joins.at(-1).codecRenegotiatedAt = Date.now();
        }
    }
    const capture = page => page.evaluate(async () => {
        const pc = window.receiver || window.__noxa.state.pc;
        const viewport = video => {
            const rect = video.getBoundingClientRect(), style = getComputedStyle(video);
            const intersection = Math.max(0, Math.min(rect.right, innerWidth) - Math.max(rect.left, 0)) *
                Math.max(0, Math.min(rect.bottom, innerHeight) - Math.max(rect.top, 0));
            return { x: rect.x, y: rect.y, width: rect.width, height: rect.height,
                viewport_width: innerWidth, viewport_height: innerHeight,
                intersection_ratio: rect.width > 0 && rect.height > 0 ? intersection / (rect.width * rect.height) : 0,
                style_visible: style.display !== 'none' && style.visibility === 'visible' && Number(style.opacity) > 0 };
        };
        const keys = ['id', 'type', 'timestamp', 'kind', 'ssrc', 'rid', 'active', 'trackIdentifier', 'codecId', 'mediaSourceId',
            'width', 'height', 'frameWidth', 'frameHeight', 'frames', 'framesPerSecond', 'framesEncoded', 'framesSent', 'framesReceived', 'framesDecoded', 'framesDropped',
            'bytesSent', 'bytesReceived', 'packetsSent', 'packetsReceived', 'packetsLost', 'retransmittedBytesSent', 'retransmittedPacketsSent',
            'retransmittedBytesReceived', 'retransmittedPacketsReceived', 'keyFramesEncoded', 'keyFramesDecoded', 'nackCount', 'pliCount', 'firCount',
            'freezeCount', 'totalFreezesDuration', 'jitter', 'jitterBufferDelay', 'jitterBufferEmittedCount', 'totalDecodeTime', 'totalEncodeTime',
            'concealedSamples', 'silentConcealedSamples', 'totalSamplesReceived', 'concealmentEvents',
            'totalInterFrameDelay', 'totalSquaredInterFrameDelay', 'totalPacketSendDelay', 'targetBitrate', 'qualityLimitationReason',
            'qualityLimitationDurations', 'qualityLimitationResolutionChanges', 'encoderImplementation', 'decoderImplementation', 'mimeType',
            'powerEfficientEncoder', 'powerEfficientDecoder', 'totalProcessingDelay',
            'currentRoundTripTime', 'availableOutgoingBitrate', 'availableIncomingBitrate', 'state', 'nominated'];
        return { at: Date.now(), state: pc.connectionState, firstFrameAt: window.firstFrameAt || null, visibility: document.visibilityState,
            workload: window.sourceWorkload ? { ...window.sourceWorkload } : null,
            sourceFrameTiming: window.sourceFrameTiming?.snapshot() ?? null,
            encodings: window.__noxa?.state.shareVideoTransceiver.sender.getParameters().encodings,
            sourceSettings: window.__noxa?.state.shareVideoTransceiver.sender.track ? ((settings) => ({ width: settings.width, height: settings.height, frameRate: settings.frameRate }))(window.__noxa.state.shareVideoTransceiver.sender.track.getSettings()) : null,
            rows: [...(await pc.getStats()).values()].filter(row => ['outbound-rtp', 'inbound-rtp', 'media-source', 'codec', 'candidate-pair'].includes(row.type))
                .map(row => Object.fromEntries(keys.filter(key => row[key] !== undefined).map(key => [key, row[key]]))),
            playback: [...document.querySelectorAll('video')].filter(video => video.srcObject).map(video => ({
                id: video.srcObject.getVideoTracks()[0]?.id, width: video.videoWidth, height: video.videoHeight, paused: video.paused,
                ready_state: video.readyState, viewport: viewport(video),
                observed_presentation_callbacks: window.presentationCounts?.get(video)?.count ?? null,
                presentation_epoch: window.presentationCounts?.get(video)?.epoch ?? null,
                presentation: window.presentationObservers?.get(video)?.snapshot(performance.now()) ?? null,
                total: video.getVideoPlaybackQuality().totalVideoFrames, dropped: video.getVideoPlaybackQuality().droppedVideoFrames })) };
    });
    async function sample(stage, seconds) {
        const completeCycle = workload === 'desktop' && ['capacity', 'slow-writer', 'recovery'].includes(stage);
        let firstAt;
        // N interval samples normally span N-1 seconds. Desktop scoring needs
        // an endpoint at least a whole 60-second cycle after its baseline.
        for (let second = 0; second < seconds || (completeCycle && samples.at(-1).sender.at - firstAt < seconds * 1000); second++) {
            const started = Date.now();
            const sender = await capture(publisher);
            assert.equal(sender.encodings?.length, 1, 'one source encoding throughout the run');
            assert.equal(sender.encodings[0].rid, undefined, 'no simulcast RID');
            if (configuration.capacity) assert.equal(sender.encodings[0].maxBitrate, 8_000_000, 'configured capacity ceiling survives publication updates');
            assert.equal(sender.rows.filter(row => row.type === 'outbound-rtp' && row.kind === 'video').length, 1, 'one outgoing video RTP source');
            const outbound = sender.rows.find(row => row.type === 'outbound-rtp' && row.kind === 'video');
            if (configuration.codec && outbound.framesEncoded > 0) assert.equal(sender.rows.find(row => row.id === outbound.codecId)?.mimeType?.toLowerCase(), `video/${configuration.codec}`, 'actual requested codec, no silent alternative');
            const received = await Promise.all(receivers.map(async ({ id, page }) => {
                const measured = await capture(page);
                const server = await page.evaluate(({ id, generation }) => window.request('diagnostics', { id, generation }), { id, generation });
                const diagnostic = await page.evaluate(id => window.request('pacer', { id }), id);
                assert.ok(diagnostic?.pacer, `${id}: actual production pacer binding is present`);
                const pacer = { target_bps: diagnostic.pacer.target_bitrate_bps, packets: diagnostic.pacer.queued_packets,
                    bytes: diagnostic.pacer.queued_bytes, rejected: diagnostic.pacer.dropped_queue_full, expired: diagnostic.pacer.dropped_expired };
                return { id, ...measured, server, pacer, operator: diagnostic };
            }));
            samples.push({ stage, sender, receivers: received });
            firstAt ??= sender.at;
            if (second % 5 === 0) console.log(`${stage} ${second}s: sender ${sender.rows.find(row => row.type === 'outbound-rtp')?.framesPerSecond ?? '?'} fps`);
            await new Promise(resolve => setTimeout(resolve, Math.max(0, 1000 - (Date.now() - started))));
        }
    }
    // Chromium's bandwidth ramp varies between runs. Start late viewers only
    // after actual source traffic exceeds a cold recipient's pacing budget.
    async function waitForBusySource(stage, minimumBitrate) {
        let sustained = 0;
        for (let second = 0; second < 45; second++) {
            await sample(stage, 1);
            const before = samples.at(-2).sender, after = samples.at(-1).sender;
            const row = sample => sample.rows.find(row => row.type === 'outbound-rtp' && row.kind === 'video');
            const seconds = (after.at - before.at) / 1000;
            const bitrate = (row(after).bytesSent - row(before).bytesSent) * 8 / seconds;
            const measuredFPS = (row(after).framesEncoded - row(before).framesEncoded) / seconds;
            sustained = bitrate >= minimumBitrate && measuredFPS >= 15 ? sustained + 1 : 0;
            if (sustained >= 2) return;
        }
        throw new Error(`${stage}: source did not sustain ${minimumBitrate / 1e6} Mbps and 15 fps on this host`);
    }
    if (configuration.capacity) {
        await receiver('viewer'); await sample('warmup', 10);
        let fullSize = 0;
        // Include the initial ten seconds in the 45-second warmup bound. Keep
        // all cumulative stats and adaptation samples; only scoring starts later.
        for (let second = 0; second < 35; second++) {
            await sample('geometry-warmup', 1);
            const row = samples.at(-1).sender.rows.find(row => row.type === 'outbound-rtp' && row.kind === 'video');
            fullSize = row?.frameWidth === width && row?.frameHeight === height && row?.framesPerSecond >= 15 ? fullSize + 1 : 0;
            if (fullSize >= 2) break;
        }
        assert.ok(fullSize >= 2, 'capacity source reaches requested geometry during bounded warmup');
        for (let index = 2; index <= configuration.viewers; index++) await receiver(`viewer${index}`);
        if (configuration.viewers > 1) await sample('join-warmup', 5);
        await sample('capacity', configuration.stageSeconds);
        if (configuration.slowWriteMS) {
            const slow = receivers.at(-1);
            const setImpairment = async active => {
                const requestedAt = Date.now();
                const result = await slow.page.evaluate(({ id, active }) => window.request('impairment', { id, active }), { id: slow.id, active });
                impairments.push({ stage: active ? 'slow-writer' : 'recovery', id: slow.id, at: requestedAt,
                    requestedAt, acknowledgedAt: Date.now(), ...result });
            };
            await setImpairment(true);
            try {
                await sample('slow-writer', configuration.stageSeconds);
                assert.ok(Date.now() < impairments[0].expires_at_ms, 'scored slow stage completed before automatic impairment expiry');
            }
            finally { await setImpairment(false); }
            await sample('recovery', configuration.stageSeconds);
            assert.ok(impairments.at(-1).delayed_packets > impairments[0].delayed_packets, 'slow-writer fixture actually delayed video packets');
        }
    } else {
        await receiver('viewer'); await sample('one', 15);
        await waitForBusySource('one-warmup', 12_000_000);
        await receiver('viewer2'); await sample('two', 15);
        await waitForBusySource('two-warmup', 15_000_000);
        await receiver('viewer3'); await sample('three', 20);
    }
    const last = samples.at(-1);
    if (configuration.capacity) {
        const stages = configuration.slowWriteMS ? ['capacity', 'slow-writer', 'recovery'] : ['capacity'];
        for (const stage of stages) {
            const measured = samples.filter(sample => sample.stage === stage);
            assert.ok(measured.length >= 2 && measured.at(-1).sender.at - measured[0].sender.at >= 5000, `${stage}: sufficient measured window`);
            if (workload === 'desktop') assert.ok(measured.at(-1).sender.at - measured[0].sender.at >= 60000, `${stage}: complete desktop cycle between baseline and endpoint`);
            const source = sample => sample.sender.rows.find(row => row.type === 'outbound-rtp' && row.kind === 'video');
            const sentFrames = counterDelta(measured.map(source), 'framesEncoded', ['id', 'ssrc']);
            assert.ok(sentFrames >= 120, `${stage}: sustained source frames`);
            for (const sample of measured) {
                assert.equal(source(sample).frameWidth, width, `${stage}: source preserves requested width`);
                assert.equal(source(sample).frameHeight, height, `${stage}: source preserves requested height`);
            }
            if (workload === 'desktop') assert.deepEqual([...new Set(measured.map(sample => sample.sender.workload.phase))].sort(),
                ['motion', 'scrolling-text', 'static-text'], `${stage}: all desktop phases measured`);
            const healthy = stage === 'slow-writer' ? receivers.slice(0, -1) : receivers;
            for (const { id } of healthy) {
                const inbound = sample => sample.receivers.find(receiver => receiver.id === id).rows.find(row => row.type === 'inbound-rtp' && row.trackIdentifier === 'a|screen');
                for (const sample of measured) {
                    assert.equal(inbound(sample)?.frameWidth, width, `${stage}/${id}: actual decoded width`);
                    assert.equal(inbound(sample)?.frameHeight, height, `${stage}/${id}: actual decoded height`);
                    const received = sample.receivers.find(receiver => receiver.id === id);
                    const playback = received.playback.find(video => video.id === 'a|screen');
                    assert.ok(received.visibility === 'visible' && playback && !playback.paused && playback.ready_state >= 2 &&
                        playback.viewport.style_visible && playback.viewport.intersection_ratio >= .999,
                    `${stage}/${id}: actual screen plays fully inside the fixture viewport`);
                }
                const decodedFrames = counterDelta(measured.map(inbound), 'framesDecoded', ['id', 'ssrc']);
                assert.ok(decodedFrames !== null && decodedFrames / sentFrames >= .85, `${stage}/${id}: transport capacity floor (not 60fps presentation acceptance)`);
            }
        }
    }
    assert.equal(last.sender.encodings.length, 1, 'one source encoding');
    assert.equal(last.sender.encodings[0].rid, undefined, 'no simulcast RID');
    for (const received of last.receivers) {
        const row = received.rows.find(row => row.type === 'inbound-rtp' && row.kind === 'video');
        assert.ok(row?.framesDecoded > 0, `${received.id}: decoded source frames`);
        assert.equal(row.frameWidth, width, `${received.id}: full chosen width`);
        assert.equal(row.frameHeight, height, `${received.id}: full chosen height`);
        const playback = received.playback.find(row => row.id === 'a|screen');
        assert.ok(playback && !playback.paused && playback.width === width && playback.height === height,
            `${received.id}: video element plays the full source`);
    }
    {
        const terminalStage = configuration.capacity ? (configuration.slowWriteMS ? 'recovery' : 'capacity') : 'three';
        const stageRows = samples.filter(sample => sample.stage === terminalStage);
        const terminal = configuration.capacity ? stageRows : stageRows.slice(-10);
        const senderRow = sample => sample.sender.rows.find(row => row.type === 'outbound-rtp' && row.kind === 'video');
        const sentFrames = senderRow(terminal.at(-1)).framesEncoded - senderRow(terminal[0]).framesEncoded;
        assert.ok(sentFrames >= 120, 'source sends sustained measured frames');
        const elapsedSeconds = (terminal.at(-1).sender.at - terminal[0].sender.at) / 1000;
        const sourceBitrate = (senderRow(terminal.at(-1)).bytesSent - senderRow(terminal[0]).bytesSent) * 8 / elapsedSeconds;
        if (!configuration.capacity) assert.ok(sourceBitrate >= 10_000_000, 'workload exercises a busy source above the old startup pacing budget');
        for (const { id } of receivers) {
            const frame = sample => sample.receivers.find(row => row.id === id).rows.find(row => row.type === 'inbound-rtp' && row.trackIdentifier === 'a|screen').framesDecoded;
            assert.ok((frame(terminal.at(-1)) - frame(terminal[0])) / sentFrames >= .85, `${id}: keeps up with source frames`);
            const join = joins.find(row => row.id === id);
            const firstFrameAt = last.receivers.find(row => row.id === id).firstFrameAt;
            assert.ok(firstFrameAt >= join.startedAt && Math.max(0, firstFrameAt - join.connectedAt) < 5000,
                `${id}: first picture within 5 seconds`);
        }
        if (configuration.slowWriteMS) {
            const slowStage = samples.filter(sample => sample.stage === 'slow-writer');
            const sourceFrames = senderRow(slowStage.at(-1)).framesEncoded - senderRow(slowStage[0]).framesEncoded;
            assert.ok(sourceFrames >= 120, 'source continues during one slow writer');
            for (const { id } of receivers.slice(0, -1)) {
                const frames = sample => sample.receivers.find(receiver => receiver.id === id).rows.find(row => row.type === 'inbound-rtp' && row.kind === 'video').framesDecoded;
                assert.ok((frames(slowStage.at(-1)) - frames(slowStage[0])) / sourceFrames >= .85, `${id}: healthy viewer remains isolated from slow writer`);
            }
        }
        for (const receiver of samples.flatMap(sample => sample.receivers)) {
            assert.ok(receiver.pacer.packets <= 1024 && receiver.pacer.bytes <= 2 * 1024 * 1024, 'bounded video send queue');
        }
    }
    assert.deepEqual(errors, []);
} catch (error) {
    failure ||= String(error?.stack || error);
} finally {
    clearTimeout(watchdog);
    try {
      const stageWindows = [...new Set(samples.map(sample => sample.stage))].map(stage => {
        const rows = samples.filter(sample => sample.stage === stage);
        const scoring = configuration.capacity ? ['capacity', 'slow-writer', 'recovery'].includes(stage) : stage === 'three';
        const scored = scoring ? (configuration.capacity ? rows : rows.slice(-10)) : [];
        const source = row => row.sender.rows.find(value => value.type === 'outbound-rtp' && value.kind === 'video');
        const seconds = (rows.at(-1).sender.at - rows[0].sender.at) / 1000;
        const sentFrames = counterDelta(rows.map(source), 'framesEncoded', ['id', 'ssrc']);
        const capture = row => row.sender.rows.find(value => value.type === 'media-source' && value.kind === 'video');
        const captureFrames = counterDelta(rows.map(capture), 'frames', ['id']);
        const finite = value => Number.isFinite(value) ? value : null;
        const delivery = rows.at(-1).receivers.map(receiver => {
            const first = rows[0].receivers.find(value => value.id === receiver.id);
            const inbound = value => value?.rows.find(row => row.type === 'inbound-rtp' && row.trackIdentifier === 'a|screen');
            const receiverRows = rows.map(row => row.receivers.find(value => value.id === receiver.id));
            const inboundRows = receiverRows.map(inbound);
            const frames = counterDelta(inboundRows, 'framesDecoded', ['id', 'ssrc']);
            const receiverSeconds = (receiver.at - first?.at) / 1000;
            const playbackRows = receiverRows.map(row => row?.playback.find(video => video.id === 'a|screen'));
            const change = field => counterDelta(playbackRows, field, ['id', 'presentation_epoch']);
            const playbackTotal = change('total'), playbackDropped = change('dropped');
            const callbacks = change('observed_presentation_callbacks');
            const nonDropped = playbackTotal !== null && playbackDropped !== null && playbackDropped <= playbackTotal ? playbackTotal - playbackDropped : null;
            const decodedFPS = counterRate(frames, receiverSeconds), playbackFPS = counterRate(nonDropped, receiverSeconds);
            return { id: receiver.id, sampleSeconds: finite(receiverSeconds), decodedFPS,
                decodedSourceRatio: frames !== null && sentFrames > 0 ? frames / sentFrames : null,
                decodedTargetRatio: decodedFPS === null ? null : decodedFPS / fps,
                playbackTotalDelta: playbackTotal, playbackDroppedDelta: playbackDropped,
                playbackNonDroppedFPS: playbackFPS,
                playbackNonDroppedTargetRatio: playbackFPS === null ? null : playbackFPS / fps,
                observedPresentationCallbacksDelta: callbacks,
                observedPresentationCallbacksFPS: counterRate(callbacks, receiverSeconds),
                freezes: counterDelta(inboundRows, 'freezeCount', ['id', 'ssrc']),
                freezeSeconds: counterDelta(inboundRows, 'totalFreezesDuration', ['id', 'ssrc']) };
        });
        return { stage, startAt: rows[0].sender.at, endAt: rows.at(-1).sender.at, samples: rows.length,
            scoring, scoredSamples: scored.length, scoredStartAt: scored[0]?.sender.at ?? null, scoredEndAt: scored.at(-1)?.sender.at ?? null,
            workload, phases: [...new Set(rows.map(row => row.sender.workload?.phase).filter(Boolean))],
            sourceFrameTiming: sourceFrameTimingSummary(rows.map(row => row.sender.sourceFrameTiming), seconds),
            // Draw/capture/encode/decode rates are separate observations; none
            // alone establishes that the physical display presented at 60fps.
            fullStageDrawnFPS: counterRate(counterDelta(rows.map(row => row.sender.workload), 'drawn_frames', ['name']), seconds),
            fullStageCaptureFPS: counterRate(captureFrames, seconds), fullStageCaptureTargetRatio: captureFrames === null ? null : counterRate(captureFrames / fps, seconds),
            fullStageEncodedFPS: counterRate(sentFrames, seconds), fullStageEncodedTargetRatio: sentFrames === null ? null : counterRate(sentFrames / fps, seconds),
            fullStageDelivery: delivery };
      });
      fs.writeFileSync(output, JSON.stringify({ profile, configuration, width, height, fps, joins, impairments, stageWindows, errors, failure, samples,
        sourceFrameTimingSemantics: { sync_draw_bin_upper_bounds_exclusive_ms: drawDurationBoundsMS, raf_gap_bin_upper_bounds_exclusive_ms: rafGapBoundsMS,
            note: 'Desktop only. Null final bin bound means unbounded. Bins are cumulative counts in samples and interval deltas in stage summaries; no exact percentiles. Epoch changes invalidate stage timing aggregates. Maxima are lifetime maxima, not stage maxima. Synchronous draw duration measures JavaScript/canvas call submission, excluding deferred raster/GPU work. rAF timestamp gaps are opportunities, not captured-frame intervals.' },
        note: 'Real Chromium encoding and decoding of a synthetic canvas over a local SFU; all contexts share this host. This is not native display capture or proof of GPU acceleration. One source encoding/upload. Capacity video is displayed at 960x540 in a 1280x800 headless viewport; intersection is geometric, not proof of lack of occlusion or physical display. Playback non-dropped rates subtract droppedVideoFrames from totalVideoFrames as a browser quality-counter proxy, not physical display frames. Observed presentation callbacks can miss compositor submissions and are not a complete frame count. Receiver rates use receiver-local sample intervals; decoded/source ratios compare frame-count deltas from separately sampled windows. Missing counters, identity changes or observed intermediate counter regressions invalidate the affected stage aggregate. presentation_epoch identifies observer binding, not rolling timing-window resets. freezeSeconds is browser-reported duration of freezes completed in the sampled window, including any part before the stage; an ongoing freeze may only be reported later. Impairment at/requestedAt precede the request; acknowledgedAt follows its response. Desktop adapts the native 20s static-text/20s scrolling-text/20s motion pattern; elapsed-time labels replace UTC and measured-FPS labels, so pixels are not identical. Pan remains the original high-entropy workload. The 85% decoded/source ratio is a transport capacity floor, not 1080p60 or 95% presentation acceptance; >=15fps warmup is fixture viability only. Slow-writer mode delays only one local video writer, not network jitter. Presentation percentiles are overlapping bounded windows, not a whole-run distribution.' }, null, 2));
    } finally {
        try { await closeBrowser(); } finally { await bounded(() => vite.close(), 3000, 'fixture server close'); }
    }
}
console.log(JSON.stringify({ profile, samples: samples.length, receivers: receivers.length, output,
    rendered: samples.at(-1)?.receivers.map(receiver => ({ id: receiver.id,
        firstPictureMS: receiver.firstFrameAt ? Math.max(0, receiver.firstFrameAt - joins.find(join => join.id === receiver.id).connectedAt) : null,
        pacer: receiver.pacer, decoded: receiver.rows.find(row => row.type === 'inbound-rtp' && row.trackIdentifier === 'a|screen') })) }));
if (failure) throw new Error(failure);
