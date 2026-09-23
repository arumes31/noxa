import { test, expect } from "@playwright/test";

test.beforeEach(async ({ page }) => {
    await page.route("**/streams-fixture", route => route.fulfill({ contentType: "text/html", body: `<!doctype html><html lang="en"><head><link rel="stylesheet" href="/src/style.css"><link rel="stylesheet" href="/src/streams.css"></head><body><main><div id="video-grid"></div></main></body></html>` }));
    await page.goto("/streams-fixture");
    await page.evaluate(async () => {
        const pc = {}, audio = { muted: false, volume: 100 };
        const streams = ["alice", "bob"].map((publisher_id, i) => ({ publisher_id, slot: "screen", generation: String(i + 1), preview_at: 0, watch_revision: "0" }));
        const calls = [], shown = new Set(), tracks = new Map();
        window.__streams = { streams, calls, shown, tracks, audio, session: "10" };
        window.__noxa = { state: { pc, activeTabID: "one", serverGeneration: 1, sessionGeneration: 1, myChannelID: 1, myClientID: "me", settings: {}, clients: [{ client_id: "alice", nickname: "Alice" }, { client_id: "bob", nickname: "Bob" }] },
            shareAudioCtl: { get: () => audio, setMuted: (_id, muted) => { audio.muted = muted; }, setVolume: (_id, volume) => { audio.volume = volume; } }, sysMsg: () => {}, initials: name => name.slice(0, 1) };
        window.go = { main: { App: { VideoStreamControlForTab: async (tab, msg) => {
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
