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
    for (const phone of [kai, lou]) {
        phone.desktop.conn.close();
        await phone.context.close();
    }
});
