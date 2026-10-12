import assert from "node:assert/strict";
import test from "node:test";
import { registerHooks } from "node:module";

registerHooks({ load(url, context, next) {
    if (url.endsWith(".css")) return { format: "module", source: "export {};", shortCircuit: true };
    return next(url, context);
} });
globalThis.window = { addEventListener() {} };
const { createVideoPublication } = await import("../src/video-publication.js");
const { createVideoSenderRecovery } = await import("../src/video-sender-hardware-recovery.js");
const { createHardwareRecoveryScheduler } = await import("../src/video-hardware-recovery.js");

function fixture(options = {}) {
    let parameters = { degradationPreference: "maintain-framerate", encodings: [{ active: true, scaleResolutionDownBy: 1, maxBitrate: 8000000 }] };
    let frames = 0;
    const writes = [], queries = [];
    const size = options.size || { width: 1920, height: 1080 };
    const track = { id: "capture-track", kind: "video", readyState: "live", enabled: true, contentHint: "motion",
        getSettings: () => ({ ...size, frameRate: 60 }) };
    const sender = { track,
        getParameters: () => structuredClone(parameters),
        async setParameters(next) {
            writes.push(structuredClone(next));
            parameters = structuredClone(next);
            await options.onWrite?.(next, { sender, state, track });
        },
        async getStats() {
            const scale = parameters.encodings[0].scaleResolutionDownBy || 1;
            await options.onStats?.(scale);
            frames++;
            return new Map([
                ["v", { id: "v", type: "outbound-rtp", kind: "video", codecId: "c", timestamp: frames * 150,
                    framesEncoded: options.stale ? 1 : frames, frameWidth: size.width / scale, frameHeight: size.height / scale,
                    powerEfficientEncoder: false, qualityLimitationReason: "none", ...(options.capture ? {
                        mediaSourceId: "source", encoderImplementation: "OpenH264" } : {}), ...options.row }],
                ["c", { id: "c", type: "codec", mimeType: "video/H264", sdpFmtpLine: "profile-level-id=42001f;packetization-mode=1" }],
                ...(options.capture ? [["source", { id: "source", type: "media-source", kind: "video", trackIdentifier: track.id,
                    timestamp: frames * 150, frames: frames * 10, ...options.capture }]] : []),
            ]);
        },
    };
    const transceiver = { sender, receiver: { track: { kind: "video" } }, direction: "sendonly" };
    const pc = { connectionState: "connected", signalingState: "stable", getTransceivers: () => [transceiver], getSenders: () => [sender] };
    const state = { pc, shareVideoTransceiver: transceiver, shareStream: { getVideoTracks: () => [track] },
        serverGeneration: 1, sessionGeneration: 1, activeTabID: "a", myClientID: "me", myChannelID: 1 };
    window.__noxa = { state, sysMsg() {} };
    Object.defineProperty(globalThis, "navigator", { configurable: true, value: { mediaCapabilities: {
        encodingInfo: async config => { queries.push(config); return { supported: true, powerEfficient: options.capable !== false }; },
    } } });
    const api = createVideoPublication({ policy: { lowBandwidth: false } });
    return { api, sender, track, transceiver, pc, state, writes, queries };
}

test("hardware retry pulses one existing sender and restores its source quality", async () => {
    const f = fixture();
    assert.equal(await f.api.retryVideoHardware("screen", f.track), true);
    assert.deepEqual(f.writes.map(p => p.encodings[0].scaleResolutionDownBy), [1, 2, 1]);
    assert.deepEqual(f.queries.map(q => [q.video.width, q.video.height]), [[1920, 1080], [960, 540]]);
    assert.equal(f.sender.track, f.track);
    assert.equal(f.track.readyState, "live");
    assert.deepEqual(f.sender.getParameters(), { degradationPreference: "maintain-framerate", encodings: [{ active: true, scaleResolutionDownBy: 1, maxBitrate: 8000000 }] });
});

test("hardware retry skips network adaptation, unsupported hardware, and stale frames", async () => {
    for (const options of [{ row: { qualityLimitationReason: "bandwidth", frameWidth: 1280, frameHeight: 720 } },
        { row: { qualityLimitationReason: "other" } }, { row: { qualityLimitationReason: undefined } },
        { row: { frameWidth: 320, frameHeight: 180 } }, { row: { frameWidth: 1280, frameHeight: 720 } },
        { row: { powerEfficientEncoder: true } }, { capable: false }, { stale: true }]) {
        const f = fixture(options);
        assert.equal(await f.api.retryVideoHardware("screen", f.track), false);
        assert.equal(f.writes.length, 0);
    }
});

test("the verified 360p geometry can pulse to even 180p without compounding adaptation", async () => {
    const f = fixture({ size: { width: 640, height: 360 } });
    assert.equal(await f.api.retryVideoHardware("screen", f.track), true);
    assert.deepEqual(f.queries.map(q => [q.video.width, q.video.height]), [[640, 360], [320, 180]]);
    assert.equal(f.sender.getParameters().encodings[0].scaleResolutionDownBy, 1);
});

test("CPU-limited software can retry hardware while keeping pulse geometry below the current load", async () => {
    const f = fixture({ row: { qualityLimitationReason: "cpu" } });
    assert.equal(await f.api.retryVideoHardware("screen", f.track), true);
    assert.deepEqual(f.writes.map(p => p.encodings[0].scaleResolutionDownBy), [1, 2, 1]);
});

test("encoder rejection restores only the pulse parameters", async () => {
    const f = fixture({ onWrite(parameters) {
        if (parameters.encodings[0].scaleResolutionDownBy === 2) throw new Error("encoder unavailable");
    } });
    await assert.rejects(f.api.retryVideoHardware("screen", f.track), /encoder unavailable/);
    assert.equal(f.sender.getParameters().encodings[0].scaleResolutionDownBy, 1);
    assert.equal(f.sender.getParameters().degradationPreference, "maintain-framerate");
});

test("a stopped or replaced source is never restored by late retry completion", async () => {
    const replacement = { kind: "video", readyState: "live", enabled: true };
    const f = fixture({ onWrite(parameters, { sender }) {
        if (parameters.encodings[0].scaleResolutionDownBy === 2) sender.track = replacement;
    } });
    assert.equal(await f.api.retryVideoHardware("screen", f.track), true);
    assert.equal(f.sender.track, replacement);
    assert.equal(f.writes.length, 2);
});

test("loss of retry eligibility still restores a live source's owned parameters", async () => {
    let relevant = true;
    const f = fixture({ onWrite(parameters) {
        if (parameters.encodings[0].scaleResolutionDownBy === 2) relevant = false;
    } });
    assert.equal(await f.api.retryVideoHardware("screen", f.track, () => relevant), true);
    assert.equal(f.sender.getParameters().encodings[0].scaleResolutionDownBy, 1);
    assert.equal(f.sender.getParameters().degradationPreference, "maintain-framerate");
    assert.equal(f.writes.length, 3);
});

test("bandwidth changes wait for the pulse and retain the newer budget", async () => {
    let finish, entered;
    const active = new Promise(resolve => { entered = resolve; });
    const wait = new Promise(resolve => { finish = resolve; });
    const f = fixture({ onStats: async scale => { if (scale === 2) { entered(); await wait; } } });
    const retry = f.api.retryVideoHardware("screen", f.track);
    await active;
    f.state.mediaLimits = { video_max_bitrate: 4000000 };
    const caps = f.api.applySendCaps();
    assert.equal(f.writes.length, 2);
    finish();
    await Promise.all([retry, caps]);
    assert.equal(f.sender.getParameters().encodings[0].maxBitrate, 3400000);
    assert.equal(f.sender.getParameters().encodings[0].scaleResolutionDownBy, 1);
    assert.equal(f.writes.length, 4);
});

test("a hung sender stats call times out and is reused without parameter changes", async () => {
    const f = fixture();
    let calls = 0;
    f.sender.getStats = () => { calls++; return new Promise(() => {}); };
    await assert.rejects(f.api.retryVideoHardware("screen", f.track), /stats timeout/);
    await assert.rejects(f.api.retryVideoHardware("screen", f.track), /stats timeout/);
    assert.equal(calls, 1);
    assert.equal(f.writes.length, 0);
});

test("later parameter ownership survives a retry cleanup", async () => {
    let relevant = true;
    const f = fixture({ async onWrite(parameters, { sender }) {
        if (parameters.encodings[0].scaleResolutionDownBy !== 2) return;
        relevant = false;
        await sender.setParameters({ ...parameters, degradationPreference: "balanced",
            encodings: [{ ...parameters.encodings[0], scaleResolutionDownBy: 3, maxBitrate: 2345678 }] });
    } });
    assert.equal(await f.api.retryVideoHardware("screen", f.track, () => relevant), true);
    assert.equal(f.sender.getParameters().degradationPreference, "balanced");
    assert.equal(f.sender.getParameters().encodings[0].scaleResolutionDownBy, 3);
    assert.equal(f.sender.getParameters().encodings[0].maxBitrate, 2345678);
});

test("the elapsed pulse deadline restores quality before a delayed stats read finishes", async () => {
    let finish;
    const pending = new Promise(resolve => { finish = resolve; });
    const f = fixture({ onStats: scale => scale === 2 ? pending : undefined });
    try {
        assert.equal(await f.api.retryVideoHardware("screen", f.track), true);
        assert.equal(f.sender.getParameters().encodings[0].scaleResolutionDownBy, 1);
        assert.equal(f.sender.getParameters().degradationPreference, "maintain-framerate");
    } finally { finish(); }
});


test("bandwidth-limited unadapted software retains one sender, cap and pulse restoration", async () => {
    const f = fixture({ row: { qualityLimitationReason: "bandwidth" } });
    assert.equal(await f.api.retryVideoHardware("screen", f.track), true);
    assert.deepEqual(f.writes.map(p => p.encodings[0].scaleResolutionDownBy), [1, 2, 1]);
    assert.ok(f.writes.every(p => p.encodings.length === 1 && p.encodings[0].maxBitrate === 8000000));
    assert.equal(f.sender.track, f.track);
    assert.equal(f.sender.getParameters().degradationPreference, "maintain-framerate");
});

test("delayed bandwidth reason remains eligible only while geometry and other guards survive", async () => {
    for (const atRead of [2, 3]) {
        const options = { row: { qualityLimitationReason: "none" } };
        let reads = 0;
        options.onStats = () => { if (++reads === atRead) options.row.qualityLimitationReason = "bandwidth"; };
        const f = fixture(options);
        assert.equal(await f.api.retryVideoHardware("screen", f.track), true);
        assert.deepEqual(f.writes.map(p => p.encodings[0].scaleResolutionDownBy), [1, 2, 1]);
    }
});

test("delayed adaptation or unknown limitation prevents the pulse and restores owned preference", async () => {
    for (const atRead of [2, 3]) {
        for (const change of [{ frameWidth: 1280, frameHeight: 720 }, { qualityLimitationReason: "other" }]) {
            const options = { row: { qualityLimitationReason: "bandwidth" } };
            let reads = 0;
            options.onStats = () => { if (++reads === atRead) Object.assign(options.row, change); };
            const f = fixture(options);
            assert.equal(await f.api.retryVideoHardware("screen", f.track), false);
            assert.deepEqual(f.writes.map(p => p.encodings[0].scaleResolutionDownBy), atRead === 2 ? [] : [1, 1]);
            assert.equal(f.sender.getParameters().degradationPreference, "maintain-framerate");
        }
    }
});

const captureProof = { sourceId: "source", codec: "video/H264", fmtp: "profile-level-id=42001f;packetization-mode=1", width: 1920, height: 1080 };

test("zero encoded output can pulse only with prior-hardware proof and independently advancing capture", async () => {
    const options = { capture: {}, row: { framesEncoded: 0 } };
    // The normal pulse completion criterion is still actual encoded output.
    options.onStats = scale => { if (scale === 2) options.row.framesEncoded = 1; };
    const actual = fixture(options);
    assert.equal(await actual.api.retryVideoHardware("screen", actual.track, () => true, captureProof), true);
    assert.deepEqual(actual.writes.map(p => p.encodings[0].scaleResolutionDownBy), [1, 2, 1]);
    assert.ok(actual.writes.every(p => p.encodings.length === 1 && p.encodings[0].maxBitrate === 8000000));
    assert.equal(actual.sender.track, actual.track);
});

test("stalled-output pulse rejects absent proof, idle, changed or stale capture and restores delayed changes", async () => {
    for (const mode of ["no-proof", "wrong-proof", "idle", "wrong-track", "stale", "counter-reset", "late-source", "late-geometry", "unknown-encoder"]) {
        let reads = 0;
        const options = { capture: {}, row: { framesEncoded: 0 } };
        if (mode === "idle") options.capture.frames = 1;
        if (mode === "wrong-track") options.capture.trackIdentifier = "other";
        if (mode === "stale") options.capture.timestamp = -10000;
        if (mode === "unknown-encoder") options.row.encoderImplementation = "unknown";
        options.onStats = () => {
            reads++;
            if (mode === "counter-reset" && reads === 2) options.capture.frames = 0;
            if (mode === "late-source" && reads === 3) options.capture.id = "other";
            if (mode === "late-geometry" && reads === 3) options.row.frameWidth = 1280;
        };
        const f = fixture(options);
        const proof = mode === "no-proof" ? null : mode === "wrong-proof" ? { ...captureProof, sourceId: "other" } : captureProof;
        assert.equal(await f.api.retryVideoHardware("screen", f.track, () => true, proof), false, mode);
        assert.deepEqual(f.writes.map(p => p.encodings[0].scaleResolutionDownBy), mode.startsWith("late-") ? [1, 1] : [], mode);
        assert.equal(f.sender.getParameters().degradationPreference, "maintain-framerate");
    }
});

test("default sender recovery reaches both capability probes with zero encoded FPS", async () => {
    let time = 0;
    const options = { capture: {}, row: { framesEncoded: 10, framesPerSecond: 60, powerEfficientEncoder: true } };
    options.onStats = scale => { if (scale === 2) options.row.framesEncoded = 1; };
    const f = fixture(options);
    const publication = { pc: f.pc, track: f.track, slot: "screen", generation: "1",
        scope: { tabID: "a", generation: 1, session: 1, clientID: "me", channelID: 1 } };
    const recover = createVideoSenderRecovery({ state: f.state, retry: f.api.retryVideoHardware,
        snapshot: () => [{ publication, generation: "1" }], uploadActive: () => true,
        scheduler: createHardwareRecoveryScheduler({ now: () => time }) });
    // No capable override: exercise the actual scheduler, default capability
    // implementation, publication proof forwarding and full/half pulse probes.
    await recover(f.pc, await f.sender.getStats());
    Object.assign(options.row, { framesEncoded: 0, framesPerSecond: 0, powerEfficientEncoder: false });
    for (const at of [5000, 10000, 15000]) {
        time = at;
        await recover(f.pc, await f.sender.getStats());
    }
    assert.deepEqual(f.queries.map(q => [q.video.width, q.video.height, q.video.framerate]), [[1920, 1080, 60], [960, 540, 60]]);
    assert.deepEqual(f.writes.map(p => p.encodings[0].scaleResolutionDownBy), [1, 2, 1]);
    assert.ok(f.writes.every(p => p.encodings.length === 1 && p.encodings[0].maxBitrate === 8000000));
    assert.equal(f.sender.track, f.track);
    assert.equal(f.sender.getParameters().degradationPreference, "maintain-framerate");
});
