import { expect, test } from "@playwright/test";

import { ChatSession } from "../src/core/chat-session.js";
import { parseFileTokens } from "../src/core/files.js";
import { desktopSession } from "./harness.mjs";
import { linkPhone } from "./helpers.mjs";

const server = JSON.parse(process.env.NOXA_E2E_SERVER_STATE || "{}");
let info;

test.beforeAll(async () => {
    info = await (await fetch(`${server.webURL}/api/v1/web/info`)).json();
});

// A 1×1 PNG.
const PNG = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==", "base64");

test("the phone sends an encrypted attachment and opens it again", async ({ page }) => {
    page.on("pageerror", error => console.log("pageerror:", error.message));
    const desktop = await desktopSession(server.controlAddr, server.accounts.owner, { version: info.version });
    const deskChat = new ChatSession(desktop.conn, desktop.identity, desktop.auth);
    await linkPhone(page, desktop, server.webURL, expect);
    await page.getByRole("link", { name: "Servers", exact: true }).click();
    await page.getByRole("link", { name: "Server chat" }).click();

    const received = new Promise(resolve => deskChat.addEventListener("messages", () => {
        const found = (deskChat.messages.get(0) || []).find(m => /\[file:/.test(m.text || ""));
        if (found) resolve(found);
    }));
    await page.getByRole("textbox", { name: /Message/ }).fill("our logo");
    await page.locator("input[type=file]").setInputFiles({ name: "logo.png", mimeType: "image/png", buffer: PNG });
    const message = await received;
    const [ref] = parseFileTokens(message.text);
    expect(message.text.startsWith("our logo ")).toBe(true);
    expect(ref.name).toBe("logo.png");
    expect(ref.size).toBe(PNG.length);
    expect(ref.storage).toMatch(/^[0-9a-f]{32}\.vcx$/);

    // Tapping the chip downloads the ciphertext over /ft and decrypts it.
    await page.getByRole("button", { name: "Show image logo.png" }).click();
    const image = page.getByRole("img", { name: "logo.png" });
    await expect(image).toBeVisible();
    expect(await image.evaluate(img => img.naturalWidth)).toBe(1);
    deskChat.close();
});
