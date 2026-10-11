import assert from "node:assert/strict";
import test from "node:test";

globalThis.window = { addEventListener() {} };
const publication = await import("../src/stream-publication.js");
const { publicationCodecSupport } = await import("../src/video-codecs.js");
const h264 = { mimeType: "video/H264", clockRate: 90000, sdpFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f" };
const flush = async () => { await new Promise(resolve => setImmediate(resolve)); };

async function fixture(t, options = {}) {
    let now = 1000, healthy = false, selected = "vp8", probes = 0;
    t.mock.method(Date, "now", () => now);
    const requests = [], applied = [], pc = {}, settings = { width: 1920, height: 1080, frameRate: 60 };
    const track = { id: "camera", readyState: "live", getSettings: () => ({ ...settings }) };
    const navigatorDescriptor = Object.getOwnPropertyDescriptor(globalThis, "navigator");
    const senderDescriptor = Object.getOwnPropertyDescriptor(globalThis, "RTCRtpSender");
    Object.defineProperty(globalThis, "RTCRtpSender", { configurable: true, value: { getCapabilities: () => ({ codecs: [h264] }) } });
    const mediaCapabilities = { encodingInfo: async () => {
        probes++;
        if (options.query) return options.query({ healthy, settings });
        if (!healthy) throw new Error("transient query failure");
        return { supported: true, powerEfficient: true };
    } };
    if (options.missingAPI) delete mediaCapabilities.encodingInfo;
    Object.defineProperty(globalThis, "navigator", { configurable: true, value: { mediaCapabilities } });
    const state = { pc, activeTabID: "a", serverGeneration: 1, sessionGeneration: 1, myClientID: "self", myChannelID: 1 };
    window.__noxa = { state };
    window.go = { main: { App: {
        SupportsStreamSourceQualityForTab: async () => true, SupportsStreamCodecForTab: async () => true,
        SupportsStreamCodecRefreshForTab: async () => options.supported !== false,
        VideoStreamControlForTab: async (_tab, body) => {
            requests.push(body);
            if (body.generation !== "0" && body.active && body.codecs?.includes("h264")) selected = options.forcedVP8 ? "vp8" : "h264";
            await options.request?.(body);
            return { ...body, publisher_id: "self", generation: body.generation === "0" ? "42" : body.generation,
                codec: selected, quality_mode: "source", upload_active: true };
        },
    } } };
    t.after(async () => {
        await publication.stopPublication("cam", track);
        if (navigatorDescriptor) Object.defineProperty(globalThis, "navigator", navigatorDescriptor); else delete globalThis.navigator;
        if (senderDescriptor) Object.defineProperty(globalThis, "RTCRtpSender", senderDescriptor); else delete globalThis.RTCRtpSender;
    });
    await publication.preparePublicationUpload("cam", track, () => applied.push(publication.publicationCodec("cam", track)));
    await publication.startPublication("cam", track);
    const reconcile = () => publication.reconcilePublicationUploads(publication.publicationSnapshot(),
        [{ publisher_id: "self", slot: "cam", generation: "42", upload_active: true, codec: selected }]);
    return { track, pc, state, settings, requests, applied, mediaCapabilities, reconcile, probes: () => probes,
        advance: milliseconds => { now += milliseconds; }, healthy: () => { healthy = true; } };
}

test("transient initial capability failure returns the same publication to authoritative H264", async t => {
    const f = await fixture(t);
    assert.equal(publication.publicationCodec("cam", f.track), "vp8");
    f.healthy(); f.advance(30001); f.reconcile(); await flush();
    assert.equal(f.probes(), 2, "existing catalog polling retries the now-supported hardware query");
    assert.equal(f.requests.length, 2, "one scoped capability addition, no stop or replacement");
    assert.deepEqual(f.requests[1], { publisher_id: "", slot: "cam", generation: "42", revision: "0", session: "0",
        active: true, action: "publish", quality_mode: "source", codecs: ["h264", "vp8"] });
    f.reconcile(); await flush();
    assert.equal(publication.publicationCodec("cam", f.track), "h264");
    assert.deepEqual(f.applied, ["vp8", "h264"]);
    assert.equal(publication.publicationSnapshot()[0].generation, "42");
});

test("forced VP8 policy remains authoritative and a completed augmentation is not repeated", async t => {
    const f = await fixture(t, { forcedVP8: true });
    f.healthy(); f.advance(30001); f.reconcile(); await flush();
    f.reconcile(); await flush();
    assert.equal(publication.publicationCodec("cam", f.track), "vp8");
    for (let i = 0; i < 10; i++) { f.advance(300000); f.reconcile(); await flush(); }
    assert.equal(f.requests.length, 2); assert.equal(f.probes(), 2);
    assert.deepEqual(f.applied, ["vp8"], "an incompatible watcher/bounds/recorder can keep the single source on VP8");
});

test("an accepted update with lost acknowledgement reconciles through the authoritative catalog", async t => {
    let updates = 0;
    const f = await fixture(t, { request: async body => {
        if (body.active && body.generation !== "0" && ++updates === 1) throw new Error("lost acknowledgement");
    } });
    f.healthy(); f.advance(30001); f.reconcile(); await flush();
    f.reconcile(); await flush();
    assert.equal(publication.publicationCodec("cam", f.track), "h264");
    for (let i = 0; i < 10; i++) f.reconcile();
    assert.equal(f.requests.length, 2, "lost replies do not cause poll-rate control requests");
    f.advance(30001); f.reconcile(); await flush();
    assert.equal(f.requests.length, 3, "same generation update retries idempotently after backoff");
    assert.equal(f.requests[2].generation, "42");
});

test("unsupported servers and permanently missing APIs do not run retry probes", async t => {
    const f = await fixture(t, { supported: false });
    f.healthy();
    for (let i = 0; i < 5; i++) { f.advance(300000); f.reconcile(); await flush(); }
    assert.equal(f.requests.length, 1); assert.equal(f.probes(), 1);
});

test("missing media capability API stays quiescent across catalog polls", async t => {
    const f = await fixture(t, { missingAPI: true });
    for (let i = 0; i < 5; i++) { f.advance(300000); f.reconcile(); await flush(); }
    assert.equal(f.requests.length, 1); assert.equal(f.probes(), 0);
});

test("negative capability evidence uses a five-minute retry interval", async t => {
    const f = await fixture(t, { query: () => ({ supported: true, powerEfficient: false }) });
    for (let i = 0; i < 9; i++) { f.advance(30000); f.reconcile(); await flush(); }
    assert.equal(f.probes(), 1); assert.equal(f.requests.length, 1);
    f.advance(30001); f.reconcile(); await flush();
    assert.equal(f.probes(), 2); assert.equal(f.requests.length, 1);
});

test("unknown capability probes back off at30seconds, two minutes and five minutes", async t => {
    const f = await fixture(t);
    for (const interval of [30000, 30000, 120000, 300000]) {
        const before = f.probes();
        f.advance(interval - 1); f.reconcile(); await flush(); assert.equal(f.probes(), before);
        f.advance(1); f.reconcile(); await flush(); assert.equal(f.probes(), before + 1);
    }
    assert.equal(f.requests.length, 1);
});

test("one pending refresh query cannot duplicate requests or mutate a changed capture", async t => {
    let resolve;
    const f = await fixture(t, { query: ({ healthy }) => {
        if (!healthy) throw new Error("transient");
        return new Promise(done => { resolve = done; });
    } });
    f.healthy(); f.advance(30001); f.reconcile(); await flush();
    for (let i = 0; i < 10; i++) f.reconcile();
    assert.equal(f.probes(), 2);
    f.settings.width = 1280; f.settings.height = 720;
    resolve({ supported: true, powerEfficient: true }); await flush();
    assert.equal(f.requests.length, 1, "late old-geometry support cannot update this publication");
    f.advance(30001); f.reconcile(); await flush();
    assert.equal(f.probes(), 3, "changed geometry receives its own actual capability query");
    resolve({ supported: true, powerEfficient: true }); await flush();
    assert.equal(f.requests.length, 2);
});

test("stopping while a query is pending cancels every later publication effect", async t => {
    let resolve;
    const f = await fixture(t, { query: ({ healthy }) => {
        if (!healthy) throw new Error("transient");
        return new Promise(done => { resolve = done; });
    } });
    f.healthy(); f.advance(30001); f.reconcile(); await flush();
    await publication.stopPublication("cam", f.track);
    resolve({ supported: true, powerEfficient: true }); await flush();
    assert.equal(f.requests.filter(row => row.active).length, 1);
    assert.equal(publication.publicationCodec("cam", f.track), undefined);
});

test("a delayed update acknowledgement cannot mutate a replacement publication", async t => {
    let resolve;
    const f = await fixture(t, { request: body => {
        if (body.active && body.generation !== "0") return new Promise(done => { resolve = done; });
    } });
    f.healthy(); f.advance(30001); f.reconcile(); await flush();
    const replacement = { ...f.track, id: "replacement" };
    await publication.stopPublication("cam", f.track);
    await publication.preparePublicationUpload("cam", replacement, () => {});
    await publication.startPublication("cam", replacement);
    const before = publication.publicationSnapshot()[0];
    resolve(); await flush();
    assert.equal(publication.publicationSnapshot()[0].publication, before.publication);
    assert.equal(publication.publicationCodec("cam", f.track), undefined);
    assert.equal(f.requests.filter(row => row.active && row.generation !== "0").length, 1);
    await publication.stopPublication("cam", replacement);
});

test("initial hung capability query is reused by timed-out catalog retries", async t => {
    t.mock.timers.enable({ apis: ["setTimeout"] });
    let resolve;
    const starting = fixture(t, { query: () => new Promise(done => { resolve = done; }) });
    await flush(); t.mock.timers.tick(2001); await flush();
    const f = await starting;
    assert.equal(f.probes(), 1);
    for (const delay of [30001, 30001, 120001]) {
        f.advance(delay); f.reconcile(); await flush();
        t.mock.timers.tick(2001); await flush();
        assert.equal(f.probes(), 1, "a timed-out native promise remains owned until it settles");
    }
    assert.equal(f.requests.length, 1);
    resolve({ supported: true, powerEfficient: true }); await flush();
});

test("geometry changes cannot create an unbounded number of hung native queries", async t => {
    t.mock.timers.enable({ apis: ["setTimeout"] });
    const f = await fixture(t);
    let pending = 0;
    navigator.mediaCapabilities = { encodingInfo: () => { pending++; return new Promise(() => {}); } };
    const queries = Array.from({ length: 40 }, (_, index) => publicationCodecSupport({
        getSettings: () => ({ width: 640 + index * 4, height: 360, frameRate: 60 }),
    }));
    await flush();
    assert.equal(pending, 16);
    t.mock.timers.tick(2001);
    const results = await Promise.all(queries);
    assert.ok(results.every(result => result.status === "unknown" && result.codecs.length === 1));
    assert.equal(f.requests.length, 1);
});

test("the initial query cannot advertise hardware for settings changed while it was pending", async t => {
    const f = await fixture(t);
    let resolve;
    navigator.mediaCapabilities = { encodingInfo: () => new Promise(done => { resolve = done; }) };
    const pending = publicationCodecSupport(f.track);
    await flush(); f.settings.width = 1280;
    resolve({ supported: true, powerEfficient: true });
    const result = await pending;
    assert.equal(result.status, "unknown"); assert.deepEqual(result.codecs, ["vp8"]);
});
