import { expect, test } from "@playwright/test";

import { Msg } from "../src/core/frame.js";
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
    // Earlier specs link phones to the same accounts; stay under the device limit.
    const list = await desktop.conn.request(Msg.DeviceRequest, { action: "list" }, Msg.DeviceResult, { accept: r => r.action === "list" });
    for (const device of (list.devices || []).filter(d => !d.current && !d.first_device)) {
        await desktop.conn.request(Msg.DeviceRequest, { action: "revoke", device_id: device.device_id }, Msg.DeviceResult).catch(() => {});
    }
    await linkPhone(page, desktop, server.webURL, expect);
    // The passkey offer is a modal; the install banner has a "Not now" of its own.
    await page.getByRole("dialog").getByRole("button", { name: "Not now" }).click({ timeout: 15000 }).catch(() => {});
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

/** Keeps the latest tree snapshot and every event a desktop session received. */
function trackSnapshots(desktop) {
    desktop.snapshot = null;
    desktop.events = [];
    desktop.conn.on(Msg.Snapshot, snapshot => { desktop.snapshot = snapshot; });
    desktop.conn.on(Msg.Event, event => desktop.events.push(event));
}

function findChannel(snapshot, name) {
    const walk = nodes => {
        for (const node of nodes || []) {
            if (node.Name === name) return node;
            const found = walk(node.children);
            if (found) return found;
        }
        return null;
    };
    return walk(snapshot?.root_channels);
}

function clientIn(snapshot, uid) {
    const all = [];
    const walk = nodes => { for (const node of nodes || []) { all.push(...(node.clients || [])); walk(node.children); } };
    walk(snapshot?.root_channels);
    return all.find(c => c.unique_id === uid) || null;
}

const storedSettings = page => page.evaluate(() => JSON.parse(localStorage.getItem("noxa.settings") || "{}"));

test("voice settings, member volume, whisper, hands, push-to-talk delay, mic test and data saver", async ({ browser }) => {
    test.setTimeout(180000);
    const owner = await desktopSession(server.controlAddr, server.accounts.owner, { version: info.version });
    await openServerToMembers(owner);
    owner.conn.close();
    const rowan = await desktopSession(server.controlAddr, server.accounts.rowan, { version: info.version });
    trackSnapshots(rowan);
    const a = await phoneIn(browser, server.accounts.owner, "Public1");
    const page = a.page;

    // Rowan's desktop joins the phone's channel.
    await expect.poll(() => findChannel(rowan.snapshot, "Public1")?.ChannelID || 0, { timeout: 20000 }).toBeGreaterThan(0);
    const channelID = findChannel(rowan.snapshot, "Public1").ChannelID;
    await rowan.conn.request(Msg.JoinChannel, { ack_requested: true, channel_id: channelID }, 158);
    await expect(page.locator(".voice-tile")).toHaveCount(2, { timeout: 20000 });

    // Per-member sheet: volume and "mute for me", keyed by unique ID.
    const rowanName = rowan.auth.nickname;
    await page.getByRole("button", { name: `Options for ${rowanName}` }).click();
    const memberSheet = page.getByRole("dialog", { name: rowanName });
    await memberSheet.getByRole("slider", { name: "Volume" }).fill("150");
    await memberSheet.getByRole("switch", { name: /Mute for me/ }).check();
    await memberSheet.getByRole("button", { name: "Close" }).click();
    await expect(memberSheet).toHaveCount(0);
    await expect(page.getByLabel("Muted for you")).toBeVisible();
    const stored = await storedSettings(page);
    expect(stored.userVolumes[rowan.uniqueID]).toBe(1.5);
    expect(stored.mutedUsers).toContain(rowan.uniqueID);

    // Whisper to Rowan: the phone's WhisperSet reaches the server and Rowan is told.
    await page.getByRole("button", { name: "Choose whisper targets" }).click();
    const targets = page.getByRole("dialog", { name: "Whisper to" });
    await targets.getByRole("checkbox", { name: new RegExp(rowanName) }).check();
    await targets.getByRole("button", { name: "Save targets" }).click();
    await expect(page.getByRole("button", { name: "Whisper targets (1)" })).toBeVisible();
    const whisper = page.getByRole("button", { name: "Hold to whisper" });
    await whisper.dispatchEvent("pointerdown");
    await expect(page.getByText("You are whispering.")).toBeVisible({ timeout: 10000 });
    await expect.poll(() => rowan.events.some(e => e.type === "whisper" && e.data?.from_unique_id === a.desktop.uniqueID)
        || rowan.events.some(e => e.type === "speaking_changed" && e.data?.whisper), { timeout: 30000 }).toBe(true);
    await whisper.dispatchEvent("pointerup");
    await expect(page.getByText("You are whispering.")).toHaveCount(0);

    // Raise hand: the queue changes for everyone; the owner moderates it.
    await page.getByRole("button", { name: "Raise hand" }).click();
    await expect(page.getByRole("button", { name: "Lower hand" }).first()).toBeVisible({ timeout: 10000 });
    await expect.poll(() => clientIn(rowan.snapshot, a.desktop.uniqueID)?.hand_raised_order || 0, { timeout: 10000 }).toBeGreaterThan(0);
    const queue = page.getByRole("region", { name: "Speaking queue" });
    await expect(queue.getByRole("listitem")).toHaveCount(1);
    await queue.getByRole("button", { name: "Call next" }).click();
    await expect(queue.getByText("You're up: a moderator called on you.")).toBeVisible({ timeout: 10000 });
    await queue.getByRole("button", { name: "Clear queue" }).click();
    await expect(queue).toBeHidden({ timeout: 10000 });

    // Priority speaker is shown on the own tile.
    await page.getByRole("button", { name: "Priority speaker" }).click();
    await expect(page.getByRole("button", { name: "Priority speaker" })).toHaveAttribute("aria-pressed", "true", { timeout: 10000 });
    await expect(page.locator(".voice-tile.priority")).toHaveCount(1);

    // Settings sheet: microphone, processing, push-to-talk with a release delay.
    await page.getByRole("button", { name: "Voice settings" }).click();
    const settingsSheet = page.getByRole("dialog", { name: "Voice settings" });
    await expect(settingsSheet.getByRole("combobox", { name: "Microphone" }).locator("option").first()).toHaveText("Default");
    await settingsSheet.getByRole("switch", { name: "Echo cancellation" }).uncheck();
    // Noise suppression learns the fake microphone's steady beep as noise; the meter needs it raw.
    await settingsSheet.getByRole("switch", { name: "Noise suppression" }).uncheck();
    await settingsSheet.getByRole("radio", { name: "Push to talk" }).check();
    await settingsSheet.getByRole("slider", { name: "Release delay" }).fill("800");
    await settingsSheet.getByRole("button", { name: "Close" }).click();
    expect(await storedSettings(page)).toMatchObject({ echoCancellation: false, activation: "ptt", pttReleaseMs: 800 });
    const micState = page.locator(".vc-mic-state");
    await expect(micState).toHaveText("Microphone off");
    const talk = page.getByRole("button", { name: "Hold to talk" });
    await talk.dispatchEvent("pointerdown");
    await expect(micState).toHaveText("Microphone on");
    const released = Date.now();
    await talk.dispatchEvent("pointerup");
    await expect(micState).toHaveText("Microphone on");
    await expect(micState).toHaveText("Microphone off", { timeout: 5000 });
    expect(Date.now() - released).toBeGreaterThanOrEqual(700);

    // Microphone test: live level from the fake microphone, record and play back.
    await page.getByRole("button", { name: "Test microphone" }).click();
    const micTest = page.getByRole("dialog", { name: "Test microphone" });
    await expect(micTest.getByRole("meter", { name: "Microphone level" })).toBeVisible();
    await expect.poll(async () => Number(await micTest.getByRole("meter").getAttribute("aria-valuenow")), { timeout: 15000 }).toBeGreaterThan(0);
    await micTest.getByRole("button", { name: /^Record 5 s/ }).click();
    await expect(micTest.getByText(/Recording for 5 seconds/)).toBeVisible();
    await expect(micTest.locator("audio.vc-playback")).toHaveCount(1, { timeout: 15000 });
    await micTest.getByRole("button", { name: "Close" }).click();

    // Data saver: shown in the room and kept across a reload.
    await page.getByRole("button", { name: "Voice settings" }).click();
    await settingsSheet.getByRole("switch", { name: /Data saver/ }).check();
    await settingsSheet.getByRole("button", { name: "Close" }).click();
    await expect(page.getByText("Data saver is on.")).toBeVisible();
    await page.reload();
    await page.getByRole("dialog").getByRole("button", { name: "Not now" }).click({ timeout: 5000 }).catch(() => {});
    await page.getByRole("link", { name: "You", exact: true }).click();
    // Voice settings live in the You screen's collapsible "Voice" section.
    await page.locator('details[data-section="voice"] > summary').click();
    await expect(page.getByRole("switch", { name: /Data saver/ })).toBeChecked();
    await expect(page.getByRole("switch", { name: "Echo cancellation" })).not.toBeChecked();

    rowan.conn.close();
    a.desktop.conn.close();
    await a.context.close();
});
