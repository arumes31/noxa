import test from "node:test";
import assert from "node:assert/strict";
import { publicationVideoCodecs, receiverVideoCodecs, preferPublicationCodec } from "../src/video-codecs.js";
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
    install(t, [vp8, h264], async () => { if (fail) throw new Error("unsupported query"); return { supported: true, powerEfficient: false }; });
    assert.deepEqual(await publicationVideoCodecs({}), ["vp8"]);
    fail = true; assert.deepEqual(await publicationVideoCodecs({}), ["vp8"]);
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
