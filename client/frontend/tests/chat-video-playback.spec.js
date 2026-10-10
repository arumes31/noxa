import { readFileSync } from "node:fs";
import { test, expect } from "./fixtures.js";

// Eight silent red/blue frames from an isolated canvas MediaRecorder; no devices.
const clip = "data:video/webm;base64," + readFileSync(new URL("./chat-playback.webm", import.meta.url)).toString("base64");

test.beforeEach(async ({ page }) => {
    await page.route("**/__chat_video_playback__", route => route.fulfill({ contentType: "text/html", body:
        '<!doctype html><title>Chat video playback</title><div id="chat" style="height:200px;overflow:auto"><video class="msg-video" id="visible" width="100" height="100" controls muted loop></video><video class="msg-video" id="manual" width="100" height="100" controls muted loop></video><div style="height:500px"></div><video class="msg-video" id="offscreen" width="100" height="100" controls muted loop></video></div><video class="msg-video" id="live" width="100" height="100" muted autoplay></video>' }));
    await page.goto("/__chat_video_playback__");
    await page.evaluate(async source => {
        window.__focused = true;
        Object.defineProperty(document, "hasFocus", { value: () => window.__focused });
        Object.defineProperty(document, "hidden", { get: () => false });
        for (const video of document.querySelectorAll("#chat video")) video.src = source;
        const canvas = document.createElement("canvas"); canvas.width = canvas.height = 24;
        document.getElementById("live").srcObject = canvas.captureStream(0);
        canvas.getContext("2d").fillRect(0, 0, 24, 24);
        document.getElementById("live").srcObject.getVideoTracks()[0].requestFrame();
        await document.getElementById("visible").play();
        await document.getElementById("live").play();
    }, clip);
    await page.clock.install();
    await page.evaluate(async () => {
        const module = await import("/src/chat-gif-playback.js");
        window.__stopChatMedia = module.manageChatGIFPlayback(document.body, "img, video.msg-video, .lightbox video");
    });
    await page.clock.runFor(50);
});

async function focusWindow(page, focused) {
    await page.evaluate(focused => {
        window.__focused = focused;
        window.dispatchEvent(new Event(focused ? "focus" : "blur"));
    }, focused);
}
const paused = (page, id) => page.locator("#" + id).evaluate(video => video.paused);

test("recorded chat video pauses offscreen and resumes at the same position only when visible", async ({ page }) => {
    await expect.poll(() => paused(page, "visible")).toBe(false);
    await page.locator("#chat").evaluate(root => { root.scrollTop = root.scrollHeight; });
    await page.clock.runFor(50);
    await expect.poll(() => paused(page, "visible")).toBe(true);
    const position = await page.locator("#visible").evaluate(video => video.currentTime);
    await expect.poll(() => paused(page, "offscreen")).toBe(true); // Never started.
    await page.locator("#chat").evaluate(root => { root.scrollTop = 0; });
    await page.clock.runFor(50);
    await expect.poll(() => paused(page, "visible")).toBe(false);
    expect(await page.locator("#visible").evaluate(video => video.currentTime)).toBeGreaterThanOrEqual(position);
    await expect.poll(() => paused(page, "manual")).toBe(true);
});

test("one background minute pauses recorded media, never live streams, and focus resumes only visible playback", async ({ page }) => {
    await focusWindow(page, false);
    await page.clock.runFor(59_000);
    await expect.poll(() => paused(page, "visible")).toBe(false);
    await page.clock.runFor(1_000);
    await expect.poll(() => paused(page, "visible")).toBe(true);
    await expect.poll(() => paused(page, "live")).toBe(false);
    await page.locator("#chat").evaluate(root => { root.scrollTop = root.scrollHeight; });
    await page.clock.runFor(50);
    await focusWindow(page, true);
    await expect.poll(() => paused(page, "visible")).toBe(true);
    await expect.poll(() => paused(page, "offscreen")).toBe(true);
    await page.locator("#chat").evaluate(root => { root.scrollTop = 0; });
    await page.clock.runFor(50);
    await expect.poll(() => paused(page, "visible")).toBe(false);
});

test("manual pause survives scrolling and focus changes, and removed media cannot restart", async ({ page }) => {
    await page.locator("#visible").evaluate(video => video.pause());
    await focusWindow(page, false); await page.clock.runFor(60_000);
    await focusWindow(page, true);
    await expect.poll(() => paused(page, "visible")).toBe(true);
    await page.locator("#visible").evaluate(video => video.play());
    await focusWindow(page, false); await page.clock.runFor(60_000);
    await expect.poll(() => paused(page, "visible")).toBe(true);
    await page.locator("#visible").evaluate(video => { window.__removedVideo = video; video.remove(); });
    await focusWindow(page, true);
    expect(await page.evaluate(() => window.__removedVideo.paused)).toBe(true);
});

test("a live source assigned to a managed attachment is never paused", async ({ page }) => {
    await page.locator("#visible").evaluate(async video => {
        video.srcObject = document.getElementById("live").srcObject;
        await video.play();
    });
    await focusWindow(page, false); await page.clock.runFor(60_000);
    await expect.poll(() => paused(page, "visible")).toBe(false);
    await page.evaluate(() => window.__stopChatMedia());
    await expect.poll(() => paused(page, "visible")).toBe(false);
});

test("new lightbox video obeys an already suspended window and resumes only on focus", async ({ page }) => {
    await focusWindow(page, false); await page.clock.runFor(60_000);
    await page.evaluate(async source => {
        const overlay = document.createElement("div"); overlay.className = "lightbox";
        overlay.style.cssText = "position:fixed;top:0;left:0";
        const video = document.createElement("video"); video.id = "expanded";
        video.muted = true; video.loop = true; video.controls = true;
        video.width = video.height = 100; video.src = source;
        overlay.append(video); document.body.append(overlay);
        await video.play().catch(() => {}); // A policy pause may abort play().
    }, clip);
    await page.clock.runFor(50);
    await expect.poll(() => paused(page, "expanded")).toBe(true);
    await focusWindow(page, true);
    await expect.poll(() => paused(page, "expanded")).toBe(false);
    await page.locator(".lightbox").evaluate(overlay => { window.__expandedVideo = overlay.querySelector("video"); overlay.remove(); });
    expect(await page.evaluate(() => window.__expandedVideo.paused)).toBe(true);
});

test("rejected automatic resume does not retry, and a replaced attachment never inherits old playback intent", async ({ page }) => {
    await focusWindow(page, false); await page.clock.runFor(60_000);
    await page.locator("#visible").evaluate(video => {
        window.__resumeAttempts = 0;
        video.play = () => { window.__resumeAttempts++; return Promise.reject(new DOMException("Autoplay denied", "NotAllowedError")); };
    });
    await focusWindow(page, true);
    expect(await page.evaluate(() => window.__resumeAttempts)).toBe(1);
    await focusWindow(page, false); await page.clock.runFor(60_000); await focusWindow(page, true);
    expect(await page.evaluate(() => window.__resumeAttempts)).toBe(1);
    await page.locator("#visible").evaluate(async video => { delete video.play; await video.play(); });
    await focusWindow(page, false); await page.clock.runFor(60_000);
    await page.locator("#visible").evaluate(video => { video.src = video.src.replace("video/webm;", "video/webm;codecs=vp8;"); });
    await focusWindow(page, true);
    await expect.poll(() => paused(page, "visible")).toBe(true);
});

test("removing a playing attachment or disposing its controller stops only recorded playback", async ({ page }) => {
    await page.locator("#visible").evaluate(video => { window.__removedVideo = video; video.remove(); });
    expect(await page.evaluate(() => window.__removedVideo.paused)).toBe(true);
    await page.locator("#manual").evaluate(video => video.play());
    await page.evaluate(() => window.__stopChatMedia());
    await expect.poll(() => paused(page, "manual")).toBe(true);
    await expect.poll(() => paused(page, "live")).toBe(false);
});
