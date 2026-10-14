import test from "node:test";
import assert from "node:assert/strict";
import { publicationCodecSupport, publicationVideoCodecs, receiverVideoCodecs, preferPublicationCodec } from "../src/video-codecs.js";
const vp8 = { mimeType: "video/VP8", clockRate: 90000 };
const h264 = { mimeType: "video/H264", clockRate: 90000, sdpFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f" };
function install(t, codecs, result) {
    for (const [name, value] of Object.entries({ RTCRtpSender: { getCapabilities: () => ({ codecs }) }, RTCRtpReceiver: { getCapabilities: () => ({ codecs }) }, navigator: { mediaCapabilities: { encodingInfo: result } } })) {
        const previous = Object.getOwnPropertyDescriptor(globalThis, name);
        Object.defineProperty(globalThis, name, { configurable: true, value });
        t.after(() => { if (previous) Object.defineProperty(globalThis, name, previous); else delete globalThis[name]; });
    }
}
test("hardware encoding preference uses the actual capture dimensions and fps", async t => {
    let query;
    install(t, [vp8, h264], async value => { query = value; return { supported: true, powerEfficient: true }; });
    assert.deepEqual(await publicationVideoCodecs({ getSettings: () => ({ width: 1920, height: 1080, frameRate: 60 }) }), ["h264", "vp8"]);
    assert.equal(query.video.framerate, 60); assert.equal(query.video.width, 1920);
    assert.deepEqual(receiverVideoCodecs(), ["h264", "vp8"]);
});
test("software H264 and capability-query errors retain VP8", async t => {
    let fail = false;
    let now = 1000;
    t.mock.method(Date, "now", () => now);
    install(t, [vp8, h264], async () => { if (fail) throw new Error("unsupported query"); return { supported: true, powerEfficient: false }; });
    const track = { getSettings: () => ({ width: 1920, height: 1080, frameRate: 60 }) };
    assert.deepEqual(await publicationVideoCodecs(track), ["vp8"]);
    fail = true; now += 300001; assert.deepEqual(await publicationVideoCodecs(track), ["vp8"]);
});
test("incompatible H264 profiles and packetization modes are not advertised", async t => {
    install(t, [vp8, { ...h264, sdpFmtpLine: "packetization-mode=0;profile-level-id=42001f" }, { ...h264, sdpFmtpLine: "packetization-mode=1;profile-level-id=640032" }], async () => { throw new Error("must not query incompatible encoder"); });
    assert.deepEqual(receiverVideoCodecs(), ["vp8"]);
    assert.deepEqual(await publicationVideoCodecs({}), ["vp8"]);
});
test("codec changes reorder alternatives on the same transceiver", t => {
    const rtx = { mimeType: "video/rtx", clockRate: 90000 };
    install(t, [vp8, h264, rtx], async () => ({}));
    const preferences = [], transceiver = { setCodecPreferences: codecs => preferences.push(codecs) };
    assert.equal(preferPublicationCodec(transceiver, "h264"), true);
    assert.equal(preferPublicationCodec(transceiver, "vp8"), true);
    assert.deepEqual(preferences.map(codecs => codecs[0].mimeType), ["video/H264", "video/VP8"]);
    assert.ok(preferences.every(codecs => codecs.includes(rtx)), "codec selection must retain packet repair");
    assert.equal(preferPublicationCodec(transceiver, "h265"), false);
    assert.equal(preferPublicationCodec({}, "vp8"), false);
});


const flush = async () => { for (let i = 0; i < 12; i++) await Promise.resolve(); };
const screen = () => ({ getSettings: () => ({ width: 1920, height: 1080, frameRate: 60 }) });
function countRaces(t) {
    let calls = 0;
    const original = Promise.race;
    t.mock.method(Promise, "race", values => { calls++; return original.call(Promise, values); });
    return () => calls;
}
function countTimeoutClears(t) {
    let calls = 0;
    const original = globalThis.clearTimeout;
    t.mock.method(globalThis, "clearTimeout", timer => { calls++; return original(timer); });
    return () => calls;
}

test("timed-out publication retries share one finite wait instead of retaining native promise reactions", async t => {
    t.mock.timers.enable({ apis: ["setTimeout"] });
    let probes = 0;
    install(t, [h264], () => { probes++; return new Promise(() => {}); });
    const races = countRaces(t), clears = countTimeoutClears(t), track = screen();
    const first = publicationCodecSupport(track); await flush();
    t.mock.timers.tick(2000); assert.equal((await first).status, "unknown");
    const retries = Array.from({ length: 100 }, () => publicationCodecSupport(track));
    await flush();
    try {
        assert.equal(probes, 1);
        assert.equal(races(), 1, "one native query must own only one Promise.race, including repeated retries");
        const values = await Promise.all(retries);
        assert.ok(values.every(value => value.status === "unknown"));
        assert.equal(clears(), 1, "the one shared timeout is cleaned up once, not per retry");
    } finally {
        t.mock.timers.tick(2000); await Promise.all(retries);
    }
});

test("concurrent initial publication callers share the wait and both receive hardware success", async t => {
    t.mock.timers.enable({ apis: ["setTimeout"] });
    let finish, probes = 0;
    install(t, [h264], () => { probes++; return new Promise(resolve => { finish = resolve; }); });
    const races = countRaces(t), clears = countTimeoutClears(t), track = screen();
    const first = publicationCodecSupport(track); await flush();
    t.mock.timers.tick(1000);
    const second = publicationCodecSupport(track); await flush();
    finish({ supported: true, powerEfficient: true });
    const results = await Promise.all([first, second]);
    assert.equal(probes, 1); assert.equal(races(), 1);
    assert.equal(clears(), 1, "native success before the deadline clears the shared timer");
    assert.ok(results.every(result => result.status === "hardware" && result.codecs[0] === "h264"));
});

test("native success after the shared timeout is visible to later callers with the existing TTL", async t => {
    t.mock.timers.enable({ apis: ["setTimeout"] });
    let finish, probes = 0, now = 1000;
    t.mock.method(Date, "now", () => now);
    install(t, [h264], () => { probes++; return probes === 1 ? new Promise(resolve => { finish = resolve; }) : { supported: false, powerEfficient: false }; });
    const track = screen(), first = publicationCodecSupport(track); await flush();
    t.mock.timers.tick(2000); const timedOut = await first;
    assert.equal(timedOut.status, "unknown");
    now = 10000; finish({ supported: true, powerEfficient: true }); await flush();
    assert.equal((await publicationCodecSupport(track)).status, "hardware"); assert.equal(probes, 1);
    assert.equal(timedOut.status, "unknown", "late native success must not rewrite the earlier timeout reply");
    now = 39999; assert.equal((await publicationCodecSupport(track)).status, "hardware"); assert.equal(probes, 1);
    now = 40000; assert.equal((await publicationCodecSupport(track)).status, "unsupported"); assert.equal(probes, 2);
    now = 339999; assert.equal((await publicationCodecSupport(track)).status, "unsupported"); assert.equal(probes, 2);
    now = 340000; await publicationCodecSupport(track); assert.equal(probes, 3);
});
