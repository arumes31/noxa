import { readFileSync } from "node:fs";
import { test, expect } from "./fixtures.js";

const clip = "data:video/webm;base64," + readFileSync(new URL("./chat-playback.webm", import.meta.url)).toString("base64");

test.beforeEach(async ({ page }) => {
    await page.route("**/__chat_lightbox__", route => route.fulfill({ contentType: "text/html", body:
        '<!doctype html><title>Chat media lightbox</title><style>.lightbox{position:fixed;inset:0;display:grid;place-items:center;background:#10212be6}.lightbox video{width:240px;height:240px}#chat{height:200px;overflow:auto}</style><main id="app"><div id="chat"><div class="msg"><span class="msg-file"><video id="clip" class="msg-video" title="test clip" width="100" height="100" controls muted loop></video><button id="zoom">Expand</button></span></div><div style="height:600px"></div></div></main>' }));
    await page.goto("/__chat_lightbox__");
    await page.evaluate(async source => {
        window.__focused = true;
        window.__noxa = { state: { serverGeneration: 1 } };
        Object.defineProperty(document, "hasFocus", { value: () => window.__focused });
        Object.defineProperty(document, "hidden", { get: () => false });
        const video = window.__clip = document.getElementById("clip");
        video.src = source;
        await video.play(); video.pause();
        video.volume = .37; video.muted = true; video.playbackRate = .5; video.currentTime = .2;
        const modal = window.__modal = await import("/src/modal.js"); modal.initModalSystem();
        const lightbox = await import("/src/chat-media-lightbox.js"); window.__open = lightbox.openChatMediaLightbox;
        document.getElementById("zoom").onclick = () => { window.__overlay = window.__open(video); };
    }, clip);
    await page.clock.install();
    await page.evaluate(async () => {
        const module = await import("/src/chat-gif-playback.js");
        window.__stopMedia = module.manageChatGIFPlayback(document.body, ".msg img, .lightbox img, .msg video.msg-video, .lightbox video");
    });
    await page.clock.runFor(50);
});

const mediaState = page => page.evaluate(() => ({
    paused: window.__clip.paused, time: window.__clip.currentTime,
    volume: window.__clip.volume, muted: window.__clip.muted, rate: window.__clip.playbackRate,
    count: document.querySelectorAll("video").length,
    same: document.querySelector(".lightbox video") === window.__clip,
}));

async function focusWindow(page, focused) {
    await page.evaluate(focused => {
        window.__focused = focused;
        window.dispatchEvent(new Event(focused ? "focus" : "blur"));
    }, focused);
}

test("expanding a paused clip keeps one owner and preserves position and audio settings", async ({ page }) => {
    await page.locator("#zoom").click();
    await expect(page.locator(".lightbox video")).toHaveCount(1);
    expect(await mediaState(page)).toMatchObject({ paused: true, time: .2, volume: .37, muted: true, rate: .5, count: 1, same: true });
    await page.keyboard.press("Escape");
    await expect(page.locator(".lightbox")).toHaveCount(0);
    expect(await mediaState(page)).toMatchObject({ paused: true, time: .2, volume: .37, muted: true, rate: .5, count: 1 });
    await expect(page.locator(".msg-file > #clip")).toHaveCount(1);
});

test("playing video remains the same owner and expanded controls carry back on close", async ({ page }) => {
    await page.evaluate(() => window.__clip.play());
    await page.locator("#zoom").click();
    expect(await mediaState(page)).toMatchObject({ paused: false, count: 1, same: true });
    await page.evaluate(() => {
        const video = window.__clip; video.pause(); video.currentTime = .35;
        video.volume = .61; video.muted = false; video.playbackRate = 1.25;
    });
    await page.keyboard.press("Escape");
    expect(await mediaState(page)).toMatchObject({ paused: true, time: .35, volume: .61, muted: false, rate: 1.25, count: 1 });
});

for (const fallback of [false, true]) {
    test(`playing video keeps its state after expanding and closing${fallback ? " without atomic DOM move support" : ""}`, async ({ page }) => {
        if (fallback) await page.evaluate(() => { Element.prototype.moveBefore = undefined; });
        await page.evaluate(() => window.__clip.play());
        await page.locator("#zoom").click();
        await page.clock.runFor(50);
        expect(await mediaState(page)).toMatchObject({ paused: false, volume: .37, muted: true, rate: .5, count: 1, same: true });
        await page.keyboard.press("Escape");
        await page.clock.runFor(50);
        expect(await mediaState(page)).toMatchObject({ paused: false, volume: .37, muted: true, rate: .5, count: 1 });
        await expect(page.locator(".msg-file > #clip")).toHaveCount(1);
    });
}

test("opening an already suspended clip does not autoplay, and closing offscreen preserves policy resume", async ({ page }) => {
    await page.evaluate(() => window.__clip.play());
    await focusWindow(page, false); await page.clock.runFor(60_000);
    expect((await mediaState(page)).paused).toBe(true);
    await page.evaluate(() => { window.__overlay = window.__open(window.__clip); });
    await page.clock.runFor(50);
    expect(await mediaState(page)).toMatchObject({ paused: true, count: 1, same: true });
    await focusWindow(page, true);
    await expect.poll(async () => (await mediaState(page)).paused).toBe(false);
    await page.locator("#chat").evaluate(chat => { chat.scrollTop = chat.scrollHeight; });
    const scrollTop = await page.locator("#chat").evaluate(chat => chat.scrollTop);
    await page.keyboard.press("Escape");
    await page.clock.runFor(50);
    expect(await page.locator("#chat").evaluate(chat => chat.scrollTop)).toBe(scrollTop);
    await expect.poll(async () => (await mediaState(page)).paused).toBe(true);
    await page.locator("#chat").evaluate(chat => { chat.scrollTop = 0; });
    await page.clock.runFor(50);
    await expect.poll(async () => (await mediaState(page)).paused).toBe(false);
});

test("manual pause in the expanded player remains paused across background and close", async ({ page }) => {
    await page.evaluate(() => window.__clip.play());
    await page.locator("#zoom").click();
    await page.evaluate(() => window.__clip.pause());
    await focusWindow(page, false); await page.clock.runFor(60_000); await focusWindow(page, true);
    await page.keyboard.press("Escape"); await page.clock.runFor(50);
    expect(await mediaState(page)).toMatchObject({ paused: true, count: 1 });
});

test("removed source chat and server changes close the player without resurrecting media", async ({ page }) => {
    await page.evaluate(() => window.__clip.play());
    await page.locator("#zoom").click();
    await page.locator("#chat").evaluate(chat => chat.remove());
    await expect(page.locator(".lightbox")).toHaveCount(0);
    expect(await mediaState(page)).toMatchObject({ paused: true, count: 0 });
    expect(await page.evaluate(() => window.__clip.isConnected)).toBe(false);
});

test("server-scoped close stops the player even before the old chat DOM is replaced", async ({ page }) => {
    await page.evaluate(() => window.__clip.play());
    await page.locator("#zoom").click();
    await page.evaluate(() => { window.__noxa.state.serverGeneration++; window.__modal.closeServerDialogs(); });
    await expect(page.locator(".lightbox")).toHaveCount(0);
    expect(await mediaState(page)).toMatchObject({ paused: true, count: 0 });
    await expect(page.locator(".chat-media-placeholder")).toHaveCount(0);
});

test("programmatic close and repeated expand retain a single player", async ({ page }) => {
    await page.evaluate(() => window.__clip.play());
    await page.locator("#zoom").click();
    const same = await page.evaluate(() => window.__open(window.__clip) === window.__overlay);
    expect(same).toBe(true);
    await expect(page.locator(".lightbox")).toHaveCount(1);
    await page.evaluate(() => window.__modal.closeDialog(window.__overlay));
    await page.clock.runFor(50);
    expect(await mediaState(page)).toMatchObject({ paused: false, count: 1 });
    await page.locator("#zoom").click();
    expect(await mediaState(page)).toMatchObject({ paused: false, count: 1, same: true });
});

test("backdrop and direct DOM removal restore manually paused video without autoplay", async ({ page }) => {
    await page.locator("#zoom").click();
    await page.locator(".lightbox").click({ position: { x: 10, y: 10 } });
    expect(await mediaState(page)).toMatchObject({ paused: true, time: .2, count: 1 });
    await page.locator("#zoom").click();
    await page.locator(".lightbox").evaluate(overlay => overlay.remove());
    await expect(page.locator(".msg-file > #clip")).toHaveCount(1);
    expect(await mediaState(page)).toMatchObject({ paused: true, time: .2, count: 1 });
});

test("changing the expanded source closes and stops the old attachment ownership", async ({ page }) => {
    await page.locator("#zoom").click();
    await page.evaluate(() => { window.__clip.src = window.__clip.src.replace("video/webm;", "video/webm;codecs=vp8;"); });
    await expect(page.locator(".lightbox")).toHaveCount(0);
    expect(await mediaState(page)).toMatchObject({ paused: true, count: 0 });
    await expect(page.locator(".chat-media-placeholder")).toHaveCount(0);
});

test("image zoom still uses the original animation and live video cannot enter this lightbox", async ({ page }) => {
    await page.evaluate(async () => {
        const image = document.createElement("img"); image.id = "animation";
        image.src = "data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7";
        image.width = image.height = 100; document.querySelector(".msg").append(image);
        await image.decode(); window.__image = image;
    });
    await focusWindow(page, false); await page.clock.runFor(60_000);
    await expect(page.locator("#animation")).toHaveAttribute("src", /^data:image\/svg\+xml,/);
    await focusWindow(page, true);
    await page.evaluate(() => window.__open(window.__image));
    await expect(page.locator(".lightbox img")).toHaveAttribute("src", /^data:image\/gif;/);
    await page.keyboard.press("Escape");
    const live = await page.evaluate(() => {
        const video = document.createElement("video"); document.body.append(video);
        video.srcObject = new MediaStream();
        return window.__open(video);
    });
    expect(live).toBeNull();
    await expect(page.locator(".lightbox")).toHaveCount(0);
});
