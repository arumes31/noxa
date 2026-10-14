import test from "node:test";
import assert from "node:assert/strict";
import { createVideoCodecCapabilityProbe, checkVideoCodecCapabilities } from "../src/video-codec-capabilities.js";

const good = { supported: true, smooth: true, powerEfficient: true };
const codec = (mimeType, sdpFmtpLine = "") => ({ mimeType: `video/${mimeType}`, clockRate: 90000, sdpFmtpLine });
const advertised = [codec("H264", "profile-level-id=42001f;packetization-mode=1;level-asymmetry-allowed=1"),
    codec("H265", "profile-id=1;tier-flag=0;level-id=120"), codec("AV1", "profile=0;level-idx=5;tier=0"),
    codec("VP9", "profile-id=0"), codec("VP8")];
const flush = async () => { for (let i = 0; i < 12; i++) await Promise.resolve(); };
function clock() {
    let at = 0, id = 0;
    const timers = new Map();
    return { now: () => at, wallNow: () => 1000000,
        schedule: (fn, delay) => { const key = ++id; timers.set(key, { fn, at: at + delay }); return key; },
        cancel: key => timers.delete(key),
        async advance(ms) {
            const end = at + ms;
            while (true) {
                const due = [...timers].filter(([, timer]) => timer.at <= end).sort((a, b) => a[1].at - b[1].at)[0];
                if (!due) break;
                at = due[1].at; timers.delete(due[0]); due[1].fn(); await flush();
            }
            at = end; await flush();
        },
        size: () => timers.size,
    };
}
function fixture({ rows = advertised, encode = async () => good, decode = encode, receiverRows = rows, ...dependencies } = {}) {
    const calls = [], enumerations = { sender: 0, receiver: 0 };
    const sender = { getCapabilities(kind) { assert.equal(kind, "video"); enumerations.sender++; return { codecs: rows }; } };
    const receiver = { getCapabilities(kind) { assert.equal(kind, "video"); enumerations.receiver++; return { codecs: receiverRows }; } };
    const mediaCapabilities = {
        encodingInfo(value) { assert.equal(this, mediaCapabilities); calls.push({ direction: "encode", value }); return encode(value); },
        decodingInfo(value) { assert.equal(this, mediaCapabilities); calls.push({ direction: "decode", value }); return decode(value); },
    };
    return { calls, enumerations, probe: createVideoCodecCapabilityProbe({ sender, receiver, mediaCapabilities, ...dependencies }) };
}
const group = (report, name, direction = "encode") => report.codecs.find(row => row.codec === name && row.direction === direction);

test("explicit probe enumerates each direction once and queries exact advertised canonical profiles at fixed settings", async () => {
    const f = fixture(); assert.equal(f.calls.length, 0);
    const report = await f.probe.run();
    assert.deepEqual(f.enumerations, { sender: 1, receiver: 1 });
    assert.equal(report.status, "complete"); assert.equal(report.actual_hardware_verified, false);
    assert.equal(f.calls.length, 10); assert.equal(report.counts.native_pending, 0);
    for (const { value } of f.calls) {
        assert.equal(value.type, "webrtc");
        assert.deepEqual({ ...value.video, contentType: null }, { contentType: null, width: 1920, height: 1080, framerate: 60, bitrate: 8000000 });
    }
    assert.equal(f.calls[0].value.video.contentType, "video/H264;level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f");
    assert.equal(group(report, "H265").profiles[0].parameters["profile-id"], 1);
    assert.equal(group(report, "H265").profiles[0].power_efficient, true);
    assert.ok(report.codecs.every(value => value.advertised));
    assert.ok(!JSON.stringify(report).includes("contentType"));
});

test("H264 mode1 main and high are retained before redundant baseline levels and mode0", async () => {
    const rows = [codec("H264", "profile-level-id=42001f;packetization-mode=0"),
        ...Array.from({ length: 10 }, (_, index) => codec("H264", `profile-level-id=4200${(20 + index).toString(16)};packetization-mode=1`)),
        codec("H264", "profile-level-id=4d001f;packetization-mode=1"), codec("H264", "profile-level-id=64001f;packetization-mode=1")];
    const f = fixture({ rows }), report = await f.probe.run();
    for (const direction of ["encode", "decode"]) {
        const selected = group(report, "H264", direction);
        assert.equal(selected.profiles.length, 8); assert.equal(selected.omitted_profiles, 5);
        assert.deepEqual(selected.profiles.slice(0, 3).map(row => row.parameters["profile-level-id"].slice(0, 2)), ["42", "4d", "64"]);
        assert.ok(selected.profiles.every(row => row.parameters["packetization-mode"] === 1));
    }
    assert.equal(report.status, "partial");
});

function manyProfiles() {
    return Array.from({ length: 8 }, (_, i) => [codec("H264", `packetization-mode=1;profile-level-id=4200${(20 + i).toString(16)}`),
        codec("H265", `profile-id=${i}`), codec("AV1", `profile=0;level-idx=${i}`),
        codec("VP9", `profile-id=0;max-fs=${i + 1}`), codec("VP8", `max-fs=${i + 1}`)]).flat();
}

test("32 query budget is shared fairly across codec families and directions with exact omissions", async () => {
    const f = fixture({ rows: manyProfiles() }), report = await f.probe.run();
    assert.equal(f.calls.length, 32); assert.equal(report.counts.selected_profiles, 32);
    assert.equal(report.counts.advertised_profiles, 80); assert.equal(report.counts.omitted_profiles, 48);
    assert.ok(report.codecs.every(row => row.profiles.length >= 3 && row.profiles.length <= 8));
    assert.deepEqual(f.calls.slice(0, 10).map(row => row.direction), ["encode", "decode", "encode", "decode", "encode", "decode", "encode", "decode", "encode", "decode"]);
});

test("unadvertised is false, unavailable enumeration is unknown, and negative query predictions remain explicit", async () => {
    const empty = fixture({ rows: [] }), none = await empty.probe.run();
    assert.equal(empty.calls.length, 0); assert.ok(none.codecs.every(row => row.advertised === false));
    const absent = await createVideoCodecCapabilityProbe({ sender: null, receiver: null, mediaCapabilities: null }).run();
    assert.equal(absent.status, "partial"); assert.ok(absent.codecs.every(row => row.advertised === null));
    const negative = await fixture({ rows: [codec("VP8")], encode: async () => ({ supported: false, smooth: false, powerEfficient: false }) }).probe.run();
    assert.equal(negative.status, "complete"); assert.equal(group(negative, "VP8").profiles[0].supported, false);
});

test("missing query API preserves advertised capability without claiming unsupported hardware", async () => {
    const report = await fixture({ mediaCapabilities: null }).probe.run();
    assert.equal(report.status, "partial");
    assert.ok(report.codecs.every(row => row.advertised && row.profiles[0].status === "api_unavailable" && row.profiles[0].supported === null));
});

test("malformed and unknown fmtp is never stripped then queried or exported", async () => {
    const rows = [codec("H264", "profile-level-id=SECRET"), codec("H265", "sprop-vps=PRIVATE_PAYLOAD"),
        codec("VP8", "arbitrary-private-name=PRIVATE_VALUE"), codec("VP9", "profile-id=0;profile-id=1"),
        codec("AV1", "profile=0\r\nPRIVATE"), codec("H264", "profile-level-id=" + "a".repeat(600)),
        { ...codec("VP8"), clockRate: 8000 }, codec("H265", "profile-id=-1"), codec("AV1", "tier=2")];
    const f = fixture({ rows }), report = await f.probe.run();
    assert.equal(f.calls.length, 0); assert.equal(report.counts.invalid_profiles, rows.length * 2);
    assert.equal(report.status, "partial");
    assert.doesNotMatch(JSON.stringify(report), /SECRET|PRIVATE|PAYLOAD|sprop-vps|arbitrary-private/);
});

test("equivalent fmtp ordering and hex casing deduplicate native queries", async () => {
    const f = fixture({ rows: [codec("H264", "PROFILE-LEVEL-ID=42E01F; packetization-mode=1"), codec("H264", "packetization-mode=1;profile-level-id=42e01f")] });
    const report = await f.probe.run(); assert.equal(f.calls.length, 2); assert.equal(report.counts.duplicate_profiles, 2);
});

test("native errors and malformed responses never export raw error text or arbitrary data", async () => {
    for (const encode of [() => { throw new Error("SECRET_NATIVE_ERROR"); }, async () => Promise.reject("SECRET"),
        async () => ({ supported: "SECRET", powerEfficient: 1, smooth: null, deviceId: "PRIVATE" }),
        async () => ({ get supported() { throw new Error("PRIVATE_GETTER"); } })]) {
        const report = await fixture({ rows: [codec("VP8")], encode }).probe.run();
        assert.equal(report.status, "partial"); assert.equal(group(report, "VP8").profiles[0].supported, null);
        assert.doesNotMatch(JSON.stringify(report), /SECRET|PRIVATE|deviceId|stack/);
    }
});

test("enumeration errors remain unknown and raw errors are not returned", async () => {
    const report = await fixture({ sender: { getCapabilities() { throw new Error("PRIVATE_SDP"); } } }).probe.run();
    assert.equal(report.apis.sender, "error"); assert.equal(group(report, "H264").advertised, null);
    assert.doesNotMatch(JSON.stringify(report), /PRIVATE_SDP/);
});

test("hung native slots survive timeouts and repeated runs cannot exceed two unresolved calls", async () => {
    const time = clock(), finishes = [];
    const f = fixture({ ...time, encode: () => new Promise(resolve => finishes.push(resolve)) });
    const first = f.probe.run(); await flush(); await time.advance(4000);
    const before = await first;
    assert.equal(f.calls.length, 2); assert.equal(before.counts.native_pending, 2);
    assert.equal(before.status, "partial");
    const serialized = JSON.stringify(before);
    const second = f.probe.run(); await flush(); await time.advance(4000); await second;
    assert.equal(f.calls.length, 2, "explicit repeat leaves pending calls alone instead of starting more");
    for (const finish of finishes) finish(good); await flush();
    assert.equal(JSON.stringify(before), serialized, "late settlement never mutates an exported report");
    assert.equal(time.size(), 0);
});

test("concurrent run is explicitly busy and does not enumerate or start native work", async () => {
    const time = clock(), f = fixture({ ...time, rows: [codec("VP8")], encode: () => new Promise(() => {}) });
    const first = f.probe.run(); await flush();
    const busy = await f.probe.run();
    assert.equal(busy.status, "busy"); assert.deepEqual(f.enumerations, { sender: 1, receiver: 1 });
    await time.advance(4000); await first;
});

test("cancellation before invocation makes no API calls", async () => {
    for (const current of [() => false, () => { throw new Error("PRIVATE_SCOPE"); }]) {
        const f = fixture(), report = await f.probe.run({ current });
        assert.equal(report.status, "cancelled"); assert.equal(f.calls.length, 0);
        assert.deepEqual(f.enumerations, { sender: 0, receiver: 0 });
    }
});

test("cancellation during a native query stops further calls and retains its unresolved slot", async () => {
    const time = clock(); let current = true;
    const f = fixture({ ...time, encode: () => new Promise(() => {}) });
    const pending = f.probe.run({ current: () => current }); await flush(); current = false;
    await time.advance(2000); const report = await pending;
    assert.equal(report.status, "cancelled"); assert.equal(f.calls.length, 1);
    assert.equal(report.counts.native_pending, 1);
    assert.ok(report.codecs.every(value => value.profiles.every(row => row.status === "cancelled")));
});

test("20 second overall deadline bounds slow settling queries and leaves remaining predictions unknown", async () => {
    const time = clock();
    const f = fixture({ ...time, rows: manyProfiles(), encode: () => new Promise(resolve => time.schedule(() => resolve(good), 1500)) });
    const pending = f.probe.run(); await flush(); await time.advance(20000); const report = await pending;
    assert.equal(report.elapsed_ms, 20000); assert.equal(report.status, "partial");
    assert.equal(f.calls.length, 14); assert.equal(report.counts.native_pending, 1);
    assert.ok(report.codecs.some(value => value.profiles.some(row => row.status === "budget_exhausted" && row.supported === null)));
    await time.advance(1000); assert.equal(f.calls.length, 14);
});

test("enumeration is bounded and uninspected families cannot be declared absent", async () => {
    const f = fixture({ rows: [...Array.from({ length: 128 }, () => codec("VP8")), codec("H265", "profile-id=1")] });
    const report = await f.probe.run();
    assert.equal(report.counts.uninspected_entries, 2); assert.equal(group(report, "H265").advertised, null);
    assert.equal(group(report, "VP8").advertised, true); assert.equal(f.calls.length, 2);
    assert.ok(JSON.stringify(report).length < 65536);
});

test("cancelled empty enumeration is not labelled a complete capability check", async () => {
    let current = true;
    const f = fixture({ rows: [], receiver: { getCapabilities() { current = false; return { codecs: [] }; } } });
    assert.equal((await f.probe.run({ current: () => current })).status, "cancelled");
});

test("default probe is lazy and does not require browser globals at module import", async t => {
    let calls = 0;
    for (const [key, value] of Object.entries({ RTCRtpSender: { getCapabilities: () => ({ codecs: [codec("VP8")] }) },
        RTCRtpReceiver: { getCapabilities: () => ({ codecs: [] }) }, navigator: { mediaCapabilities: {
            encodingInfo: async () => { calls++; return good; }, decodingInfo: async () => good } } })) {
        const old = Object.getOwnPropertyDescriptor(globalThis, key);
        Object.defineProperty(globalThis, key, { configurable: true, value });
        t.after(() => { if (old) Object.defineProperty(globalThis, key, old); else delete globalThis[key]; });
    }
    assert.equal(calls, 0);
    const report = await checkVideoCodecCapabilities();
    assert.equal(calls, 1); assert.equal(group(report, "VP8").profiles[0].supported, true);
});


test("repeated runs attach no timers or wait races to previously timed-out native queries", async () => {
    const time = clock(); let scheduled = 0;
    const f = fixture({ ...time, rows: [codec("VP8")],
        schedule: (callback, delay) => { scheduled++; return time.schedule(callback, delay); },
        encode: () => new Promise(() => {}) });
    const first = f.probe.run(); await flush(); await time.advance(4000); await first;
    assert.equal(f.calls.length, 2); assert.equal(scheduled, 2);
    for (let i = 0; i < 100; i++) {
        const report = await f.probe.run();
        assert.equal(report.counts.queries_started, 0);
        assert.equal(report.counts.native_pending, 2);
        assert.equal(report.elapsed_ms, 0);
        assert.ok(report.codecs.filter(row => row.advertised).every(row => row.profiles[0].status === "budget_exhausted"));
    }
    assert.equal(f.calls.length, 2);
    assert.equal(scheduled, 2, "each Promise.race wait schedules a timer; repeated runs must create neither");
    assert.equal(time.size(), 0);
});
