import { test, expect } from "@playwright/test";

test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
        window.__member = { client_id: "member", channel_id: 7, nickname: "Alex", unique_id: "alex", server_muted: false, server_deafened: false };
        window.__noxa = { state: { serverGeneration: 1, clients: [window.__member] }, renderTree() {} };
        window.__voiceCalls = [];
        window.go = { main: { App: { SetMemberVoice: async (change) => {
            window.__voiceCalls.push(change);
            if (window.__gate) await window.__gate;
            if (window.__fail) throw new Error("permission denied");
            return { revision: 1, client_id: change.client_id, channel_id: change.channel_id, muted: change.muted ?? false, deafened: change.deafened ?? false };
        } } } };
        window.__noxa.state.activeTabID = "server-a";
        window.__nativeTabID = "server-a";
        window.__tabCalls = [];
        const app = window.go.main.App;
        for (const name of ["SetMemberVoice"]) {
            const original = app[name];
            app[`${name}ForTab`] = async (tabID, request) => {
                window.__tabCalls.push({ name, tabID });
                if (tabID !== window.__nativeTabID) throw new Error("server tab changed; refresh");
                return original(request);
            };
            app[name] = () => { throw new Error("unscoped native call"); };
        }
    });
    await page.route("**/__voice_moderation__", (route) => route.fulfill({ contentType: "text/html", body: `<!doctype html><html lang="en"><head><meta name="viewport" content="width=device-width, initial-scale=1"><link rel="stylesheet" href="/src/style.css"></head><body><button id="launch">Voice controls</button><script type="module">
        import { openVoiceModeration } from '/src/voice-moderation-ui.js';
        import { initModalSystem } from '/src/modal.js';
        import { setLanguage } from '/src/i18n.js';
        initModalSystem(); window.setTestLanguage = setLanguage;
        document.querySelector('#launch').onclick = () => openVoiceModeration(window.__member);
        window.ready = true;
    </script></body></html>` }));
    await page.goto("/__voice_moderation__");
    await page.waitForFunction(() => window.ready);
});

test("native tab activation cannot redirect voice moderation before frontend reset", async ({ page }) => {
    await page.getByRole("button", { name: "Voice controls" }).click();
    await page.getByRole("checkbox", { name: "Mute on server", exact: true }).check();
    await page.evaluate(() => { window.__nativeTabID = "server-b"; });
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect(page.getByRole("alert")).toContainText("Could not save");
    expect(await page.evaluate(() => window.__voiceCalls)).toEqual([]);
    expect(await page.evaluate(() => window.__member.server_muted)).toBe(false);
    expect(await page.evaluate(() => window.__tabCalls)).toEqual([{ name: "SetMemberVoice", tabID: "server-a" }]);
});

test("server voice controls wait for acknowledgement and send only edited flags", async ({ page }) => {
    await page.getByRole("button", { name: "Voice controls" }).click();
    const mute = page.getByRole("checkbox", { name: "Mute on server", exact: true });
    await mute.focus();
    await page.keyboard.press("Space");
    await page.evaluate(() => { window.__gate = new Promise((resolve) => { window.__resolve = resolve; }); });
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect(page.getByRole("button", { name: "Save", exact: true })).toBeDisabled();
    expect(await page.evaluate(() => window.__member.server_muted)).toBe(false);
    await page.evaluate(() => window.__resolve());
    await expect(page.getByRole("status")).toHaveText("Voice controls saved.");
    expect(await page.evaluate(() => window.__voiceCalls)).toEqual([{ client_id: "member", channel_id: 7, muted: true }]);
    expect(await page.evaluate(() => window.__member.server_muted)).toBe(true);
});

test("denied moderation keeps the draft and leaves participant state unchanged", async ({ page }) => {
    await page.evaluate(() => { window.__fail = true; });
    await page.getByRole("button", { name: "Voice controls" }).click();
    await page.getByRole("checkbox", { name: "Deafen on server", exact: true }).check();
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect(page.getByRole("alert")).toContainText("Could not save");
    await expect(page.getByRole("checkbox", { name: "Deafen on server", exact: true })).toBeChecked();
    expect(await page.evaluate(() => window.__member.server_deafened)).toBe(false);
});

test("late moderation acknowledgement cannot update a replacement server", async ({ page }) => {
    await page.getByRole("button", { name: "Voice controls" }).click();
    await page.getByRole("checkbox", { name: "Mute on server", exact: true }).check();
    await page.evaluate(() => { window.__gate = new Promise((resolve) => { window.__resolve = resolve; }); });
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await page.evaluate(() => { window.__noxa.state.serverGeneration++; window.__resolve(); });
    expect(await page.evaluate(() => window.__member.server_muted)).toBe(false);
});

test("German voice moderation fits a compact viewport", async ({ page }) => {
    await page.setViewportSize({ width: 640, height: 480 });
    await page.evaluate(() => window.setTestLanguage("de"));
    await page.getByRole("button", { name: "Voice controls" }).click();
    await expect(page.getByRole("dialog")).toHaveAccessibleName("Sprachmoderation");
    await expect(page.getByRole("checkbox", { name: "Auf dem Server stummschalten", exact: true })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
});

test("late acknowledgement preserves a newer moderation snapshot", async ({ page }) => {
    await page.getByRole("button", { name: "Voice controls" }).click();
    await page.getByRole("checkbox", { name: "Mute on server", exact: true }).check();
    await page.evaluate(() => { window.__gate = new Promise((resolve) => { window.__resolve = resolve; }); });
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await page.evaluate(() => { window.__member.voice_revision = 2; window.__resolve(); });
    await expect(page.getByRole("status")).toHaveText("Voice controls saved.");
    await expect(page.getByRole("checkbox", { name: "Mute on server", exact: true })).not.toBeChecked();
    expect(await page.evaluate(() => window.__member.voice_revision)).toBe(2);
});
