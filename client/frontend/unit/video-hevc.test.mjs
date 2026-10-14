import test from "node:test";
import assert from "node:assert/strict";
import { hevcFormat, preferredHEVCFormat, receiverHEVCSupport, knownSoftwareVideoEncoder } from "../src/video-hevc.js";
import { publicationCodecSupport, preferPublicationCodec } from "../src/video-codecs.js";

const vp8 = { mimeType: "video/VP8", clockRate: 90000 };
const h264 = { mimeType: "video/H264", clockRate: 90000, sdpFmtpLine: "packetization-mode=1;profile-level-id=42001f" };
const h265 = { mimeType: "video/H265", clockRate: 90000, sdpFmtpLine: "profile-id=1;tier-flag=0;level-id=153;tx-mode=SRST" };
const track = () => ({ id: "camera", readyState: "live", getSettings: () => ({ width: 1920, height: 1080, frameRate: 60 }) });
const flush = async () => { for (let i = 0; i < 30; i++) await Promise.resolve(); };
function install(t, { codecs = [h265, h264, vp8], encode = async () => ({ supported: true, powerEfficient: true }), decode = encode } = {}) {
    for (const [key, value] of Object.entries({ RTCRtpSender: { getCapabilities: () => ({ codecs }) }, RTCRtpReceiver: { getCapabilities: () => ({ codecs }) },
        navigator: { mediaCapabilities: { encodingInfo: encode, decodingInfo: decode } } })) {
        const old = Object.getOwnPropertyDescriptor(globalThis, key);
        Object.defineProperty(globalThis, key, { configurable: true, value });
        t.after(() => { if (old) Object.defineProperty(globalThis, key, old); else delete globalThis[key]; });
    }
}

test("HEVC contract rejects ambiguous profiles, high tier, DONL and unsupported packet modes", () => {
    assert.deepEqual(hevcFormat(h265), { level: 153 });
    assert.deepEqual(hevcFormat({ ...h265, sdpFmtpLine: "level-id=123" }), { level: 123 });
    for (const fmtp of ["profile-id=2;level-id=153", "tier-flag=1;level-id=153", "level-id=153;tx-mode=MRST",
        "level-id=153;sprop-max-don-diff=1", "level-id=153;LEVEL-ID=153", "profile-id=1", "level-id=0", "level-id=255",
        "level-id=0153", "level-id=153;unknown=1", "level-id=153;interop-constraints=oops"]) {
        assert.equal(hevcFormat({ ...h265, sdpFmtpLine: fmtp }), null, fmtp);
    }
    assert.equal(hevcFormat({ ...h265, clockRate: 8000 }), null);
});

test("HEVC is explicit opt-in and preferences retain one exact native HEVC level", async t => {
    const lower = { ...h265, sdpFmtpLine: "profile-id=1;level-id=120" }, rtx = { mimeType: "video/rtx", clockRate: 90000 };
    const calls = []; install(t, { codecs: [lower, h265, h264, vp8, rtx], encode: async value => { calls.push(value); return { supported: true, powerEfficient: true }; } });
    assert.deepEqual((await publicationCodecSupport(track())).codecs, ["h264", "vp8"]);
    assert.ok(calls.every(row => !row.video.contentType.includes("H265")), "legacy servers do not query or advertise HEVC");
    const result = await publicationCodecSupport(track(), true);
    assert.deepEqual(result.codecs, ["h265", "h264", "vp8"]); assert.equal(result.hevc_level_id, 153);
    assert.equal(result.hevcFormat, h265); assert.equal(preferredHEVCFormat([lower, h265]), h265);
    const preferences = []; const transceiver = { setCodecPreferences: codecs => preferences.push(codecs) };
    assert.equal(preferPublicationCodec(transceiver, "h265", result.hevcFormat), true);
    assert.equal(preferences[0][0], h265); assert.equal(preferences[0].includes(lower), false); assert.ok(preferences[0].includes(rtx));
    assert.equal(preferPublicationCodec(transceiver, "vp8"), true);
    assert.ok(preferences[1].every(row => row.mimeType !== "video/H265"));
    assert.equal(preferPublicationCodec(transceiver, "h265"), false, "no invented level for unpinned HEVC");
});

test("unsupported HEVC retains hardware H264 and no level field", async t => {
    install(t, { encode: async value => ({ supported: true, powerEfficient: !value.video.contentType.includes("H265") }) });
    const result = await publicationCodecSupport(track(), true);
    assert.deepEqual(result.codecs, ["h264", "vp8"]); assert.equal(result.hevc_level_id, undefined); assert.equal(result.preferredAvailable, false);
});

test("pending old capture evidence cannot advertise HEVC after geometry changes", async t => {
    let resolve; const settings = { width: 1920, height: 1080, frameRate: 60 };
    install(t, { encode: () => new Promise(done => { resolve = done; }), codecs: [h265, vp8] });
    const pending = publicationCodecSupport({ getSettings: () => settings }, true); await flush();
    settings.width = 1280; resolve({ supported: true, powerEfficient: true });
    assert.deepEqual((await pending).codecs, ["vp8"]);
});

test("receiver hardware prediction retains one wait for repeated hung calls and admits late success", async t => {
    t.mock.timers.enable({ apis: ["setTimeout"] });
    let queries = 0, resolve;
    install(t, { decode: () => { queries++; return new Promise(done => { resolve = done; }); } });
    const pending = receiverHEVCSupport(); await flush(); t.mock.timers.tick(2000);
    assert.equal(await pending, null);
    for (let i = 0; i < 100; i++) assert.equal(await receiverHEVCSupport(), null);
    assert.equal(queries, 1);
    resolve({ supported: true, powerEfficient: true }); await flush();
    assert.deepEqual(await receiverHEVCSupport(), { hevc_level_id: 153 });
});

test("receiver does not mistake advertised codec or software support for hardware prediction", async t => {
    install(t, { decode: async () => ({ supported: true, powerEfficient: false }) });
    assert.equal(await receiverHEVCSupport(), null);
    assert.equal(knownSoftwareVideoEncoder(h265, "FFmpeg"), true);
    assert.equal(knownSoftwareVideoEncoder(h265, "unknown"), false);
    assert.equal(knownSoftwareVideoEncoder(h265, "MediaFoundationVideoEncoder"), false);
});

async function publicationFixture(t, { hevc = true, encode, selected = null, level = 153, change } = {}) {
    install(t, { encode });
    globalThis.window = { addEventListener() {} };
    const publication = await import("../src/stream-publication.js");
    const requests = [], pc = {}, source = track(), state = { pc, activeTabID: "a", serverGeneration: 1, sessionGeneration: 1, myClientID: "self", myChannelID: 1 };
    window.__noxa = { state };
    window.go = { main: { App: { SupportsStreamSourceQualityForTab: async () => true, SupportsStreamCodecForTab: async () => true,
        SupportsStreamCodecRefreshForTab: async () => true, SupportsStreamHEVCForTab: async () => hevc,
        VideoStreamControlForTab: async (_tab, body) => { requests.push(body); return { ...body, generation: body.generation === "0" ? "42" : body.generation,
            codec: selected || body.codecs?.[0], hevc_level_id: (selected || body.codecs?.[0]) === "h265" ? level : undefined, upload_active: true }; } } } };
    t.after(() => publication.stopPublication("cam", source));
    const applied = [];
    await publication.preparePublicationUpload("cam", source, options => { applied.push(publication.publicationCodec("cam", source)); return change?.(options); });
    return { publication, requests, state, source, applied };
}

test("one publication changes authoritative HEVC to fallback and back without replacing source", async t => {
    const f = await publicationFixture(t); await f.publication.startPublication("cam", f.source);
    assert.deepEqual(f.requests[0].codecs, ["h265", "h264", "vp8"]); assert.equal(f.requests[0].hevc_level_id, 153);
    for (const codec of ["vp8", "h264", "h265"]) {
        f.publication.reconcilePublicationUploads(f.publication.publicationSnapshot(), [{ publisher_id: "self", slot: "cam", generation: "42",
            codec, codec_revision: "1", hevc_level_id: codec === "h265" ? 153 : undefined, upload_active: true }]); await flush();
    }
    assert.deepEqual(f.applied, ["h265", "vp8", "h264", "h265"]);
    assert.equal(f.requests.length, 1); assert.equal(f.publication.publicationSnapshot()[0].publication.track, f.source);
});

test("old server receives no HEVC metadata and changed selected level is rejected", async t => {
    const f = await publicationFixture(t, { hevc: false, selected: "h264" }); await f.publication.startPublication("cam", f.source);
    assert.deepEqual(f.requests[0].codecs, ["h264", "vp8"]); assert.equal(Object.hasOwn(f.requests[0], "hevc_level_id"), false);
    await f.publication.stopPublication("cam", f.source);
    const g = await publicationFixture(t, { level: 120 });
    await assert.rejects(g.publication.startPublication("cam", g.source), /HEVC publication level changed/);
    assert.equal(g.publication.publicationSnapshot().length, 0);
});


test("lower admitted native HEVC format wins when highest is not hardware supported", async t => {
    const lower = { ...h265, sdpFmtpLine: "profile-id=1;level-id=120" };
    install(t, { codecs: [h265, lower, h264, vp8], encode: async value => ({ supported: true, powerEfficient: !value.video.contentType.includes("level-id=153") }) });
    const result = await publicationCodecSupport(track(), true);
    assert.equal(result.hevc_level_id, 120); assert.equal(result.hevcFormat, lower);
    assert.deepEqual(await receiverHEVCSupport(), { hevc_level_id: 120 });
});

test("prepared HEVC preference failure falls back before publication without another source", async t => {
    const f = await publicationFixture(t);
    assert.equal(await f.publication.withdrawPublicationHEVC("cam", f.source, () => true, false), true);
    assert.equal(f.requests.length, 0);
    await f.publication.startPublication("cam", f.source);
    assert.deepEqual(f.requests[0].codecs, ["h264", "vp8"]);
    assert.equal(f.requests[0].codec_revision, "1");
    assert.equal(f.publication.publicationCodec("cam", f.source), "h264");
});

test("rejected withdrawal retries exact revision despite older catalog and rejects stale HEVC selection", async t => {
    t.mock.timers.enable({ apis: ["Date"], now: 1000000 });
    const f = await publicationFixture(t); await f.publication.startPublication("cam", f.source);
    const original = window.go.main.App.VideoStreamControlForTab;
    let rejected = false;
    window.go.main.App.VideoStreamControlForTab = async (tab, body) => {
        if (body.codec_revision === "2" && !rejected) { rejected = true; throw new Error("not applied"); }
        return original(tab, body);
    };
    await assert.rejects(f.publication.withdrawPublicationHEVC("cam", f.source), /not applied/);
    const old = [{ publisher_id: "self", slot: "cam", generation: "42", codec_revision: "1", codec: "h265", hevc_level_id: 153, upload_active: true }];
    f.publication.reconcilePublicationUploads(f.publication.publicationSnapshot(), old); await flush();
    assert.equal(f.requests.length, 1);
    t.mock.timers.tick(30000);
    f.publication.reconcilePublicationUploads(f.publication.publicationSnapshot(), old); await flush();
    assert.equal(f.requests.at(-1).codec_revision, "2"); assert.deepEqual(f.requests.at(-1).codecs, ["h264", "vp8"]);
    assert.equal(f.publication.publicationCodec("cam", f.source), "h264");
    f.publication.reconcilePublicationUploads(f.publication.publicationSnapshot(), old); await flush();
    assert.equal(f.publication.publicationCodec("cam", f.source), "h264");
});

test("withdrawal never commits to a replaced source after native capability completion", async t => {
    const f = await publicationFixture(t); await f.publication.startPublication("cam", f.source);
    let current = true;
    const pending = f.publication.withdrawPublicationHEVC("cam", f.source, () => current);
    current = false;
    assert.equal(await pending, false); assert.equal(f.requests.length, 1);
    f.publication.reconcilePublicationUploads(f.publication.publicationSnapshot(), [{ publisher_id: "self", slot: "cam", generation: "42",
        codec_revision: "1", codec: "vp8", upload_active: true }]); await flush();
    assert.equal(f.publication.publicationCodec("cam", f.source), "vp8", "unsent reservation cannot strand authoritative catalog");
});


test("three HEVC re-admissions back off5/15/60minutes then remain on fallback until a fresh source", async t => {
    t.mock.timers.enable({ apis: ["Date"], now: 1000000 });
    const f = await publicationFixture(t); await f.publication.startPublication("cam", f.source);
    for (const delay of [300000, 900000, 3600000]) {
        assert.equal(await f.publication.withdrawPublicationHEVC("cam", f.source), true);
        const revision = f.requests.at(-1).codec_revision;
        const catalog = [{ publisher_id: "self", slot: "cam", generation: "42", codec_revision: revision, codec: "h264", upload_active: true }];
        const before = f.requests.length;
        t.mock.timers.tick(delay - 1); f.publication.reconcilePublicationUploads(f.publication.publicationSnapshot(), catalog); await flush();
        assert.equal(f.requests.length, before);
        t.mock.timers.tick(1); f.publication.reconcilePublicationUploads(f.publication.publicationSnapshot(), catalog); await flush();
        assert.equal(f.requests.length, before + 1); assert.equal(f.requests.at(-1).codecs[0], "h265");
    }
    assert.equal(await f.publication.withdrawPublicationHEVC("cam", f.source), true);
    const before = f.requests.length, revision = f.requests.at(-1).codec_revision;
    for (let i = 0; i < 5; i++) {
        t.mock.timers.tick(3600000);
        f.publication.reconcilePublicationUploads(f.publication.publicationSnapshot(), [{ publisher_id: "self", slot: "cam", generation: "42",
            codec_revision: revision, codec: "h264", upload_active: true }]); await flush();
    }
    assert.equal(f.requests.length, before); assert.equal(f.publication.publicationCodec("cam", f.source), "h264");
    await f.publication.stopPublication("cam", f.source);
    await f.publication.preparePublicationUpload("cam", f.source, () => {});
    await f.publication.startPublication("cam", f.source);
    assert.equal(f.requests.at(-1).codecs[0], "h265");
});

test("older codec revision still pauses exact-generation upload demand while a replacement is pending", async t => {
    const f = await publicationFixture(t); await f.publication.startPublication("cam", f.source);
    const original = window.go.main.App.VideoStreamControlForTab;
    window.go.main.App.VideoStreamControlForTab = (tab, body) => body.codec_revision === "2" ? Promise.reject(new Error("not applied")) : original(tab, body);
    await assert.rejects(f.publication.withdrawPublicationHEVC("cam", f.source));
    f.publication.reconcilePublicationUploads(f.publication.publicationSnapshot(), [{ publisher_id: "self", slot: "cam", generation: "42",
        codec_revision: "1", codec: "vp8", upload_active: false }]); await flush();
    assert.equal(f.publication.publicationUploadActive("cam", f.source), false);
    assert.equal(f.publication.publicationCodec("cam", f.source), "h265", "do not adopt stale catalog codec");
});


test("queued demand pause retains demand-only mode behind an older encoder update", async t => {
    let blocked = false, finish; const calls = [];
    const f = await publicationFixture(t, { change: options => {
        calls.push(options);
        if (blocked) { blocked = false; return new Promise(resolve => { finish = resolve; }); }
    } });
    await f.publication.startPublication("cam", f.source);
    blocked = true;
    const snapshot = f.publication.publicationSnapshot();
    f.publication.reconcilePublicationUploads(snapshot, [{ publisher_id: "self", slot: "cam", generation: "42",
        codec_revision: "1", codec: "vp8", upload_active: true }]); await flush();
    const original = window.go.main.App.VideoStreamControlForTab;
    window.go.main.App.VideoStreamControlForTab = (tab, body) => body.codec_revision === "2" ? Promise.reject(new Error("not applied")) : original(tab, body);
    await assert.rejects(f.publication.withdrawPublicationHEVC("cam", f.source));
    f.publication.reconcilePublicationUploads(snapshot, [{ publisher_id: "self", slot: "cam", generation: "42",
        codec_revision: "1", codec: "h265", hevc_level_id: 153, upload_active: false }]); await flush();
    finish(); await flush();
    assert.equal(calls.at(-1).demandOnly, true);
    assert.equal(f.publication.publicationUploadActive("cam", f.source), false);
});


test("rejected unchanged demand-only parameters retry on catalog polling, never an immediate loop", async t => {
    let attempts = 0;
    const f = await publicationFixture(t, { change: options => {
        if (options.demandOnly) { attempts++; throw new Error("encoder unavailable"); }
    } });
    await f.publication.startPublication("cam", f.source);
    const original = window.go.main.App.VideoStreamControlForTab;
    window.go.main.App.VideoStreamControlForTab = (tab, body) => body.codec_revision === "2" ? Promise.reject(new Error("not applied")) : original(tab, body);
    await assert.rejects(f.publication.withdrawPublicationHEVC("cam", f.source));
    const snapshot = f.publication.publicationSnapshot(), catalog = [{ publisher_id: "self", slot: "cam", generation: "42",
        codec_revision: "1", codec: "h265", hevc_level_id: 153, upload_active: false }];
    f.publication.reconcilePublicationUploads(snapshot, catalog); await flush(); await flush();
    assert.equal(attempts, 1);
    f.publication.reconcilePublicationUploads(snapshot, catalog); await flush(); await flush();
    assert.equal(attempts, 2);
});
