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

test("explicit menu quit calls the application quit path", async ({ page }) => {
    await page.getByRole("menuitem", { name: "Connections", exact: true }).click();
    await page.getByRole("menuitem", { name: "Quit", exact: true }).click();
    await expect.poll(() => page.evaluate(() => window.__quit)).toBe(true);
});

test("contacts can block, edit, and unblock without reopening", async ({ page }) => {
    await page.evaluate(async () => {
        window.__saved.contacts = [{ unique_id: "alice-uid", label: "Alice" }];
        window.__noxa.state.settings = structuredClone(window.__saved);
        window.__noxaSocial.openContacts();
    });
    await page.locator(".ct-block").click();
    await expect(page.locator(".ct-block")).toHaveAttribute("title", "unblock");
    await page.getByTitle("notify when online: off", { exact: true }).click();
    expect(await page.evaluate(() => window.__saved.blocked_users)).toEqual(["alice-uid"]);
    await page.locator(".ct-block").click();
    await expect(page.locator(".ct-block")).toHaveAttribute("title", "block (hide chat + mute voice)");
    expect(await page.evaluate(() => window.__saved.blocked_users)).toEqual([]);
    await page.evaluate(() => { window.__saveFail = true; });
    await page.locator(".ct-block").click();
    await expect(page.locator(".ct-block")).toHaveAttribute("title", "block (hide chat + mute voice)");
    expect(await page.evaluate(() => window.__saved.blocked_users)).toEqual([]);
});

test("contact and note failures preserve committed state and allow retry", async ({ page }) => {
    await page.evaluate(() => {
        window.__saved.contacts = [{ unique_id: "alice-uid", label: "Alice", notify_online: false }];
        window.__saved.user_notes = { "alice-uid": "original" };
        window.__noxa.state.settings = structuredClone(window.__saved);
        window.__saveFail = true;
        window.__noxaSocial.openContacts();
    });
    await page.getByTitle("notify when online: off", { exact: true }).click();
    await expect(page.locator("#toasts")).toContainText("save failed");
    await expect(page.getByTitle("notify when online: off", { exact: true })).toBeVisible();
    await page.locator(".ct-del").click();
    await expect(page.locator(".ct-name")).toContainText("Alice");
    await page.locator(".ct-uid").fill("bob-uid");
    await page.locator(".ct-add-btn").click();
    await expect(page.locator(".ct-row")).toHaveCount(1);
    await expect(page.locator(".ct-uid")).toHaveValue("bob-uid");
    const noteError = await page.evaluate(async () => {
        try { await window.__noxaSocial.saveUserNote("alice-uid", "changed"); return ""; }
        catch (error) { return String(error); }
    });
    expect(noteError).toContain("save failed");
    expect(await page.evaluate(() => window.__noxaSocial.userNote("alice-uid"))).toBe("original");
    expect(await page.evaluate(() => window.__noxa.state.settings.contacts)).toEqual([{ unique_id: "alice-uid", label: "Alice", notify_online: false }]);
    await page.evaluate(() => { window.__saveFail = false; });
    await page.locator(".ct-add-btn").click();
    await expect(page.locator(".ct-row")).toHaveCount(2);
    await page.evaluate(() => window.__noxaSocial.saveUserNote("alice-uid", "changed"));
    expect(await page.evaluate(() => window.__saved.user_notes["alice-uid"])).toBe("changed");
});

test("settings Apply refreshes its edit baseline before the next edit", async ({ page }) => {
    await page.evaluate(() => {
        window.__saved.settings_base = "revision-1";
        window.__noxa.state.settings = structuredClone(window.__saved);
        const app = window.go.main.App;
        window.__baselines = [];
        window.go.main.App = new Proxy(app, { get(target, method) {
            if (method === "SaveSettings") return async settings => {
                window.__baselines.push(settings.settings_base);
                if (settings.settings_base !== window.__saved.settings_base) return "stale edit baseline";
                window.__saved = { ...structuredClone(settings), settings_base: `revision-${window.__baselines.length + 1}` };
                return "";
            };
            return target[method];
        } });
        window.__noxa.openSettings();
    });
    await page.getByRole("spinbutton", { name: "Chat max lines", exact: true }).fill("777");
    await page.locator("#set-apply").click();
    await expect(page.locator(".settings-save-status")).toHaveText("Settings saved.");
    await page.getByRole("spinbutton", { name: "Chat max lines", exact: true }).fill("888");
    await page.locator("#set-apply").click();
    await expect.poll(() => page.evaluate(() => window.__saved.chat_max_lines)).toBe(888);
    expect(await page.evaluate(() => window.__baselines)).toEqual(["revision-1", "revision-2"]);
});

test("notification edits survive retry after persisted settings fail audio application", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.applyLiveAudioSettings = async () => { throw new Error("audio device unavailable"); };
        window.__noxa.openSettings("notifications");
    });
    const toast = page.locator('.notify-matrix input[aria-label="mention: toast"]');
    await toast.uncheck();
    await page.locator("#set-apply").click();
    await expect(page.locator(".settings-save-status")).toContainText("audio device unavailable");
    expect(await page.evaluate(() => window.__saved.notify_matrix.mention.toast)).toBe(false);
    await page.evaluate(() => { window.__noxa.applyLiveAudioSettings = async () => {}; });
    await toast.check();
    await page.locator("#set-apply").click();
    await expect.poll(() => page.evaluate(() => window.__saved.notify_matrix.mention.toast)).toBe(true);
});

test("context contact and note failures show errors without announcing success", async ({ page }) => {
    await page.evaluate(() => { window.__saveFail = true; });
    await page.locator('.client[data-clid="alice"]').click({ button: "right" });
    await page.locator('[data-act="contact"]').click();
    await expect(page.locator("#toasts")).toContainText("save failed");
    expect(await page.evaluate(() => window.__noxa.state.settings.contacts || [])).toEqual([]);
    await expect(page.locator("#toasts")).not.toContainText("contact added");
    await page.evaluate(() => window.__noxa.openClientInfo(window.__noxa.state.clients.find(c => c.client_id === "alice")));
    await page.locator(".ci-note-input").fill("retry this note");
    await page.locator(".ci-note-input").press("Tab");
    await expect(page.locator("#toasts")).not.toContainText("note saved");
    expect(await page.evaluate(() => window.__noxaSocial.userNote("alice-uid"))).toBe("");
    await page.evaluate(() => { window.__saveFail = false; });
    await page.locator(".ci-note-input").fill("retry succeeded");
    await page.locator(".ci-note-input").press("Tab");
    await expect(page.locator("#toasts")).toContainText("note saved");
    expect(await page.evaluate(() => window.__saved.user_notes["alice-uid"])).toBe("retry succeeded");
});

test("selected wins: pane translations update live", async ({ page }) => {
    await expect(page.getByRole("separator", { name: "Resize channels pane" })).toHaveAttribute("title", /double-click/);
    await page.evaluate(async () => { (await import("/src/i18n.js")).setLanguage("de"); window.dispatchEvent(new Event("noxa-language-changed")); });
    await expect(page.getByRole("separator", { name: "Breite der Kanalleiste ändern" })).toHaveAttribute("title", /Doppelklick/);
});

test("selected wins: byte-aware composer and accessible message copy", async ({ page }) => {
    await page.evaluate(async () => { await (await import("/src/chat-ui.js")).onConnect(); });
    await page.locator("#chat-text").fill("😀".repeat(24));
    await expect(page.locator("#chat-length")).toHaveText("24 characters · 96 / 100 bytes");
    await expect(page.locator("#chat-length")).toHaveClass(/warn/);
    await page.evaluate(async () => { (await import("/src/chat-ui.js")).addChat({ id: 8800, channel_id: 1, from_unique_id: "alice-uid", from: "Alice", text: "Copy this message" }); });
    await page.locator('.msg[data-msg-id="8800"]').hover();
    await page.getByRole("button", { name: "Copy message", exact: true }).click();
    await expect(page.getByRole("button", { name: "Message copied", exact: true })).toBeVisible();
    expect(await page.evaluate(() => window.__copied)).toBe("Copy this message");
    await page.locator("#chat-text").fill("");
    await expect(page.locator("#chat-length")).toBeHidden();
});

test("selected wins: volume reset persists and amplification is explicit", async ({ page }) => {
    await page.evaluate(() => window.__noxa.setDetailsOpen(true));
    const slider = page.locator("#member-volume");
    await slider.fill("150");
    await slider.dispatchEvent("change");
    await expect(page.locator("#member-volume-value")).toHaveText("150% · amplified");
    await page.getByRole("button", { name: "Reset to 100%", exact: true }).click();
    await expect(slider).toHaveValue("100");
    await expect.poll(() => page.evaluate(() => window.__saved.user_volumes["alice-uid"])).toBe(100);
});

test("selected wins: effective PTT binding, failures and whisper preview", async ({ page }) => {
    await expect(page.locator("#voice-shortcut-hint")).toHaveText("Push to talk: F5");
    await page.evaluate(() => {
        window.__events.hotkey_binding.forEach(fn => fn({ action: "ptt", spec: "Ctrl+F8" }));
        window.__events.hotkey_status.forEach(fn => fn({ action: "ptt", registered: true }));
    });
    await expect(page.locator("#voice-shortcut-hint")).toHaveText("Push to talk: Ctrl+F8");
    await page.evaluate(() => window.__events.hotkey_status.forEach(fn => fn({ action: "ptt", registered: false, error: "Unavailable" })));
    await expect(page.locator("#voice-shortcut-hint")).toContainText("shortcut unavailable");
    await page.evaluate(async () => {
        Object.assign(window.__noxa.state.settings, { whisper_active: true, whisper_clients: ["alice-uid"], whisper_channels: [2] });
        window.__noxa.renderVoiceStatus();
        await (await import("/src/media-controls.js")).setWhisperRouting({ active: true, clients: ["alice-uid"], channels: [2] });
    });
    await expect(page.locator("#voice-whisper-preview")).toHaveText("Whisper targets: Alice, Channel: Team");
    await page.evaluate(async () => {
        window.go.main.App = { WhisperSetForTab: async () => "permission denied" };
        await (await import("/src/media-controls.js")).setWhisperRouting({ active: true, clients: ["alice-uid"], channels: [] });
    });
    await expect(page.locator("#voice-whisper-preview")).toContainText("Whisper routing is not confirmed");
    await expect(page.locator("#voice-whisper-preview")).not.toContainText("Whisper targets");
});

test("selected wins: snooze duration, expiry, failure and DND independence", async ({ page }) => {
    await page.clock.install();
    await page.locator("#notif-bell").click();
    await page.getByRole("button", { name: "Snooze for 30 minutes", exact: true }).click();
    await expect(page.locator(".notification-snooze [role=status]")).toContainText("Notifications snoozed until");
    const halfHour = await page.evaluate(() => window.__saved.notification_snooze_until - Date.now());
    expect(halfHour).toBeGreaterThan(1799000);
    expect(halfHour).toBeLessThanOrEqual(1800000);
    expect(await page.evaluate(() => window.__noxaPolish.dndActive())).toBe(true);
    await page.clock.fastForward(1800001);
    expect(await page.evaluate(() => window.__noxaPolish.dndActive())).toBe(false);
    await expect(page.locator(".notification-snooze [role=status]")).toContainText("No temporary snooze");
    await page.getByRole("button", { name: "Snooze for one hour", exact: true }).click();
    const hour = await page.evaluate(() => window.__saved.notification_snooze_until - Date.now());
    expect(hour).toBeGreaterThan(3599000);
    expect(hour).toBeLessThanOrEqual(3600000);
    await page.evaluate(() => { window.__noxa.state.settings.dnd_enabled = true; });
    await page.getByRole("button", { name: "End snooze", exact: true }).click();
    expect(await page.evaluate(() => window.__noxaPolish.dndActive())).toBe(true);
    await page.evaluate(() => { window.__saveFail = true; });
    await page.getByRole("button", { name: "Snooze for 30 minutes", exact: true }).click();
    await expect(page.locator(".notification-snooze [role=alert]")).toContainText("Could not save");
    expect(await page.evaluate(() => window.__noxa.state.settings.notification_snooze_until)).toBe(0);
});

test("selected wins: version copy includes both build identities, excludes identity and address", async ({ page }) => {
    await page.getByRole("menuitem", { name: "Help", exact: true }).click();
    await page.getByRole("menuitem", { name: "About noXa", exact: true }).click();
    await page.getByRole("button", { name: "Copy version information", exact: true }).click();
    expect(await page.evaluate(() => window.__copied)).toBe("noXa\nClient: 0.5.0-dev+gabc123\nServer: 0.5.0+gdef456");
});

test("selected wins: stream codec, bandwidth and isolated diagnostics", async ({ page }) => {
    await page.evaluate(async () => {
        const v = window.__noxa;
        let sample = 0;
        v.state.pc = { getSenders: () => [], getStats: async () => {
            sample++;
            return new Map([
                ["codec", { type: "codec", mimeType: "video/VP8" }],
                ["stream", { id: "stream", type: "inbound-rtp", kind: "video", codecId: "codec", trackIdentifier: "alice|cam", timestamp: sample * 3000, bytesReceived: sample * 600000, framesDecoded: sample * 90, frameWidth: 1280, frameHeight: 720, packetsLost: 0 }],
                ["secret", { id: "secret", type: "candidate-pair", localAddress: "do-not-copy", bytesReceived: 100000000 }],
            ]);
        } };
        const canvas = document.createElement("canvas");
        canvas.width = 640; canvas.height = 360;
        const ctx = canvas.getContext("2d"); ctx.fillStyle = "#33485a"; ctx.fillRect(0, 0, 640, 360);
        const stream = canvas.captureStream(1), track = stream.getVideoTracks()[0];
        Object.defineProperty(track, "id", { value: "alice|cam" });
        (await import("/src/video.js")).videoTrackAdded(track.id, stream, "alice");
    });
    await page.locator(".vtile-diagnostics summary").click();
    await expect(page.locator(".vtile-codec")).toHaveText("Codec: VP8", { timeout: 10000 });
    await expect(page.locator(".vtile-rate")).toHaveText("Received: 1.60 Mbit/s", { timeout: 10000 });
    await page.getByRole("button", { name: "Copy this stream’s diagnostics", exact: true }).click();
    const result = JSON.parse(await page.evaluate(() => window.__copied));
    expect(result).toMatchObject({ codec: "VP8", bitrate_bits_per_second: 1600000, direction: "received", slot: "cam" });
    expect(JSON.stringify(result)).not.toContain("do-not-copy");
    await page.screenshot({ path: ".cache/selected-wins-wide.png" });
    await page.setViewportSize({ width: 720, height: 900 });
    await page.evaluate(async () => { (await import("/src/i18n.js")).setLanguage("de"); window.dispatchEvent(new Event("noxa-language-changed")); });
    await expect(page.locator(".vtile-rate")).toHaveText("Empfangen: 1.60 Mbit/s");
    await page.screenshot({ path: ".cache/selected-wins-compact.png" });
});
