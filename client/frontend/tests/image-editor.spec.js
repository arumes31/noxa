import { test, expect } from "./fixtures.js";

test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
        window.__events = {};
        window.__saved = { language: "en", activation_mode: "ptt", hotkey_ptt: "F5", volume: 100, user_volumes: {}, bookmarks: [], onboarding_done: true, alpha_dismissed: "0.5.0-dev+gabc123", chat_max_lines: 200, window_opacity: 100 };
        window.runtime = {
            EventsOn: (name, fn) => { (window.__events[name] ||= []).push(fn); return () => {}; }, EventsEmit() {},
            WindowIsFullscreen: async () => false,
            ClipboardSetText: async value => { window.__copied = value; return !window.__clipboardFail; },
        };
        window.go = { main: { App: new Proxy({}, { get(_target, method) {
            return async (...args) => {
                if (method === "GetSettings") return structuredClone(window.__saved);
                if (method === "Quit") { window.__quit = true; return; }
                if (method === "SetNotificationSnooze") {
                    if (window.__saveFail) throw new Error("save failed");
                    const until = args[0] ? Date.now() + args[0] * 60000 : 0;
                    window.__saved.notification_snooze_until = until;
                    return until;
                }
                if (method === "SaveSettings") {
                    if (window.__saveFail) return "save failed";
                    window.__saved = structuredClone(args[0]); return "";
                }
                if (["ListTabs", "GetPermissions", "SubscriptionsForTab", "DMHistoryLoadForContext"].includes(method)) return [];
                if (["IdentityInfo", "GetAvatar"].includes(method)) return {};
                if (method === "DMHistoryContextForTab") return { tab_id: "one", identity_uid: "me", activation: "0", identity_revision: "0" };
                if (method === "Connected" || method === "IsGuest") return false;
                if (method === "ClientVersion" || method === "ClientVersionShort") return "0.5.0-dev+gabc123";
                if (method === "ServerInfoForTab") return { name: "Test server", version: "0.5.0+gdef456", chat_max_bytes: 100 };
                if (method === "SystemCPUPercent") return 20;
                return "";
            };
        } }) } };
        Object.defineProperty(navigator, "mediaDevices", { configurable: true, value: { enumerateDevices: async () => [], addEventListener() {} } });
    });
    await page.goto("/");
    await page.waitForFunction(() => !!window.__noxa?.openSettings);
    await page.evaluate(() => {
        const v = window.__noxa;
        v.showWorkspace(false);
        Object.assign(v.state, { activeTabID: "one", myClientID: "self", myUniqueID: "me", myChannelID: 1,
            selectedClientID: "alice", channels: [{ ChannelID: 1, Name: "Lobby" }, { ChannelID: 2, Name: "Team" }],
            clients: [{ client_id: "self", unique_id: "me", nickname: "Me", channel_id: 1 }, { client_id: "alice", unique_id: "alice-uid", nickname: "Alice", channel_id: 1 }] });
        v.renderTree();
    });
});

// Stages a 320x200 PNG with a horizontal gradient (neighbouring pixels differ).
async function stageGradient(page, name = "shot.png", stripes = false) {
    await page.evaluate(async ([fileName, striped]) => {
        const canvas = new OffscreenCanvas(320, 200);
        const ctx = canvas.getContext("2d");
        for (let x = 0; x < 320; x++) {
            // Stripes: 1-pixel black and white columns, like small text.
            ctx.fillStyle = striped ? (x % 2 ? "#ffffff" : "#000000") : "rgb(" + (x % 256) + "," + ((x * 3) % 256) + ",90)";
            ctx.fillRect(x, 0, 1, 200);
        }
        const png = await canvas.convertToBlob({ type: "image/png" });
        const data = new DataTransfer();
        data.items.add(new File([png], fileName, { type: "image/png" }));
        document.getElementById("chat-text").dispatchEvent(new ClipboardEvent("paste", { clipboardData: data, bubbles: true, cancelable: true }));
    }, [name, stripes]);
    await expect(page.locator(".file-preview img")).toHaveCount(1);
}

// Decodes the staged image and returns its size and a few RGBA samples.
function stagedImage(page, points = []) {
    return page.evaluate(async samples => {
        const img = document.querySelector(".file-preview img");
        const bitmap = await createImageBitmap(await (await fetch(img.src)).blob());
        const canvas = new OffscreenCanvas(bitmap.width, bitmap.height);
        const ctx = canvas.getContext("2d");
        ctx.drawImage(bitmap, 0, 0);
        return {
            width: bitmap.width, height: bitmap.height, src: img.src, alt: img.alt,
            pixels: samples.map(([x, y]) => [...ctx.getImageData(x, y, 1, 1).data]),
        };
    }, points);
}

async function drag(page, from, to) {
    const box = await page.locator(".ie-canvas").boundingBox();
    const at = p => [box.x + box.width * p[0], box.y + box.height * p[1]];
    await page.mouse.move(...at(from));
    await page.mouse.down();
    await page.mouse.move(...at([(from[0] + to[0]) / 2, (from[1] + to[1]) / 2]), { steps: 3 });
    await page.mouse.move(...at(to), { steps: 3 });
    await page.mouse.up();
}

test("a staged image opens in the editor with labelled tools", async ({ page }) => {
    await stageGradient(page);
    const edit = page.locator(".file-preview-edit");
    await expect(edit).toHaveAttribute("title", /^Edit shot/);
    await edit.click();
    const dialog = page.locator(".image-editor");
    await expect(dialog).toBeVisible();
    for (const name of ["Crop", "Pixelate (redact)", "Pen", "Arrow", "Rectangle", "Text", "Rotate 90°", "Undo (Ctrl+Z)", "Redo (Ctrl+Y)"]) {
        await expect(dialog.getByRole("button", { name, exact: true })).toHaveAttribute("title", name);
    }
    await expect(dialog.getByRole("button", { name: "Pen", exact: true })).toHaveAttribute("aria-pressed", "true");
    await expect(dialog.getByRole("button", { name: "Undo (Ctrl+Z)" })).toBeDisabled();
    // Nothing edited: Done closes without replacing the staged file.
    const before = await stagedImage(page);
    await dialog.getByRole("button", { name: "Done", exact: true }).click();
    await expect(dialog).toHaveCount(0);
    expect((await stagedImage(page)).src).toBe(before.src);
});

test("pixelating a region replaces those pixels in the staged file", async ({ page }) => {
    await stageGradient(page, "shot.png", true);
    const contrast = (a, b) => Math.abs(a[0] - b[0]);
    const before = await stagedImage(page, [[20, 20], [21, 20]]);
    expect(contrast(before.pixels[0], before.pixels[1])).toBeGreaterThan(150);
    await page.locator(".file-preview-edit").click();
    await page.getByRole("button", { name: "Pixelate (redact)", exact: true }).click();
    await drag(page, [0.02, 0.02], [0.4, 0.4]);
    await page.getByRole("button", { name: "Done", exact: true }).click();
    await expect(page.locator(".image-editor")).toHaveCount(0);
    const after = await stagedImage(page, [[20, 20], [21, 20], [300, 180], [301, 180]]);
    expect([after.width, after.height]).toEqual([320, 200]);
    // Inside: the stripes became one grey block (the export may be lossy).
    expect(contrast(after.pixels[0], after.pixels[1])).toBeLessThan(25);
    expect(after.pixels[0][0]).toBeGreaterThan(90);
    expect(after.pixels[0][0]).toBeLessThan(170);
    // Outside: the stripes are still there.
    expect(contrast(after.pixels[2], after.pixels[3])).toBeGreaterThan(60);
    expect(after.alt).toMatch(/^shot.*\.(png|webp)$/);
});

test("rotate and keyboard crop change the exported size; undo and redo work", async ({ page }) => {
    await stageGradient(page);
    await page.locator(".file-preview-edit").click();
    const dialog = page.locator(".image-editor");
    await dialog.getByRole("button", { name: "Rotate 90°", exact: true }).click();
    await dialog.getByRole("button", { name: "Rotate 90°", exact: true }).click();
    await page.keyboard.press("Control+z");
    await expect(dialog.getByRole("button", { name: "Redo (Ctrl+Y)" })).toBeEnabled();
    await page.keyboard.press("Control+y");
    await page.keyboard.press("Control+z"); // one rotation left: 200x320
    await dialog.getByRole("button", { name: "Crop", exact: true }).click();
    const box = dialog.locator(".ie-crop-box");
    await expect(box).toBeFocused();
    // The initial crop keeps 90% of each side; shrink the width by 40 steps of 1%.
    for (let i = 0; i < 40; i++) await page.keyboard.press("Shift+ArrowLeft");
    await page.keyboard.press("Enter");
    await expect(box).toBeHidden();
    await dialog.getByRole("button", { name: "Done", exact: true }).click();
    await expect(dialog).toHaveCount(0);
    const out = await stagedImage(page);
    expect(out.width).toBe(100); // 200 * 0.5
    expect(out.height).toBe(288); // 320 * 0.9
});

test("annotations are drawn into the exported pixels", async ({ page }) => {
    await stageGradient(page);
    await page.locator(".file-preview-edit").click();
    const dialog = page.locator(".image-editor");
    await dialog.locator(".ie-color").evaluate(input => { input.value = "#00ff00"; input.dispatchEvent(new Event("input", { bubbles: true })); });
    await dialog.getByRole("button", { name: "Rectangle", exact: true }).click();
    await drag(page, [0.25, 0.25], [0.75, 0.75]);
    await dialog.getByRole("button", { name: "Arrow", exact: true }).click();
    await drag(page, [0.1, 0.9], [0.4, 0.6]);
    await dialog.getByRole("button", { name: "Text", exact: true }).click();
    await drag(page, [0.6, 0.1], [0.6, 0.1]);
    const prompt = page.getByRole("dialog").filter({ hasText: "Add text" });
    await prompt.getByRole("textbox").fill("look here");
    await prompt.getByRole("button", { name: "OK" }).click();
    await expect(dialog.getByRole("button", { name: "Undo (Ctrl+Z)" })).toBeEnabled();
    await dialog.getByRole("button", { name: "Done", exact: true }).click();
    await expect(dialog).toHaveCount(0);
    const out = await stagedImage(page, [[80, 100]]);
    const [r, g, b] = out.pixels[0];
    expect(g).toBeGreaterThan(200); // the green rectangle's left edge
    expect(r).toBeLessThan(80);
    expect(b).toBeLessThan(80);
});

test("cancel leaves the staged file untouched, asking first when edited", async ({ page }) => {
    await stageGradient(page);
    const before = await stagedImage(page);
    await page.locator(".file-preview-edit").click();
    await expect(page.locator(".image-editor")).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(page.locator(".image-editor")).toHaveCount(0);
    await page.locator(".file-preview-edit").click();
    await drag(page, [0.1, 0.1], [0.9, 0.9]); // pen stroke
    await page.keyboard.press("Escape");
    const confirm = page.getByRole("dialog").filter({ hasText: "Discard changes?" });
    await expect(confirm).toBeVisible();
    await confirm.getByRole("button", { name: "Cancel" }).click();
    await expect(page.locator(".image-editor")).toBeVisible();
    await page.keyboard.press("Escape");
    await page.getByRole("dialog").filter({ hasText: "Discard changes?" }).getByRole("button", { name: "Discard" }).click();
    await expect(page.locator(".image-editor")).toHaveCount(0);
    expect((await stagedImage(page)).src).toBe(before.src);
});
