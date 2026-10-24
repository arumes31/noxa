import { expect, test } from "@playwright/test";

import { desktopSession } from "./harness.mjs";
import { linkPhone, openServerToMembers } from "./helpers.mjs";

const server = JSON.parse(process.env.NOXA_E2E_SERVER_STATE || "{}");
let info;

test.beforeAll(async () => {
    info = await (await fetch(`${server.webURL}/api/v1/web/info`)).json();
});

async function phoneIn(browser, account, channel) {
    const context = await browser.newContext({ ...test.info().project.use, permissions: ["microphone"] });
    const page = await context.newPage();
    page.on("pageerror", error => console.log("pageerror:", error.message));
    const desktop = await desktopSession(server.controlAddr, account, { version: info.version });
    await openServerToMembers(desktop).catch(() => {});
    await linkPhone(page, desktop, server.webURL, expect);
    await page.getByRole("link", { name: "Voice", exact: true }).click();
    await page.getByRole("button", { name: new RegExp(`^${channel}`) }).click();
    await expect(page.getByText("Voice connected")).toBeVisible({ timeout: 20000 });
    return { page, context, desktop };
}

test("two phones join a voice channel and hear each other", async ({ browser }) => {
    test.setTimeout(150000);
    const owner = await desktopSession(server.controlAddr, server.accounts.owner, { version: info.version });
    await openServerToMembers(owner);
    owner.conn.close();
    const a = await phoneIn(browser, server.accounts.owner, "Public1");
    const b = await phoneIn(browser, server.accounts.rowan, "Public1");

    for (const phone of [a, b]) {
        await expect(phone.page.locator(".voice-tile")).toHaveCount(2, { timeout: 20000 });
        // Each phone plays the other's microphone and receives media.
        await expect.poll(() => phone.page.evaluate(() => document.querySelectorAll("audio[data-publisher]").length), { timeout: 20000 }).toBe(1);
        await expect(phone.page.getByText(/Data used: ↑ [0-9.]+ MB · ↓ [0-9.]+ MB/)).toBeVisible();
        await expect.poll(() => phone.page.evaluate(() => {
            const audio = document.querySelector("audio[data-publisher]");
            return audio?.srcObject?.getAudioTracks()[0]?.readyState;
        }), { timeout: 20000 }).toBe("live");
    }

    // A turns the camera on; B watches it.
    await a.page.getByRole("button", { name: "Turn camera on" }).click();
    await expect(a.page.getByRole("button", { name: "Turn camera off" })).toBeVisible({ timeout: 20000 });
    const watch = b.page.getByRole("button", { name: /^Watch .*camera/ });
    await expect(watch).toBeVisible({ timeout: 30000 });
    await watch.click();
    await expect.poll(() => b.page.evaluate(() => {
        const video = document.querySelector(".stream-video");
        return video?.srcObject?.getVideoTracks()[0]?.readyState === "live" && video.videoWidth > 0;
    }), { timeout: 30000 }).toBe(true);
    // Double tap switches fill and fit.
    await b.page.locator(".stream-video").dblclick();
    await expect(b.page.locator(".stream-video")).toHaveClass(/fill/);
    await a.page.getByRole("button", { name: "Turn camera off" }).click();

    // The mini call bar follows the user around and can mute.
    await a.page.getByRole("link", { name: "Home", exact: true }).click();
    const bar = a.page.getByRole("region", { name: "Ongoing voice call" });
    await expect(bar).toContainText("Public1");
    await bar.getByRole("button", { name: "Mute" }).click();
    await expect(bar.getByRole("button", { name: "Unmute" })).toBeVisible();

    // Leaving from the bar ends the call for this phone only.
    await bar.getByRole("button", { name: "Leave" }).click();
    await expect(bar).toHaveCount(0);
    await expect(b.page.locator(".voice-tile")).toHaveCount(1, { timeout: 20000 });
    for (const phone of [a, b]) {
        phone.desktop.conn.close();
        await phone.context.close();
    }
});

test("a phone watches two cameras on a stage with quality, focus, swap, fullscreen and PiP", async ({ browser }) => {
    test.setTimeout(200000);
    const owner = await desktopSession(server.controlAddr, server.accounts.owner, { version: info.version });
    await openServerToMembers(owner);
    owner.conn.close();
    const a = await phoneIn(browser, server.accounts.ivy, "Public1");
    const b = await phoneIn(browser, server.accounts.mia, "Public1");
    const c = await phoneIn(browser, server.accounts.noa, "Public1");
    await expect(c.page.locator(".voice-tile")).toHaveCount(3, { timeout: 20000 });

    // Two publishers; the front camera flips to the back one and back.
    for (const phone of [a, b]) {
        await phone.page.getByRole("button", { name: "Turn camera on" }).click();
        await expect(phone.page.getByRole("button", { name: "Turn camera off" })).toBeVisible({ timeout: 20000 });
    }
    const flip = a.page.getByRole("button", { name: "Switch camera" });
    await expect(flip).toHaveAttribute("data-facing", "user");
    await flip.click();
    await expect(flip).toHaveAttribute("data-facing", "environment");
    await expect(flip).toHaveAttribute("aria-pressed", "true");

    // The watcher picks both cameras; both play on the stage.
    for (let i = 0; i < 2; i++) {
        const watch = c.page.getByRole("button", { name: /^Watch .*camera/ }).first();
        await expect(watch).toBeVisible({ timeout: 30000 });
        await watch.click();
    }
    const tiles = c.page.locator(".stage-tile");
    await expect(tiles).toHaveCount(2, { timeout: 30000 });
    await expect.poll(() => c.page.evaluate(() => [...document.querySelectorAll(".stage-tile video")]
        .filter(video => video.srcObject?.getVideoTracks()[0]?.readyState === "live" && video.videoWidth > 0).length), { timeout: 30000 }).toBe(2);

    // Receive quality: the server confirms the scoped low layer.
    const firstKey = await tiles.first().getAttribute("data-key");
    await tiles.first().getByRole("combobox", { name: /^Quality for/ }).selectOption("low");
    await expect(c.page.locator(`.stage-tile[data-key="${firstKey}"]`)).toHaveAttribute("data-quality", "low", { timeout: 10000 });

    // Tap focuses a tile; the other becomes small and can swap in.
    const stage = c.page.getByRole("region", { name: "Streams" });
    await tiles.first().locator("video").click();
    await expect(stage).toHaveAttribute("data-layout", "focus");
    await expect(c.page.locator(".stage-tile.focused")).toHaveAttribute("data-key", firstKey);
    await c.page.locator(".stage-tile:not(.focused)").getByRole("button", { name: "Swap with the large picture" }).click();
    await expect(c.page.locator(".stage-tile.focused")).not.toHaveAttribute("data-key", firstKey);
    await c.page.getByRole("button", { name: /^Layout:/ }).click();
    await expect(stage).toHaveAttribute("data-layout", "grid");

    // Fullscreen with an overlay; PiP where the browser supports it.
    await c.page.getByRole("button", { name: "Fullscreen", exact: true }).click();
    await expect.poll(() => c.page.evaluate(() => document.fullscreenElement?.classList.contains("stage") || false)).toBe(true);
    await expect(c.page.getByRole("button", { name: "Exit fullscreen" })).toBeVisible();
    await c.page.getByRole("button", { name: "Exit fullscreen" }).click();
    await expect.poll(() => c.page.evaluate(() => !!document.fullscreenElement)).toBe(false);
    const pip = await c.page.evaluate(() => !!document.pictureInPictureEnabled);
    await expect(c.page.getByRole("button", { name: "Picture in picture" })).toHaveCount(pip ? 1 : 0);

    // Diagnostics list the codecs this phone decodes.
    await c.page.getByRole("button", { name: "Video settings" }).click();
    await c.page.getByRole("button", { name: "Video diagnostics" }).click();
    await expect(c.page.getByRole("dialog", { name: "Video diagnostics" })).toContainText("VP8");

    for (const phone of [a, b, c]) {
        phone.desktop.conn.close();
        await phone.context.close();
    }
});
