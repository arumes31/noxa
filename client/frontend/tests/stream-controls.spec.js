import { test, expect } from "./fixtures.js";

test.beforeEach(async ({ page }) => {
    await page.route("**/streams-fixture", route => route.fulfill({ contentType: "text/html", body: `<!doctype html><html lang="en"><head><link rel="stylesheet" href="/src/style.css"><link rel="stylesheet" href="/src/streams.css"><link rel="stylesheet" href="/src/workspace.css"></head><body><main><div id="video-grid"></div></main></body></html>` }));
    await page.goto("/streams-fixture");
    await page.evaluate(async () => {
        const pc = {}, audio = { muted: false, volume: 100 };
        const streams = ["alice", "bob"].map((publisher_id, i) => ({ publisher_id, slot: "screen", generation: String(i + 1), preview_at: 0, watch_revision: "0" }));
        const calls = [], shown = new Set(), tracks = new Map();
        window.__streams = { streams, calls, shown, tracks, audio, session: "10" };
        window.__noxa = { state: { pc, activeTabID: "one", serverGeneration: 1, sessionGeneration: 1, myChannelID: 1, myClientID: "me", settings: {}, clients: [{ client_id: "alice", nickname: "Alice" }, { client_id: "bob", nickname: "Bob" }] },
            $: id => document.getElementById(id),
            shareAudioCtl: { get: () => audio, setMuted: (_id, muted) => { audio.muted = muted; }, setVolume: (_id, volume) => { audio.volume = volume; } }, sysMsg: () => {}, initials: name => name.slice(0, 1) };
        window.go = { main: { App: {
            SupportsStreamVideoQualityForTab: async () => true,
            SetStreamVideoQualityForTab: async (tab, publisher, slot, generation, session, quality) => {
                calls.push({ action: "quality", tab, publisher, slot, generation, session, quality });
                if (window.__streams.delayQuality) await new Promise(resolve => { window.__streams.resolveQuality = resolve; });
                return window.__streams.qualityError || "";
            },
            VideoStreamControlForTab: async (tab, msg) => {
            calls.push({ tab, ...msg });
            if (msg.action === "watch" && window.__streams.delayWatch) await new Promise(resolve => { window.__streams.resolveWatch = resolve; });
            return { ...msg, streams: structuredClone(window.__streams.streams), session: msg.action === "list" ? window.__streams.session : msg.session };
        } } } };
        const controls = await import("/src/stream-controls.js");
        const video = await import("/src/video.js");
        window.__streams.controls = controls;
        window.__streams.start = () => controls.startStreamSession(pc,
            (id, stream, publisher, buttons) => { shown.add(id); return video.videoTrackAdded(id, stream, publisher, buttons); },
            id => { shown.delete(id); video.videoTrackRemoved(id); });
        window.__streams.start();
        for (const stream of streams) {
            const canvas = document.createElement("canvas");
            canvas.getContext("2d").fillRect(0, 0, 100, 100);
            const track = canvas.captureStream(1).getVideoTracks()[0];
            Object.defineProperty(track, "id", { value: `${stream.publisher_id}|screen` });
            tracks.set(stream.publisher_id, track);
            controls.receiveStreamTrack(track, { client_id: stream.publisher_id });
        }
        const sharedAudio = { enabled: true };
        controls.receiveShareAudio(sharedAudio, "alice");
        window.__streams.sharedAudio = sharedAudio;
    });
    await expect(page.getByRole("button", { name: "Watch", exact: true })).toHaveCount(2);
});

test("upload invalidation serializes catalog refresh and cannot apply the older in-flight snapshot", async ({ page }) => {
    await page.evaluate(async () => {
        const publications = await import("/src/stream-publication.js");
        const track = document.createElement("canvas").captureStream(1).getVideoTracks()[0];
        const own = { publisher_id: "me", slot: "cam", generation: "20", watch_revision: "0", quality_mode: "source" };
        const original = window.go.main.App.VideoStreamControlForTab;
        Object.assign(window.__streams, { uploadChanges: [], uploadLists: 0, uploadPending: 0, uploadMaxPending: 0 });
        window.go.main.App.SupportsStreamSourceQualityForTab = async () => true;
        window.go.main.App.VideoStreamControlForTab = async (tab, body) => {
            if (body.action === "publish") return { ...body, generation: "20", upload_active: false };
            if (body.action !== "list") return original(tab, body);
            const n = ++window.__streams.uploadLists;
            window.__streams.uploadPending++;
            window.__streams.uploadMaxPending = Math.max(window.__streams.uploadMaxPending, window.__streams.uploadPending);
            if (n === 1) await new Promise(resolve => { window.__streams.finishUploadList = resolve; });
            window.__streams.uploadPending--;
            return { session: "10", streams: [...window.__streams.streams, { ...own, upload_active: n !== 2 }] };
        };
        await publications.preparePublicationUpload("cam", track, () => { window.__streams.uploadChanges.push(publications.publicationUploadActive("cam", track)); });
        await publications.startPublication("cam", track);
        window.__streams.uploadEvent = { publisher_id: "me", slot: "cam", generation: "20" };
        window.__streams.controls.refreshStreamUploads(window.__streams.uploadEvent);
    });
    await expect.poll(() => page.evaluate(() => typeof window.__streams.finishUploadList)).toBe("function");
    await page.evaluate(() => {
        window.__streams.controls.refreshStreamUploads(window.__streams.uploadEvent);
        window.__streams.controls.refreshStreamUploads(window.__streams.uploadEvent);
    });
    expect(await page.evaluate(() => window.__streams.uploadLists)).toBe(1);
    await page.evaluate(() => window.__streams.finishUploadList());
    await expect.poll(() => page.evaluate(() => window.__streams.uploadLists)).toBe(2);
    expect(await page.evaluate(() => window.__streams.uploadChanges)).toEqual([false]);
    expect(await page.evaluate(() => window.__streams.uploadMaxPending)).toBe(1);
    await page.evaluate(() => window.__streams.controls.refreshStreamUploads(window.__streams.uploadEvent));
    await expect.poll(() => page.evaluate(() => window.__streams.uploadChanges)).toEqual([false, true]);
});

test("watched shares have independent quality and allow multiple High selections", async ({ page }) => {
    for (const publisher of ["alice", "bob"]) await page.locator(`[data-publisher="${publisher}"] .stream-watch`).click();
    for (const publisher of ["alice", "bob"]) {
        await page.locator(`.vtile[data-clid="${publisher}"] .vtile-quality-button`).click();
        await page.locator('.ctx-menu [data-q="high"]').click();
    }
    await expect.poll(() => page.evaluate(() => Object.fromEntries(window.__streams.calls.filter(c => c.action === "quality").map(c => [c.publisher, c.quality])))).toEqual({ alice: "high", bob: "high" });
    await page.locator('.vtile[data-clid="alice"] .vtile-quality-button').click();
    await page.locator('.ctx-menu [data-q="low"]').click();
    await expect.poll(() => page.evaluate(() => Object.fromEntries(window.__streams.calls.filter(c => c.action === "quality").map(c => [c.publisher, c.quality])))).toEqual({ alice: "low", bob: "high" });
    expect(await page.evaluate(() => window.__streams.calls.filter(c => c.action === "quality").every(c => c.session === "10" && c.slot === "screen"))).toBe(true);
});

test("source-quality streams expose details without requesting alternate receiver layers", async ({ page }) => {
    await page.evaluate(() => {
        window.__streams.controls.stopStreamSession();
        for (const stream of window.__streams.streams) stream.quality_mode = 'source';
        window.__streams.start();
        for (const publisher of ['alice', 'bob']) {
            const track = document.createElement('canvas').captureStream(1).getVideoTracks()[0];
            window.__streams.controls.receiveStreamTrack(track, { client_id: publisher }, `${publisher}|screen`);
        }
    });
    for (const publisher of ['alice', 'bob']) await page.locator(`[data-publisher="${publisher}"] .stream-watch`).click();
    await expect(page.locator('.vtile[data-clid="alice"] .vtile-quality-button')).toBeHidden();
    await page.locator('.vtile[data-clid="alice"]').click({ button: 'right' });
    await expect(page.locator('.ctx-menu [data-q]')).toHaveCount(0);
    await page.locator('[data-stream-details]').click();
    await expect(page.locator('.vtile[data-clid="alice"] .vtile-quality')).toContainText('sender');
    expect(await page.evaluate(() => window.__streams.calls.filter(c => c.action === 'quality'))).toEqual([]);
});

test("a denied quality request remains visible and stale publication replies are ignored", async ({ page }) => {
    await page.locator('[data-publisher="alice"] .stream-watch').click();
    await page.evaluate(() => { window.__streams.qualityError = "permission changed"; });
    await page.locator('.vtile[data-clid="alice"] .vtile-quality-button').click();
    await page.locator('.ctx-menu [data-q="low"]').click();
    await page.locator('.vtile[data-clid="alice"] .vtile-diagnostics summary').click();
    await expect(page.locator('.vtile-quality-error')).toContainText("permission changed");
    await page.evaluate(() => { window.__streams.delayQuality = true; });
    await page.locator('.vtile[data-clid="alice"] .vtile-quality-button').click();
    await page.locator('.ctx-menu [data-q="mid"]').click();
    await expect.poll(() => page.evaluate(() => typeof window.__streams.resolveQuality)).toBe("function");
    await page.evaluate(() => { window.__streams.controls.stopStreamSession(); window.__streams.resolveQuality(); });
    await expect(page.locator('.vtile')).toHaveCount(0);
});

test("received details show measured dimensions and FPS separately from requested quality", async ({ page }, testInfo) => {
    await page.clock.install();
    await page.evaluate(() => {
        let sample = 0;
        window.go.main.App.SystemCPUPercent = async () => 15;
        window.__noxa.state.pc.getStats = async () => {
            sample++;
            return new Map([['alice-video', { id: 'alice-video', ssrc: 42, type: 'inbound-rtp', kind: 'video',
                trackIdentifier: 'alice|screen', frameWidth: 1920, frameHeight: 1080,
                framesDecoded: sample * 90, bytesReceived: sample * 1500000, timestamp: sample * 3000,
                packetsLost: 0, codecId: 'vp8' }], ['vp8', { type: 'codec', mimeType: 'video/VP8' }]]);
        };
    });
    await page.locator('[data-publisher="alice"] .stream-watch').click();
    await page.clock.fastForward(3000);
    await expect(page.locator('.vtile-received')).toHaveText('Received: 1920 × 1080 · — fps');
    await page.clock.fastForward(3000);
    await expect(page.locator('.vtile-received')).toHaveText('Received: 1920 × 1080 · 30.0 fps');
    await page.locator('.vtile-quality-button').click();
    await page.locator('.ctx-menu [data-q="low"]').click();
    await page.locator('.vtile-diagnostics summary').click();
    await expect(page.locator('.vtile-quality')).toContainText('requested layer: low');
    await expect(page.locator('.vtile-received')).toContainText('1920 × 1080');
    await page.screenshot({ path: testInfo.outputPath('received-quality.png') });
});

test("older servers expose an honest connection-wide quality control", async ({ page }) => {
    await page.evaluate(() => {
        window.go.main.App.SupportsStreamVideoQualityForTab = async () => false;
        window.go.main.App.SetVideoQualityForTab = async (tab, quality) => { window.__streams.legacyQuality = [tab, quality]; return ''; };
    });
    for (const publisher of ['alice', 'bob']) await page.locator(`[data-publisher="${publisher}"] .stream-watch`).click();
    await page.locator('.vtile[data-clid="alice"] .vtile-quality-button').click();
    await page.locator('.ctx-menu [data-q="high"]').click();
    await expect(page.getByRole('button', { name: 'Receive quality for all streams on this server', exact: true })).toHaveCount(2);
    await expect(page.locator('.vtile[data-clid="bob"] .vtile-quality-button')).toContainText('High');
    expect(await page.evaluate(() => window.__streams.legacyQuality)).toEqual(['one', 'high']);
    expect(await page.evaluate(() => window.__streams.calls.some(call => call.action === 'quality'))).toBe(false);
});

for (const failure of ["reject", "pending"]) test(`receiver diagnostics expire when stats are ${failure} and recover with a new baseline`, async ({ page }) => {
    await page.clock.install();
    await page.evaluate(() => {
        let sample = 0;
        window.__streams.statsMode = "healthy";
        window.__noxa.toast = () => {};
        window.runtime = { ClipboardSetText: async value => { window.__streams.copied = value; return true; } };
        window.go.main.App.SystemCPUPercent = async () => 10;
        window.__noxa.state.pc.getStats = async () => {
            if (window.__streams.statsMode === "reject") throw new Error("stats rejected");
            if (window.__streams.statsMode === "pending") await new Promise(resolve => { window.__streams.resolveStats = resolve; });
            sample++;
            return new Map([["video", { id: "video", type: "inbound-rtp", kind: "video", ssrc: 42, trackIdentifier: "alice|screen",
                timestamp: sample * 3000, framesDecoded: sample * 90, bytesReceived: sample * 1500000, frameWidth: 1920, frameHeight: 1080 }]]);
        };
    });
    await page.locator('[data-publisher="alice"] .stream-watch').click();
    await page.clock.fastForward(3000);
    await expect(page.locator(".vtile-received")).toContainText("— fps");
    await page.clock.fastForward(3000);
    await expect(page.locator(".vtile-received")).toContainText("30.0 fps");
    await page.locator(".vtile-diagnostics summary").click();
    await page.evaluate(mode => { window.__streams.statsMode = mode; }, failure);
    await page.clock.fastForward(3000);
    await page.clock.fastForward(16000);
    await expect(page.locator(".vtile-received")).toHaveText("Received: — · — fps");
    await expect(page.locator(".vtile-badge")).toBeHidden();
    await page.getByRole("button", { name: "Copy this stream’s diagnostics" }).click();
    expect(await page.evaluate(() => JSON.parse(window.__streams.copied).receiver)).toBe(null);
    await page.evaluate(() => { window.__streams.statsMode = "healthy"; window.__streams.resolveStats?.(); });
    if (failure === "pending") await expect(page.locator(".vtile-received")).toContainText("— fps");
    else { await page.clock.fastForward(3000); await expect(page.locator(".vtile-received")).toContainText("— fps"); }
    await page.clock.fastForward(3000);
    await expect(page.locator(".vtile-received")).toContainText("30.0 fps");
});

test("ordinary stream viewers can inspect the sender, server and receiver path", async ({ page }) => {
    await page.clock.install();
    await page.evaluate(() => {
        const stage = { fps: 12, sample_ms: 3000, age_ms: 0, stale: false };
        window.__streams.diagnosticCalls = 0;
        window.go.main.App.SupportsStreamDiagnosticsForTab = async () => true;
        window.go.main.App.StreamDiagnosticsForTab = async (_tab, publisher, slot, generation, session) => {
            window.__streams.diagnosticCalls++;
            return { publisher_id: publisher, slot, generation, session,
                sender_report: { age_ms: 1000, stale: false, rows: [{ ssrc: 42, slot, generation, sample_ms: 5000,
                    capture_fps: 60, encoded_fps: 12, sent_fps: 12, reported_fps: 60 }] },
                layers: [{ ssrc: 42, rid: "f", ingress: stage }], forwarding: { source_ssrc: 42, stage } };
        };
    });
    await page.locator('[data-publisher="alice"] .stream-watch').click();
    expect(await page.evaluate(() => window.__streams.diagnosticCalls)).toBe(0);
    await page.locator('.vtile-quality-button').click();
    await page.locator('[data-stream-details]').click();
    await expect(page.locator('[data-stage="capture"]')).toHaveText('60.0 fps');
    await expect(page.locator('[data-stage="encoded"]')).toHaveText('12.0 fps');
    await expect(page.locator('[data-stage="ingress"]')).toHaveText('12.0 fps');
    await page.setViewportSize({ width: 360, height: 729 });
    expect(await page.locator('.vtile-diagnostics-body').evaluate(el => el.scrollWidth <= el.clientWidth + 1)).toBe(true);
    await page.evaluate(async () => { (await import('/src/i18n.js')).setLanguage('de'); window.dispatchEvent(new Event('noxa-language-changed')); });
    await expect(page.locator('.vtile-diagnostics summary')).toHaveText('Streamdetails');
});

test("stream details stay scrollable and quality controls remain reachable in short tiles", async ({ page }, testInfo) => {
    await page.setViewportSize({ width: 720, height: 600 });
    await page.evaluate(() => {
        window.__noxa.toast = () => {};
        window.runtime = { ClipboardSetText: async value => { window.__streams.copied = value; return true; } };
    });
    for (const publisher of ["alice", "bob"]) await page.locator(`[data-publisher="${publisher}"] .stream-watch`).click();
    await page.addStyleTag({ content: "#video-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); max-height: none; } .vtile { height: 250px; aspect-ratio: auto; }" });
    const tile = page.locator('.vtile[data-clid="alice"]');
    await tile.locator(".vtile-diagnostics summary").click();
    await tile.locator(".vtile-quality-button").click();
    await page.locator('.ctx-menu [data-q="high"]').click();
    const copy = tile.getByRole("button", { name: "Copy this stream’s diagnostics" });
    await copy.click();
    expect(await page.evaluate(() => JSON.parse(window.__streams.copied).direction)).toBe("received");
    const bounds = await tile.evaluate(el => {
        const tileRect = el.getBoundingClientRect();
        const details = el.querySelector(".vtile-diagnostics");
        const summary = details.querySelector("summary").getBoundingClientRect();
        const button = details.querySelector(".vtile-copy-diagnostics").getBoundingClientRect();
        return { inside: button.top >= tileRect.top && button.bottom <= tileRect.bottom,
            summaryVisible: summary.top >= tileRect.top && summary.bottom <= tileRect.bottom,
            scrolled: details.scrollTop > 0, overflowX: details.scrollWidth > details.clientWidth + 1 };
    });
    expect(bounds).toEqual({ inside: true, summaryVisible: true, scrolled: true, overflowX: false });
    await tile.locator(".vtile-quality-button").click();
    await page.locator('.ctx-menu [data-q="mid"]').click();
    await page.screenshot({ path: testInfo.outputPath("short-tile-diagnostics.png") });
    await tile.locator(".vtile-diagnostics summary").click();
    await expect(tile.locator(".vtile-diagnostics")).not.toHaveAttribute("open", "");
});

test("diagnostics ignore replies from replaced streams and deny stale access", async ({ page }) => {
    await page.evaluate(() => {
        window.go.main.App.SupportsStreamDiagnosticsForTab = async () => true;
        window.go.main.App.StreamDiagnosticsForTab = () => new Promise(resolve => { window.__streams.resolveDiagnostics = resolve; });
    });
    await page.locator('[data-publisher="alice"] .stream-watch').click();
    await page.locator('.vtile-diagnostics summary').click();
    await expect.poll(() => page.evaluate(() => typeof window.__streams.resolveDiagnostics)).toBe('function');
    await page.evaluate(() => {
        window.__streams.controls.stopStreamSession();
        window.__streams.resolveDiagnostics({ publisher_id: 'alice', slot: 'screen', generation: '1', session: '10' });
        window.__streams.start();
        const track = document.createElement('canvas').captureStream(1).getVideoTracks()[0];
        window.__streams.controls.receiveStreamTrack(track, { client_id: 'alice' }, 'alice|screen');
        window.go.main.App.StreamDiagnosticsForTab = async () => { throw new Error('permission changed'); };
    });
    await page.locator('[data-publisher="alice"] .stream-watch').click();
    await page.locator('.vtile-diagnostics summary').click();
    await expect(page.locator('.vtile-path-status')).toContainText('permission changed');
    await expect(page.locator('[data-stage="encoded"]')).toHaveText('—');
});

test("two independent watches, stop, and shared audio controls leave voice intact", async ({ page }) => {
    expect(await page.evaluate(() => [...window.__streams.tracks.values()].map(track => track.enabled))).toEqual([false, false]);
    expect(await page.evaluate(() => window.__streams.sharedAudio.enabled)).toBe(false);
    const alice = page.locator('[data-publisher="alice"]');
    const bob = page.locator('[data-publisher="bob"]');
    const aliceVideo = page.locator('.vtile[data-clid="alice"]');
    const bobVideo = page.locator('.vtile[data-clid="bob"]');
    await alice.getByRole("button", { name: "Watch", exact: true }).click();
    await bob.getByRole("button", { name: "Watch", exact: true }).click();
    await expect(page.getByRole("button", { name: "Stop watching", exact: true })).toHaveCount(2);
    await expect(alice).toBeHidden();
    await expect(bob).toBeHidden();
    await expect(page.locator("#stream-catalog")).toBeHidden();
    expect(await page.evaluate(() => [...window.__streams.shown])).toEqual(["alice|screen", "bob|screen"]);
    await aliceVideo.locator(".stream-audio button").click();
    expect(await page.evaluate(() => window.__streams.audio.muted)).toBe(true);
    await aliceVideo.getByRole("slider").fill("35");
    await expect(aliceVideo.locator("output")).toHaveText("35%");
    await aliceVideo.getByRole("button", { name: "Stop watching", exact: true }).click();
    await expect(alice.getByRole("button", { name: "Watch", exact: true })).toBeVisible();
    expect(await page.evaluate(() => ({ shown: [...window.__streams.shown], audio: window.__streams.sharedAudio.enabled, voice: !!window.__noxa.state.pc }))).toEqual({ shown: ["bob|screen"], audio: false, voice: true });
    await page.evaluate(async () => { (await import("/src/i18n.js")).setLanguage("de"); window.dispatchEvent(new Event("noxa-language-changed")); });
    await expect(alice.getByRole("button", { name: "Ansehen", exact: true })).toBeVisible();
    await expect(bobVideo.getByRole("button", { name: "Ansehen beenden", exact: true })).toBeVisible();
});

test("watch uses negotiated publisher identity when a browser reuses an opaque receiver track", async ({ page }) => {
    await page.evaluate(() => {
        const track = document.createElement("canvas").captureStream(1).getVideoTracks()[0];
        window.__reusedReceiver = track;
        window.__streams.controls.receiveStreamTrack(track, { client_id: "alice" }, "alice|screen");
    });
    await page.locator('[data-publisher="alice"] .stream-watch').click();
    expect(await page.evaluate(() => window.__reusedReceiver.enabled)).toBe(true);
    await expect(page.locator('.vtile[data-clid="alice"][data-slot="screen"]')).toBeVisible();
    await expect.poll(() => page.evaluate(() => document.querySelector('.vtile[data-clid="alice"] video').srcObject.getVideoTracks()[0] === window.__reusedReceiver)).toBe(true);
});

test("watch overlays the preview and stop moves inside the active stream", async ({ page }) => {
    const card = page.locator('[data-publisher="alice"]');
    for (const width of [360, 1008]) {
        await page.setViewportSize({ width, height: 729 });
        const preview = await card.locator(".stream-thumbnail").boundingBox();
        const button = card.getByRole("button", { name: "Watch", exact: true });
        const bounds = await button.boundingBox();
        expect(bounds.x).toBeGreaterThanOrEqual(preview.x);
        expect(bounds.y).toBeGreaterThanOrEqual(preview.y);
        expect(bounds.x + bounds.width).toBeLessThanOrEqual(preview.x + preview.width);
        expect(bounds.y + bounds.height).toBeLessThanOrEqual(preview.y + preview.height);
        await button.focus();
        await page.keyboard.press("Enter");
        await expect(card).toBeHidden();
        const liveVideo = page.locator('.vtile[data-clid="alice"]');
        const stop = liveVideo.getByRole("button", { name: "Stop watching", exact: true });
        await expect(stop).toBeVisible();
        const liveBounds = await liveVideo.boundingBox(), stopBounds = await stop.boundingBox();
        expect(stopBounds.x).toBeGreaterThanOrEqual(liveBounds.x);
        expect(stopBounds.x + stopBounds.width).toBeLessThanOrEqual(liveBounds.x + liveBounds.width);
        expect(stopBounds.y).toBeGreaterThanOrEqual(liveBounds.y);
        expect(stopBounds.y + stopBounds.height).toBeLessThanOrEqual(liveBounds.y + liveBounds.height);
        await stop.focus();
        await page.keyboard.press("Enter");
        await expect(button).toBeVisible();
    }
});

test("three active streams never overlap in a height-constrained chat pane", async ({ page }) => {
    await page.setViewportSize({ width: 1008, height: 729 });
    await page.evaluate(() => {
        document.querySelector("main").style.cssText = "display:flex;flex-direction:column;height:500px";
        window.__streams.streams.push({ publisher_id: "bob", slot: "cam", generation: "3", preview_at: 0, watch_revision: "0" });
        const track = document.createElement("canvas").captureStream(1).getVideoTracks()[0];
        Object.defineProperty(track, "id", { value: "bob|cam" });
        window.__streams.controls.receiveStreamTrack(track, { client_id: "bob" });
    });
    await expect(page.getByRole("button", { name: "Watch", exact: true })).toHaveCount(3);
    for (let i = 0; i < 3; i++) await page.getByRole("button", { name: "Watch", exact: true }).first().click();
    const bounds = await page.locator(".vtile").evaluateAll(tiles => tiles.map(tile => tile.getBoundingClientRect().toJSON()));
    expect(bounds).toHaveLength(3);
    for (let i = 0; i < bounds.length; i++) for (let j = i + 1; j < bounds.length; j++) {
        const a = bounds[i], b = bounds[j];
        expect(a.right <= b.left || b.right <= a.left || a.bottom <= b.top || b.bottom <= a.top).toBe(true);
    }
});

test("one channel member can show camera and screen tiles and stop either independently", async ({ page }) => {
    await page.evaluate(() => {
        window.__streams.streams.push({ publisher_id: "alice", slot: "cam", generation: "3", preview_at: 0, watch_revision: "0" });
        const track = document.createElement("canvas").captureStream(1).getVideoTracks()[0];
        Object.defineProperty(track, "id", { value: "alice|cam" });
        window.__streams.camera = track;
        window.__streams.controls.receiveStreamTrack(track, { client_id: "alice" });
    });
    const camera = page.locator('[data-publisher="alice"][data-slot="cam"]');
    const screen = page.locator('[data-publisher="alice"][data-slot="screen"]');
    await camera.getByRole("button", { name: "Watch", exact: true }).click();
    await screen.getByRole("button", { name: "Watch", exact: true }).click();
    await expect(page.locator('.vtile[data-clid="alice"]')).toHaveCount(2);
    expect(await page.evaluate(() => [window.__streams.camera.enabled, window.__streams.tracks.get("alice").enabled, window.__streams.sharedAudio.enabled])).toEqual([true, true, true]);
    await page.locator('.vtile[data-clid="alice"]').filter({ has: page.locator('video') }).first().getByRole("button", { name: "Stop watching", exact: true }).click();
    expect(await page.evaluate(() => [window.__streams.camera.enabled, window.__streams.tracks.get("alice").enabled, window.__streams.sharedAudio.enabled])).toEqual([false, true, true]);
    await expect(page.locator('.vtile[data-clid="alice"]')).toHaveCount(1);
    await page.locator('.vtile[data-clid="alice"]').getByRole("button", { name: "Stop watching", exact: true }).click();
    expect(await page.evaluate(() => window.__streams.sharedAudio.enabled)).toBe(false);
    await camera.getByRole("button", { name: "Watch", exact: true }).click();
    expect(await page.evaluate(() => [window.__streams.camera.enabled, window.__streams.sharedAudio.enabled])).toEqual([true, false]);
});

test("catalog invalidation stops publication without cancelling a newly accepted generation", async ({ page }) => {
    const result = await page.evaluate(async () => {
        const p = await import("/src/stream-publication.js");
        const capture = () => document.createElement("canvas").captureStream(1).getVideoTracks()[0];
        const original = window.go.main.App.VideoStreamControlForTab;
        let generation = 20;
        window.go.main.App.VideoStreamControlForTab = async (tab, msg) => msg.action === "publish"
            ? { ...msg, generation: msg.active ? String(++generation) : msg.generation, streams: [] }
            : original(tab, msg);
        const first = capture();
        await p.startPublication("cam", first);
        const oldSnapshot = p.publicationSnapshot();
        const next = capture();
        await p.startPublication("cam", next);
        p.reconcilePublications(oldSnapshot, []);
        const replacementSurvived = next.readyState;
        p.reconcilePublications(p.publicationSnapshot(), []);
        first.stop();
        return { replacementSurvived, revoked: next.readyState, publications: p.publicationSnapshot().length };
    });
    expect(result).toEqual({ replacementSurvived: "live", revoked: "ended", publications: 0 });
});

test("late watch acknowledgement cannot attach video in a replacement session", async ({ page }) => {
    await page.evaluate(() => { window.__streams.delayWatch = true; });
    await page.locator('[data-publisher="alice"] .stream-watch').click();
    await expect.poll(() => page.evaluate(() => typeof window.__streams.resolveWatch)).toBe("function");
    await page.evaluate(() => {
        window.__noxa.state.myChannelID = 2;
        window.__streams.session = "11";
        window.__streams.start();
        window.__streams.resolveWatch();
    });
    await expect(page.getByRole("button", { name: "Watch", exact: true })).toHaveCount(2);
    expect(await page.evaluate(() => [...window.__streams.shown])).toEqual([]);
    expect(await page.evaluate(() => window.__streams.tracks.get("alice").enabled)).toBe(false);
});

test("delayed watch acknowledgement leaves a newer keyboard focus choice intact", async ({ page }) => {
    await page.evaluate(() => {
        window.__streams.delayWatch = true;
        const input = document.createElement("textarea");
        input.setAttribute("aria-label", "Chat message");
        document.body.append(input);
    });
    await page.locator('[data-publisher="alice"] .stream-watch').click();
    const chat = page.getByRole("textbox", { name: "Chat message" });
    await chat.focus();
    await page.evaluate(() => window.__streams.resolveWatch());
    await expect(page.locator('.vtile[data-clid="alice"]').getByRole("button", { name: "Stop watching", exact: true })).toBeVisible();
    await expect(chat).toBeFocused();
});

async function waitForMissingStream(page) {
    await page.clock.install();
    await page.evaluate(() => {
        const s = window.__streams;
        s.controls.removeStreamTrack(s.tracks.get("alice"), "alice|screen");
    });
    await page.locator('[data-publisher="alice"] .stream-watch').click();
    await page.clock.runFor(12_000);
    return page.locator('[data-publisher="alice"]');
}

test("persistent waiting retries with a fresh revision and keeps voice connected", async ({ page }) => {
    const card = await waitForMissingStream(page);
    await expect(card.getByRole("button", { name: "Retry stream", exact: true })).toBeVisible();
    await card.getByRole("button", { name: "Retry stream", exact: true }).click();
    expect(await page.evaluate(() => window.__streams.calls.filter(call => call.action === "watch").map(call => [call.active, call.revision, call.generation, call.session]))).toEqual([[true, "1", "1", "10"], [true, "2", "1", "10"]]);
    expect(await page.evaluate(() => !!window.__noxa.state.pc)).toBe(true);
    await expect(card.getByRole("button", { name: "Stop watching", exact: true })).toBeVisible();
});

test("retry failure stays actionable and late retry cannot attach to a replacement session", async ({ page }) => {
    const card = await waitForMissingStream(page);
    await page.evaluate(() => {
        const original = window.go.main.App.VideoStreamControlForTab;
        window.go.main.App.VideoStreamControlForTab = async (tab, msg) => {
            if (msg.action === "watch" && !window.__streams.delayWatch) throw new Error("Retry unavailable");
            return original(tab, msg);
        };
    });
    await card.getByRole("button", { name: "Retry stream", exact: true }).click();
    await expect(card.locator(".stream-status")).toContainText("Retry unavailable");
    await expect(card.getByRole("button", { name: "Retry stream", exact: true })).toBeEnabled();
    await page.evaluate(() => { window.__streams.delayWatch = true; });
    await card.getByRole("button", { name: "Retry stream", exact: true }).click();
    await page.evaluate(() => {
        window.__streams.session = "11";
        window.__streams.start();
        window.__streams.resolveWatch();
    });
    await expect(page.getByRole("button", { name: "Watch", exact: true })).toHaveCount(2);
    expect(await page.evaluate(() => [...window.__streams.shown])).toEqual([]);
});

test("confirmed publication stop replaces waiting with a dismissible stopped notice", async ({ page }) => {
    const card = await waitForMissingStream(page);
    await page.evaluate(() => { window.__streams.streams = window.__streams.streams.filter(s => s.publisher_id !== "alice"); });
    await page.clock.runFor(3_000);
    await expect(card.locator(".stream-status")).toHaveText("Stream stopped");
    await expect(card.getByRole("button", { name: "Retry stream", exact: true })).toBeHidden();
    await card.getByRole("button", { name: "Dismiss", exact: true }).click();
    await expect(card).toHaveCount(0);
    expect(await page.evaluate(() => !!window.__noxa.state.pc)).toBe(true);
});

test("decoded static screens and intentional low bandwidth pauses do not offer recovery", async ({ page }) => {
    await page.locator('[data-publisher="alice"] .stream-watch').click();
    const tile = page.locator('.vtile[data-clid="alice"]');
    await expect(tile).toHaveClass(/has-video/);
    await page.clock.install();
    await page.clock.runFor(18_000);
    await expect(tile.getByRole("button", { name: "Retry stream", exact: true })).toBeHidden();
    await page.evaluate(async () => { (await import("/src/video.js")).setLowBandwidth(true); });
    await page.clock.runFor(18_000);
    await expect(tile.getByRole("button", { name: "Retry stream", exact: true })).toBeHidden();
});

test("ended receiver has a truthful translated notice until a replacement track arrives", async ({ page }) => {
    await page.clock.install();
    await page.locator('[data-publisher="alice"] .stream-watch').click();
    await page.evaluate(() => {
        const s = window.__streams, track = s.tracks.get("alice");
        track.stop();
        s.controls.removeStreamTrack(track, "alice|screen");
    });
    const card = page.locator('[data-publisher="alice"]');
    await expect(card.locator(".stream-status")).toHaveText("Video track ended. Ask the sender to restart this stream.");
    await expect(page.getByRole("button", { name: "Retry stream", exact: true })).toBeHidden();
    await page.evaluate(async () => { (await import("/src/i18n.js")).setLanguage("de"); window.dispatchEvent(new Event("noxa-language-changed")); });
    await expect(card.locator(".stream-status")).toHaveText("Die Videospur wurde beendet. Bitte den Sender, den Stream neu zu starten.");
    await page.evaluate(() => {
        const canvas = document.createElement("canvas");
        canvas.getContext("2d").fillRect(0, 0, 100, 100);
        window.__streams.controls.receiveStreamTrack(canvas.captureStream(1).getVideoTracks()[0], { client_id: "alice" }, "alice|screen");
    });
    await expect(card).toBeHidden();
    await expect(page.locator('.vtile[data-clid="alice"] .stream-live-notice')).not.toContainText("Die Videospur wurde beendet");
});

test("late retry cannot resume a newer publication", async ({ page }) => {
    const card = await waitForMissingStream(page);
    await page.evaluate(() => { window.__streams.delayWatch = true; });
    await card.getByRole("button", { name: "Retry stream", exact: true }).click();
    await page.evaluate(() => { window.__streams.streams[0].generation = "99"; });
    await page.clock.runFor(3_000);
    await page.evaluate(() => window.__streams.resolveWatch());
    await expect(card.getByRole("button", { name: "Watch", exact: true })).toBeVisible();
    expect(await page.evaluate(() => [...window.__streams.shown])).toEqual([]);
    await page.evaluate(async () => { (await import("/src/i18n.js")).setLanguage("de"); window.dispatchEvent(new Event("noxa-language-changed")); });
    await expect(card.getByRole("button", { name: "Ansehen", exact: true })).toBeVisible();
});

test("camera diagnostics expose a stalled stream and successful retry clears the warning on decoded video", async ({ page }) => {
    await page.clock.install();
    await page.evaluate(() => {
        const s = window.__streams;
        s.streams[0].slot = "cam";
        s.canvas = document.createElement("canvas");
        s.canvas.getContext("2d").fillRect(0, 0, 100, 100);
        const track = s.canvas.captureStream(1).getVideoTracks()[0];
        Object.defineProperty(track, "id", { value: "alice|cam" });
        s.camera = track;
        s.controls.receiveStreamTrack(track, { client_id: "alice" });
        window.__noxa.state.pc.getStats = async () => new Map([["camera", { type: "inbound-rtp", kind: "video", trackIdentifier: "alice|cam", frameWidth: 640, framesDecoded: 1 }]]);
        window.__noxa.state.pc.getSenders = () => [];
        window.go.main.App.SystemCPUPercent = async () => 10;
    });
    await page.clock.runFor(3_000);
    await page.locator('[data-publisher="alice"][data-slot="cam"] .stream-watch').click();
    await page.evaluate(() => window.__streams.camera.requestFrame());
    const tile = page.locator('.vtile[data-clid="alice"][data-slot="cam"]');
    await expect(tile).toHaveClass(/has-video/);
    await page.clock.runFor(21_000);
    await expect(tile.locator(".stream-live-notice")).toContainText("Video has stalled");
    await tile.getByRole("button", { name: "Retry stream", exact: true }).click();
    await page.evaluate(() => window.__streams.camera.requestFrame());
    await page.clock.runFor(3_000);
    await expect(tile.getByRole("button", { name: "Retry stream", exact: true })).toBeHidden();
    await expect(tile).toHaveClass(/has-video/);
});
