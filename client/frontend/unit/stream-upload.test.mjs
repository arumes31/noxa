import test from "node:test";
import assert from "node:assert/strict";
import { preparePublicationUpload, publicationUploadActive, startPublication, stopPublication,
    publicationSnapshot, reconcilePublicationUploads, publicationCodec } from "../src/stream-publication.js";
import { useChannelMediaSession } from "../src/channel-media.js";

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

function screenFixture(t) {
    t.mock.timers.enable({ apis: ["setTimeout"] });
    const requests = [], created = [];
    const state = { pc: {}, activeTabID: "one", serverGeneration: 1, sessionGeneration: 1, myChannelID: 1, myClientID: "self" };
    let generation = 0;
    globalThis.window = { __noxa: { state, sysMsg: () => {} }, go: { main: { App: {
        VideoStreamControlForTab: async (tab, body) => {
            requests.push({ tab, ...body });
            return { ...body, generation: body.action === "publish" && body.active ? String(++generation) : body.generation };
        },
    } } } };
    globalThis.document = { createElement: type => { created.push(type); return {}; } };
    t.after(async () => {
        await stopPublication("screen"); await stopPublication("cam");
        useChannelMediaSession(null);
        for (const key of ["window", "document"]) delete globalThis[key];
    });
    const track = id => Object.assign(new EventTarget(), { id, readyState: "live", stop() { this.readyState = "ended"; } });
    return { state, requests, created, track };
}

// P3-3: previews are local-only under mandatory media E2EE. A screen
// publication never captures a frame or uploads a preview, with or without a
// live media session, and schedules no preview refresh.
for (const session of [false, true]) {
    test(`screen publication never captures or uploads a preview (media session ${session ? "live" : "absent"})`, async t => {
        const f = screenFixture(t);
        if (session) useChannelMediaSession({ active: true, publicationsChanged() {}, stop() {} });
        assert.equal(await startPublication("screen", f.track("screen-1")), true);
        await settle();
        t.mock.timers.tick(10 * 60 * 1000); await settle();
        assert.deepEqual(f.requests.map(r => r.action), ["publish"]);
        assert.equal(f.requests.some(r => r.action === "preview_upload" || "jpeg" in r), false);
        assert.deepEqual(f.created, [], "no video or canvas is created for a preview");
    });
}

// P3-1: starting or stopping a local camera, screen or Studio (screen slot)
// publication rekeys the encrypted channel media, as the PWA does.
test("publication start and stop notify the media session", async t => {
    const f = screenFixture(t);
    const changes = [];
    useChannelMediaSession({ active: true, publicationsChanged() { changes.push("changed"); }, stop() {} });
    const screen = f.track("screen-1"), cam = f.track("cam-1");
    assert.equal(await startPublication("screen", screen), true);
    assert.equal(changes.length, 1, "screen start");
    assert.equal(await startPublication("cam", cam), true);
    assert.equal(changes.length, 2, "camera start");
    await stopPublication("cam", cam);
    assert.equal(changes.length, 3, "camera stop");
    await stopPublication("screen");
    assert.equal(changes.length, 4, "screen stop");
    await stopPublication("screen");
    assert.equal(changes.length, 4, "stopping nothing does not rekey");
});

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
