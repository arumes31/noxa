import { test, expect } from "./fixtures.js";

const gif = "data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7";
const frozen = /^data:image\/svg\+xml,/;

test.beforeEach(async ({ page }) => {
    await page.route("**/__gif_playback__", route => route.fulfill({ contentType: "text/html", body:
        '<!doctype html><title>GIF playback</title><div id="chat" style="height:200px;overflow:auto"><img id="visible" width="100" height="100"><div style="height:500px"></div><img id="offscreen" width="100" height="100"></div>' }));
    await page.goto("/__gif_playback__");
    await page.clock.install();
    await page.evaluate(async source => {
        window.__gifFocused = true;
        window.__gifHidden = false;
        Object.defineProperty(document, "hasFocus", { value: () => window.__gifFocused });
        Object.defineProperty(document, "hidden", { get: () => window.__gifHidden });
        for (const image of document.images) image.src = source;
        await Promise.all([...document.images].map(image => image.decode()));
        const module = await import("/src/chat-gif-playback.js");
        window.__originalGIF = module.originalGIFSource;
        window.__stopGIF = module.manageChatGIFPlayback(document.getElementById("chat"));
    }, gif);
    await page.clock.runFor(50);
});

async function focusWindow(page, focused) {
    await page.evaluate(focused => {
        window.__gifFocused = focused;
        window.dispatchEvent(new Event(focused ? "focus" : "blur"));
    }, focused);
}

test("GIFs pause after one unfocused minute and only visible images resume", async ({ page }) => {
    await focusWindow(page, false);
    await page.clock.runFor(59_000);
    await expect(page.locator("#visible")).toHaveAttribute("src", gif);
    await page.clock.runFor(1_000);
    for (const id of ["visible", "offscreen"]) await expect(page.locator("#" + id)).toHaveAttribute("src", frozen);
    expect(await page.locator("#visible").evaluate(image => [image.width, image.height])).toEqual([100, 100]);
    expect(await page.locator("#visible").evaluate(image => window.__originalGIF(image))).toBe(gif);
    await focusWindow(page, true);
    await expect(page.locator("#visible")).toHaveAttribute("src", gif);
    await expect(page.locator("#offscreen")).toHaveAttribute("src", frozen);
    await page.locator("#chat").evaluate(element => { element.scrollTop = element.scrollHeight; });
    await page.clock.runFor(50);
    await expect(page.locator("#offscreen")).toHaveAttribute("src", gif);
});

test("returning early cancels the timer and repeated blur starts a fresh minute", async ({ page }) => {
    await focusWindow(page, false); await page.clock.runFor(40_000);
    await focusWindow(page, true); await page.clock.runFor(30_000);
    await expect(page.locator("#visible")).toHaveAttribute("src", gif);
    await focusWindow(page, false); await page.clock.runFor(59_000);
    await expect(page.locator("#visible")).toHaveAttribute("src", gif);
    await page.clock.runFor(1_000);
    await expect(page.locator("#visible")).toHaveAttribute("src", frozen);
});

test("hidden windows and newly loaded GIFs pause without altering PNGs or video", async ({ page }) => {
    await page.evaluate(() => { window.__gifHidden = true; document.dispatchEvent(new Event("visibilitychange")); });
    await page.clock.runFor(60_000);
    await page.evaluate(source => {
        const root = document.getElementById("chat");
        const image = document.createElement("img"); image.id = "new-gif"; image.src = source; root.append(image);
        const png = document.createElement("img"); png.id = "still"; png.src = document.createElement("canvas").toDataURL(); root.append(png);
        const video = document.createElement("video"); video.id = "stream"; root.append(video);
        window.__videoPauseCalls = 0;
        video.pause = () => { window.__videoPauseCalls++; };
    }, gif);
    await expect(page.locator("#new-gif")).toHaveAttribute("src", frozen);
    await page.evaluate(() => {
        window.__gifHidden = false; document.dispatchEvent(new Event("visibilitychange"));
    });
    await expect(page.locator("#visible")).toHaveAttribute("src", gif);
    await expect(page.locator("#still")).toHaveAttribute("src", /^data:image\/png/);
    expect(await page.evaluate(() => window.__videoPauseCalls)).toBe(0);
});

test("removed GIFs and a disposed controller cannot resume obsolete images", async ({ page }) => {
    await focusWindow(page, false); await page.clock.runFor(60_000);
    await page.evaluate(() => { window.__removedGIF = document.getElementById("offscreen"); window.__removedGIF.remove(); });
    await focusWindow(page, true);
    expect(await page.evaluate(() => window.__removedGIF.src)).toMatch(frozen);
    // IntersectionObserver reports visibility asynchronously. Establish resumed
    // playback before testing that disposal prevents a later background pause.
    await expect(page.locator("#visible")).toHaveAttribute("src", gif);
    await page.evaluate(() => window.__stopGIF());
    await focusWindow(page, false); await page.clock.runFor(60_000);
    await expect(page.locator("#visible")).toHaveAttribute("src", gif);
});

test("frozen GIF previews preserve large landscape and portrait layout and pixels", async ({ page }) => {
    const before = await page.evaluate(async () => {
        const root = document.getElementById("chat"), dimensions = [];
        for (const [name, width, height] of [["landscape", 1800, 600], ["portrait", 600, 1800]]) {
            const canvas = document.createElement("canvas"); canvas.width = width; canvas.height = height;
            canvas.getContext("2d").fillRect(0, 0, width, height);
            const image = document.createElement("img"); image.id = name;
            image.style.cssText = "max-width:320px;max-height:220px";
            // The controller identifies GIF URLs; a static test bitmap supplies
            // deterministic dimensions and pixels through the browser decoder.
            image.src = canvas.toDataURL().replace("image/png", "image/gif");
            root.append(image); await image.decode();
            const box = image.getBoundingClientRect(); dimensions.push([box.width, box.height]);
        }
        return dimensions;
    });
    await focusWindow(page, false); await page.clock.runFor(60_000);
    const after = [];
    for (const name of ["landscape", "portrait"]) {
        const image = page.locator("#" + name);
        await expect(image).toHaveAttribute("src", frozen);
        after.push(await image.evaluate(async image => {
            await image.decode();
            const box = image.getBoundingClientRect();
            const canvas = document.createElement("canvas"); canvas.width = canvas.height = 1;
            const context = canvas.getContext("2d"); context.drawImage(image, 0, 0, 1, 1);
            if (context.getImageData(0, 0, 1, 1).data[3] !== 255) throw Error("Static preview lost its pixels");
            return [box.width, box.height];
        }));
    }
    expect(after).toEqual(before);
});
