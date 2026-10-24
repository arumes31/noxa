import { expect, test } from "@playwright/test";

import { ChatSession } from "../src/core/chat-session.js";
import { desktopSession } from "./harness.mjs";
import { linkPhone } from "./helpers.mjs";

const server = JSON.parse(process.env.NOXA_E2E_SERVER_STATE || "{}");
let info;

test.beforeAll(async () => {
    info = await (await fetch(`${server.webURL}/api/v1/web/info`)).json();
});

test("messages written offline wait in the outbox and send in order on reconnect", async ({ page }) => {
    page.on("pageerror", error => console.log("pageerror:", error.message));
    // Route the control socket so the test can cut and restore it.
    let offline = false;
    const sockets = new Set();
    await page.routeWebSocket(/\/ws$/, ws => {
        if (offline) {
            ws.close();
            return;
        }
        ws.connectToServer();
        sockets.add(ws);
    });
    const desktop = await desktopSession(server.controlAddr, server.accounts.owner, { version: info.version });
    const deskChat = new ChatSession(desktop.conn, desktop.identity, desktop.auth);
    await linkPhone(page, desktop, server.webURL, expect);
    await page.getByRole("link", { name: "Servers", exact: true }).click();
    await page.getByRole("link", { name: "Server chat" }).click();
    const composer = page.getByRole("textbox", { name: /Message/ });

    // Offline: the socket drops and the banner appears.
    offline = true;
    for (const ws of sockets) ws.close();
    sockets.clear();
    await expect(page.locator(".banner")).toBeVisible({ timeout: 20000 });
    for (const text of ["first while offline", "second while offline"]) {
        await composer.fill(text);
        await composer.press("Enter");
    }
    await expect(page.locator(".msg-state")).toHaveCount(2);
    await expect(page.locator(".banner")).toContainText("2 waiting to send");

    const received = [];
    const both = new Promise(resolve => deskChat.addEventListener("messages", () => {
        for (const m of deskChat.messages.get(0) || []) {
            if (/while offline/.test(m.text || "") && !received.includes(m.text)) received.push(m.text);
        }
        if (received.length === 2) resolve();
    }));
    offline = false;
    await page.evaluate(() => window.dispatchEvent(new Event("online")));
    await both;
    expect(received).toEqual(["first while offline", "second while offline"]);
    await expect(page.locator(".banner")).toHaveCount(0, { timeout: 20000 });
    await expect(page.locator(".msg-state")).toHaveCount(0);
    await expect(page.getByRole("log")).toContainText("second while offline");
    deskChat.close();
});
