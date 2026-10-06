import fs from 'node:fs';
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { fileURLToPath, pathToFileURL } from 'node:url';
const frontend = fileURLToPath(new URL('../../../client/frontend/', import.meta.url));
const require = createRequire(`${frontend}/package.json`);
const { chromium, expect } = require('@playwright/test');
const { createServer } = await import(pathToFileURL(require.resolve('vite')).href);
const endpoint = process.argv[2];
const [width, height, fps] = process.argv.slice(3).map(Number);
const profile = 'pan';
const output = process.argv[6];
const vite = await createServer({ root: frontend, logLevel: 'error', server: { host: '127.0.0.1', port: 0, hmr: false } });
await vite.listen();
const origin = `http://127.0.0.1:${vite.httpServer.address().port}`;
let browser;
let failure;
let publisher;
let generation;
const receivers = [];
const errors = [];
const samples = [];
const joins = [];
try {
    browser = await chromium.launch({ headless: true });
    async function fixture() {
        const context = await browser.newContext({ viewport: { width: 1280, height: 800 } });
        context.on('weberror', event => errors.push(String(event.error())));
        const page = await context.newPage();
        await page.route('**/fanout-fixture', route => route.fulfill({ contentType: 'text/html', body: `<!doctype html>
            <button id="voice-screen">Share</button><button id="voice-video">Camera</button>
            <video id="local-video"></video><div id="mic-status"></div><button id="ptt-btn">Talk</button>
            <section id="sharing-status" hidden></section>` }));
        await page.goto(`${origin}/fanout-fixture`);
        await page.exposeFunction('request', async (path, body) => {
            const response = await fetch(`${endpoint}/${path}`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
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
    await publisher.evaluate(async ({ width, height, fps }) => {
        const pc = new RTCPeerConnection({ iceServers: [] });
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
        setInterval(() => {
            frame++;
            const shift = (frame * 13) % width;
            ctx.drawImage(texture, shift, 0, width, height); ctx.drawImage(texture, shift - width, 0, width, height);
            ctx.fillStyle = '#072530'; ctx.fillRect(0, 0, 600, 80);
            ctx.fillStyle = '#40e2fa'; ctx.font = '48px sans-serif'; ctx.fillText(`Synthetic ${frame}`, 20, 60);
        }, 1000 / fps);
        const localStream = new MediaStream();
        window.__noxa = { state: { pc, localStream, serverGeneration: 1, sessionGeneration: 1,
            activeTabID: 'a', settings: {}, myChannelID: 1, myClientID: 'a', clients: [] },
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
    }, { width, height, fps });
    await publisher.locator('#voice-screen').click();
    await publisher.locator('.sh-preset').selectOption(fps === 60 ? 'hdMotion' : 'original');
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
        await page.evaluate(async id => {
            const pc = new RTCPeerConnection({ iceServers: [] });
            window.receiver = pc;
            pc.ontrack = ({ track }) => {
                if (track.kind === 'audio') {
                    const audio = document.createElement('audio'); audio.muted = true; audio.autoplay = true;
                    audio.srcObject = new MediaStream([track]); document.body.append(audio); return;
                }
                if (track.kind !== 'video') return;
                const video = document.createElement('video');
                video.muted = true; video.autoplay = true; video.playsInline = true; video.style.width = '960px';
                video.srcObject = new MediaStream([track]); document.body.append(video);
                video.requestVideoFrameCallback(() => { window.firstFrameAt ||= Date.now(); });
            };
            await pc.setRemoteDescription(await window.request('viewer', { id }));
            await pc.setLocalDescription(await pc.createAnswer()); await window.gather(pc);
            await window.request('answer', { id, sdp: pc.localDescription.sdp });
        }, id);
        joins.push({ id, startedAt, connectedAt: Date.now() });
        receivers.push({ id, page });
        await refresh();
    }
    const capture = page => page.evaluate(async () => {
        const pc = window.receiver || window.__noxa.state.pc;
        const keys = ['id', 'type', 'timestamp', 'kind', 'ssrc', 'rid', 'active', 'trackIdentifier', 'codecId', 'mediaSourceId',
            'frameWidth', 'frameHeight', 'frames', 'framesPerSecond', 'framesEncoded', 'framesSent', 'framesReceived', 'framesDecoded', 'framesDropped',
            'bytesSent', 'bytesReceived', 'packetsSent', 'packetsReceived', 'packetsLost', 'retransmittedBytesSent', 'retransmittedPacketsSent',
            'retransmittedBytesReceived', 'retransmittedPacketsReceived', 'keyFramesEncoded', 'keyFramesDecoded', 'nackCount', 'pliCount', 'firCount',
            'freezeCount', 'totalFreezesDuration', 'jitter', 'jitterBufferDelay', 'jitterBufferEmittedCount', 'totalDecodeTime', 'totalEncodeTime',
            'concealedSamples', 'silentConcealedSamples', 'totalSamplesReceived', 'concealmentEvents',
            'totalInterFrameDelay', 'totalSquaredInterFrameDelay', 'totalPacketSendDelay', 'targetBitrate', 'qualityLimitationReason',
            'qualityLimitationDurations', 'qualityLimitationResolutionChanges', 'encoderImplementation', 'decoderImplementation', 'mimeType',
            'currentRoundTripTime', 'availableOutgoingBitrate', 'availableIncomingBitrate', 'state', 'nominated'];
        return { at: Date.now(), state: pc.connectionState, firstFrameAt: window.firstFrameAt || null,
            encodings: window.__noxa?.state.shareVideoTransceiver.sender.getParameters().encodings,
            rows: [...(await pc.getStats()).values()].filter(row => ['outbound-rtp', 'inbound-rtp', 'media-source', 'codec', 'candidate-pair'].includes(row.type))
                .map(row => Object.fromEntries(keys.filter(key => row[key] !== undefined).map(key => [key, row[key]]))),
            playback: [...document.querySelectorAll('video')].filter(video => video.srcObject).map(video => ({
                id: video.srcObject.getVideoTracks()[0]?.id, width: video.videoWidth, height: video.videoHeight, paused: video.paused,
                total: video.getVideoPlaybackQuality().totalVideoFrames, dropped: video.getVideoPlaybackQuality().droppedVideoFrames })) };
    });
    async function sample(stage, seconds) {
        for (let second = 0; second < seconds; second++) {
            const started = Date.now();
            const sender = await capture(publisher);
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
    await receiver('viewer'); await sample('one', 15);
    await waitForBusySource('one-warmup', 12_000_000);
    await receiver('viewer2'); await sample('two', 15);
    await waitForBusySource('two-warmup', 15_000_000);
    await receiver('viewer3'); await sample('three', 20);
    const last = samples.at(-1);
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
        const terminal = samples.filter(sample => sample.stage === 'three').slice(-10);
        const senderRow = sample => sample.sender.rows.find(row => row.type === 'outbound-rtp' && row.kind === 'video');
        const sentFrames = senderRow(terminal.at(-1)).framesEncoded - senderRow(terminal[0]).framesEncoded;
        assert.ok(sentFrames >= 120, 'source sends sustained measured frames');
        const elapsedSeconds = (terminal.at(-1).sender.at - terminal[0].sender.at) / 1000;
        const sourceBitrate = (senderRow(terminal.at(-1)).bytesSent - senderRow(terminal[0]).bytesSent) * 8 / elapsedSeconds;
        assert.ok(sourceBitrate >= 10_000_000, 'workload exercises a busy source above the old startup pacing budget');
        for (const id of ['viewer', 'viewer2', 'viewer3']) {
            const frame = sample => sample.receivers.find(row => row.id === id).rows.find(row => row.type === 'inbound-rtp' && row.trackIdentifier === 'a|screen').framesDecoded;
            assert.ok((frame(terminal.at(-1)) - frame(terminal[0])) / sentFrames >= .85, `${id}: keeps up with source frames`);
            const join = joins.find(row => row.id === id);
            const firstFrameAt = last.receivers.find(row => row.id === id).firstFrameAt;
            assert.ok(firstFrameAt >= join.startedAt && Math.max(0, firstFrameAt - join.connectedAt) < 5000,
                `${id}: first picture within 5 seconds`);
        }
        for (const receiver of samples.flatMap(sample => sample.receivers)) {
            assert.ok(receiver.pacer.packets <= 1024 && receiver.pacer.bytes <= 2 * 1024 * 1024, 'bounded video send queue');
        }
    }
    assert.deepEqual(errors, []);
} catch (error) {
    failure = String(error?.stack || error);
} finally {
    fs.writeFileSync(output, JSON.stringify({ profile, width, height, fps, joins, errors, failure, samples }, null, 2));
    try { await browser?.close(); } finally { await vite.close(); }
}
console.log(JSON.stringify({ profile, samples: samples.length, receivers: receivers.length, output,
    rendered: samples.at(-1)?.receivers.map(receiver => ({ id: receiver.id,
        firstPictureMS: receiver.firstFrameAt ? Math.max(0, receiver.firstFrameAt - joins.find(join => join.id === receiver.id).connectedAt) : null,
        pacer: receiver.pacer, decoded: receiver.rows.find(row => row.type === 'inbound-rtp' && row.trackIdentifier === 'a|screen') })) }));
if (failure) throw new Error(failure);
