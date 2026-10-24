import { expect, test } from "@playwright/test";

import { fromBase64, openDM } from "../src/core/crypto.js";
import { downloadBlob, openAttachment, parseFileTokens } from "../src/core/files.js";
import { isVoiceNoteBody } from "../src/core/voice-notes.js";
import { contactDesktop, directFrom, openServer, phoneAccount, sendDM } from "./conversation-helpers.mjs";

const server = JSON.parse(process.env.NOXA_E2E_SERVER_STATE || "{}");
let info;

// Node's WebSocket sends no Origin; the web listener's /ft requires one.
const NativeWebSocket = globalThis.WebSocket;
globalThis.WebSocket = class extends NativeWebSocket {
    constructor(url) { super(url, { headers: { origin: server.webURL } }); }
};

test.beforeAll(async () => {
    info = await (await fetch(`${server.webURL}/api/v1/web/info`)).json();
    await openServer(server, info.version);
});

test("a voice message is recorded, previewed, sent in the desktop's format and played", async ({ page }) => {
    page.on("pageerror", error => console.log("pageerror:", error.message));
    const { desktop: xan } = await phoneAccount(page, server, server.accounts.xan, info.version, expect);
    const yara = await contactDesktop(server, server.accounts.yara, info.version);
    await page.evaluate(peer => { location.hash = `#/dm/${encodeURIComponent(peer)}`; }, yara.uniqueID);
    await expect(page.getByRole("textbox", { name: /Message/ })).toBeEnabled({ timeout: 15000 });

    // Record (Chromium's fake microphone), stop, preview.
    await page.getByRole("button", { name: "Record a voice message" }).click();
    const recorder = page.getByRole("dialog", { name: "Voice message" });
    await expect(recorder.getByRole("status")).toHaveText("Recording…");
    await expect(recorder.getByRole("meter", { name: "Microphone level" })).toBeVisible();
    await expect(recorder.locator(".voice-elapsed")).toContainText("0:02", { timeout: 6000 });
    await recorder.getByRole("button", { name: "Stop" }).click();
    await expect(recorder.getByRole("status")).toContainText("Listen before you send");
    await recorder.getByRole("button", { name: "Play", exact: true }).click();
    // A short clip may finish at once; the position shows that it played.
    await expect.poll(() => recorder.getByRole("slider", { name: "Playback position" }).inputValue()).not.toBe("0");

    // Sent: one sealed attachment token, voice-<uuid>.weba with duration and waveform.
    const received = directFrom(yara, xan.uniqueID);
    await recorder.getByRole("button", { name: "Send", exact: true }).click();
    const message = await received;
    const text = openDM(message.text, fromBase64(message.sender_device_key), yara.identity.xSecret);
    expect(isVoiceNoteBody(text)).toBe(true);
    const [ref] = parseFileTokens(text);
    expect(ref.name).toMatch(/^voice-[0-9a-f-]{36}\.weba$/);
    expect(ref.durationMs).toBeGreaterThan(500);
    expect(ref.peaks?.length).toBe(48);
    const audio = await openAttachment(await downloadBlob(yara.conn, 0, ref.storage, 6 << 20, `${server.webURL.replace(/^http/, "ws")}/ft`), ref.master);
    expect(audio.length).toBe(ref.size);
    expect(Buffer.from(audio.subarray(0, 4)).toString("hex")).toBe("1a45dfa3"); // WebM (EBML)

    // The desktop's copy of the token plays on the phone with speed control.
    await sendDM(yara, xan.uniqueID, text);
    const player = page.getByRole("log").getByRole("group", { name: /Voice message/ }).last();
    await player.getByRole("button", { name: "Playback speed 1x" }).click();
    await player.getByRole("button", { name: "Play", exact: true }).click();
    await expect.poll(() => player.getByRole("slider", { name: "Playback position" }).inputValue()).not.toBe("0");
    expect(await player.locator("audio").evaluate(element => element.playbackRate)).toBe(1.5);
    xan.conn.close();
    yara.conn.close();
});
