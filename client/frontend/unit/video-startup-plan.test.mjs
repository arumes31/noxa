import assert from "node:assert/strict";
import test from "node:test";
import { screenStartupPlan, createScreenStartupAttempt } from "../src/video-startup-plan.js";
import { rtcStatsEpochNow } from "../src/rtc-stats-clock.js";

function mockPerformance(t, value) {
    const descriptor = Object.getOwnPropertyDescriptor(globalThis, "performance");
    Object.defineProperty(globalThis, "performance", { configurable: true, value });
    t.after(() => Object.defineProperty(globalThis, "performance", descriptor));
}

function fixture() {
    const now = 1792020000000;
    const stats = new Map([
        ["v", { type: "outbound-rtp", kind: "video", mid: "2", timestamp: now, codecId: "codec", transportId: "transport" }],
        ["codec", { type: "codec", mimeType: "video/H264", payloadType: 102 }],
        ["transport", { type: "transport", dtlsState: "connected", selectedCandidatePairId: "pair", timestamp: now }],
        ["pair", { type: "candidate-pair", state: "succeeded", timestamp: now, availableOutgoingBitrate: 41600 }],
    ]);
    const parameters = { encodings: [{ active: false, maxBitrate: 8000000 }] };
    const track = { readyState: "live", enabled: true };
    const pc = { connectionState: "connected", signalingState: "stable" };
    const transceiver = { mid: "2", sender: { track, getParameters: () => structuredClone(parameters) } };
    return { stats, now, mid: "2", parameters, profile: { width: 1920, height: 1080, fps: 60 },
        settings: { width: 1920, height: 1080, frameRate: 60 }, limits: {}, pc, transceiver, track,
        current: () => true, otherVideoActive: () => false };
}

test("a paused 1080p60 sender can seed a fresh audio-sized estimate without changing caps", () => {
    const f = fixture(), before = structuredClone(f.parameters);
    assert.deepEqual(screenStartupPlan(f), { mid: "2", payloadType: 102, bitrateKbps: 2000, expiresAt: f.now + 1500 });
    assert.deepEqual(f.parameters, before);
});

test("user, encoding and live server ceilings bound the initial estimate", () => {
    const f = fixture();
    f.profile.bitrateCeiling = 600000;
    assert.equal(screenStartupPlan(f).bitrateKbps, 600);
    f.parameters.encodings[0].maxBitrate = 400000;
    assert.equal(screenStartupPlan(f).bitrateKbps, 400);
    f.limits.video_max_bitrate = 200000;
    assert.equal(screenStartupPlan(f).bitrateKbps, 170);
    f.limits.video_max_bitrate = 40000;
    assert.equal(screenStartupPlan(f), null, "never raise an estimate already adequate for the current cap");
});

test("missing, stale, ambiguous and unrelated statistics cannot authorize a reset", () => {
    const mutations = [
        f => f.stats.delete("v"), f => f.stats.set("duplicate", { ...f.stats.get("v") }),
        f => { f.stats.get("v").mid = "other"; }, f => { f.stats.get("v").transportId = "missing"; },
        f => { f.stats.get("transport").selectedCandidatePairId = "missing"; },
        f => { f.stats.get("transport").dtlsState = "connecting"; },
        f => { f.stats.get("pair").state = "in-progress"; },
        f => { f.stats.get("pair").timestamp -= 1501; }, f => { f.stats.get("v").timestamp++; },
        f => { f.stats.get("pair").availableOutgoingBitrate = NaN; },
        f => { f.stats.get("pair").availableOutgoingBitrate = 0; },
        f => { f.stats.get("pair").availableOutgoingBitrate = 1000000; },
        f => { f.stats.get("codec").mimeType = "video/rtx"; },
        f => { f.stats.get("codec").payloadType = "102"; },
        f => { f.parameters.encodings[0].active = true; },
        f => { f.parameters.encodings.push({ active: false }); },
        f => { f.parameters.encodings[0].maxBitrate = Infinity; },
        f => { f.profile.width = undefined; }, f => { f.profile.fps = 30; },
        f => { f.limits.video_max_bitrate = -1; },
    ];
    for (const mutate of mutations) { const f = fixture(); mutate(f); assert.equal(screenStartupPlan(f), null); }
});

test("one peer receives at most one attempt across viewers and publications", async () => {
    const f = fixture(); let reads = 0;
    const attempt = createScreenStartupAttempt({ now: () => f.now, sample: async (pc, options) => {
        reads++; assert.equal(pc, f.pc); assert.equal(options.fresh, true); return f.stats;
    } });
    assert.equal((await attempt(f)).bitrateKbps, 2000);
    assert.equal(await attempt(f), null);
    const second = fixture(); second.pc = f.pc;
    assert.equal(await attempt(second), null);
    assert.equal(reads, 1);
});

test("active video and stale demand skip seeding, including changes while stats are pending", async () => {
    const f = fixture(); let reads = 0, finish;
    const attempt = createScreenStartupAttempt({ now: () => f.now, sample: () => { reads++; return new Promise(resolve => { finish = resolve; }); } });
    f.otherVideoActive = () => true;
    assert.equal(await attempt(f), null); assert.equal(reads, 0);
    f.otherVideoActive = () => false;
    const pending = attempt(f);
    f.current = () => false;
    // The passed function owns session state, rather than a later object field.
    f.track.enabled = false;
    finish(f.stats);
    assert.equal(await pending, null);
    assert.equal(reads, 1);
});

test("timed out and rejected observations never become polling loops or late mutations", async () => {
    for (const sample of [() => new Promise(() => {}), async () => { throw Error("unavailable"); }]) {
        const f = fixture(); let reads = 0;
        const attempt = createScreenStartupAttempt({ timeoutMs: 5, now: () => f.now, sample: (...args) => { reads++; return sample(...args); } });
        assert.equal(await attempt(f), null);
        assert.equal(await attempt(f), null);
        assert.equal(reads, 1);
    }
});


test("the oldest authorizing timestamp bounds the plan lifetime", () => {
    for (const id of ["v", "transport", "pair"]) {
        const f = fixture();
        f.stats.get(id).timestamp = f.now - 1490;
        assert.equal(screenStartupPlan(f).expiresAt, f.now + 10);
    }
});

test("default clocks accept fractional samples without spending an attempt on rounded wall time", async t => {
    const f = fixture(), origin = f.now;
    delete f.now;
    for (const id of ["v", "transport", "pair"]) f.stats.get(id).timestamp = origin + 10.25;
    mockPerformance(t, { timeOrigin: origin, now: () => 10.75 });
    t.mock.method(Date, "now", () => origin + 10);
    assert.equal(screenStartupPlan(f).bitrateKbps, 2000);
    let reads = 0;
    const attempt = createScreenStartupAttempt({ sample: async () => { reads++; return f.stats; } });
    assert.equal((await attempt(f)).expiresAt, origin + 1510.25);
    assert.equal(await attempt(f), null);
    assert.equal(reads, 1, "successful default-clock seeding remains once per peer");
});

test("wall-clock adjustments do not alter freshness, and true future or expired samples remain rejected", t => {
    const f = fixture(), origin = f.now;
    delete f.now;
    let elapsed = 10.75, wall = origin + 10;
    mockPerformance(t, { timeOrigin: origin, now: () => elapsed });
    t.mock.method(Date, "now", () => wall);
    for (const id of ["v", "transport", "pair"]) f.stats.get(id).timestamp = origin + 10.25;
    for (const jump of [-3600000, 3600000]) {
        wall += jump;
        assert.equal(screenStartupPlan(f).bitrateKbps, 2000);
    }
    elapsed = 10;
    assert.equal(screenStartupPlan(f), null, "a sample ahead of the performance clock is still future");
    elapsed = 1510.25;
    assert.equal(screenStartupPlan(f).bitrateKbps, 2000, "the exact 1500ms bound is retained");
    elapsed += 0.25;
    assert.equal(screenStartupPlan(f), null, "fractional expiry is not rounded away");
});

test("missing or invalid performance clocks fail closed without falling back to wall time", async t => {
    const f = fixture(); delete f.now;
    for (const clock of [undefined, {}, { timeOrigin: NaN, now: () => 1 },
        { timeOrigin: Infinity, now: () => 1 }, { timeOrigin: -1, now: () => 1 },
        { timeOrigin: 1, now: () => NaN }, { timeOrigin: 1, now: () => -1 },
        { timeOrigin: Number.MAX_VALUE, now: () => Number.MAX_VALUE },
        { timeOrigin: 1, now() { throw Error("clock unavailable"); } }]) {
        await t.test(String(clock?.timeOrigin), async child => {
            mockPerformance(child, clock);
            assert.equal(Number.isNaN(rtcStatsEpochNow()), true);
            assert.equal(screenStartupPlan(f), null);
            let reads = 0;
            const attempt = createScreenStartupAttempt({ sample: async () => { reads++; return f.stats; } });
            assert.equal(await attempt(f), null);
            assert.equal(await attempt(f), null);
            assert.equal(reads, 1, "unavailable clocks do not create retry loops");
        });
    }
});
