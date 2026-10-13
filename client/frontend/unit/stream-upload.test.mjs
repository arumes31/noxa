import test from "node:test";
import assert from "node:assert/strict";
import { preparePublicationUpload, publicationUploadActive, startPublication, stopPublication,
    publicationSnapshot, reconcilePublicationUploads, publicationCodec } from "../src/stream-publication.js";

function setup(t, supported = true) {
    const requests = [], changes = [], track = { id: "capture", readyState: "live", stop() { this.readyState = "ended"; } };
    const state = { pc: {}, activeTabID: "one", serverGeneration: 1, sessionGeneration: 1, myChannelID: 1, myClientID: "self" };
    let generation = 0;
    globalThis.window = { __noxa: { state, sysMsg: () => {} }, go: { main: { App: {
        SupportsStreamSourceQualityForTab: async () => supported,
        VideoStreamControlForTab: async (_tab, body) => {
            requests.push(body);
            return { ...body, generation: body.active ? String(++generation) : body.generation, upload_active: false };
        },
    } } } };
    t.after(async () => { await stopPublication("cam"); delete globalThis.window; });
    return { state, track, requests, changes, prepare: () => preparePublicationUpload("cam", track, () => { changes.push(publicationUploadActive("cam", track)); }) };
}

const settle = async () => { for (let n = 0; n < 12; n++) await Promise.resolve(); };

function previewFixture(t, { pendingRead = false } = {}) {
    t.mock.timers.enable({ apis: ["setTimeout"] });
    const requests = [], encodes = [], readers = [], videos = [], messages = [];
    const state = { pc: {}, activeTabID: "one", serverGeneration: 1, sessionGeneration: 1, myChannelID: 1, myClientID: "self" };
    let generation = 0;
    globalThis.window = { __noxa: { state, sysMsg: text => messages.push(text) }, go: { main: { App: {
        VideoStreamControlForTab: async (tab, body) => {
            requests.push({ tab, ...body });
            return { ...body, generation: body.action === "publish" && body.active ? String(++generation) : body.generation };
        },
    } } } };
    globalThis.MediaStream = class { constructor(tracks) { this.tracks = tracks; } };
    globalThis.document = { createElement: type => {
        if (type === "video") {
            const video = { videoWidth: 1920, videoHeight: 1080, play: async () => {}, pause() { this.paused = true; } };
            videos.push(video); return video;
        }
        assert.equal(type, "canvas");
        const canvas = { getContext: () => ({ drawImage() {} }),
            toDataURL() { throw new Error("synchronous JPEG encoding is forbidden"); },
            toBlob(callback, type, quality) { encodes.push({ callback, type, quality, width: canvas.width, height: canvas.height }); },
        };
        return canvas;
    } };
    globalThis.FileReader = class {
        constructor() { this.readyState = 0; readers.push(this); }
        readAsDataURL(blob) {
            this.readyState = 1;
            this.complete = () => { this.readyState = 2; this.result = `data:image/jpeg;base64,${blob.base64 || "YWJj"}`; this.onload?.(); };
            if (!pendingRead) queueMicrotask(this.complete);
        }
        abort() { this.aborted = true; this.readyState = 2; this.onabort?.(); }
    };
    t.after(async () => {
        await stopPublication("screen");
        for (const encode of encodes) encode.callback(null); // Release a deliberately hung native callback.
        await settle();
        for (const key of ["window", "document", "MediaStream", "FileReader"]) delete globalThis[key];
    });
    const start = async () => {
        const track = Object.assign(new EventTarget(), { id: `screen-${generation + 1}`, readyState: "live",
            stop() { this.readyState = "ended"; },
        });
        await startPublication("screen", track); await settle(); return track;
    };
    return { state, requests, encodes, readers, videos, messages, start,
        uploads: () => requests.filter(r => r.action === "preview_upload") };
}

test("screen preview asynchronously uploads the bounded JPEG for the exact publication and repeats after two minutes", async t => {
    const f = previewFixture(t);
    await f.start();
    assert.equal(f.uploads().length, 0);
    assert.deepEqual({ ...f.encodes[0], callback: undefined }, { callback: undefined, type: "image/jpeg", quality: 0.55, width: 640, height: 360 });
    f.encodes[0].callback({ type: "image/jpeg", size: 49152, base64: "A".repeat(65536) });
    await settle();
    assert.deepEqual(f.uploads().map(({ tab, slot, generation, jpeg }) => ({ tab, slot, generation, bytes: jpeg.length })),
        [{ tab: "one", slot: "screen", generation: "1", bytes: 65536 }]);
    assert.equal(f.videos[0].srcObject, null);
    t.mock.timers.tick(119999); await settle(); assert.equal(f.encodes.length, 1);
    t.mock.timers.tick(1); await settle(); assert.equal(f.encodes.length, 2);
});

for (const invalid of [null, { type: "image/jpeg", size: 49153 }, { type: "image/png", size: 3 }, { type: "image/jpeg", size: 0 }]) {
    test(`invalid preview blob never reaches conversion or upload: ${JSON.stringify(invalid)}`, async t => {
        const f = previewFixture(t); await f.start();
        f.encodes[0].callback(invalid); await settle();
        assert.equal(f.readers.length, 0); assert.equal(f.uploads().length, 0);
        assert.equal(f.videos[0].srcObject, null);
    });
}

for (const cancel of ["stop", "ended", "scope"]) {
    test(`preview encoding pending across ${cancel} cannot upload stale source bytes`, async t => {
        const f = previewFixture(t); const track = await f.start();
        if (cancel === "stop") await stopPublication("screen", track);
        if (cancel === "ended") { track.readyState = "ended"; track.dispatchEvent(new Event("ended")); }
        if (cancel === "scope") f.state.sessionGeneration++;
        f.encodes[0].callback({ type: "image/jpeg", size: 3 }); await settle();
        assert.equal(f.readers.length, 0); assert.equal(f.uploads().length, 0);
        assert.equal(f.videos[0].srcObject, null);
    });
}

test("replacing a preview source cannot start overlapping native JPEG encodes or upload the old result", async t => {
    const f = previewFixture(t); await f.start(); await f.start();
    assert.equal(f.encodes.length, 1);
    f.encodes[0].callback({ type: "image/jpeg", size: 3 }); await settle();
    assert.equal(f.uploads().length, 0);
    t.mock.timers.tick(120000); await settle();
    assert.equal(f.encodes.length, 2);
    f.encodes[1].callback({ type: "image/jpeg", size: 3 }); await settle();
    assert.deepEqual(f.uploads().map(r => r.generation), ["2"]);
});

test("a missing encode callback times out without stacking native encodes and a late callback permits later previews", async t => {
    const f = previewFixture(t); await f.start();
    t.mock.timers.tick(5000); await settle();
    assert.equal(f.videos[0].srcObject, null);
    t.mock.timers.tick(120000); await settle();
    assert.equal(f.encodes.length, 1); assert.equal(f.uploads().length, 0);
    f.encodes[0].callback({ type: "image/jpeg", size: 3 }); await settle();
    assert.equal(f.readers.length, 0);
    t.mock.timers.tick(120000); await settle();
    assert.equal(f.encodes.length, 2);
});

for (const cancel of ["stop", "timeout", "scope"]) {
    test(`pending JPEG conversion is retired on ${cancel} without uploading its late result`, async t => {
        const f = previewFixture(t, { pendingRead: true }); const track = await f.start();
        f.encodes[0].callback({ type: "image/jpeg", size: 3 }); await settle();
        assert.equal(f.readers.length, 1);
        if (cancel === "stop") await stopPublication("screen", track);
        if (cancel === "timeout") t.mock.timers.tick(5000);
        if (cancel === "scope") f.state.serverGeneration++;
        f.readers[0].complete(); await settle();
        assert.equal(f.uploads().length, 0); assert.equal(f.videos[0].srcObject, null);
        if (cancel !== "scope") assert.equal(f.readers[0].aborted, true);
    });
}

test("codec-only catalog changes reconfigure one current publication and ignore stale generations", async t => {
    const fixture = setup(t);
    window.go.main.App.SupportsStreamCodecForTab = async () => true;
    fixture.track.getSettings = () => ({ width: 640, height: 360, frameRate: 30 });
    const previous = Object.getOwnPropertyDescriptor(globalThis, "navigator");
    const sender = Object.getOwnPropertyDescriptor(globalThis, "RTCRtpSender");
    Object.defineProperty(globalThis, "navigator", { configurable: true, value: { mediaCapabilities: { encodingInfo: async () => ({ supported: true, powerEfficient: true }) } } });
    Object.defineProperty(globalThis, "RTCRtpSender", { configurable: true, value: { getCapabilities: () => ({ codecs: [{ mimeType: "video/H264", clockRate: 90000, sdpFmtpLine: "packetization-mode=1;profile-level-id=42001f" }] }) } });
    t.after(() => { if (previous) Object.defineProperty(globalThis, "navigator", previous); else delete globalThis.navigator; if (sender) Object.defineProperty(globalThis, "RTCRtpSender", sender); else delete globalThis.RTCRtpSender; });
    const request = window.go.main.App.VideoStreamControlForTab;
    window.go.main.App.VideoStreamControlForTab = async (...args) => ({ ...await request(...args), codec: "h264" });
    const choices = [];
    await preparePublicationUpload("cam", fixture.track, () => { choices.push(publicationCodec("cam", fixture.track)); });
    await startPublication("cam", fixture.track);
    assert.deepEqual(fixture.requests[0].codecs, ["h264", "vp8"]);
    const snapshot = publicationSnapshot(), own = { publisher_id: "self", slot: "cam", generation: snapshot[0].generation, upload_active: false };
    reconcilePublicationUploads(snapshot, [{ ...own, generation: "999", codec: "vp8" }]); await settle();
    assert.deepEqual(choices, ["h264"]);
    reconcilePublicationUploads(snapshot, [{ ...own, codec: "vp8" }]); await settle();
    assert.deepEqual(choices, ["h264", "vp8"]);
    reconcilePublicationUploads(snapshot, [{ ...own, codec: "vp8" }]); await settle();
    assert.equal(choices.length, 2);
});

test("capable source publishers start paused and only current authoritative demand resumes them", async t => {
    const fixture = setup(t);
    assert.equal(await fixture.prepare(), true);
    assert.equal(publicationUploadActive("cam", fixture.track), false);
    await startPublication("cam", fixture.track);
    assert.equal(fixture.requests[0].quality_mode, "source");
    const snapshot = publicationSnapshot();
    const own = { publisher_id: "self", slot: "cam", generation: snapshot[0].generation };
    for (const row of [{ ...own }, { ...own, generation: "999", upload_active: true }, { ...own, publisher_id: "other", upload_active: true }]) {
        reconcilePublicationUploads(snapshot, [row]);
        assert.equal(publicationUploadActive("cam", fixture.track), false);
    }
    reconcilePublicationUploads(snapshot, [{ ...own, upload_active: true }]);
    assert.equal(publicationUploadActive("cam", fixture.track), true);
    await settle();
    reconcilePublicationUploads(snapshot, [{ ...own, upload_active: false }]);
    assert.equal(publicationUploadActive("cam", fixture.track), false);
    await settle();
    assert.deepEqual(fixture.changes, [false, true, false]);
});

test("a rejected encoder update retries when the same authoritative state is polled again", async t => {
    const fixture = setup(t); let calls = 0;
    await preparePublicationUpload("cam", fixture.track, () => { if (++calls === 2) throw new Error("temporary encoder failure"); });
    await startPublication("cam", fixture.track);
    const snapshot = publicationSnapshot(), rows = [{ publisher_id: "self", slot: "cam", generation: snapshot[0].generation, upload_active: true }];
    reconcilePublicationUploads(snapshot, rows); await settle();
    assert.equal(calls, 2);
    reconcilePublicationUploads(snapshot, rows); await settle();
    assert.equal(calls, 3);
    reconcilePublicationUploads(snapshot, rows); await settle();
    assert.equal(calls, 3);
});

test("failed initial encoder confirmation stops the acknowledged server publication", async t => {
    const fixture = setup(t);
    await preparePublicationUpload("cam", fixture.track, () => { throw new Error("encoder failed"); });
    await assert.rejects(startPublication("cam", fixture.track), /encoder failed/);
    assert.deepEqual(fixture.requests.map(row => [row.active, row.generation]), [[true, "0"], [false, "1"]]);
    assert.equal(publicationSnapshot().length, 0);
    assert.equal(publicationUploadActive("cam", fixture.track), undefined);
});

test("a stop while encoder confirmation is pending cannot finish the obsolete publication", async t => {
    const fixture = setup(t); let finish;
    await preparePublicationUpload("cam", fixture.track, () => new Promise(resolve => { finish = resolve; }));
    const started = startPublication("cam", fixture.track);
    await settle();
    assert.equal(typeof finish, "function");
    await stopPublication("cam");
    finish();
    assert.equal(await started, false);
    assert.equal(publicationSnapshot().length, 0);
});

test("older servers retain continuous upload without source-mode fields", async t => {
    const fixture = setup(t, false);
    await fixture.prepare();
    await startPublication("cam", fixture.track);
    assert.equal(publicationUploadActive("cam", fixture.track), undefined);
    assert.equal("quality_mode" in fixture.requests[0], false);
    assert.deepEqual(fixture.changes, []);
});

test("replacement remains paused while the previous publication stop acknowledgement is pending", async t => {
    const fixture = setup(t);
    await fixture.prepare(); await startPublication("cam", fixture.track);
    await fixture.prepare();
    const original = window.go.main.App.VideoStreamControlForTab;
    let finish;
    window.go.main.App.VideoStreamControlForTab = async (tab, body) => {
        if (!body.active) await new Promise(resolve => { finish = resolve; });
        return original(tab, body);
    };
    const replacement = startPublication("cam", fixture.track);
    assert.equal(typeof finish, "function");
    assert.equal(publicationUploadActive("cam", fixture.track), false);
    finish(); await replacement;
    assert.equal(publicationUploadActive("cam", fixture.track), false);
    window.go.main.App.VideoStreamControlForTab = original;
});

test("late capability and stale publication snapshots cannot control a replacement", async t => {
    const fixture = setup(t);
    let resolve;
    window.go.main.App.SupportsStreamSourceQualityForTab = () => new Promise(done => { resolve = done; });
    const prepared = fixture.prepare();
    fixture.state.sessionGeneration++;
    resolve(true);
    assert.equal(await prepared, false);
    assert.equal(publicationUploadActive("cam", fixture.track), undefined);
    window.go.main.App.SupportsStreamSourceQualityForTab = async () => true;
    await fixture.prepare(); await startPublication("cam", fixture.track);
    const stale = publicationSnapshot();
    await fixture.prepare(); await startPublication("cam", fixture.track);
    reconcilePublicationUploads(stale, [{ publisher_id: "self", slot: "cam", generation: stale[0].generation, upload_active: true }]);
    assert.equal(publicationUploadActive("cam", fixture.track), false);
    await stopPublication("cam");
    assert.equal(publicationUploadActive("cam", fixture.track), undefined);
});
