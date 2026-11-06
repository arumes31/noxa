import { createRequire } from "node:module";
import { expect, test } from "./fixtures.js";

// qrcode-generator (a dev dependency, as in the mobile app) draws real codes
// for the jsQR path.
const require = createRequire(import.meta.url);
const qrcode = require("qrcode-generator");
function modules(text) {
    const code = qrcode(0, "M");
    code.addData(text, "Byte");
    code.make();
    const size = code.getModuleCount();
    return Array.from({ length: size }, (_, row) => Array.from({ length: size }, (_, col) => code.isDark(row, col)));
}

const DIGEST = "5a".repeat(32);
const PNG = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=";
const code = (from, to, digest = DIGEST, scheme = "o") => `noxa-sn:v1:${scheme}:${from}:${to}:${digest}`;

// camera: "detector" (fake BarcodeDetector reading window.__codes),
// "pixels" (no BarcodeDetector: a canvas stream jsQR must decode) or "denied".
async function open(page, camera) {
    await page.addInitScript(({ png, digest, camera }) => {
        window.__saved = [];
        window.__tracksStopped = 0;
        window.__codes = [];
        window.runtime = { EventsOn: () => () => {}, EventsEmit() {}, WindowIsFullscreen: async () => false };
        const settings = { language: "en", onboarding_done: true, alpha_dismissed: "test", bookmarks: [], chat_max_lines: 200 };
        window.go = { main: { App: new Proxy({}, { get(_target, method) { return async (...args) => {
            if (method === "GetSettings") return structuredClone(settings);
            if (method === "SaveSettings") { window.__saved.push(structuredClone(args[0])); return ""; }
            if (["ListTabs", "GetPermissions", "SubscriptionsForTab", "DMHistoryLoadForContext"].includes(method)) return [];
            if (["IdentityInfo", "GetAvatar"].includes(method)) return {};
            if (["Connected", "IsGuest"].includes(method)) return false;
            if (["ClientVersion", "ClientVersionShort"].includes(method)) return "test";
            if (method === "ConversationForTab") return { conversations: [] };
            if (method === "DiscussionForTab") return { threads: [], forum: false };
            if (method === "DMHistoryContextForTab") return { tab_id: "a", identity_uid: "me", activation: "0", identity_revision: "0" };
            if (method === "E2EEDiagnosticsForTab" || method === "E2EEDiagnostics") {
                return { active: true, cipher: "Olm", peer_unique_id: "bob", peer_key_available: true,
                    safety_number: "11111 22222 33333 44444 55555 66666 77777 88888 99999 00000 11111 22222",
                    safety_digest: digest, own_unique_id: "me", safety_qr: png };
            }
            return "";
        }; } }) } };
        const canvas = document.createElement("canvas");
        canvas.width = canvas.height = 320;
        window.__drawCode = (rows) => {
            const ctx = canvas.getContext("2d");
            ctx.fillStyle = "#fff"; ctx.fillRect(0, 0, 320, 320);
            if (!rows) return;
            const scale = Math.floor(280 / rows.length);
            ctx.fillStyle = "#000";
            rows.forEach((row, y) => row.forEach((dark, x) => { if (dark) ctx.fillRect(20 + x * scale, 20 + y * scale, scale, scale); }));
        };
        window.__drawCode(null);
        const getUserMedia = async constraints => {
            window.__constraints = constraints;
            if (camera === "denied") throw new DOMException("Permission denied", "NotAllowedError");
            const stream = canvas.captureStream(10);
            setInterval(() => { const ctx = canvas.getContext("2d"); ctx.fillRect(0, 0, 1, 1); }, 50);
            for (const track of stream.getTracks()) {
                const stop = track.stop.bind(track);
                track.stop = () => { window.__tracksStopped++; stop(); };
            }
            return stream;
        };
        Object.defineProperty(navigator, "mediaDevices", { configurable: true, value: { enumerateDevices: async () => [], addEventListener() {}, getUserMedia } });
        if (camera === "detector") {
            window.BarcodeDetector = class {
                static async getSupportedFormats() { return ["qr_code"]; }
                async detect() { const next = window.__codes.shift(); return next ? [{ rawValue: next }] : []; }
            };
        } else {
            delete window.BarcodeDetector;
        }
    }, { png: PNG, digest: DIGEST, camera });
    await page.goto("/"); await page.waitForFunction(() => !!window.__noxaChat);
    await page.evaluate(() => {
        const v = window.__noxa; v.showWorkspace(false);
        Object.assign(v.state, { activeTabID: "a", myChannelID: 0, myUniqueID: "me", channels: [], clients: [] });
        window.__noxaChat.openPM("bob", "Bob");
    });
    // The diagnostics button lives in a menu; open it directly.
    await page.evaluate(() => document.getElementById("chat-e2ee-btn").click());
    const dialog = page.locator(".e2ee-diagnostics");
    await expect(dialog.locator(".safety-qr-image")).toHaveAttribute("src", PNG);
    await expect(dialog.locator(".safety-qr-image")).toHaveAttribute("alt", "QR code of your safety number with this contact");
    return dialog;
}

test("scanning the contact's matching code verifies them; other codes keep scanning", async ({ page }) => {
    const dialog = await open(page, "detector");
    await dialog.getByRole("button", { name: "Scan their code" }).click();
    const scanner = page.getByRole("dialog", { name: "Scan your contact's safety code" });
    await expect(scanner).toBeVisible();
    expect(await page.evaluate(() => window.__constraints)).toEqual({ video: { facingMode: { ideal: "environment" } }, audio: false });
    await page.evaluate(code => window.__codes.push(code), "https://example.com/not-a-code");
    await expect(scanner.getByRole("status")).toHaveText("This is not a noXa safety code.");
    await page.evaluate(code => window.__codes.push(code), code("carol", "me"));
    await expect(scanner.getByRole("status")).toHaveText(/different conversation/);
    await page.evaluate(code => window.__codes.push(code), code("bob", "me", DIGEST, "f"));
    await expect(scanner.getByRole("status")).toHaveText(/mobile app/);
    expect(await page.evaluate(() => window.__saved.length)).toBe(0);
    await page.evaluate(code => window.__codes.push(code), code("bob", "me"));
    await expect(scanner).toHaveCount(0);
    await expect(dialog).toHaveCount(0);
    await expect.poll(() => page.evaluate(() => window.__saved.at(-1)?.e2ee_verified?.bob)).toBe("11111 22222 33333 44444 55555 66666 77777 88888 99999 00000 11111 22222");
    expect(await page.evaluate(() => window.__tracksStopped)).toBeGreaterThan(0);
});

test("a mismatching code decoded from camera pixels warns and never verifies", async ({ page }) => {
    const dialog = await open(page, "pixels");
    await dialog.getByRole("button", { name: "Scan their code" }).click();
    const scanner = page.getByRole("dialog", { name: "Scan your contact's safety code" });
    await expect(scanner).toBeVisible();
    await page.evaluate(rows => window.__drawCode(rows), modules(code("bob", "me", "c3".repeat(32))));
    await expect(scanner).toHaveCount(0, { timeout: 10000 });
    await expect(dialog.locator(".safety-scan-result")).toHaveText(/do not match/);
    await expect(dialog.locator(".safety-scan-result")).toHaveClass(/warn/);
    expect(await page.evaluate(() => window.__saved.length)).toBe(0);
    expect(await page.evaluate(() => window.__tracksStopped)).toBeGreaterThan(0);
    // Manual verification stays available.
    await expect(dialog.getByRole("button", { name: "Mark verified" })).toBeVisible();
});

test("a denied camera explains the fallback and closing stops scanning", async ({ page }) => {
    const dialog = await open(page, "denied");
    const scan = dialog.getByRole("button", { name: "Scan their code" });
    await scan.click();
    const scanner = page.getByRole("dialog", { name: "Scan your contact's safety code" });
    await expect(scanner.getByRole("status")).toHaveText(/camera is unavailable or access was denied/i);
    await expect(scanner.locator("video")).toBeHidden();
    await page.keyboard.press("Escape");
    await expect(scanner).toHaveCount(0);
    await expect(scan).toBeFocused();
    await expect(dialog.getByRole("button", { name: "Mark verified" })).toBeVisible();
    expect(await page.evaluate(() => window.__saved.length)).toBe(0);
});
