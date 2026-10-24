import { readFile } from "node:fs/promises";

import { expect, test } from "@playwright/test";
import { sha256 } from "@noble/hashes/sha2.js";

import { ChatSession } from "../src/core/chat-session.js";
import { uploadStream } from "../src/core/files.js";
import { Msg } from "../src/core/frame.js";
import { desktopSession } from "./harness.mjs";
import { linkPhone } from "./helpers.mjs";
import { releaseDevices } from "./device-cleanup.mjs";

// The desktop session of the running test; released after it, pass or fail.
let desktop = null;
test.afterEach(async () => {
    if (desktop) await releaseDevices(desktop);
    desktop = null;
});

const server = JSON.parse(process.env.NOXA_E2E_SERVER_STATE || "{}");
let info;

/** The passkey offer after linking hides the screen; decline it. */
async function declinePasskey(page) {
    await page.getByRole("dialog").getByRole("button", { name: "Not now" }).click({ timeout: 5000 }).catch(() => {});
}

test.beforeAll(async () => {
    info = await (await fetch(`${server.webURL}/api/v1/web/info`)).json();
});

const hex = bytes => Buffer.from(sha256(bytes)).toString("hex");
const ftURL = () => `${server.webURL.replace(/^http/, "ws")}/ft`;

/**
 * Uploads like a client on the web port. /ft checks the Origin header, which
 * Node's WebSocket only sends when given explicitly.
 */
async function desktopUpload(desktop, location, name, bytes) {
    const NodeWebSocket = globalThis.WebSocket;
    globalThis.WebSocket = class extends NodeWebSocket {
        constructor(url) { super(url, { headers: { Origin: server.webURL } }); }
    };
    try {
        await uploadStream(desktop.conn, location, name, new Blob([bytes]), { url: ftURL() });
    } finally {
        globalThis.WebSocket = NodeWebSocket;
    }
}

async function listing(desktop, channelID, folder = "") {
    const result = await desktop.conn.request(Msg.FileList, { channel_id: channelID, folder }, Msg.FileListResponse);
    return { names: (result.entries || []).map(e => e.name).sort(), folders: result.folders || [], entries: result.entries || [] };
}

async function downloaded(download) {
    return (await readFile(await download.path())).toString("utf8");
}

test("the phone browses, uploads, checks, renames, moves and deletes channel files", async ({ page }) => {
    test.setTimeout(120000);
    page.on("pageerror", error => console.log("pageerror:", error.message));
    desktop = await desktopSession(server.controlAddr, server.accounts.owner, { version: info.version });
    const deskChat = new ChatSession(desktop.conn, desktop.identity, desktop.auth);
    await new Promise(resolve => deskChat.snapshot ? resolve() : deskChat.addEventListener("snapshot", resolve, { once: true }));
    const chats = deskChat.channels().filter(c => c.chat);
    const channel = chats.find(c => c.name === "Public1") || chats[0];
    const other = chats.find(c => c.id !== channel.id);

    // The desktop uploads a file twice: the second upload keeps the first as an old version.
    const first = new TextEncoder().encode("first draft\n");
    const second = new TextEncoder().encode("final text, version two\n");
    await desktopUpload(desktop, { channelID: channel.id, folder: "" }, "notes.txt", first);
    await desktopUpload(desktop, { channelID: channel.id, folder: "" }, "notes.txt", second);

    await linkPhone(page, desktop, server.webURL, expect);
    await declinePasskey(page);
    await page.getByRole("link", { name: "Servers", exact: true }).click();
    await page.locator(`[data-files-link="${channel.id}"]`).click();
    await expect(page.getByRole("heading", { name: /Files/ })).toBeVisible();
    const notes = page.getByRole("button", { name: "Actions for notes.txt", exact: true });
    await expect(notes).toBeVisible();
    await expect(page.locator(".files-quota")).toContainText("used");

    // Filter and sort.
    await page.getByRole("searchbox", { name: "Filter files" }).fill("zzz");
    await expect(page.getByText("No files match the filter.")).toBeVisible();
    await page.getByRole("searchbox", { name: "Filter files" }).fill("");
    await expect(notes).toBeVisible();

    // Upload two files from the phone through the queue.
    await page.locator("input[data-files-upload]").setInputFiles([
        { name: "phone-a.txt", mimeType: "text/plain", buffer: Buffer.from("from the phone A") },
        { name: "phone-b.txt", mimeType: "text/plain", buffer: Buffer.from("from the phone B, a little longer") },
    ]);
    await expect(page.locator(".upload-panel")).toContainText("2 of 2 uploaded", { timeout: 20000 });
    await expect(page.getByRole("button", { name: "Actions for phone-b.txt", exact: true })).toBeVisible();
    expect((await listing(desktop, channel.id)).names).toEqual(["notes.txt", "notes.txt.v1", "phone-a.txt", "phone-b.txt"]);
    await page.getByRole("button", { name: "Clear finished" }).click();
    await expect(page.locator(".upload-panel")).toHaveCount(0);

    await page.getByRole("combobox", { name: "Sort by" }).selectOption("size");
    await expect(page.locator(".files-file .row-title")).toHaveText(["notes.txt.v1", "phone-a.txt", "notes.txt", "phone-b.txt"]);

    // Checksum: shown, and verified by downloading and hashing.
    await notes.click();
    await page.getByRole("dialog").getByRole("button", { name: "Checksum" }).click();
    await expect(page.locator("[data-checksum]")).toHaveText(hex(second));
    await page.getByRole("button", { name: "Verify" }).click();
    await expect(page.getByText("Checksum matches. The file is intact.")).toBeVisible();
    await page.getByRole("dialog").getByRole("button", { name: "Close" }).click();

    // Like the desktop, the listing shows rotated old versions as files too.
    await expect(page.getByRole("button", { name: "Actions for notes.txt.v1", exact: true })).toBeVisible();

    // Download the current file.
    await notes.click();
    let pending = page.waitForEvent("download");
    await page.getByRole("dialog").getByRole("button", { name: "Download" }).click();
    let download = await pending;
    expect(download.suggestedFilename()).toBe("notes.txt");
    expect(await downloaded(download)).toBe("final text, version two\n");

    // The older version downloads by its own name.
    await notes.click();
    await page.getByRole("dialog").getByRole("button", { name: "Versions" }).click();
    const versionButton = page.getByRole("button", { name: /^Download version / });
    await expect(versionButton).toHaveCount(1);
    pending = page.waitForEvent("download");
    await versionButton.click();
    download = await pending;
    expect(await downloaded(download)).toBe("first draft\n");
    await page.getByRole("dialog").getByRole("button", { name: "Close" }).click();

    // A temporary link needs HTTPS on the server; either a /dl/ link or the server's reason shows.
    await notes.click();
    await page.getByRole("dialog").getByRole("button", { name: "Temporary link" }).click();
    const link = page.locator("[data-file-link]");
    const refused = page.locator(".toast", { hasText: "No link:" });
    await expect(link.or(refused)).toBeVisible();
    if (await link.count()) {
        await expect(link).toHaveValue(/^https:\/\/[^/]+\/dl\/[0-9a-f]{32}$/);
        await page.getByRole("dialog").getByRole("button", { name: "Close" }).click();
    }

    // Rename into a folder.
    await page.getByRole("button", { name: "Actions for phone-a.txt", exact: true }).click();
    await page.getByRole("dialog").getByRole("button", { name: "Rename" }).click();
    await page.getByLabel("New name (folder/name moves it)").fill("docs/renamed.txt");
    await page.getByRole("button", { name: "Save" }).click();
    await expect(page.getByRole("button", { name: "Folder docs" })).toBeVisible();
    expect((await listing(desktop, channel.id, "docs")).names).toEqual(["renamed.txt"]);

    // Move it from the folder to another channel's top level.
    await page.getByRole("button", { name: "Folder docs" }).click();
    await page.getByRole("button", { name: "Actions for renamed.txt", exact: true }).click();
    await page.getByRole("dialog").getByRole("button", { name: "Move" }).click();
    await page.getByLabel("Channel", { exact: true }).selectOption(String(other.id));
    await page.getByLabel("Folder (empty = top level)").fill("");
    await page.getByRole("dialog").getByRole("button", { name: "Move" }).click();
    await expect(page.locator(".toast", { hasText: "renamed.txt moved to" })).toBeVisible();
    await expect.poll(async () => (await listing(desktop, other.id)).names).toContain("renamed.txt");
    expect((await listing(desktop, channel.id, "docs")).names).toEqual([]);

    // Delete after confirming.
    await page.getByRole("button", { name: "All files" }).click();
    await page.getByRole("button", { name: "Actions for phone-b.txt", exact: true }).click();
    await page.getByRole("dialog").getByRole("button", { name: "Delete" }).click();
    await page.getByRole("dialog", { name: "Delete phone-b.txt?" }).getByRole("button", { name: "Delete" }).click();
    await expect(page.getByRole("button", { name: "Actions for phone-b.txt", exact: true })).toHaveCount(0);
    expect((await listing(desktop, channel.id)).names).toEqual(["notes.txt", "notes.txt.v1"]);

    // The chat header opens the same browser.
    await page.goto(`${server.webURL}/app/#/chat/${channel.id}`);
    await page.locator(".screen-header").getByRole("link", { name: "Files", exact: true }).click();
    await expect(notes).toBeVisible();
    deskChat.close();
});
