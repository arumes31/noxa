import { expect, test } from "@playwright/test";

import { desktopSession } from "./harness.mjs";
import { linkPhone, openServerToMembers, publishKeyRecord, registerDesktop } from "./helpers.mjs";

const server = JSON.parse(process.env.NOXA_E2E_SERVER_STATE || "{}");
let info;

test.beforeAll(async () => {
    info = await (await fetch(`${server.webURL}/api/v1/web/info`)).json();
});

async function phoneOf(browser, account) {
    const context = await browser.newContext({ ...test.info().project.use, permissions: ["microphone", "camera"] });
    const page = await context.newPage();
    page.on("pageerror", error => console.log("pageerror:", error.message));
    page.on("console", message => { if (message.type() === "warning" || message.type() === "error") console.log("console:", message.text()); });
    const desktop = await desktopSession(server.controlAddr, account, { version: info.version });
    await registerDesktop(desktop);
    await publishKeyRecord(desktop);
    await linkPhone(page, desktop, server.webURL, expect);
    return { page, context, desktop };
}

test("a phone calls another phone, which answers, and they hang up", async ({ browser }) => {
    test.setTimeout(150000);
    const owner = await desktopSession(server.controlAddr, server.accounts.owner, { version: info.version });
    await openServerToMembers(owner);
    owner.conn.close();
    const kai = await phoneOf(browser, server.accounts.kai);
    const lou = await phoneOf(browser, server.accounts.lou);

    await kai.page.goto(`${server.webURL}/app/#/dm/${encodeURIComponent(lou.desktop.uniqueID)}`);
    await kai.page.getByRole("button", { name: /^Call / }).click();
    await expect(kai.page.getByText("Ringing…")).toBeVisible({ timeout: 20000 });

    // Lou's phone rings and answers.
    await expect(lou.page.getByText("Incoming call")).toBeVisible({ timeout: 20000 });
    await lou.page.getByRole("button", { name: "Accept", exact: true }).click();
    for (const phone of [kai, lou]) {
        await expect(phone.page.getByText("In call")).toBeVisible({ timeout: 20000 });
        await expect(phone.page.getByText("connected", { exact: true })).toBeVisible({ timeout: 20000 });
        // Media flows through the relay: the peer's audio plays.
        await expect.poll(() => phone.page.evaluate(() => [...document.querySelectorAll("audio")]
            .some(a => a.srcObject?.getAudioTracks().some(track => track.readyState === "live"))), { timeout: 30000 }).toBe(true);
        await expect(phone.page.getByText(/Data used: ↑ [0-9.]+ MB/)).toBeVisible({ timeout: 10000 });
    }

    await kai.page.getByRole("button", { name: "Hang up" }).click();
    await expect(lou.page.getByText("Call ended")).toBeVisible({ timeout: 20000 });

    // A second call: Lou asks to be reminded instead of answering.
    await kai.page.goto(`${server.webURL}/app/#/dm/${encodeURIComponent(lou.desktop.uniqueID)}`);
    await kai.page.getByRole("button", { name: /^Call / }).click();
    await expect(lou.page.getByText("Incoming call")).toBeVisible({ timeout: 20000 });
    await lou.page.getByRole("button", { name: "Remind me" }).click();
    await expect(kai.page.getByText("Call ended")).toBeVisible({ timeout: 20000 });
    await expect.poll(() => lou.page.evaluate(() => JSON.parse(localStorage.getItem("noxa.callReminders") || "[]").length)).toBe(1);
    const reminder = await lou.page.evaluate(() => JSON.parse(localStorage.getItem("noxa.callReminders"))[0]);
    expect(reminder.uid).toBe(kai.desktop.uniqueID);
    expect(reminder.at - Date.now()).toBeGreaterThan(9 * 60 * 1000);
    // Pretend ten minutes passed.
    await lou.page.evaluate(() => {
        const list = JSON.parse(localStorage.getItem("noxa.callReminders"));
        list[0].at = Date.now() - 1000;
        localStorage.setItem("noxa.callReminders", JSON.stringify(list));
    });
    await lou.page.goto(`${server.webURL}/app/#/home`);
    await lou.page.reload();
    await expect(lou.page.getByRole("group", { name: /^Call back / })).toBeVisible({ timeout: 15000 });
    await lou.page.getByRole("button", { name: "Dismiss" }).click();
    await expect(lou.page.getByRole("group", { name: /^Call back / })).toHaveCount(0);

    for (const phone of [kai, lou]) {
        phone.desktop.conn.close();
        await phone.context.close();
    }
});

const MsgCallRequest = 172;
const MsgCallResult = 173;

test("in a call: remote screen share, fit/fill, deafen, camera flip and call waiting", async ({ browser }) => {
    test.setTimeout(200000);
    const owner = await desktopSession(server.controlAddr, server.accounts.owner, { version: info.version });
    await openServerToMembers(owner);
    owner.conn.close();
    const kai = await phoneOf(browser, server.accounts.vic);
    const lou = await phoneOf(browser, server.accounts.wes);
    await kai.page.goto(`${server.webURL}/app/#/dm/${encodeURIComponent(lou.desktop.uniqueID)}`);
    // Kai's "screen" is a canvas: a fake display capture.
    await kai.page.evaluate(() => {
        navigator.mediaDevices.getDisplayMedia = async () => {
            const canvas = Object.assign(document.createElement("canvas"), { width: 640, height: 360 });
            const context = canvas.getContext("2d");
            setInterval(() => { context.fillStyle = `hsl(${(Date.now() / 10) % 360} 80% 50%)`; context.fillRect(0, 0, 640, 360); }, 50);
            return canvas.captureStream(15);
        };
    });
    await kai.page.getByRole("button", { name: /^Call / }).click();
    await expect(lou.page.getByText("Incoming call")).toBeVisible({ timeout: 20000 });
    await lou.page.getByRole("button", { name: "Accept", exact: true }).click();
    for (const phone of [kai, lou]) await expect(phone.page.getByText("connected", { exact: true })).toBeVisible({ timeout: 30000 });

    // Kai shares the screen; Lou sees it in its own tile.
    await kai.page.getByRole("button", { name: "Share screen" }).click();
    await expect(kai.page.getByRole("button", { name: "Stop sharing" })).toBeVisible();
    const screen = lou.page.locator(".screen-tile video");
    await expect.poll(() => screen.evaluate(video => video.videoWidth > 0).catch(() => false), { timeout: 30000 }).toBe(true);
    const tile = lou.page.locator(".screen-tile");
    await tile.getByRole("button", { name: "Switch between fit and fill" }).click();
    await expect(screen).toHaveClass(/fill/);
    await expect(tile.getByRole("button", { name: "Fullscreen" })).toBeVisible();
    const pip = await lou.page.evaluate(() => !!document.pictureInPictureEnabled);
    await expect(tile.getByRole("button", { name: "Picture in picture" })).toHaveCount(pip ? 1 : 0);
    await kai.page.getByRole("button", { name: "Stop sharing" }).click();
    await expect(lou.page.locator(".screen-tile")).toHaveCount(0, { timeout: 20000 });

    // Deafen silences the other side.
    await lou.page.getByRole("button", { name: "Silence call" }).click();
    await expect(lou.page.getByRole("button", { name: "Hear call" })).toHaveAttribute("aria-pressed", "true");
    expect(await lou.page.evaluate(() => [...document.querySelectorAll("audio")].filter(a => a.srcObject).every(a => a.muted))).toBe(true);
    await lou.page.getByRole("button", { name: "Hear call" }).click();

    // Camera on, then flip to the back camera.
    await kai.page.getByRole("button", { name: "Camera", exact: true }).click();
    await expect(kai.page.locator(".self-view")).toHaveAttribute("data-facing", "user", { timeout: 20000 });
    await kai.page.getByRole("button", { name: "Switch camera" }).click();
    await expect(kai.page.locator(".self-view")).toHaveAttribute("data-facing", "environment", { timeout: 20000 });
    await expect(kai.page.getByRole("button", { name: "Switch camera" })).toHaveAttribute("aria-pressed", "true");
    // The camera sheet explains the privacy blur and the server's limits.
    await kai.page.getByRole("button", { name: "Camera options" }).click();
    const sheet = kai.page.getByRole("dialog", { name: "Camera options" });
    await expect(sheet).toContainText("does not limit");
    await sheet.getByRole("combobox", { name: "Camera effect" }).selectOption("blur");
    await expect(sheet.getByText("Blur strength")).toBeVisible();
    await sheet.getByRole("button", { name: "Close" }).click();
    await expect(kai.page.locator(".self-view")).toHaveAttribute("data-facing", "environment");

    // Call waiting: Xan calls Wes, who is busy on this phone but free on
    // another session, so the call rings here as well.
    const louElsewhere = await desktopSession(server.controlAddr, server.accounts.wes, { version: info.version });
    const mia = await desktopSession(server.controlAddr, server.accounts.xan, { version: info.version });
    await registerDesktop(mia);
    await publishKeyRecord(mia);
    const ring = async () => (await mia.conn.request(MsgCallRequest, { action: "start", target: lou.desktop.uniqueID }, MsgCallResult, { accept: r => r.action === "start" })).call;
    const first = await ring();
    const banner = lou.page.getByRole("alertdialog", { name: "Call waiting" });
    await expect(banner).toContainText("is calling", { timeout: 20000 });
    await banner.getByRole("button", { name: "Decline" }).click();
    await expect(banner).toHaveCount(0);
    const declined = (await mia.conn.request(MsgCallRequest, { action: "get", id: first.id }, MsgCallResult, { accept: r => r.action === "get" })).call;
    expect(declined.ended_at).toBeTruthy();
    await expect(lou.page.getByText("In call")).toBeVisible();

    const second = await ring();
    await expect(banner).toBeVisible({ timeout: 20000 });
    await banner.getByRole("button", { name: "End and accept" }).click();
    await expect(kai.page.getByText("Call ended")).toBeVisible({ timeout: 20000 });
    await expect(lou.page).toHaveURL(new RegExp(`#/call/${second.id}$`), { timeout: 20000 });
    await expect(lou.page.getByText("In call")).toBeVisible({ timeout: 20000 });
    const answered = (await mia.conn.request(MsgCallRequest, { action: "get", id: second.id }, MsgCallResult, { accept: r => r.action === "get" })).call;
    expect(answered.participants.find(p => p.unique_id === lou.desktop.uniqueID).state).toBe("accepted");

    await lou.page.getByRole("button", { name: "Hang up" }).click();
    for (const session of [louElsewhere, mia]) session.conn.close();
    for (const phone of [kai, lou]) {
        phone.desktop.conn.close();
        await phone.context.close();
    }
});
