import { expect, test } from "@playwright/test";

async function installSaveScenario(page, settings = {}) {
    await page.evaluate((settings) => {
        const app = window.go.main.App;
        window.__noxa.state.settings = { ...window.__noxa.state.settings, ...settings };
        window.__persistedSettings = structuredClone(window.__noxa.state.settings);
        window.__saveAttempts = 0;
        window.__saveMode = "pending";
        window.go.main.App = new Proxy(app, { get(target, method) {
            if (method === "GetSettings") return async () => {
                if (window.__refreshError) throw new Error("refresh unavailable");
                return structuredClone(window.__persistedSettings);
            };
            if (method === "SaveSettings") return async (value) => {
                window.__saveAttempts++;
                const snapshot = structuredClone(value);
                if (window.__saveMode === "pending") await new Promise(resolve => { window.__finishSave = resolve; });
                if (window.__saveMode === "reject") throw new Error("disk unavailable");
                if (window.__saveMode === "error") return "disk full";
                window.__persistedSettings = snapshot;
                return "";
            };
            return target[method];
        } });
    }, settings);
}

test("offline settings save does not require a live whisper connection", async ({ page }) => {
    await installSaveScenario(page);
    await page.evaluate(() => {
        window.__saveMode = "success";
        window.__noxa.state.myClientID = "";
        const app = window.go.main.App;
        window.go.main.App = new Proxy(app, { get(target, method) {
            if (method === "WhisperSet") return async () => "not connected";
            return target[method];
        } });
        window.__noxa.openSettings();
    });
    await page.locator("#set-ok").click();
    await expect(page.locator("#settings-overlay")).toHaveCount(0);
    expect(await page.evaluate(() => window.__saveAttempts)).toBe(1);
});

test("bookmark retries after persistence succeeds but refreshing settings fails", async ({ page }) => {
    await installSaveScenario(page, { bookmarks: [{ name: "Original", addr: "example.test:12333", nickname: "Alice" }] });
    await page.evaluate(() => {
        window.__saveMode = "success";
        let failOnce = true;
        const app = window.go.main.App;
        window.go.main.App = new Proxy(app, { get(target, method) {
            if (method === "SaveSettings") return async value => {
                const error = await target.SaveSettings(value);
                // The real backend emits settings_update before resolving SaveSettings.
                for (const cb of window.__events.settings_update || []) cb(structuredClone(window.__persistedSettings));
                if (failOnce) { window.__refreshError = true; failOnce = false; }
                return error;
            };
            return target[method];
        } });
        window.__noxa.showWorkspace(false);
    });
    await page.locator("#menubar > .menu-item").filter({ hasText: /^Bookmarks/ }).click();
    await page.getByRole("menuitem", { name: "Manage bookmarks…", exact: true }).click();
    await page.locator(".bm-edit").click();
    const dialog = page.getByRole("dialog", { name: "Edit bookmark", exact: true });
    await dialog.locator(".bm-f-name").fill("Renamed");
    await dialog.getByRole("button", { name: "Save", exact: true }).click();
    await expect(dialog.locator(".bookmark-save-status")).toContainText("refresh unavailable");
    await page.evaluate(() => { window.__refreshError = false; });
    await dialog.getByRole("button", { name: "Save", exact: true }).click();
    await expect(dialog).toHaveCount(0);
    await expect(page.locator(".bm-name")).toHaveText("Renamed");
});

test("settings save blocks duplicates, retains draft after failure and retries", async ({ page }) => {
    const errors = [];
    page.on("pageerror", error => errors.push(error.message));
    await installSaveScenario(page);
    await page.evaluate(() => window.__noxa.openSettings());
    const dialog = page.locator("#settings-overlay");
    await page.getByRole("spinbutton", { name: "Chat max lines", exact: true }).fill("777");
    await page.locator("#set-apply").click();
    await expect(page.locator("#set-apply")).toBeDisabled();
    await expect(page.locator("#set-ok")).toBeDisabled();
    await page.keyboard.press("Escape");
    await expect(dialog).toBeVisible();
    await page.evaluate(() => {
        document.querySelector("#set-ok").click();
        window.__noxa.openSettings("playback");
    });
    expect(await page.evaluate(() => window.__saveAttempts)).toBe(1);
    await page.evaluate(() => { window.__saveMode = "reject"; window.__finishSave(); });
    await expect(dialog.locator(".settings-save-status")).toContainText("disk unavailable");
    await expect(page.locator("#set-apply")).toBeEnabled();
    await expect(page.getByRole("spinbutton", { name: "Chat max lines", exact: true })).toHaveValue("777");
    await page.evaluate(() => { window.__saveMode = "error"; });
    await page.locator("#set-ok").click();
    await expect(dialog.locator(".settings-save-status")).toContainText("disk full");
    await expect(dialog).toBeVisible();
    await page.evaluate(() => { window.__saveMode = "success"; });
    await page.locator("#set-ok").click();
    await expect(dialog).toHaveCount(0);
    expect(await page.evaluate(() => window.__noxa.state.settings.chat_max_lines)).toBe(777);
    expect(errors).toEqual([]);
});

test("bookmark save keeps edits on failure and commits only on success", async ({ page }) => {
    await installSaveScenario(page, { bookmarks: [{ name: "Original", addr: "example.test:12333", nickname: "Alice" }] });
    await page.evaluate(() => window.__noxa.showWorkspace(false));
    await page.locator("#menubar > .menu-item").filter({ hasText: /^Bookmarks/ }).click();
    await page.getByRole("menuitem", { name: "Manage bookmarks…", exact: true }).click();
    await page.locator(".bm-edit").click();
    const dialog = page.getByRole("dialog", { name: "Edit bookmark", exact: true });
    await dialog.locator(".bm-f-name").fill("Renamed");
    await dialog.getByRole("button", { name: "Save", exact: true }).click();
    await expect(dialog).toBeVisible();
    await expect(dialog.getByRole("button", { name: "Saving…", exact: true })).toBeDisabled();
    expect(await page.evaluate(() => window.__noxa.state.settings.bookmarks[0].name)).toBe("Original");
    await page.keyboard.press("Escape");
    await expect(dialog).toBeVisible();
    await page.evaluate(() => { window.__saveMode = "error"; window.__finishSave(); });
    await expect(dialog.locator(".bookmark-save-status")).toContainText("disk full");
    await expect(dialog.locator(".bm-f-name")).toHaveValue("Renamed");
    await page.evaluate(() => { window.__saveMode = "reject"; });
    await dialog.getByRole("button", { name: "Save", exact: true }).click();
    await expect(dialog.locator(".bookmark-save-status")).toContainText("disk unavailable");
    await page.evaluate(() => { window.__saveMode = "success"; });
    await dialog.getByRole("button", { name: "Save", exact: true }).click();
    await expect(dialog).toHaveCount(0);
    await expect(page.locator(".bm-name")).toHaveText("Renamed");
    expect(await page.evaluate(() => window.__noxa.state.settings.bookmarks[0].name)).toBe("Renamed");
});

test("DND save reports errors and changes state only after success", async ({ page }) => {
    await installSaveScenario(page, { dnd_enabled: false });
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        window.__dndFeedback = [];
        window.__noxa.toast = (...args) => window.__dndFeedback.push(args);
    });
    const toggle = async () => {
        await page.locator("#menubar > .menu-item").filter({ hasText: /^View/ }).click();
        await page.getByRole("menuitem", { name: "Do not disturb", exact: true }).click();
    };
    await toggle();
    expect(await page.evaluate(() => window.__noxa.state.settings.dnd_enabled)).toBe(false);
    await toggle();
    expect(await page.evaluate(() => window.__saveAttempts)).toBe(1);
    await page.evaluate(() => { window.__saveMode = "error"; window.__finishSave(); });
    await expect.poll(() => page.evaluate(() => window.__dndFeedback)).toEqual([["save failed: disk full", "warn", "alert", { bypassDND: true }]]);
    expect(await page.evaluate(() => window.__noxa.state.settings.dnd_enabled)).toBe(false);
    await page.evaluate(() => { window.__saveMode = "reject"; });
    await toggle();
    await expect.poll(() => page.evaluate(() => window.__dndFeedback.at(-1))).toEqual(["save failed: disk unavailable", "warn", "alert", { bypassDND: true }]);
    await page.evaluate(() => { window.__saveMode = "success"; });
    await toggle();
    await expect.poll(() => page.evaluate(() => window.__noxa.state.settings.dnd_enabled)).toBe(true);
    expect(await page.evaluate(() => window.__dndFeedback.at(-1))).toEqual(["do not disturb on", "info", "alert", { bypassDND: true }]);
});

test("DND disable failures remain visible while ordinary notifications stay muted", async ({ page }) => {
    await installSaveScenario(page, { dnd_enabled: true });
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        window.__saveMode = "error";
        window.__noxa.toast("ordinary notification");
    });
    await expect(page.locator("#toasts .toast").filter({ hasText: "ordinary notification" })).toHaveCount(0);
    await page.locator("#menubar > .menu-item").filter({ hasText: /^View/ }).click();
    await page.getByRole("menuitem", { name: "Do not disturb", exact: true }).click();
    await expect(page.locator("#toasts .toast").filter({ hasText: "save failed: disk full" })).toBeVisible();
    expect(await page.evaluate(() => window.__noxa.state.settings.dnd_enabled)).toBe(true);
});

test("audio output failures warn once and ignore obsolete selections", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.state.settings.playback_device_id = "missing-speaker";
        const element = { setSinkId: async () => { throw new Error("device missing"); } };
        for (let n = 0; n < 5; n++) window.__noxa.applyOutputSettings(element);
    });
    await expect(page.locator("#toasts .toast").filter({ hasText: "Could not switch audio output" })).toHaveCount(1);
    await page.evaluate(async () => {
        window.__noxa.state.settings.playback_device_id = "old-speaker";
        window.__noxa.applyOutputSettings({ setSinkId: () => new Promise((_, reject) => { window.__failOldOutput = reject; }) });
        await Promise.resolve();
        window.__noxa.state.settings.playback_device_id = "working-speaker";
        window.__failOldOutput(new Error("obsolete error"));
    });
    await expect(page.locator("#toasts .toast").filter({ hasText: "Could not switch audio output" })).toHaveCount(1);
});

test("settings search announces counts and explains the result cap", async ({ page }, testInfo) => {
    await page.evaluate(() => window.__noxa.openSettings());
    const search = page.locator("#settings-search");
    await search.fill("a");
    await expect(page.locator(".set-search-hit")).toHaveCount(40);
    await expect(page.locator(".settings-search-summary")).toContainText(/Showing 40 of \d+ results\. Narrow your search/);
    await page.screenshot({ path: testInfo.outputPath("settings-search-count.png") });
    await search.fill("voice volume");
    await expect(page.locator(".settings-search-summary")).toHaveText("1 result");
    await search.fill("nothing-matches-this-value");
    await expect(page.locator(".settings-search-summary")).toHaveText("0 results");
    await search.fill("");
    await expect(page.locator(".settings-search-summary")).toBeHidden();
    await page.evaluate(async () => (await import("/src/i18n.js")).setLanguage("de"));
    await search.fill("sprachlautstärke");
    await expect(page.locator(".settings-search-summary")).toHaveText("1 Ergebnis");
    await search.fill("a");
    await expect(page.locator(".settings-search-summary")).toContainText(/40 von \d+ Ergebnissen angezeigt/);
});

test("settings search reuses labels and invalidates after edits, language changes and reopen", async ({ page }) => {
    const errors = [];
    page.on("pageerror", error => errors.push(error.message));
    await page.evaluate(() => {
        window.__noxa.openSettings();
        window.__searchControls = 0;
        const createElement = document.createElement.bind(document);
        document.createElement = (name, options) => {
            if (name === "select") window.__searchControls++;
            return createElement(name, options);
        };
    });
    const search = page.locator("#settings-search");
    await search.fill("volume");
    await expect(page.locator(".set-search-hit").first()).toBeVisible();
    const firstBuild = await page.evaluate(() => window.__searchControls);
    expect(firstBuild).toBeGreaterThan(0);
    await search.fill("volum");
    await search.fill("volume");
    expect(await page.evaluate(() => window.__searchControls)).toBe(firstBuild);
    expect(await page.evaluate(() => window.__calls.ListIdentities || 0)).toBe(0);
    await search.fill("voice volume");
    await page.locator(".set-search-hit").first().click();
    await expect(page.getByRole("slider", { name: "Voice volume", exact: true })).toBeVisible();
    await page.getByRole("slider", { name: "Voice volume", exact: true }).fill("65");
    const beforeEditedSearch = await page.evaluate(() => window.__searchControls);
    await search.fill("volume");
    expect(await page.evaluate(() => window.__searchControls)).toBeGreaterThan(beforeEditedSearch);
    await page.evaluate(async () => (await import("/src/i18n.js")).setLanguage("de"));
    const beforeGermanSearch = await page.evaluate(() => window.__searchControls);
    await search.fill("lautstärke");
    await expect(page.locator(".set-search-hit").first()).toContainText("Wiedergabe");
    const germanBuild = await page.evaluate(() => window.__searchControls);
    expect(germanBuild).toBeGreaterThan(beforeGermanSearch);
    await search.fill("lautstärk");
    expect(await page.evaluate(() => window.__searchControls)).toBe(germanBuild);
    await page.keyboard.press("Escape");
    await page.evaluate(async () => {
        (await import("/src/i18n.js")).setLanguage("en");
        window.__noxa.openSettings();
    });
    const beforeReopenedSearch = await page.evaluate(() => window.__searchControls);
    await search.fill("volume");
    expect(await page.evaluate(() => window.__searchControls)).toBeGreaterThan(beforeReopenedSearch);
    expect(errors).toEqual([]);
});

test("German menus translate remaining actions and bookmark dialogs", async ({ page }, testInfo) => {
    await page.evaluate(async () => {
        window.__noxa.showWorkspace(false);
        (await import("/src/i18n.js")).setLanguage("de");
        (await import("/src/menu.js")).initMenu();
    });
    await page.getByRole("menuitem", { name: "Ansicht", exact: true }).click();
    for (const name of ["Details ein-/ausblenden", "Chat in eigenem Fenster", "Nicht stören", "Immer im Vordergrund", "Design: dunkel", "Design: hell", "Design: hoher Kontrast"]) {
        await expect(page.getByRole("menuitem", { name, exact: true })).toBeVisible();
    }
    await page.screenshot({ path: testInfo.outputPath("german-view-menu.png") });
    await page.keyboard.press("Escape");
    await page.getByRole("menuitem", { name: "Lesezeichen", exact: true }).click();
    await expect(page.getByRole("menuitem", { name: "Aktuellen Server als Lesezeichen speichern", exact: true })).toBeVisible();
    await page.getByRole("menuitem", { name: "Lesezeichen verwalten…", exact: true }).click();
    const dialog = page.getByRole("dialog", { name: "Lesezeichen verwalten", exact: true });
    await expect(dialog).toContainText("Noch keine Lesezeichen");
    await dialog.getByRole("button", { name: "Schließen", exact: true }).click();
    await page.getByRole("menuitem", { name: "Selbst", exact: true }).click();
    await page.getByRole("menuitem", { name: "Spitznamen ändern…", exact: true }).click();
    await expect(page.getByRole("dialog", { name: "Spitznamen ändern", exact: true })).toContainText("Spitzname für die nächste Verbindung:");
    await page.getByRole("button", { name: "Abbrechen", exact: true }).click();
});

test("client language translates every settings page and persists on Apply @a11y", async ({ page }, testInfo) => {
    const errors = [];
    page.on("pageerror", error => errors.push(error.message));
    await page.evaluate(() => {
        const app = window.go.main.App;
        window.go.main.App = new Proxy(app, { get(target, method) {
            if (method === "GetSettings") return async () => structuredClone(window.__savedSettings || window.__noxa.state.settings);
            if (method === "ListIdentities") return async () => [{ id: "test", name: "My identity", unique_id: "identity-123456789", active: true, protection: "dpapi", security_level: 4 }];
            return target[method];
        } });
        window.__noxa.openSettings();
    });
    await page.getByRole("combobox", { name: "Language", exact: true }).selectOption("de");
    await page.getByRole("spinbutton", { name: "Chat max lines", exact: true }).fill("500");
    await page.getByRole("button", { name: "Apply", exact: true }).click();
    await expect(page.getByRole("dialog", { name: "Einstellungen", exact: true })).toBeVisible();
    await expect(page.getByRole("combobox", { name: "Sprache", exact: true })).toHaveValue("de");
    await expect(page.locator("html")).toHaveAttribute("lang", "de");
    await expect(page.getByRole("spinbutton", { name: "Chat max. Zeilen", exact: true })).toHaveValue("500");
    await page.screenshot({ path: testInfo.outputPath("settings-german-application.png") });
    const pages = [
        ["Anwendung", "Chat max. Zeilen"], ["Aufnahme", "Aufnahmegerät"],
        ["Wiedergabe", "Ausgabegerät"], ["Tastenkürzel", "Als neues Profil speichern…"],
        ["Flüstern", "Flüstern aktivieren"], ["Downloads", "Downloadordner"],
        ["Chat", "Zeitstempel"], ["Sicherheit", "Identitäten"],
        ["Server", "Die Serverkonfiguration ist nur für Administratoren verfügbar."],
        ["Benachrichtigungen", "Gesprochene Systemmeldungen"],
    ];
    for (const [tab, label] of pages) {
        await page.getByRole("tab", { name: tab, exact: true }).click();
        await expect(page.locator("#settings-content").getByText(label, { exact: true })).toBeVisible();
        if (tab === "Sicherheit") {
            await expect(page.getByText("My identity", { exact: false })).toBeVisible();
            await page.screenshot({ path: testInfo.outputPath("settings-german-security.png") });
        }
    }
    await expect(page.getByRole("button", { name: "Sprachmeldung testen", exact: true })).toBeVisible();
    await page.screenshot({ path: testInfo.outputPath("settings-german-notifications.png") });
    expect(await page.locator(".notify-matrix").evaluate(table => {
        const content = document.getElementById("settings-content");
        return table.getBoundingClientRect().right <= content.getBoundingClientRect().right;
    })).toBe(true);
    await page.getByRole("textbox", { name: "Einstellungen suchen", exact: true }).fill("Lautstärke");
    await expect(page.locator(".set-search-hit").first()).toContainText("Wiedergabe");
    await page.getByRole("button", { name: "Abbrechen", exact: true }).click();
    await page.evaluate(() => window.__noxa.openSettings());
    await expect(page.getByRole("combobox", { name: "Sprache", exact: true })).toHaveValue("de");
    await page.getByRole("combobox", { name: "Sprache", exact: true }).selectOption("en");
    await page.getByRole("button", { name: "Abbrechen", exact: true }).click();
    expect(await page.evaluate(() => window.__noxa.state.settings.language)).toBe("de");
    await page.evaluate(() => window.__noxa.openSettings());
    await page.getByRole("combobox", { name: "Sprache", exact: true }).selectOption("en");
    await page.getByRole("button", { name: "OK", exact: true }).click();
    await page.evaluate(() => window.__noxa.openSettings());
    await expect(page.getByRole("dialog", { name: "Settings", exact: true })).toBeVisible();
    await expect(page.getByRole("combobox", { name: "Language", exact: true })).toHaveValue("en");
    expect(errors).toEqual([]);
});

test("static speech follows language, rare events, draft volume and mute settings", async ({ page }) => {
    await page.evaluate(async () => {
        const { state, soundEngine, speechQueue } = window.__noxa;
        state.settings = { ...state.settings, language:"de", play_sounds:true, sound_volume:100, speech_volume:75,
            spoken_messages:true, speech_admin:true, speech_connection:true, speech_events:{}, event_sounds:{}, notify_matrix:{}, dnd_enabled:false };
        state.myClientID="speech-self";state.replayingTabID="";
        await soundEngine.preload();await soundEngine.resume();
        window.__speechPlayed=[];
        const play=soundEngine.play.bind(soundEngine);
        soundEngine.play=(id, options)=>{const ok=play(id,options);if(ok)window.__speechPlayed.push({id,volume:options?.volume});return ok;};
        speechQueue.clear();
        for(const cb of window.__events.event)cb(JSON.stringify({type:"kicked",data:{client_id:"speech-self",ban:true,from_server:true,reason:"a dynamic reason must remain visual"}}));
    });
    await expect.poll(()=>page.evaluate(()=>window.__speechPlayed.map(x=>x.id))).toContain("speech_de_banned");
    expect(await page.evaluate(()=>window.__speechPlayed.filter(x=>x.id.startsWith("speech_")).map(x=>x.id))).toEqual(["speech_de_banned"]);
    await page.evaluate(()=>{
        const {state,speechQueue}=window.__noxa;speechQueue.clear();
        state.settings.language="en";state.settings.play_sounds=false;
        window.__noxa.openSettings("notifications");
    });
    await page.getByRole("slider",{name:"Speech volume",exact:true}).fill("200");
    await page.getByRole("button",{name:"Test spoken message",exact:true}).click();
    await expect.poll(()=>page.evaluate(()=>window.__speechPlayed.at(-1))).toEqual({id:"speech_en_test",volume:200});
    expect(await page.evaluate(()=>window.__noxa.state.settings.speech_volume)).toBe(75);
    await page.getByRole("button",{name:"Stop preview",exact:true}).click();
    await page.getByRole("checkbox",{name:"Spoken system messages",exact:true}).uncheck();
    const count=await page.evaluate(()=>window.__speechPlayed.length);
    await page.getByRole("button",{name:"Test spoken message",exact:true}).click();
    await page.waitForTimeout(250);
    expect(await page.evaluate(()=>window.__speechPlayed.length)).toBe(count);
    await page.getByRole("button",{name:"Cancel",exact:true}).click();
    expect(await page.evaluate(()=>window.__noxa.speechQueue.current)).toBeNull();
});

test("sound previews use draft volume, finish Test All, and cancel on close @a11y", async ({ page }) => {
    await page.evaluate(async () => {
        const { state, soundEngine } = window.__noxa;
        state.settings = { ...state.settings, play_sounds: false, sound_volume: 100, event_sounds: {}, dnd_enabled: false };
        await soundEngine.preload();
        window.__previewedSounds = [];
        const original = soundEngine.play.bind(soundEngine);
        soundEngine.play = (name, options) => {
            const result = original(name, options);
            if (result) window.__previewedSounds.push({ name, volume: options?.settings?.sound_volume });
            return result;
        };
        window.__noxa.openSettings("notifications");
    });
    const volume = page.getByRole("slider", { name: "Sound volume", exact: true });
    await volume.fill("0");
    await page.getByRole("button", { name: "Preview Joined channel", exact: true }).click();
    expect(await page.evaluate(() => window.__previewedSounds.length)).toBe(0);
    await volume.fill("200");
    await page.getByRole("button", { name: "Preview Joined channel", exact: true }).click();
    await expect.poll(() => page.evaluate(() => window.__previewedSounds.at(-1))).toEqual({ name: "own_channel_join", volume: 200 });
    expect(await page.evaluate(() => window.__noxa.state.settings.sound_volume)).toBe(100);
    await page.getByRole("button", { name: "Stop preview", exact: true }).click();
    await page.evaluate(() => { window.__previewedSounds = []; });
    await page.getByRole("button", { name: "Test all sounds", exact: true }).click();
    await expect(page.getByRole("status").filter({ hasText: "Preview finished" })).toBeVisible({ timeout: 20000 });
    expect(await page.evaluate(() => new Set(window.__previewedSounds.map(x => x.name)).size)).toBe(32);
    await page.getByRole("button", { name: "Preview connection", exact: true }).click();
    await page.getByRole("button", { name: "Cancel", exact: true }).click();
    await expect.poll(() => page.evaluate(() => window.__noxa.soundEngine.active.size)).toBe(0);
    const count = await page.evaluate(() => window.__previewedSounds.length);
    await page.waitForTimeout(650);
    expect(await page.evaluate(() => window.__previewedSounds.length)).toBe(count);
});

test("Permission Manager lists offline admins and creates an admin key", async ({ page }, testInfo) => {
    await showB3Workspace(page);
    await page.evaluate(() => {
        window.__noxa.state.isAdmin = true;
        const app = window.go.main.App;
        window.go.main.App = new Proxy(app, { get(target, method) {
            if (method === "ServerAdminList") return async () => ({ entries: [
                { unique_id: "uid-daniel", nickname: "Daniel" },
                { unique_id: "uid-offline", nickname: "Offline Admin <img>" },
            ] });
            if (method === "TokenList") return async () => ({ entries: [] });
            if (method === "TokenAdd") return async (...args) => {
                (window.__adminKeyCalls ||= []).push(args);
                await new Promise(resolve => { window.__finishAdminKey = resolve; });
                return { entries: [{ token: "new-admin-key", group_id: 0, description: args[2] }] };
            };
            return target[method];
        } });
        window.__noxaPerms.openPermissionManager();
    });
    const manager = page.getByRole("dialog", { name: "Permission Manager", exact: true });
    await manager.getByRole("button", { name: "Server Admins", exact: true }).click();
    await expect(manager.getByText("Offline Admin <img>", { exact: true })).toBeVisible();
    await expect(manager.getByText("uid-offline", { exact: true })).toBeVisible();
    await expect(manager.getByText("Offline", { exact: true })).toBeVisible();
    await expect(manager.locator(".pm-admin-list img")).toHaveCount(0);
    for (const viewport of [{ width: 1004, height: 768 }, { width: 640, height: 480 }]) {
        await page.setViewportSize(viewport);
        await expect(manager.getByRole("button", { name: "Create admin key…", exact: true })).toBeInViewport();
        await manager.screenshot({ path: testInfo.outputPath(`admins-${viewport.width}.png`) });
    }
    await manager.getByRole("button", { name: "Create admin key…", exact: true }).click();
    const keys = page.getByRole("dialog", { name: "Admin Keys", exact: true });
    await expect(keys.getByRole("combobox", { name: "Channel restriction" })).toHaveCount(0);
    await keys.getByPlaceholder("note (optional)").fill("Second administrator");
    await keys.getByRole("button", { name: "+ Create key", exact: true }).click();
    await expect(keys.getByRole("button", { name: "+ Create key", exact: true })).toBeDisabled();
    expect(await page.evaluate(() => window.__adminKeyCalls)).toEqual([[0, 0, "Second administrator"]]);
    await page.evaluate(() => window.__finishAdminKey());
    await expect(page.getByText("new-admin-key", { exact: true }).first()).toBeVisible();
});

test("Permission Manager hides the admin roster from non-admins", async ({ page }) => {
    await page.evaluate(() => window.__noxaPerms.openPermissionManager());
    await expect(page.getByRole("button", { name: "Server Admins", exact: true })).toHaveCount(0);
});

test("Permission Manager shows a retryable error when the server lacks the admin roster", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.state.isAdmin = true;
        const app = window.go.main.App;
        window.go.main.App = new Proxy(app, { get(target, method) {
            if (method === "ServerAdminList") return async () => { throw new Error("unsupported message"); };
            return target[method];
        } });
        window.__noxaPerms.openPermissionManager();
    });
    await page.getByRole("button", { name: "Server Admins", exact: true }).click();
    await expect(page.getByText(/Could not load server admins/)).toBeVisible();
    await expect(page.getByRole("button", { name: "Refresh admins", exact: true })).toBeEnabled();
    await expect(page.getByRole("button", { name: "Create admin key…", exact: true })).toBeEnabled();
});

test("Permission Manager discards a late admin roster after switching tabs", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.state.isAdmin = true;
        const app = window.go.main.App;
        window.go.main.App = new Proxy(app, { get(target, method) {
            if (method === "ServerAdminList") return () => new Promise(resolve => { window.__finishAdmins = resolve; });
            return target[method];
        } });
        window.__noxaPerms.openPermissionManager();
    });
    await page.getByRole("button", { name: "Server Admins", exact: true }).click();
    await page.getByRole("button", { name: "Server Groups", exact: true }).click();
    await page.evaluate(() => window.__finishAdmins({ entries: [{ unique_id: "old", nickname: "Stale Admin" }] }));
    await expect(page.getByText("Stale Admin", { exact: true })).toHaveCount(0);
});

async function prepareServerInformation(page) {
    await showB3Workspace(page);
    await page.evaluate(() => {
        window.__serverInfo = { name: "noXa community", version: "0.4.3", platform: "linux/amd64", uptime_seconds: 90061, clients_online: 4, max_clients: 64, channels_online: 3 };
        const app = window.go.main.App;
        window.go.main.App = new Proxy(app, { get(target, method) {
            if (method === "ServerInfo") return async () => {
                window.__calls.ServerInfo = (window.__calls.ServerInfo || 0) + 1;
                return structuredClone(window.__serverInfo);
            };
            return target[method];
        } });
        window.runtime.ClipboardSetText = async (value) => { window.__copiedAddress = value; return true; };
        window.__noxa.state.lastConnect = { addr: "voice.example:12333" };
    });
}

test("server information opens from the name, Connections menu and latency with accessible traffic details @a11y", async ({ page }, testInfo) => {
    await prepareServerInformation(page);
    await page.setViewportSize({ width: 1004, height: 768 });
    await page.evaluate(() => {
        window.__noxa.state.pc = { getStats: async () => new Map([
            ["in", { id: "in", type: "inbound-rtp", kind: "audio", packetsReceived: 990, packetsLost: 10, jitter: .004, bytesReceived: 2097152, timestamp: 1000 }],
            ["out", { id: "out", type: "outbound-rtp", kind: "audio", packetsSent: 2000, bytesSent: 4194304, timestamp: 1000 }],
        ]) };
    });
    await page.locator("#server-name").click();
    const dialog = page.getByRole("dialog", { name: "Server information" });
    await expect(dialog.locator('[data-stat="name"]')).toHaveText("noXa community");
    await expect(dialog.locator('[data-stat="address"]')).toHaveText("voice.example:12333");
    await expect(dialog.locator('[data-stat="platform"]')).toHaveText("linux/amd64");
    await expect(dialog.locator('[data-stat="control-in"]')).toHaveText("2.00 KiB");
    await expect(dialog.locator('[data-stat="control-out"]')).toHaveText("1.00 KiB");
    await expect(dialog.locator('[data-stat="loss"]')).toHaveText("1.00 %");
    await expect(dialog.locator('[data-stat="jitter"]')).toHaveText("4.0 ms");
    await expect(dialog.locator('[data-stat="media-in"]')).toHaveText("2.00 MiB");
    await expect(dialog.locator('[data-stat="rate-in"]')).toHaveText("—");
    await dialog.getByRole("button", { name: "Copy server address" }).click();
    expect(await page.evaluate(() => window.__copiedAddress)).toBe("voice.example:12333");
    await auditAccessibility(page, "server information");
    await dialog.locator(".server-info").screenshot({ path: testInfo.outputPath("server-information.png") });
    await page.keyboard.press("Escape");
    await expect(page.locator("#server-name")).toBeFocused();
    await page.getByRole("menuitem", { name: "Connections", exact: true }).click();
    await page.getByRole("menuitem", { name: "Server information", exact: true }).click();
    await expect(dialog).toBeVisible();
    await page.keyboard.press("Escape");
    await page.evaluate(() => {
        const button = document.getElementById("voice-latency");
        button.hidden = false;
        button.textContent = "12 ms";
    });
    await page.locator("#voice-latency").click();
    await expect(dialog).toBeVisible();
    await page.setViewportSize({ width: 420, height: 600 });
    await expect(dialog.getByRole("button", { name: "Close", exact: true })).toBeInViewport();
    expect(await dialog.locator(".server-info").evaluate((el) => el.scrollWidth <= el.clientWidth)).toBe(true);
    await dialog.locator(".server-info").screenshot({ path: testInfo.outputPath("server-information-small.png") });
});

test("server information suspends hidden polling, avoids overlapping calls and stops on close", async ({ page }) => {
    await prepareServerInformation(page);
    await page.clock.install();
    await page.locator("#server-name").click();
    const dialog = page.getByRole("dialog", { name: "Server information" });
    await expect(dialog.locator('[data-stat="ping"]')).toHaveText("12 ms");
    const calls = () => page.evaluate(() => window.__calls.GetClientInfo || 0);
    const initial = await calls();
    await page.clock.runFor(1000);
    expect(await calls()).toBe(initial);
    await page.evaluate(() => { window.__clientInfoGate = new Promise((resolve) => { window.__finishInfo = resolve; }); });
    await page.clock.runFor(1000);
    await expect.poll(calls).toBe(initial + 1);
    await page.clock.runFor(10000);
    expect(await calls()).toBe(initial + 1);
    await page.evaluate(() => window.__finishInfo());
    await page.evaluate(() => {
        window.__testHidden = true;
        Object.defineProperty(document, "hidden", { configurable: true, get: () => window.__testHidden });
        document.dispatchEvent(new Event("visibilitychange"));
    });
    await page.clock.runFor(10000);
    expect(await calls()).toBe(initial + 1);
    await page.evaluate(() => { window.__testHidden = false; document.dispatchEvent(new Event("visibilitychange")); });
    await expect.poll(calls).toBe(initial + 2);
    await page.keyboard.press("Escape");
    await page.clock.runFor(10000);
    expect(await calls()).toBe(initial + 2);
});

test("server information handles older servers and discards late results after a tab reset", async ({ page }) => {
    await prepareServerInformation(page);
    await page.evaluate(() => {
        delete window.__serverInfo.platform;
        window.__clientInfoResponse = { ping_ms: -1 };
    });
    await page.locator("#server-name").click();
    const dialog = page.getByRole("dialog", { name: "Server information" });
    await expect(dialog.locator('[data-stat="platform"]')).toHaveText("Unavailable on this server");
    for (const key of ["ping", "loss", "media-in", "control-in"]) await expect(dialog.locator(`[data-stat="${key}"]`)).toHaveText("—");
    await page.keyboard.press("Escape");
    await page.evaluate(() => { window.__clientInfoGate = new Promise((resolve) => { window.__finishInfo = resolve; }); });
    await page.locator("#server-name").click();
    await page.evaluate(() => { for (const cb of window.__events.tab_reset) cb("another-tab"); });
    await expect(dialog).toHaveCount(0);
    await page.evaluate(() => window.__finishInfo());
    await expect(dialog).toHaveCount(0);
    await page.evaluate(() => { window.__noxa.state.myClientID = "other-client"; window.__serverInfo.name = "Other server"; window.__noxaMeta.openServerInfo(); });
    await expect(dialog.locator('[data-stat="name"]')).toHaveText("Other server");
});

async function showB3Workspace(page) {
    await page.evaluate(() => {
        const v = window.__noxa;
        Object.assign(v.state, {
            myClientID: "daniel", myUniqueID: "uid-daniel", myChannelID: 2,
            channels: [
                { ChannelID: 1, Name: "Echo Test", ParentID: 0 },
                { ChannelID: 2, Name: "Public", ParentID: 0, Topic: "Open voice chat for everyone, including guests." },
                { ChannelID: 3, Name: "Gaming", ParentID: 0 },
            ],
            clients: ["Daniel", "Alex", "Mia", "Jonas"].map((nickname) => ({
                client_id: nickname.toLowerCase(), unique_id: "uid-" + nickname.toLowerCase(),
                nickname, channel_id: 2, is_speaking: nickname === "Mia",
            })),
        });
        v.showWorkspace(false);
        v.renderTree();
    });
}

test("B3 participant strip follows live channel membership and opens member controls", async ({ page }) => {
    await showB3Workspace(page);
    const strip = page.getByRole("region", { name: "Voice participants" });
    await expect(strip.getByRole("button")).toHaveCount(4);
    await strip.getByRole("button", { name: /Mia.*speaking/ }).click();
    await expect(page.locator("#client-card .card-nick")).toHaveText("Mia");
    await page.getByRole("slider", { name: "User volume" }).fill("75");
    await page.getByRole("slider", { name: "User volume" }).press("Tab");
    await expect.poll(() => page.evaluate(() => window.__savedSettings?.user_volumes?.["uid-mia"])).toBe(75);
    await page.getByRole("button", { name: "Message Mia", exact: true }).click();
    await expect(page.locator("#chat-head-title")).toContainText("Mia");
    await page.evaluate(() => {
        window.__noxa.state.myChannelID = 3;
        window.__noxa.renderTree();
    });
    await expect(strip.getByRole("button")).toHaveCount(0);
    await expect(strip).toContainText("No one else is here yet");
});

test("B3 shows your detected speech even when your own playback is muted or deafened", async ({ page }) => {
    await showB3Workspace(page);
    await page.evaluate(() => {
        const v = window.__noxa;
        v.state.settings.muted_users = ["uid-daniel"];
        v.setDeafened(true);
        for (const cb of window.__events.event) cb(JSON.stringify({
            type: "speaking_changed", data: { client_id: "daniel", speaking: true },
        }));
    });
    const self = page.locator('#voice-participants [data-client-id="daniel"]');
    await expect(self).toContainText("Talking");
    await expect(self).toHaveClass(/speaking/);
    await expect(page.locator('#channel-tree .client[data-clid="daniel"]')).toHaveClass(/speaking/);
    await page.getByRole("tab", { name: "Files", exact: true }).click();
    await expect(self).toBeVisible();
    await expect(self).toContainText("Talking");
    await page.evaluate(() => {
        for (const cb of window.__events.event) cb(JSON.stringify({
            type: "speaking_changed", data: { client_id: "daniel", speaking: false },
        }));
    });
    await expect(self).toContainText("In voice");
    await expect(self).not.toHaveClass(/speaking/);
    await page.locator("#voice-mute").click();
    await expect(self).toContainText("Microphone muted");
});

test("tray follows detected self speech, input/output mute, and voice teardown without polling", async ({ page }) => {
    await showB3Workspace(page);
    await page.evaluate(() => {
        window.__noxa.state.pc = { close() {} };
        window.__noxa.renderTree();
        for (const cb of window.__events.event) cb(JSON.stringify({
            type: "speaking_changed", data: { client_id: "daniel", speaking: true },
        }));
    });
    const flags = () => page.evaluate(() => window.__callArgs.SetTrayVoiceState?.at(-1));
    await expect.poll(flags).toEqual([true, false, false]);
    const count = await page.evaluate(() => window.__calls.SetTrayVoiceState);
    await page.evaluate(() => {
        for (let i = 0; i < 20; i++) window.__noxa.renderTree();
        for (const cb of window.__events.event) cb(JSON.stringify({
            type: "speaking_changed", data: { client_id: "mia", speaking: false },
        }));
    });
    expect(await page.evaluate(() => window.__calls.SetTrayVoiceState)).toBe(count);
    await page.locator("#voice-deafen").click();
    await expect.poll(flags).toEqual([true, false, true]);
    await page.locator("#voice-mute").click();
    await expect.poll(flags).toEqual([false, true, true]);
    await page.locator("#voice-deafen").click();
    await expect.poll(flags).toEqual([false, true, false]);
    await page.locator("#voice-mute").click();
    await expect.poll(flags).toEqual([true, false, false]);
    await page.evaluate(() => window.__noxa.resetVoiceSession());
    await expect.poll(flags).toEqual([false, false, false]);
});

test("B3 keeps voice controls outside the Chat and Files panels @a11y", async ({ page }) => {
    await showB3Workspace(page);
    await page.evaluate(() => window.__noxa.openPM("uid-mia", "Mia"));
    await expect(page.locator("#chat-head-title")).toContainText("Mia");
    await page.getByRole("tab", { name: "Files", exact: true }).click();
    await expect(page.locator("#chat-head-title")).toHaveText("Public");
    await expect(page.locator("#files-pane")).toBeVisible();
    await expect(page.locator("#chat-pane")).toBeHidden();
    await expect(page.locator("#chat-input-row")).toBeHidden();
    await expect(page.getByRole("button", { name: "Mute microphone", exact: true })).toBeVisible();
    await page.getByRole("button", { name: "Mute microphone", exact: true }).click();
    await expect(page.getByRole("button", { name: "Unmute microphone", exact: true })).toHaveAttribute("aria-pressed", "true");
    await page.getByRole("button", { name: "Deafen", exact: true }).click();
    await expect(page.getByRole("button", { name: "Undeafen", exact: true })).toHaveAttribute("aria-pressed", "true");
    await auditAccessibility(page, "B3 files and voice toolbar");
    await page.getByRole("tab", { name: "Chat", exact: true }).click();
    await expect(page.locator("#chat-head-title")).toContainText("Mia");
});

test("B3 alpha details work with the keyboard and notifications stay readable @a11y", async ({ page }, testInfo) => {
    await showB3Workspace(page);
    await page.locator("#alpha-badge").focus();
    await page.keyboard.press("Enter");
    await expect(page.locator(".alpha-notice")).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(page.locator("#alpha-badge")).toBeFocused();
    await page.evaluate(() => {
        const self = window.__noxa.state.clients.find((c) => c.client_id === "daniel");
        self.is_speaking = true;
        window.__noxa.renderTree();
        window.__noxaPolish.recordNotification("warn", "insufficient permission: b_channel_modify");
    });
    for (const width of [1000, 640]) {
        await page.setViewportSize({ width, height: width === 1000 ? 730 : 480 });
        await page.screenshot({ path: testInfo.outputPath(`workspace-${width}.png`) });
        if (width === 640) await page.locator("#workspace-sidebar-toggle").click();
        await page.locator("#notif-bell").click();
        const message = page.locator(".nc-text");
        await expect(message).toHaveText("insufficient permission: b_channel_modify");
        expect(await message.evaluate((el) => el.scrollWidth <= el.clientWidth)).toBe(true);
        await auditAccessibility(page, `notifications ${width}`);
        await page.screenshot({ path: testInfo.outputPath(`notifications-${width}.png`) });
        await page.getByRole("button", { name: "Close notifications", exact: true }).click();
    }
});

test("B3 restores the persisted member volume after a failed save", async ({ page }) => {
    await showB3Workspace(page);
    await page.evaluate(() => {
        const app = window.go.main.App;
        window.go.main.App = new Proxy(app, {
            get(target, method) {
                if (method === "SaveSettings") return async (value) => {
                    if (window.__failVolumeSave) return "disk full";
                    window.__savedSettings = structuredClone(value);
                    return "";
                };
                if (method === "GetSettings") return async () => structuredClone(window.__savedSettings);
                return target[method];
            },
        });
    });
    await page.locator('#voice-participants [data-client-id="mia"]').click();
    const slider = page.getByRole("slider", { name: "User volume" });
    await slider.fill("75");
    await expect.poll(() => page.evaluate(() => window.__noxa.state.settings.user_volumes?.["uid-mia"])).toBe(75);
    await page.evaluate(() => { window.__failVolumeSave = true; });
    await slider.fill("150");
    await expect(page.locator("#member-action-error")).toHaveText("Could not save volume. Try again.");
    await expect(slider).toHaveValue("75");
    await expect(page.locator("#member-volume-value")).toHaveText("75%");
    await expect.poll(() => page.evaluate(() => window.__savedSettings.user_volumes["uid-mia"])).toBe(75);
});

test("B3 ignores a volume save failure after selecting another member", async ({ page }) => {
    await showB3Workspace(page);
    await page.evaluate(() => {
        const app = window.go.main.App;
        window.go.main.App = new Proxy(app, {
            get(target, method) {
                if (method === "SaveSettings") return () => new Promise((resolve) => { window.__finishVolumeSave = resolve; });
                return target[method];
            },
        });
    });
    await page.locator('#voice-participants [data-client-id="mia"]').click();
    await page.getByRole("slider", { name: "User volume" }).fill("150");
    await expect.poll(() => page.evaluate(() => typeof window.__finishVolumeSave)).toBe("function");
    await page.locator('#voice-participants [data-client-id="alex"]').click();
    await page.evaluate(() => window.__finishVolumeSave("disk full"));
    await expect(page.locator("#client-card .card-nick")).toHaveText("Alex");
    await expect(page.getByRole("slider", { name: "User volume" })).toHaveValue("100");
    await expect(page.locator("#member-volume-value")).toHaveText("100%");
    await expect(page.locator("#member-action-error")).toBeHidden();
});

test("B3 shows the details opener only while the pane is closed", async ({ page }) => {
    await showB3Workspace(page);
    const opener = page.locator("#details-toggle");
    await expect(opener).toBeVisible();
    await opener.click();
    await expect(opener).toBeHidden();
    await page.locator("#details-close").click();
    await expect(opener).toBeVisible();
    await expect(opener).toBeFocused();
});

test("B3 fits desktop and small windows without losing the composer or toolbar", async ({ page }) => {
    await showB3Workspace(page);
    for (const viewport of [{ width: 1440, height: 900 }, { width: 1000, height: 730 }, { width: 640, height: 480 }]) {
        await page.setViewportSize(viewport);
        await expect(page.locator("#chat-text")).toBeVisible();
        await expect(page.locator("#voice-mute")).toBeVisible();
        await expect(page.locator("#voice-disconnect")).toBeVisible();
        const bounds = await page.evaluate(() => ({
            overflow: document.documentElement.scrollWidth > innerWidth,
            composer: document.getElementById("chat-input-row").getBoundingClientRect().bottom,
            toolbar: document.getElementById("voice-bar").getBoundingClientRect().bottom,
        }));
        expect(bounds.overflow).toBe(false);
        expect(bounds.composer).toBeLessThanOrEqual(viewport.height);
        expect(bounds.toolbar).toBeLessThanOrEqual(viewport.height);
    }
});

const settings = {
    settings_version: 4,
    capture_device_id: "",
    playback_device_id: "",
    activation_mode: "ptt",
    vad_threshold: 25,
    volume: 100,
    chat_max_lines: 200,
    window_opacity: 100,
    camera_fps: 30,
    sound_volume: 100,
    auto_away_minutes: 0,
    notification_matrix: {},
    bookmarks: [],
    onboarding_done: true,
    alpha_dismissed: "test",
};

test.beforeEach(async ({ page }) => {
    await page.addInitScript(({ initialSettings }) => {
        window.__events = {};
        window.__calls = {};
        window.__callArgs = {};
        window.__browserURLs = [];
        window.__savedSettings = null;
        window.__tabs = [];
        window.runtime = {
            EventsOn(name, callback) {
                const listeners = (window.__events[name] ||= []);
                listeners.push(callback);
                return () => {
                    const index = listeners.indexOf(callback);
                    if (index >= 0) listeners.splice(index, 1);
                };
            },
            EventsEmit() {},
            WindowIsFullscreen: async () => false,
            BrowserOpenURL(url) {
                window.__browserURLs.push(url);
                if (window.__browserOpenThrow) throw new Error("browser unavailable");
                return window.__browserOpenReject ? Promise.reject(new Error("browser unavailable")) : Promise.resolve();
            },
        };
        const app = new Proxy({}, {
            get(_target, method) {
                return async (...args) => {
                    window.__calls[method] = (window.__calls[method] || 0) + 1;
                    (window.__callArgs[method] ||= []).push(structuredClone(args));
                    if (method === "GetSettings") {
                        const persisted = sessionStorage.getItem("startup-settings");
                        if (persisted) {
                            await new Promise((resolve) => { window.__resolveStartupSettings = resolve; });
                            return JSON.parse(persisted);
                        }
                        return structuredClone(initialSettings);
                    }
                    if (method === "SaveSettings") { window.__savedSettings = structuredClone(args[0]); return ""; }
                    if (method === "CertificateClockWarning") return window.__certificateClockWarning || "";
                    if (method === "ConnectBookmarkTabWithID") {
                        if (typeof window.__connectBookmarkHandler === "function") {
                            return await window.__connectBookmarkHandler(...args);
                        }
                        if (window.__connectBookmarkGate) await window.__connectBookmarkGate;
                        return {
                            tab_id: window.__connectTabID || "",
                            error: window.__connectBookmarkResult || "",
                        };
                    }
                    if (method === "ConnectGuestBookmarkTabWithID") {
                        if (typeof window.__guestConnectHandler === "function") {
                            return await window.__guestConnectHandler(...args);
                        }
                        return { tab_id: window.__guestConnectTabID || "", error: "" };
                    }
                    if (method === "ListTabs") return structuredClone(window.__tabs);
                    if (method === "DMHistoryLoad") return structuredClone(window.__dmHistory?.[args[0]] || []);
                    if (method === "Connected") {
                        if (window.__connectedGate) await window.__connectedGate;
                        return !!window.__connected;
                    }
                    if (method === "ClientID") {
                        if (window.__clientIDGate) await window.__clientIDGate;
                        return window.__activeClient || "client-a";
                    }
                    if (method === "IsAdmin") return true;
                    if (method === "ChannelEdit") {
                        if (window.__channelEditReject) throw new Error("Connection lost");
                        if (window.__channelEditGate) await window.__channelEditGate;
                        return window.__channelEditError || "";
                    }
                    if (method === "ChannelEditTree") return window.__channelTreeError || "";
                    if (method === "IsGuest") return false;
                    if (method === "IdentityUID") return "playwright-identity";
                    if (method === "ClientVersionShort") return "test";
                    if (method === "ClientVersion") {
                        if (window.__clientVersionGate) await window.__clientVersionGate;
                        if (window.__clientVersionReject) throw new Error("version unavailable");
                        return window.__clientVersion || "test";
                    }
                    if (method === "Disconnect") {
                        if (typeof window.__disconnectHandler === "function") {
                            return await window.__disconnectHandler(...args);
                        }
                        if (window.__disconnectReject) throw new Error("disconnect unavailable");
                        return "";
                    }
                    if (method === "CloseTab" && typeof window.__closeTabHandler === "function") {
                        return await window.__closeTabHandler(...args);
                    }
                    if (method === "DisconnectTab" && typeof window.__disconnectTabHandler === "function") {
                        return await window.__disconnectTabHandler(...args);
                    }
                    if (method === "SendICECandidate") {
                        if (window.__sendICECandidateReject) throw new Error("signal closed");
                        return "";
                    }
                    if (method === "UploadChatAttachment") {
                        if (typeof window.__uploadAttachmentHandler === "function") {
                            return await window.__uploadAttachmentHandler(...args);
                        }
                        if (window.__uploadAttachmentReject) throw new Error("upload unavailable");
                        return window.__uploadAttachmentResult || "[file:blob.vcx#dGVzdA==#file.bin]";
                    }
                    if (method === "DownloadChatAttachment") {
                        if (typeof window.__downloadAttachmentHandler === "function") {
                            return await window.__downloadAttachmentHandler(...args);
                        }
                        if (window.__attachmentGate) await window.__attachmentGate;
                        if (window.__attachmentReject) throw new Error(window.__attachmentReject);
                        return window.__attachmentData || "";
                    }
                    if (method === "SendChat") {
                        if (typeof window.__sendChatHandler === "function") {
                            return await window.__sendChatHandler(...args);
                        }
                        if (window.__sendChatReject) throw new Error("send unavailable");
                        return window.__sendChatResult || "";
                    }
                    if (method === "SendChatReply") {
                        if (window.__sendChatReplyReject) throw new Error("reply unavailable");
                        return window.__sendChatReplyResult || "";
                    }
                    if (method === "VerifyFile") {
                        if (window.__verifyFileGate) await window.__verifyFileGate;
                        if (window.__verifyFileReject) throw new Error("verify unavailable");
                        return window.__verifyFileResult ?? true;
                    }
                    if (method === "FileList") {
                        if (window.__fileListGate) await window.__fileListGate;
                        return structuredClone(window.__fileListResponse || {
                            entries: [], folders: [], used_bytes: 0, quota_bytes: 0,
                        });
                    }
                    if (method === "SaveChatAttachment") {
                        if (window.__saveAttachmentGate) await window.__saveAttachmentGate;
                        return window.__saveAttachmentResult || "";
                    }
                    if (method === "WebRTCAnswer" && window.__webRTCAnswerReject) {
                        throw new Error("answer rejected");
                    }
                    if (method === "GetPermissions") return structuredClone(window.__permissions || []);
                    if (method === "GroupList") return structuredClone(window.__groups || { groups: [] });
                    if (method === "PermList") {
                        const response = structuredClone(window.__permEntries || { entries: [] });
                        const gate = window.__permListGate;
                        window.__permListGate = null;
                        if (gate) await gate;
                        return response;
                    }
                    if (method === "PermSet") { window.__lastPermSet = structuredClone(args); return ""; }
                    if (method === "BanList") return structuredClone(window.__bans || { bans: [] });
                    if (method === "BanRemove") {
                        const gate = window.__banRemoveGate;
                        window.__banRemoveGate = null;
                        if (gate) await gate;
                        return window.__banRemoveResult || "";
                    }
                    if (method === "CheckForUpdate") return structuredClone(window.__updateInfo || { available: false, version: "test", size: 0 });
                    if (method === "IdentityInfo") return {};
                    if (method === "GetAvatar") return structuredClone(window.__avatarResponse || {});
                    if (method === "ServerIconGet" || method === "ServerBannerGet" || method === "ChannelIconGet" || method === "GroupIconGet" || method === "EmojiGet") {
                        return structuredClone(window.__assetResponse || {});
                    }
                    if (method === "GetClientInfo") {
                        const response = structuredClone(window.__clientInfoResponse || {
                            nickname: "Alice", unique_id: "user-a", connected_at: Date.now() / 1000 - 120,
                            idle_seconds: 5, ping_ms: 12, ip: "127.0.0.1", port: 12333,
                            bytes_in: 1024, bytes_out: 2048,
                        });
                        const gate = window.__clientInfoGate;
                        window.__clientInfoGate = null;
                        if (gate) await gate;
                        return response;
                    }
                    if (method === "SetActiveTab") {
                        window.__tabs = window.__tabs.map((tab) => ({ ...tab, active: tab.id === args[0] }));
                        window.__activeClient = args[0] === "tab-b" ? "client-b" : "client-a";
                        for (const cb of window.__events.tab_update || []) cb(structuredClone(window.__tabs));
                        for (const cb of window.__events.tab_reset || []) cb(args[0]);
                        return "";
                    }
                    return "";
                };
            },
        });
        window.go = { main: { App: app } };
        window.__mediaDevices = [
            { kind: "audioinput", deviceId: "mic-built-in", label: "Built-in Mic" },
            { kind: "audioinput", deviceId: "mic-usb", label: "USB Mic" },
            { kind: "audiooutput", deviceId: "speaker-usb", label: "USB Speakers" },
        ];
        Object.defineProperty(navigator, "mediaDevices", { configurable: true, value: {
            enumerateDevices: async () => structuredClone(window.__mediaDevices),
            getUserMedia: async () => { throw new Error("not needed by this workflow"); },
        }});
    }, { initialSettings: settings });
    await page.goto("/");
    await page.waitForFunction(() => !!window.__noxa?.openSettings);
});

async function auditAccessibility(page, context) {
    const snapshot = await page.locator("body").ariaSnapshot();
    const issues = await page.evaluate(() => {
        const found = [];
        const ids = new Map();
        for (const element of document.querySelectorAll("[id]")) {
            const id = element.id;
            if (!id) continue;
            ids.set(id, (ids.get(id) || 0) + 1);
        }
        for (const [id, count] of ids) {
            if (count > 1) found.push(`duplicate id #${id} (${count} instances)`);
        }

        for (const attribute of ["aria-labelledby", "aria-describedby", "aria-controls"]) {
            for (const element of document.querySelectorAll(`[${attribute}]`)) {
                for (const id of element.getAttribute(attribute).trim().split(/\s+/)) {
                    if (id && !document.getElementById(id)) {
                        found.push(`${element.tagName.toLowerCase()}[${attribute}] references missing #${id}`);
                    }
                }
            }
        }
        for (const label of document.querySelectorAll("label[for]")) {
            const id = label.getAttribute("for");
            if (id && !document.getElementById(id)) found.push(`label[for] references missing #${id}`);
        }
        for (const image of document.querySelectorAll("img:not([alt])")) {
            found.push(`image is missing alt text${image.id ? ` (#${image.id})` : ""}`);
        }
        return found;
    });

    const namedRoles = new Set([
        "button", "checkbox", "combobox", "dialog", "link", "menuitem", "menuitemcheckbox",
        "menuitemradio", "radio", "searchbox", "slider", "spinbutton", "switch", "tab", "textbox",
    ]);
    for (const line of snapshot.split("\n")) {
        const match = line.match(/^\s*-\s+([a-z]+)\b(.*)$/);
        if (!match || !namedRoles.has(match[1])) continue;
        const name = match[2].match(/"((?:[^"\\]|\\.)*)"/);
        if (!name || !name[1].trim()) issues.push(`unnamed ${match[1]} in accessibility tree: ${line.trim()}`);
    }

    expect(issues, `${context} accessibility issues\n\n${snapshot}`).toEqual([]);
}

test("switches the capture device and persists it", async ({ page }) => {
    await page.evaluate(() => window.__noxa.openSettings("capture"));
    const select = page.locator("#settings-content .set-row").filter({ hasText: "Capture device" }).locator("select");
    await expect(select).toHaveValue("");
    await select.selectOption("mic-usb");
    await page.getByRole("button", { name: "Apply", exact: true }).click();
    await expect.poll(() => page.evaluate(() => window.__savedSettings?.capture_device_id)).toBe("mic-usb");
});

test("refreshes capture and playback device lists on demand", async ({ page }) => {
    await page.evaluate(() => window.__noxa.openSettings("capture"));
    const captureRow = page.locator("#settings-content .set-row").filter({ hasText: "Capture device" });
    const captureSelect = captureRow.locator("select");
    await expect(captureSelect).toHaveAccessibleName("Capture device");
    await expect(captureSelect.getByRole("option", { name: "USB Mic" })).toHaveCount(1);
    await captureSelect.selectOption("mic-usb");

    await page.evaluate(() => window.__mediaDevices.push({
        kind: "audioinput", deviceId: "mic-studio", label: "Studio Mic",
    }));
    await expect(captureSelect.getByRole("option", { name: "Studio Mic" })).toHaveCount(0);
    await captureRow.getByRole("button", { name: "Refresh capture devices" }).click();
    await expect(captureSelect).toHaveAccessibleName("Capture device");
    await expect(captureSelect.getByRole("option", { name: "Studio Mic" })).toHaveCount(1);
    await expect(captureSelect).toHaveValue("mic-usb");

    await page.getByRole("tab", { name: /Playback/ }).click();
    const playbackRow = page.locator("#settings-content .set-row").filter({ hasText: "Output device" });
    const playbackSelect = playbackRow.locator("select");
    await expect(playbackSelect).toHaveAccessibleName("Output device");
    await expect(playbackSelect.getByRole("option", { name: "USB Speakers" })).toHaveCount(1);

    await page.evaluate(() => window.__mediaDevices.push({
        kind: "audiooutput", deviceId: "speaker-bt", label: "Bluetooth Speakers",
    }));
    await expect(playbackSelect.getByRole("option", { name: "Bluetooth Speakers" })).toHaveCount(0);
    await playbackRow.getByRole("button", { name: "Refresh playback devices" }).click();
    await expect(playbackSelect).toHaveAccessibleName("Output device");
    await expect(playbackSelect.getByRole("option", { name: "Bluetooth Speakers" })).toHaveCount(1);
});

test("mute control switches to an unmute affordance and back", async ({ page }) => {
    await page.evaluate(() => window.__noxa.showWorkspace(false));
    const mute = page.getByRole("button", { name: "Mute microphone" });
    await expect(mute).toHaveText("Mic on");
    await expect(mute).toHaveAttribute("title", "Mute");
    await expect(mute).toHaveAttribute("aria-pressed", "false");

    await mute.click();
    const unmute = page.getByRole("button", { name: "Unmute microphone" });
    await expect(unmute).toHaveText("Mic muted");
    await expect(unmute).toHaveAttribute("title", "Unmute");
    await expect(unmute).toHaveAttribute("aria-pressed", "true");

    await unmute.click();
    await expect(page.getByRole("button", { name: "Mute microphone" })).toHaveText("Mic on");
});

test("voice activation reopens after silence and keeps mute and PTT private", async ({ page }) => {
    // Use real browser tracks: disabling a track must silence its consumers.
    await page.locator("body").click({ position: { x: 1, y: 1 } });
    await page.evaluate(async () => {
        const v = window.__noxa;
        const ctx = new AudioContext();
        const oscillator = ctx.createOscillator();
        const gain = ctx.createGain();
        const output = ctx.createMediaStreamDestination();
        oscillator.connect(gain).connect(output);
        gain.gain.value = 0;
        oscillator.start();
        await ctx.resume();
        window.__voiceSignal = { ctx, gain };
        v.state.settings.activation_mode = "vad";
        v.state.settings.ptt_release_delay_ms = 0;
        v.state.settings.warn_muted_talking = false;
        v.state.localStream = output.stream;
        v.state.pttActive = false;
        v.state.muted = false;
        v.applyVoiceState();
        v.startVADMonitor();
        await v.state.voiceMonitorCtx.resume();
    });
    const transmitting = () => page.evaluate(() => window.__noxa.state.localStream.getAudioTracks()[0].enabled);
    expect(await transmitting()).toBe(false);
    for (let cycle = 0; cycle < 2; cycle++) {
        await page.evaluate(() => { window.__voiceSignal.gain.gain.value = 0.5; });
        await expect.poll(transmitting).toBe(true);
        await page.evaluate(() => { window.__voiceSignal.gain.gain.value = 0; });
        await expect.poll(transmitting).toBe(false);
    }
    await page.evaluate(() => {
        window.__noxa.state.muted = true;
        window.__voiceSignal.gain.gain.value = 0.5;
        window.__noxa.applyVoiceState();
    });
    await expect.poll(() => page.evaluate(() => window.__noxa.state.pttActive)).toBe(true);
    expect(await transmitting()).toBe(false);
    await page.evaluate(() => {
        const v = window.__noxa;
        v.state.settings.activation_mode = "ptt";
        v.state.muted = false;
        v.setPTT(false);
    });
    expect(await transmitting()).toBe(false);
    await page.evaluate(() => window.__noxa.setPTT(true));
    expect(await transmitting()).toBe(true);
    await page.evaluate(() => {
        window.__noxa.resetVoiceSession();
        return window.__voiceSignal.ctx.close();
    });
});

test("voice monitor releases its local capture on restart and disconnect", async ({ page }) => {
    const result = await page.evaluate(async () => {
        const v = window.__noxa;
        const inputCtx = new AudioContext();
        const track = inputCtx.createMediaStreamDestination().stream.getAudioTracks()[0];
        const clones = [];
        const clone = track.clone.bind(track);
        track.clone = () => {
            const copy = clone();
            clones.push(copy);
            return copy;
        };
        v.state.localStream = new MediaStream([track]);
        v.startVADMonitor();
        const firstCtx = v.state.voiceMonitorCtx;
        v.startVADMonitor();
        const afterRestart = clones.map((copy) => copy.readyState);
        const senderAfterRestart = track.readyState;
        v.resetVoiceSession();
        await inputCtx.close();
        return {
            afterRestart, senderAfterRestart,
            afterDisconnect: clones.map((copy) => copy.readyState),
            senderAfterDisconnect: track.readyState,
            firstContext: firstCtx.state,
            monitorContext: v.state.voiceMonitorCtx,
            timer: v.state.vadMonitor,
        };
    });
    expect(result).toEqual({
        afterRestart: ["ended", "live"], senderAfterRestart: "live",
        afterDisconnect: ["ended", "ended"], senderAfterDisconnect: "ended",
        firstContext: "closed", monitorContext: null, timer: null,
    });
});

test("retries a missing microphone without interrupting video or screen sharing", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        window.__getUserMediaCalls = 0;
        window.__cameraRequests = 0;
        window.__allowMicrophone = false;
        window.__shareTracksStopped = 0;

        const canvas = document.createElement("canvas");
        const videoStream = canvas.captureStream(1);
        window.__cameraTrack = videoStream.getVideoTracks()[0];
        const audioContext = new AudioContext();
        window.__retryAudioContext = audioContext;
        const audioStream = audioContext.createMediaStreamDestination().stream;
        navigator.mediaDevices.getUserMedia = async (constraints) => {
            window.__getUserMediaCalls++;
            if (constraints.video) window.__cameraRequests++;
            if (constraints.audio && !window.__allowMicrophone) {
                throw new DOMException("test permission denial", "NotAllowedError");
            }
            if (constraints.video) return videoStream;
            if (constraints.audio) return audioStream;
            return new MediaStream();
        };

        class FakePeerConnection {
            constructor() {
                this.senders = [];
                this.transceivers = [];
                this.iceConnectionState = "connected";
            }
            addTransceiver(track, options = {}) {
                const sender = {
                    track: typeof track === "string" ? null : track,
                    getParameters: () => ({ encodings: [{}] }),
                    setParameters: async () => {},
                    replaceTrack: async (nextTrack) => { sender.track = nextTrack; },
                };
                const transceiver = {
                    sender,
                    receiver: { track: { kind: typeof track === "string" ? track : track.kind } },
                    direction: options.direction || "sendrecv",
                    currentDirection: options.direction || "sendrecv",
                };
                this.senders.push(sender);
                this.transceivers.push(transceiver);
                return transceiver;
            }
            getSenders() { return this.senders; }
            getTransceivers() { return this.transceivers; }
            async createOffer() { return { type: "offer", sdp: "test-offer" }; }
            async setLocalDescription(description) { this.localDescription = description; }
            async setRemoteDescription(description) { this.remoteDescription = description; }
            async addIceCandidate() {}
            close() { this.iceConnectionState = "closed"; }
        }
        window.RTCPeerConnection = FakePeerConnection;
        window.__noxa.state.myClientID = "client-a";
        window.__noxa.state.myChannelID = 0;
        window.__noxa.state.channels = [{ ChannelID: 42, Name: "Lobby" }];
        window.__noxa.state.clients = [{
            client_id: "client-a", unique_id: "user-a", nickname: "Alice",
            channel_id: 0, is_speaking: false,
        }];
        const moved = JSON.stringify({ type: "user_moved", data: { client_id: "client-a", channel_id: 42 } });
        for (const callback of window.__events.event || []) callback(moved);
    });

    await expect(page.locator("#voice-status")).toHaveText("voice on");
    expect(await page.evaluate(() => window.__cameraRequests)).toBe(0);
    await expect(page.locator("#local-video")).toBeHidden();
    await page.getByRole("button", { name: "Camera off — click to turn on", exact: true }).click();
    await expect.poll(() => page.evaluate(() => window.__cameraRequests)).toBe(1);
    await expect(page.locator("#local-video")).toBeVisible();
    await expect(page.locator("#mic-status > span")).toHaveText("Microphone access denied — video only");
    const retry = page.getByRole("button", { name: "Retry microphone access" });
    await expect(retry).toBeVisible();

    await page.evaluate(() => {
        const state = window.__noxa.state;
        window.__pcBeforeMicRetry = state.pc;
        state.screenSharing = true;
        state.shareStream = {
            getTracks: () => [{ stop: () => { window.__shareTracksStopped++; } }],
        };
        window.__allowMicrophone = true;
    });

    await retry.click();

    await expect(page.locator("#mic-status")).toBeEmpty();
    await expect(page.locator("#ptt-btn")).toBeEnabled();
    await expect(page.locator("#ptt-btn")).toBeFocused();
    await expect.poll(() => page.evaluate(() => ({
        samePeerConnection: window.__noxa.state.pc === window.__pcBeforeMicRetry,
        audioTracks: window.__noxa.state.localStream.getAudioTracks().length,
        cameraLive: window.__cameraTrack.readyState === "live",
        sharing: window.__noxa.state.screenSharing,
        shareTracksStopped: window.__shareTracksStopped,
        unpublishedShare: window.__calls.SetScreenShare || 0,
        slots: window.__callArgs.WebRTCOffer.at(-1)[1].map(({ slot }) => slot),
    }))).toEqual({
        samePeerConnection: true,
        audioTracks: 1,
        cameraLive: true,
        sharing: true,
        shareTracksStopped: 0,
        unpublishedShare: 0,
        slots: ["cam", "mic"],
    });

    await page.evaluate(() => { window.__noxa.state.screenSharing = false; });
    await page.getByRole("button", { name: "Camera on — click to turn off", exact: true }).click();
    const stop = page.getByRole("button", { name: "Turn off", exact: true });
    if (await stop.isVisible()) await stop.click();
    await expect.poll(() => page.evaluate(() => window.__cameraTrack.readyState)).toBe("ended");
    await expect(page.locator("#local-video")).toBeHidden();
    expect(await page.evaluate(() => window.__noxa.state.localStream.getVideoTracks().length)).toBe(0);
});

test("camera stays off on joins and reconnects, and discards a late enable request", async ({ page }) => {
    await page.evaluate(async () => {
        const v = window.__noxa;
        v.showWorkspace(false);
        window.__cameraRequests = 0;
        window.__denyCamera = true;
        window.__cameraAudio = new AudioContext();
        navigator.mediaDevices.getUserMedia = async (constraints) => {
            if (constraints.video) {
                window.__cameraRequests++;
                if (window.__denyCamera) throw new DOMException("test denial", "NotAllowedError");
                const stream = document.createElement("canvas").captureStream(1);
                window.__enabledCameraTrack = stream.getVideoTracks()[0];
                if (window.__delayCamera) await new Promise((resolve) => { window.__resolveCameraEnable = resolve; });
                return stream;
            }
            return window.__cameraAudio.createMediaStreamDestination().stream;
        };
        // Negotiate with a real in-page peer; no camera or remote server is used.
        const app = window.go.main.App;
        window.__cameraRemote = new RTCPeerConnection();
        window.go.main.App = new Proxy(app, {
            get(target, key) {
                if (key !== "WebRTCOffer") return target[key];
                return async (sdp) => {
                    await window.__cameraRemote.setRemoteDescription({ type: "offer", sdp });
                    const answer = await window.__cameraRemote.createAnswer();
                    await window.__cameraRemote.setLocalDescription(answer);
                    return answer.sdp;
                };
            },
        });
        v.state.myChannelID = 42;
        v.state.channels = [{ ChannelID: 42, Name: "Lobby" }];
        await v.ensureVoiceForChannel();
    });
    await expect(page.locator("#voice-status")).toHaveText("voice on");
    expect(await page.evaluate(() => window.__cameraRequests)).toBe(0);
    const enable = page.getByRole("button", { name: "Camera off — click to turn on", exact: true });
    await enable.click();
    await expect(enable).toBeEnabled();
    await expect(page.locator("#local-video")).toBeHidden();
    expect(await page.evaluate(() => window.__cameraRequests)).toBe(1);

    await page.evaluate(() => { window.__denyCamera = false; });
    await enable.click();
    await expect(page.locator("#local-video")).toBeVisible();
    await page.evaluate(async () => {
        const v = window.__noxa;
        v.resetVoiceSession();
        window.__cameraRemote.close();
        window.__cameraRemote = new RTCPeerConnection();
        await v.ensureVoiceForChannel();
    });
    await expect(page.locator("#voice-status")).toHaveText("voice on");
    await expect(page.locator("#local-video")).toBeHidden();
    expect(await page.evaluate(() => window.__enabledCameraTrack.readyState)).toBe("ended");
    expect(await page.evaluate(() => window.__cameraRequests)).toBe(2);

    await page.evaluate(() => { window.__delayCamera = true; });
    await enable.click();
    await expect.poll(() => page.evaluate(() => typeof window.__resolveCameraEnable)).toBe("function");
    await page.evaluate(() => {
        window.__noxa.resetVoiceSession();
        window.__resolveCameraEnable();
    });
    await expect.poll(() => page.evaluate(() => window.__enabledCameraTrack.readyState)).toBe("ended");
    await expect(page.locator("#local-video")).toBeHidden();
    expect(await page.evaluate(() => window.__noxa.state.localStream)).toBeNull();
});

test("camera settings preview requires an explicit test and releases capture on exit", async ({ page }) => {
    await page.evaluate(() => {
        window.__cameraRequests = 0;
        navigator.mediaDevices.getUserMedia = async (constraints) => {
            if (!constraints.video || constraints.audio) throw new Error("expected camera-only test");
            window.__cameraRequests++;
            const stream = document.createElement("canvas").captureStream(1);
            window.__previewTrack = stream.getVideoTracks()[0];
            return stream;
        };
        window.__noxa.openSettings("capture");
    });
    expect(await page.evaluate(() => window.__cameraRequests)).toBe(0);
    const start = page.getByRole("button", { name: "Test camera", exact: true });
    await start.click();
    await expect(page.getByLabel("Camera test preview")).toBeVisible();
    await page.getByRole("button", { name: "Stop camera test", exact: true }).click();
    expect(await page.evaluate(() => window.__previewTrack.readyState)).toBe("ended");
    await start.click();
    await page.locator('[data-page="playback"]').click();
    expect(await page.evaluate(() => window.__previewTrack.readyState)).toBe("ended");
    await page.locator('[data-page="capture"]').click();
    await start.click();
    await page.getByRole("button", { name: "Cancel", exact: true }).click();
    expect(await page.evaluate(() => window.__previewTrack.readyState)).toBe("ended");
    expect(await page.evaluate(() => window.__noxa.state.localStream)).toBeNull();
});

test("a camera test resolved after settings closes is immediately stopped", async ({ page }) => {
    await page.evaluate(() => {
        navigator.mediaDevices.getUserMedia = () => new Promise((resolve) => { window.__resolveCamera = resolve; });
        window.__noxa.openSettings("capture");
    });
    await page.getByRole("button", { name: "Test camera", exact: true }).click();
    await page.getByRole("button", { name: "Cancel", exact: true }).click();
    await page.evaluate(() => {
        const stream = document.createElement("canvas").captureStream(1);
        window.__previewTrack = stream.getVideoTracks()[0];
        window.__resolveCamera(stream);
    });
    await expect.poll(() => page.evaluate(() => window.__previewTrack.readyState)).toBe("ended");
});

test("ignores a delayed microphone failure after the voice session changes", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        const stream = document.createElement("canvas").captureStream(1);
        window.__noxa.state.myChannelID = 42;
        window.__noxa.state.localStream = stream;
        window.__noxa.state.pc = {
            close() {},
            getSenders: () => [],
            getTransceivers: () => [],
        };
        navigator.mediaDevices.getUserMedia = () => new Promise((_resolve, reject) => {
            window.__rejectStaleMicRetry = reject;
        });
        window.__staleMicRetry = window.__noxa.retryMicrophoneAccess();
    });
    await expect.poll(() => page.evaluate(() => typeof window.__rejectStaleMicRetry)).toBe("function");

    await page.evaluate(async () => {
        window.__noxa.resetVoiceSession();
        window.__rejectStaleMicRetry(new DOMException("late denial", "NotAllowedError"));
        await window.__staleMicRetry;
    });

    await expect(page.locator("#mic-status")).toBeEmpty();
    expect(await page.evaluate(() => window.__noxa.state.micState)).toBe("unknown");
});

test("routes decrypted direct messages and echoes without mixing global chat or peers", async ({ page }) => {
    await page.evaluate(() => {
        const { state } = window.__noxa;
        state.myUniqueID = "alpha";
        state.myNickname = "ALPHA";
        window.__noxa.showWorkspace();
        for (const callback of window.__events.event || []) callback(JSON.stringify({
            type: "chat", data: { direct: true, to_unique_id: "alpha", from_unique_id: "bravo", from: "BRAVO",
                text: "private Grüße 🌿", enc_verified: true, client_msg_id: "received-dm" },
        }));
    });
    await expect(page.locator("#chat-log")).not.toContainText("private Grüße");
    const bravoTab = page.locator("#pm-tabs .pm-tab").filter({ hasText: "BRAVO" });
    await expect(bravoTab).toBeVisible();
    await bravoTab.click();
    await expect(page.locator("#chat-log")).toContainText("private Grüße 🌿");
    await expect(page.locator("#chat-log .msg-tag")).toHaveText("dm");
    await expect(page.locator("#chat-log .msg-lock")).toHaveAttribute("title", /end-to-end encrypted/);

    await page.evaluate(() => {
        window.__noxaChat.openPM("charlie", "CHARLIE");
        for (const callback of window.__events.event || []) callback(JSON.stringify({
            type: "chat", data: { direct: true, to_unique_id: "bravo", from_unique_id: "alpha", from: "ALPHA",
                text: "echo for Bravo", enc_verified: true, client_msg_id: "sent-dm" },
        }));
    });
    await expect(page.locator("#chat-log")).not.toContainText("echo for Bravo");
    await bravoTab.click();
    await expect(page.locator("#chat-log")).toContainText("echo for Bravo");
    await page.evaluate(() => {
        for (const callback of window.__events.event || []) callback(JSON.stringify({
            type: "chat", data: { direct: true, from_unique_id: "bravo", from: "BRAVO",
                text: "[encrypted message — key unavailable]", client_msg_id: "unopened-dm" },
        }));
    });
    await expect(page.locator("#chat-log .missing-key .msg-lock")).toHaveText("⚠");
    const records = await page.evaluate(() => window.__callArgs.DMHistoryAppend.map((args) => args[2]));
    expect(records.find((record) => record.client_msg_id === "received-dm").enc_verified).toBe(true);
    expect(records.find((record) => record.client_msg_id === "unopened-dm").enc_verified).toBe(false);
});

test("restores DM history without claiming legacy or plaintext records were verified", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace();
        window.__dmHistory = { bravo: [
            { body: "legacy record", from_nickname: "BRAVO", sent_at: 10 },
            { body: "plaintext record", from_nickname: "BRAVO", sent_at: 11, enc_verified: false },
            { body: "verified record", from_nickname: "BRAVO", sent_at: 12, enc_verified: true },
        ] };
        window.__noxaChat.openPM("bravo", "BRAVO");
    });
    await expect(page.locator("#chat-log")).toContainText("verified record");
    await expect(page.locator("#chat-log .msg-tag")).toHaveText(["dm", "dm", "dm"]);
    await expect(page.locator("#chat-log .msg-lock")).toHaveCount(1);
    await expect(page.locator("#chat-log .msg-lock")).toHaveAttribute("title", /end-to-end encrypted/);
});

test("tracks existing unassigned clients when they later join a channel", async ({ page }) => {
    await page.evaluate(() => {
        const { state } = window.__noxa;
        state.myClientID = "alpha";
        window.__noxa.showWorkspace();
        for (const callback of window.__events.snapshot || []) callback(JSON.stringify({
            root_channels: [{ ChannelID: 1, ParentID: 0, Name: "Echo Test", clients: [] }],
            unassigned_clients: [
                { client_id: "alpha", unique_id: "uid-alpha", nickname: "ALPHA", channel_id: 0, is_bot: true, status: "away" },
                { client_id: "bravo", unique_id: "uid-bravo", nickname: "BRAVO", channel_id: 0 },
            ],
        }));
        // Auth's join follows its snapshot; it must not duplicate self or
        // overwrite the richer snapshot metadata with partial event fields.
        for (const callback of window.__events.event || []) callback(JSON.stringify({
            type: "user_joined", data: { client_id: "alpha", unique_id: "uid-alpha", nickname: "ALPHA" },
        }));
        for (const clientID of ["alpha", "bravo"]) {
            for (const callback of window.__events.event || []) callback(JSON.stringify({
                type: "user_moved", data: { client_id: clientID, channel_id: 1 },
            }));
        }
    });
    await expect(page.locator('.channel[data-chid="1"] .ch-count')).toHaveText("2");
    await expect(page.locator('.client[data-clid="bravo"]')).toContainText("BRAVO");
    expect(await page.evaluate(() => window.__noxa.state.clients.map((client) => ({
        id: client.client_id, channel: client.channel_id, bot: !!client.is_bot, status: client.status || "",
    })))).toEqual([
        { id: "alpha", channel: 1, bot: true, status: "away" },
        { id: "bravo", channel: 1, bot: false, status: "" },
    ]);
});

test("restores recent servers when persisted startup settings resolve", async ({ page }) => {
    await page.evaluate((initialSettings) => {
        sessionStorage.setItem("startup-settings", JSON.stringify({
            ...initialSettings,
            recents: [{ addr: "127.0.0.1:12583", nickname: "ALPHA", last_used: 1789289089 }],
        }));
    }, settings);
    await page.reload();
    await page.waitForFunction(() => window.__noxaTabs && window.__resolveStartupSettings);
    await expect(page.locator("#login-recents .recent-row")).toHaveCount(0);
    await page.evaluate(() => window.__resolveStartupSettings());

    const recent = page.getByRole("button", { name: "ALPHA @ 127.0.0.1:12583", exact: true });
    await expect(recent).toBeVisible();
    await recent.click();
    await expect(page.locator("#login-addr")).toHaveValue("127.0.0.1:12583");
    await expect(page.locator("#login-nick")).toHaveValue("ALPHA");
});

test("edits a recent server in the login form and focuses its address", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.state.settings.recents = [{
            addr: "voice.example:12333",
            nickname: "Alice",
            last_used: 123,
        }];
        window.__noxaTabs.renderRecents();
    });

    const row = page.locator("#login-recents .recent-row");
    const label = row.locator(".recent-label");
    await expect(label).toHaveText("Alice @ voice.example:12333");
    await label.click();
    await expect(page.locator("#login-addr")).toHaveValue("voice.example:12333");
    await expect(page.locator("#login-nick")).toHaveValue("Alice");

    const edit = page.getByRole("button", {
        name: "Edit recent server Alice at voice.example:12333",
    });
    await expect(edit).toContainText("Edit");
    await page.locator("#login-addr").fill("wrong.example:12333");
    await page.locator("#login-nick").fill("Wrong nickname");
    await edit.click();

    await expect(page.locator("#login-addr")).toHaveValue("voice.example:12333");
    await expect(page.locator("#login-nick")).toHaveValue("Alice");
    await expect(page.locator("#login-addr")).toBeFocused();
    await expect.poll(() => page.locator("#login-addr").evaluate((input) => ({
        start: input.selectionStart,
        end: input.selectionEnd,
    }))).toEqual({ start: 0, end: "voice.example:12333".length });
});

test("shows connection-quality sample age and clears stale RTT on disconnect", async ({ page }) => {
    await page.evaluate(() => {
        const { state } = window.__noxa;
        state.myClientID = "client-a";
        const pill = document.getElementById("conn-pill");
        pill.textContent = "voice.example:12333";
        pill.classList.add("up");
        window.__noxa.showWorkspace(false);
        window.__noxa.startQualitySampler();
    });

    const pill = page.locator("#conn-pill");
    await expect(pill).toHaveAttribute("data-quality", "good");
    await expect(page.locator("#voice-latency")).toBeVisible();
    await expect(page.locator("#voice-latency")).toHaveText("12 ms");
    await expect(page.locator("#voice-latency")).toHaveAttribute("aria-label", "Server latency: 12 milliseconds, good");
    await expect(pill).toHaveAttribute(
        "title",
        /connection quality: good \(RTT 12 ms, sampled (?:just now|\d+ seconds? ago)\)/,
    );

    await page.evaluate(() => {
        window.__noxa.state.settings.reconnect_on_loss = false;
        for (const callback of window.__events.disconnected || []) callback();
    });
    await expect(pill).not.toHaveAttribute("data-quality", /.+/);
    await expect(pill).toHaveAttribute("title", "Offline — no current RTT sample");
    await expect(page.locator("#voice-latency")).toBeHidden();
});

test("latency reuses the five-second sampler, marks stale samples, and prevents overlapping requests", async ({ page }, testInfo) => {
    await page.clock.install();
    await page.evaluate(() => {
        const v = window.__noxa;
        v.state.myClientID = "client-a";
        v.showWorkspace(false);
        const app = window.go.main.App;
        window.__latencyCalls = 0;
        window.__latencyPending = false;
        window.go.main.App = new Proxy(app, {
            get(target, method) {
                if (method === "GetClientInfo") return async () => {
                    window.__latencyCalls++;
                    if (window.__latencyPending) return await new Promise((resolve) => { window.__finishLatency = resolve; });
                    return { ping_ms: 12 };
                };
                return target[method];
            },
        });
        v.startQualitySampler();
    });
    const latency = page.locator("#voice-latency");
    await expect(latency).toHaveText("12 ms");
    await page.evaluate(() => {
        window.__latencyMutations = 0;
        new MutationObserver((records) => { window.__latencyMutations += records.length; })
            .observe(document.getElementById("voice-latency"), { childList: true, attributes: true, subtree: true });
    });
    await page.clock.fastForward(4000);
    expect(await page.evaluate(() => window.__latencyCalls)).toBe(1);
    expect(await page.evaluate(() => window.__latencyMutations)).toBe(0);
    await page.evaluate(() => { window.__latencyPending = true; });
    await page.clock.fastForward(1000);
    expect(await page.evaluate(() => window.__latencyCalls)).toBe(2);
    await page.clock.fastForward(20000);
    expect(await page.evaluate(() => window.__latencyCalls)).toBe(2);
    await expect(latency).toHaveText("12 ms · stale");
    await page.evaluate(() => window.__finishLatency({ ping_ms: 275 }));
    await expect(latency).toHaveText("275 ms");
    await expect(latency).toHaveAttribute("data-quality", "poor");
    await page.setViewportSize({ width: 1000, height: 730 });
    await page.locator("#voice-bar").screenshot({ path: testInfo.outputPath("latency-desktop.png") });
    await page.setViewportSize({ width: 640, height: 480 });
    await expect(latency).toBeVisible();
    await page.locator("#voice-bar").screenshot({ path: testInfo.outputPath("latency-small.png") });
    await page.evaluate(() => window.__noxa.stopQualitySampler());
    await expect(latency).toBeHidden();
});

test("clears RTT while switching tabs and only samples a connected active tab", async ({ page }) => {
    await page.evaluate(() => {
        const { state } = window.__noxa;
        state.myClientID = "client-a";
        const pill = document.getElementById("conn-pill");
        pill.textContent = "first.example:12333";
        pill.classList.add("up");
        window.__noxa.showWorkspace(false);
        window.__noxa.startQualitySampler();
    });
    const pill = page.locator("#conn-pill");
    await expect(pill).toHaveAttribute("data-quality", "good");

    await page.evaluate(() => {
        window.__tabs = [{
            id: "offline-tab", addr: "offline.example:12333", nickname: "Alice",
            connected: false, active: true, unread: 0, mentions: 0,
        }];
        for (const callback of window.__events.tab_reset || []) callback("offline-tab");
    });

    await expect(pill).not.toHaveAttribute("data-quality", /.+/);
    await expect(pill).not.toHaveClass(/\bup\b/);
    await expect(pill).toHaveAttribute("title", "Offline — no current RTT sample");
});

test("warns after connect when the local clock is outside certificate validity", async ({ page }) => {
    await page.evaluate(() => {
        window.__certificateClockWarning = "Local clock may be inaccurate; check date, time, and time zone.";
    });
    await page.locator("#login-addr").fill("voice.example:12333");
    await page.locator("#login-nick").fill("Alice");
    await page.getByRole("button", { name: "Connect" }).click();

    await expect(page.locator("#toasts")).toContainText(
        "Local clock may be inaccurate; check date, time, and time zone.",
    );
    await expect.poll(() => page.evaluate(() => window.__calls.CertificateClockWarning || 0)).toBe(1);
});

test("does not paint a completed login over a tab selected during finalization", async ({ page }) => {
    await page.evaluate(() => {
        window.__connectTabID = "new-tab";
        window.__tabs = [
            { id: "new-tab", addr: "new.example:12333", nickname: "Alice", active: true, connected: true },
            { id: "other-tab", addr: "other.example:12333", nickname: "Bob", active: false, connected: true },
        ];
        window.__noxa.state.tabConnects.set("other-tab", {
            addr: "other.example:12333", nick: "Bob", pw: "", spw: "", bookmark: "",
        });
        window.__clientIDGate = new Promise((resolve) => {
            window.__releaseConnectIdentity = resolve;
        });
    });
    await page.locator("#login-addr").fill("new.example:12333");
    await page.locator("#login-nick").fill("Alice");
    await page.locator("#login-serverpw").fill("secret");
    await page.getByRole("button", { name: "Connect" }).click();
    await expect.poll(() => page.evaluate(() => window.__calls.ClientID || 0)).toBeGreaterThan(0);

    await page.evaluate(() => {
        window.__tabs = window.__tabs.map((tab) => ({
            ...tab,
            active: tab.id === "other-tab",
        }));
        window.__activeClient = "client-b";
        for (const callback of window.__events.tab_reset || []) callback("other-tab");
        window.__releaseConnectIdentity();
    });

    await expect.poll(() => page.evaluate(() => window.__noxa.state.lastConnect?.addr))
        .toBe("other.example:12333");
    await expect(page.locator("#conn-pill")).not.toHaveText("new.example:12333");
    expect(await page.evaluate(() => window.__noxa.state.tabConnects.get("new-tab")?.spw)).toBe("secret");
});

test("rejects A-to-B-to-A identity results during login finalization", async ({ page }) => {
    await page.evaluate(() => {
        window.__connectTabID = "tab-a";
        window.__tabs = [{
            id: "tab-a", addr: "a.example:12333", nickname: "Alice",
            active: true, connected: true,
        }];
        window.__noxa.state.activeTabID = "tab-a";
        window.__noxa.state.serverGeneration = 10;
        window.__clientIDGate = new Promise((resolve) => {
            window.__releaseABAIdentity = resolve;
        });
    });
    await page.locator("#login-addr").fill("a.example:12333");
    await page.locator("#login-nick").fill("Alice");
    await page.getByRole("button", { name: "Connect" }).click();
    await expect.poll(() => page.evaluate(() => window.__calls.ClientID || 0)).toBeGreaterThan(0);

    await page.evaluate(() => {
        // Model A → B → A: the final active tab matches, but the identity
        // response came from the intervening tab and the generation changed.
        window.__activeClient = "client-from-tab-b";
        window.__noxa.state.serverGeneration += 2;
        window.__releaseABAIdentity();
    });

    await expect(page.locator("#login-connect")).toBeEnabled();
    expect(await page.evaluate(() => window.__noxa.state.myClientID)).not.toBe("client-from-tab-b");
    await expect(page.locator("#conn-pill")).not.toHaveClass(/\bup\b/);
});

test("warns about certificate timing after a guest quick-connect", async ({ page }) => {
    await page.evaluate(async () => {
        window.__certificateClockWarning = "Local clock may be inaccurate; check date, time, and time zone.";
        window.__noxa.state.settings.recents = [{
            addr: "quick.example:12333", nickname: "Alice", last_used: 123,
        }];
        await window.__noxaTabs.quickConnectLast();
    });

    await expect(page.locator("#alert-announcer")).toContainText(
        "Certificate timing warning for quick.example:12333",
    );
    expect(await page.evaluate(() => window.__calls.ConnectGuestBookmarkTabWithID)).toBe(1);
    expect(await page.evaluate(() => window.__calls.CertificateClockWarning)).toBe(1);
});

test("reconnects the last server from the tray only while disconnected", async ({ page }) => {
    await page.evaluate(() => {
        const { state } = window.__noxa;
        state.settings.reconnect_on_loss = false;
        state.lastConnect = null;
        state.lastSuccessfulConnect = {
            addr: "voice.example:12333", nick: "Alice", pw: "secret", spw: "", bookmark: "Work",
        };
        const pill = document.getElementById("conn-pill");
        pill.textContent = "offline";
        pill.classList.remove("up");
        for (const callback of window.__events.tray_reconnect || []) callback();
    });

    await expect.poll(() => page.evaluate(() => window.__calls.ConnectBookmarkTabWithID || 0)).toBe(1);
    await expect(page.locator("#conn-pill")).toHaveClass(/\bup\b/);
    expect(await page.evaluate(() => window.__callArgs.ConnectBookmarkTabWithID[0])).toEqual([
        "Work", "voice.example:12333", "Alice", "secret", "",
    ]);

    await page.evaluate(() => {
        for (const callback of window.__events.tray_reconnect || []) callback();
    });
    await page.waitForTimeout(50);
    expect(await page.evaluate(() => window.__calls.ConnectBookmarkTabWithID)).toBe(1);
});

test("keeps an automatic reconnect pinned to the server that dropped", async ({ page }) => {
    await page.evaluate(() => {
        const state = window.__noxa.state;
        state.settings.reconnect_on_loss = true;
        state.lastConnect = {
            addr: "dropped.example:12333", nick: "Alice", pw: "secret", spw: "", bookmark: "Dropped",
        };
        for (const callback of window.__events.disconnected || []) callback();
        state.lastConnect = {
            addr: "switched.example:12333", nick: "Bob", pw: "other", spw: "", bookmark: "Switched",
        };
    });

    await expect.poll(
        () => page.evaluate(() => window.__calls.ConnectBookmarkTabWithID || 0),
        { timeout: 7000 },
    ).toBe(1);
    expect(await page.evaluate(() => window.__callArgs.ConnectBookmarkTabWithID[0])).toEqual([
        "Dropped", "dropped.example:12333", "Alice", "secret", "",
    ]);
});

test("successful automatic reconnect replaces only the exact dropped server tab", async ({ page }) => {
    await page.clock.install();
    await page.evaluate(async () => {
        const state = window.__noxa.state;
        state.activeTabID = "dropped-tab";
        state.settings.reconnect_on_loss = true;
        state.lastConnect = { addr: "voice.example:12333", nick: "Alice", pw: "secret", spw: "", bookmark: "" };
        window.__tabs = [
            { id: "dropped-tab", addr: state.lastConnect.addr, nickname: "Alice", connected: false, active: true },
            { id: "other-tab", addr: state.lastConnect.addr, nickname: "Alice", connected: false, active: false },
        ];
        window.__connectBookmarkHandler = async (_bookmark, addr, nickname) => {
            const tabID = `replacement-${window.__calls.ConnectBookmarkTabWithID}`;
            window.__tabs.forEach((tab) => { tab.active = false; });
            window.__tabs.push({ id: tabID, addr, nickname, connected: true, active: true });
            for (const callback of window.__events.tab_reset || []) callback(tabID);
            return { tab_id: tabID, error: "" };
        };
        window.__closeTabHandler = async (tabID) => {
            window.__tabs = window.__tabs.filter((tab) => tab.id !== tabID);
            for (const callback of window.__events.tab_update || []) callback(structuredClone(window.__tabs));
        };
        for (const callback of window.__events.disconnected || []) callback();
        // A user changes tabs during the retry countdown. Cleanup must still
        // target the tab that dropped, including when both addresses match.
        await window.go.main.App.SetActiveTab("other-tab");
    });
    await page.clock.runFor(5000);
    await expect.poll(() => page.evaluate(() => window.__tabs.map((tab) => tab.id))).toEqual(["other-tab", "replacement-1"]);
    expect(await page.evaluate(() => window.__callArgs.CloseTab)).toEqual([["dropped-tab"]]);

    await page.evaluate(() => {
        window.__tabs.find((tab) => tab.active).connected = false;
        for (const callback of window.__events.disconnected || []) callback();
    });
    await page.clock.runFor(5000);
    await expect.poll(() => page.evaluate(() => window.__tabs.map((tab) => tab.id))).toEqual(["other-tab", "replacement-2"]);
    expect(await page.evaluate(() => window.__callArgs.CloseTab)).toEqual([["dropped-tab"], ["replacement-1"]]);
});

test("failed automatic reconnect preserves the dropped server tab", async ({ page }) => {
    await page.clock.install();
    await page.evaluate(() => {
        const state = window.__noxa.state;
        state.activeTabID = "dropped-tab";
        state.settings.reconnect_on_loss = true;
        state.lastConnect = { addr: "voice.example:12333", nick: "Alice", pw: "secret", spw: "", bookmark: "" };
        window.__tabs = [{ id: "dropped-tab", addr: state.lastConnect.addr, nickname: "Alice", connected: false, active: true }];
        window.__connectBookmarkResult = "connection refused";
        for (const callback of window.__events.disconnected || []) callback();
    });
    await page.clock.runFor(5000);
    await expect.poll(() => page.evaluate(() => window.__calls.ConnectBookmarkTabWithID || 0)).toBe(1);
    expect(await page.evaluate(() => window.__calls.CloseTab || 0)).toBe(0);
    expect(await page.evaluate(() => window.__tabs.map((tab) => tab.id))).toEqual(["dropped-tab"]);
});

test("automatic reconnect does not retire a source tab that is connected again", async ({ page }) => {
    await page.clock.install();
    await page.evaluate(() => {
        const state = window.__noxa.state;
        state.activeTabID = "dropped-tab";
        state.settings.reconnect_on_loss = true;
        state.lastConnect = { addr: "voice.example:12333", nick: "Alice", pw: "secret", spw: "", bookmark: "" };
        window.__tabs = [{ id: "dropped-tab", addr: state.lastConnect.addr, nickname: "Alice", connected: false, active: true }];
        window.__connectBookmarkHandler = async (_bookmark, addr, nickname) => {
            window.__tabs[0].connected = true;
            window.__tabs[0].active = false;
            window.__tabs.push({ id: "replacement", addr, nickname, connected: true, active: true });
            for (const callback of window.__events.tab_reset || []) callback("replacement");
            return { tab_id: "replacement", error: "" };
        };
        for (const callback of window.__events.disconnected || []) callback();
    });
    await page.clock.runFor(5000);
    await expect(page.locator("#conn-pill")).toHaveClass(/\bup\b/);
    expect(await page.evaluate(() => window.__calls.CloseTab || 0)).toBe(0);
    expect(await page.evaluate(() => window.__tabs.map((tab) => tab.id))).toEqual(["dropped-tab", "replacement"]);
});

test("does not finish an in-flight reconnect after an intentional disconnect", async ({ page }) => {
    await page.evaluate(() => {
        const { state } = window.__noxa;
        state.settings.reconnect_on_loss = true;
        state.lastSuccessfulConnect = {
            addr: "voice.example:12333", nick: "Alice", pw: "secret", spw: "", bookmark: "Work",
        };
        window.__connectTabID = "stale-reconnect-tab";
        window.__connectBookmarkGate = new Promise((resolve) => {
            window.__releaseConnectBookmark = resolve;
        });
        for (const callback of window.__events.tray_reconnect || []) callback();
    });
    await expect.poll(() => page.evaluate(() => window.__calls.ConnectBookmarkTabWithID || 0)).toBe(1);

    await page.evaluate(() => {
        for (const callback of window.__events.tray_disconnect || []) callback();
        window.__releaseConnectBookmark();
    });

    await expect.poll(() => page.evaluate(() => window.__calls.Disconnect || 0)).toBe(1);
    await expect.poll(() => page.evaluate(() => window.__calls.CloseTab || 0)).toBe(1);
    expect(await page.evaluate(() => window.__callArgs.CloseTab[0])).toEqual(["stale-reconnect-tab"]);
    await expect(page.locator("#conn-pill")).not.toHaveClass(/\bup\b/);
    expect(await page.evaluate(() => window.__noxa.state.lastConnect)).toBeNull();
});

test("labels screen-share controls and explains low-bandwidth data use", async ({ page }) => {
    await page.evaluate(() => window.__noxa.showWorkspace(false));
    await page.getByLabel("Voice options", { exact: true }).click();
    const shareButton = page.locator("#voice-screen");
    const lowBandwidthButton = page.locator("#voice-lowbw");

    await expect(shareButton).toHaveAttribute("title", "Start sharing");
    await expect(shareButton).toHaveAccessibleName("Start sharing");
    await expect(lowBandwidthButton).toHaveAttribute("title", /150 kbps camera or screen-share cap.*incoming data are additional/);
    await expect(lowBandwidthButton).toHaveAccessibleDescription(
        /outgoing video.*68 MB\/hour.*Voice, protocol overhead, and incoming data are additional/,
    );
    await expect(lowBandwidthButton).toHaveAttribute("aria-describedby", "voice-lowbw-estimate");
    await expect(page.locator("#voice-lowbw-estimate")).toBeVisible();
    await expect(page.locator("#voice-lowbw-estimate")).toHaveText("≤68 MB/h video send");

    await lowBandwidthButton.click();
    await expect(page.locator("#voice-lowbw-estimate")).toBeVisible();
    await expect(page.locator("#voice-lowbw-estimate")).toHaveText("≤68 MB/h video send");
    await expect(page.locator("#voice-lowbw-estimate")).toHaveClass(/active/);

    await shareButton.click();
    const shareDialog = page.getByRole("dialog", { name: "Share screen" });
    await expect(shareDialog.getByRole("group", { name: "Source" })).toBeVisible();
    await expect(shareDialog.getByRole("radio")).toHaveCount(3);
    await expect(shareDialog.getByRole("combobox", { name: "Quality preset" })).toBeVisible();
    await expect(shareDialog.getByRole("checkbox", { name: "Include system audio" })).toBeVisible();
    await auditAccessibility(page, "screen-share dialog");

    await page.evaluate(() => {
        const videoTrack = { kind: "video", contentHint: "", stop() {}, onended: null };
        navigator.mediaDevices.getDisplayMedia = async () => ({
            getVideoTracks: () => [videoTrack],
            getAudioTracks: () => [],
            getTracks: () => [videoTrack],
        });
    });
    await shareDialog.getByRole("button", { name: "Start sharing" }).click();
    await expect(shareButton).toHaveAttribute("title", "Stop sharing");
    await expect(shareButton).toHaveAccessibleName("Stop sharing");

    await shareButton.click();
    await expect(shareButton).toHaveAttribute("title", "Start sharing");
    await expect(shareButton).toHaveAccessibleName("Start sharing");
});

test("switches active server tabs without retaining stale identity", async ({ page }) => {
    await page.evaluate(() => {
        window.__tabs = [
            { id: "tab-a", addr: "a.example:12333", nickname: "alice", active: true, connected: true, unread: 0, mentions: 0 },
            { id: "tab-b", addr: "b.example:12333", nickname: "bob", active: false, connected: true, unread: 2, mentions: 1 },
        ];
        for (const cb of window.__events.tab_update || []) cb(structuredClone(window.__tabs));
        for (const cb of window.__events.tab_reset || []) cb("tab-a");
    });
    await expect(page.locator('.srv-tab[data-tab-id="tab-a"]')).toHaveClass(/active/);
    await page.evaluate(() => {
        const state = window.__noxa.state;
        state.serverGroups = [{ id: 7, name: "Old server admins" }];
        state.groupByUID = new Map([["old-user", [{ id: 7 }]]]);
        state.groupIcons = new Map([[7, "old-group-icon"]]);
        state.avatars = new Map([["old-user", "old-avatar"]]);
        state.avatarPending = new Set(["old-user"]);
        state.myPerms = new Map([["b_server_admin", { value: 1 }]]);
        const serverIcon = document.getElementById("server-icon");
        serverIcon.src = "data:image/png;base64,AAAA";
        serverIcon.classList.remove("hidden");
    });
    await page.locator('.srv-tab[data-tab-id="tab-b"]').click();
    await expect(page.locator('.srv-tab[data-tab-id="tab-b"]')).toHaveClass(/active/);
    await expect.poll(() => page.evaluate(() => window.__noxa.state.myClientID)).toBe("client-b");
    await expect.poll(() => page.evaluate(() => ({
        groups: window.__noxa.state.serverGroups.length,
        memberships: window.__noxa.state.groupByUID.size,
        groupIcons: window.__noxa.state.groupIcons.size,
        avatars: window.__noxa.state.avatars.size,
        avatarPending: window.__noxa.state.avatarPending.size,
        permissions: window.__noxa.state.myPerms.size,
    }))).toEqual({ groups: 0, memberships: 0, groupIcons: 0, avatars: 0, avatarPending: 0, permissions: 0 });
    await expect(page.locator("#server-icon")).toHaveClass(/hidden/);
    await expect(page.locator("#server-icon")).not.toHaveAttribute("src", /.+/);
});

test("recognises an immediate channel join while switched-tab identity is loading", async ({ page }) => {
    await page.evaluate(() => {
        window.__tabs = [
            { id: "tab-a", addr: "a.example:12333", nickname: "alice", active: true, connected: true, unread: 0, mentions: 0 },
            { id: "tab-b", addr: "b.example:12333", nickname: "bob", active: false, connected: true, unread: 0, mentions: 0 },
        ];
        window.__activeClient = "client-a";
        window.__noxa.state.myClientID = "client-a";
        let release;
        window.__clientIDGate = new Promise((resolve) => { release = resolve; });
        window.__releaseClientID = release;
        for (const cb of window.__events.tab_update || []) cb(structuredClone(window.__tabs));
    });

    await page.locator('.srv-tab[data-tab-id="tab-b"]').click();
    await page.evaluate(() => {
        const snapshot = JSON.stringify({
            root_channels: [{
                ChannelID: 42, ParentID: 0, Name: "Lobby",
                clients: [{ client_id: "client-b", unique_id: "user-b", nickname: "Bob", channel_id: 0, is_speaking: false }],
                children: [],
            }],
        });
        const moved = JSON.stringify({ type: "user_moved", data: { client_id: "client-b", channel_id: 42 } });
        for (const cb of window.__events.snapshot || []) cb(snapshot);
        for (const cb of window.__events.event || []) cb(moved);
        window.__releaseClientID();
        window.__clientIDGate = null;
    });

    await expect.poll(() => page.evaluate(() => window.__noxa.state.myClientID)).toBe("client-b");
    await expect.poll(() => page.evaluate(() => window.__noxa.state.myChannelID)).toBe(42);
});

test("removes cascaded deleted channels and displaces every cached member safely", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        window.__noxa.state.myClientID = "";
        for (const callback of window.__events.snapshot || []) callback(JSON.stringify({
            root_channels: [
                {
                    ChannelID: 10, ParentID: 0, Name: "Parent",
                    clients: [],
                    children: [{
                        ChannelID: 11, ParentID: 10, Name: "Child",
                        clients: [{ client_id: "client-a", unique_id: "user-a", nickname: "Alice", channel_id: 11 }],
                        children: [{
                            ChannelID: 12, ParentID: 11, Name: "Grandchild",
                            clients: [{ client_id: "client-b", unique_id: "user-b", nickname: "Bob", channel_id: 12 }],
                            children: [],
                        }],
                    }],
                },
                {
                    ChannelID: 20, ParentID: 0, Name: "Unaffected",
                    clients: [],
                    children: [{
                        ChannelID: 21, ParentID: 20, Name: "Legacy child",
                        clients: [],
                        children: [{
                            ChannelID: 22, ParentID: 21, Name: "Legacy grandchild",
                            clients: [{ client_id: "client-c", unique_id: "user-c", nickname: "Carol", channel_id: 22 }],
                            children: [],
                        }],
                    }],
                },
            ],
        }));
        window.__noxa.state.myClientID = "client-a";
        window.__noxa.state.myChannelID = 11;
        window.__voiceTracksStopped = 0;
        window.__noxa.state.localStream = {
            getTracks: () => [{ stop: () => { window.__voiceTracksStopped++; } }],
            getAudioTracks: () => [],
            getVideoTracks: () => [],
        };
        document.getElementById("voice-status").textContent = "voice on";
        window.__noxa.state.collapsedChannels.add(10);
        window.__noxa.state.collapsedChannels.add(20);
        window.__noxa.state.expandedVirtual.add(12);
        window.__noxa.state.expandedVirtual.add(22);
        window.__noxa.renderTree();
    });

    await page.locator("#tab-files").click();
    await expect(page.locator("#files-pane .fb-list")).not.toContainText("Join a channel");
    await page.evaluate(() => {
        const event = JSON.stringify({
            type: "channel_deleted",
            data: { channel_id: 10, channel_ids: [10, 11, 12] },
        });
        for (const callback of window.__events.event || []) callback(event);
    });

    await expect.poll(() => page.evaluate(() => ({
        channels: window.__noxa.state.channels.map((channel) => channel.ChannelID),
        clients: Object.fromEntries(window.__noxa.state.clients.map((client) => [client.client_id, client.channel_id])),
        myChannelID: window.__noxa.state.myChannelID,
        localStreamCleared: window.__noxa.state.localStream === null,
        stopped: window.__voiceTracksStopped,
        collapsedDeleted: !window.__noxa.state.collapsedChannels.has(10),
        expandedDeleted: !window.__noxa.state.expandedVirtual.has(12),
    }))).toEqual({
        channels: [20, 21, 22],
        clients: { "client-a": 0, "client-b": 0, "client-c": 22 },
        myChannelID: 0,
        localStreamCleared: true,
        stopped: 1,
        collapsedDeleted: true,
        expandedDeleted: true,
    });
    await expect(page.locator('.channel[data-chid="10"], .channel[data-chid="11"], .channel[data-chid="12"]')).toHaveCount(0);
    await expect(page.locator('.channel[data-chid="20"]')).toHaveCount(1);
    await expect(page.locator("#voice-status")).toHaveText("voice off");
    await expect(page.locator("#files-pane .fb-list")).toContainText("Join a channel to browse its files");

    // Legacy servers send only the parent channel_id. Re-enter the surviving
    // subtree so this second deletion exercises descendant member and voice
    // cleanup rather than merely deleting a leaf.
    await page.evaluate(() => {
        const state = window.__noxa.state;
        state.myClientID = "client-c";
        state.myChannelID = 22;
        state.localStream = {
            getTracks: () => [{ stop: () => { window.__voiceTracksStopped++; } }],
            getAudioTracks: () => [],
            getVideoTracks: () => [],
        };
        document.getElementById("voice-status").textContent = "voice on";
        const event = JSON.stringify({ type: "channel_deleted", data: { channel_id: 20 } });
        for (const callback of window.__events.event || []) callback(event);
    });
    await expect.poll(() => page.evaluate(() => ({
        channels: window.__noxa.state.channels.length,
        carolChannel: window.__noxa.state.clients.find((client) => client.client_id === "client-c")?.channel_id,
        myChannelID: window.__noxa.state.myChannelID,
        localStreamCleared: window.__noxa.state.localStream === null,
        stopped: window.__voiceTracksStopped,
        collapsedDeleted: !window.__noxa.state.collapsedChannels.has(20),
        expandedDeleted: !window.__noxa.state.expandedVirtual.has(22),
    }))).toEqual({
        channels: 0,
        carolChannel: 0,
        myChannelID: 0,
        localStreamCleared: true,
        stopped: 2,
        collapsedDeleted: true,
        expandedDeleted: true,
    });
    await expect(page.locator('.channel[data-chid="20"], .channel[data-chid="21"], .channel[data-chid="22"]')).toHaveCount(0);
    await expect(page.locator("#voice-status")).toHaveText("voice off");
});

test("starts voice, plays the original channel join cue, and switches channels", async ({ page }) => {
    await page.evaluate(async () => {
        window.__getUserMediaCalls = 0;
        window.__playedMedia = [];
        const engine = window.__noxa.soundEngine;
        await engine.preload();
        await engine.resume();
        const source = engine.ctx.createBufferSource.bind(engine.ctx);
        engine.ctx.createBufferSource = () => {
            const node = source();
            const start = node.start.bind(node);
            node.start = (...args) => {
                window.__playedMedia.push([...engine.buffers].find(([, buffer]) => node.buffer === buffer)?.[0]);
                start(...args);
            };
            return node;
        };
        navigator.mediaDevices.getUserMedia = async () => {
            window.__getUserMediaCalls++;
            throw new DOMException("test permission denial", "NotAllowedError");
        };
        window.__noxa.state.myClientID = "client-a";
        window.__noxa.state.myChannelID = 0;
        window.__noxa.state.clients = [{
            client_id: "client-a", unique_id: "user-a", nickname: "Alice",
            channel_id: 0, is_speaking: false,
        }];
        const moved = JSON.stringify({ type: "user_moved", data: { client_id: "client-a", channel_id: 42 } });
        for (const cb of window.__events.event || []) cb(moved);
    });

    await expect(page.locator("#voice-join")).toHaveCount(0);
    await expect.poll(() => page.evaluate(() => window.__getUserMediaCalls)).toBeGreaterThan(0);
    await expect.poll(() => page.evaluate(
        () => window.__playedMedia.some((src) => src.includes("channel_join")),
    )).toBe(true);
    await expect(page.locator("#voice-status")).toHaveText("voice unavailable");
    await expect(page.locator("#mic-status > span")).toHaveText("Microphone access denied");

    const firstCueCount = await page.evaluate(() => window.__playedMedia.length);
    await page.evaluate(() => {
        const emitMove = (clientID, channelID) => {
            const moved = JSON.stringify({ type: "user_moved", data: { client_id: clientID, channel_id: channelID } });
            for (const cb of window.__events.event || []) cb(moved);
        };
        emitMove("someone-else", 42);
        emitMove("client-a", 42);
    });
    await expect.poll(() => page.evaluate(() => window.__playedMedia.length)).toBe(firstCueCount);

    await page.evaluate(() => {
        const moved = JSON.stringify({ type: "user_moved", data: { client_id: "client-a", channel_id: 43 } });
        for (const cb of window.__events.event || []) cb(moved);
    });
    // A switch uses its own authored cue and does not replay channel join.
    await expect.poll(() => page.evaluate(() => window.__noxa.state.myChannelID)).toBe(43);
    await expect.poll(() => page.evaluate(() => window.__playedMedia.at(-1))).toBe("own_channel_switch");
    await page.evaluate(() => {
        const moved = JSON.stringify({ type: "user_moved", data: { client_id: "client-a", channel_id: 0 } });
        for (const cb of window.__events.event || []) cb(moved);
    });
    await expect(page.locator("#mic-status")).toBeEmpty();
});

test("undeafens after a confirmed channel join but preserves deafen on duplicate and remote events", async ({ page }) => {
    await showB3Workspace(page);
    const emitMove = (clientID, channelID) => page.evaluate(({ clientID, channelID }) => {
        for (const callback of window.__events.event || []) callback(JSON.stringify({
            type: "user_moved", data: { client_id: clientID, channel_id: channelID },
        }));
    }, { clientID, channelID });
    await page.evaluate(() => window.__noxa.setDeafened(true));
    await emitMove("mia", 3);
    await emitMove("daniel", 2);
    await expect(page.getByRole("button", { name: "Undeafen", exact: true })).toHaveAttribute("aria-pressed", "true");
    await emitMove("daniel", 3);
    await expect(page.getByRole("button", { name: "Deafen", exact: true })).toHaveAttribute("aria-pressed", "false");
    expect(await page.locator("#remote-video").evaluate((element) => element.muted)).toBe(false);
    await page.evaluate(() => window.__noxa.setDeafened(true));
    await emitMove("daniel", 0);
    expect(await page.evaluate(() => window.__noxa.state.deafened)).toBe(true);
    await emitMove("daniel", 2);
    expect(await page.evaluate(() => window.__noxa.state.deafened)).toBe(false);
});

test("undeafens when a snapshot confirms joining a channel", async ({ page }) => {
    await showB3Workspace(page);
    await page.evaluate(() => {
        window.__noxa.setDeafened(true);
        for (const callback of window.__events.snapshot || []) callback(JSON.stringify({
            root_channels: [{ ChannelID: 3, Name: "Gaming", clients: [{
                client_id: "daniel", unique_id: "uid-daniel", nickname: "Daniel", channel_id: 3,
            }], children: [] }],
        }));
    });
    await expect(page.getByRole("button", { name: "Deafen", exact: true })).toHaveAttribute("aria-pressed", "false");
    expect(await page.locator("#remote-video").evaluate((element) => element.muted)).toBe(false);
});

test("file browser filters names and sorts columns without refetching @a11y", async ({ page }, testInfo) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        window.__noxa.state.myChannelID = 42;
        window.__fileListResponse = {
            folders: ["Reports", "Archive"],
            entries: [
                { name: "report10.txt", size: 2, uploaded_at: "2026-09-13T12:00:00Z" },
                { name: "Alpha.txt", size: 100, uploaded_at: "2026-09-15T12:00:00Z" },
                { name: "report2.txt", size: 30, uploaded_at: "2026-09-14T12:00:00Z" },
            ],
        };
    });
    await page.locator("#tab-files").click();
    const names = page.locator(".fb-name");
    await expect(names).toHaveText(["Alpha.txt", "report2.txt", "report10.txt"]);
    const requests = await page.evaluate(() => window.__calls.FileList);
    const filter = page.getByRole("searchbox", { name: "Filter files by name" });
    await filter.fill(" REPORT ");
    await expect(names).toHaveText(["report2.txt", "report10.txt"]);
    await expect(page.locator(".fb-folder-link")).toHaveText(["Reports/"]);
    await filter.fill("missing");
    await expect(page.locator(".fb-list")).toContainText("No matching files or folders");
    await filter.press("Escape");
    await expect(names).toHaveCount(3);
    const size = page.getByRole("button", { name: "Sort by size", exact: true });
    await size.click();
    await expect(names).toHaveText(["report10.txt", "report2.txt", "Alpha.txt"]);
    await expect(size).toBeFocused();
    await expect(size.locator("..")).toHaveAttribute("aria-sort", "ascending");
    await size.press("Enter");
    await expect(names).toHaveText(["Alpha.txt", "report2.txt", "report10.txt"]);
    await expect(size.locator("..")).toHaveAttribute("aria-sort", "descending");
    const date = page.getByRole("button", { name: "Sort by date", exact: true });
    await date.click();
    await expect(names).toHaveText(["report10.txt", "report2.txt", "Alpha.txt"]);
    await date.click();
    await expect(names).toHaveText(["Alpha.txt", "report2.txt", "report10.txt"]);
    await page.getByRole("button", { name: "Sort by name", exact: true }).click();
    await page.getByRole("button", { name: "Sort by name", exact: true }).click();
    await expect(names).toHaveText(["report10.txt", "report2.txt", "Alpha.txt"]);
    await expect(page.locator(".fb-folder-link")).toHaveText(["Reports/", "Archive/"]);
    expect(await page.evaluate(() => window.__calls.FileList)).toBe(requests);
    await auditAccessibility(page, "file filtering and sorting");
    await page.screenshot({ path: testInfo.outputPath("file-controls.png") });
    await page.evaluate(() => {
        window.__fileListGate = new Promise(resolve => { window.__finishFileList = resolve; });
    });
    await page.getByRole("button", { name: "Refresh files", exact: true }).click();
    await filter.fill("report");
    await expect(names).toHaveCount(0);
    await expect(page.locator(".fb-list")).toContainText("Loading channel files");
    await page.evaluate(() => window.__finishFileList());
    await expect(names).toHaveText(["report10.txt", "report2.txt"]);
    await filter.fill("missing");
    await page.evaluate(() => {
        window.__fileListResponse = { entries: [], folders: [] };
    });
    await page.getByRole("button", { name: "Refresh files", exact: true }).click();
    await expect(page.locator(".fb-list")).toContainText("No matching files or folders");
    await filter.press("Escape");
    await expect(page.locator(".fb-list")).toContainText("Empty folder");
});

test("debug console preserves older entries while new frames arrive and jumps to latest", async ({ page }, testInfo) => {
    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        window.__noxaMeta.openDebugConsole();
        for (let i = 0; i < 200; i++) {
            for (const cb of window.__events.debug_frame) cb({ dir: "in", type: i % 2 ? "odd" : "even", payload: `frame ${i}` });
        }
    });
    const list = page.locator(".dbg-list");
    await expect(page.locator(".dbg-row").last()).toContainText("frame 199");
    await list.evaluate(el => { el.scrollTop = 500; });
    const anchor = await list.evaluate(el => {
        const row = [...el.children].find(row => row.getBoundingClientRect().bottom > el.getBoundingClientRect().top);
        return { text: row.querySelector(".dbg-payload").textContent, top: row.getBoundingClientRect().top };
    });
    await page.evaluate(() => {
        for (let i = 200; i < 210; i++) {
            for (const cb of window.__events.debug_frame) cb({ dir: "in", type: "sample", payload: `frame ${i}` });
        }
    });
    const row = page.locator(".dbg-row").filter({ has: page.locator(".dbg-payload", { hasText: new RegExp(`^${anchor.text}$`) }) });
    expect(Math.abs(await row.evaluate(el => el.getBoundingClientRect().top) - anchor.top)).toBeLessThan(2);
    await expect(page.locator(".dbg-row")).toHaveCount(200);
    const jump = page.getByRole("button", { name: "Jump to latest" });
    await expect(jump).toBeVisible();
    await page.screenshot({ path: testInfo.outputPath("debug-reading.png") });
    await jump.click();
    await expect(jump).toBeHidden();
    await page.evaluate(() => {
        for (const cb of window.__events.debug_frame) cb({ dir: "out", type: "sample", payload: "newest frame" });
    });
    await expect(page.locator(".dbg-row").last()).toContainText("newest frame");
    expect(await list.evaluate(el => el.scrollHeight - el.clientHeight - el.scrollTop)).toBeLessThan(3);
    await page.locator(".dbg-filter").fill("even");
    await expect(page.locator(".dbg-row b").first()).toHaveText("even");
    await page.locator(".dbg-filter").fill("");
    await expect(page.locator(".dbg-payload").first()).toHaveText("frame 11");
    await expect(page.locator(".dbg-payload").nth(1)).toHaveText("frame 12");
    await expect(page.locator(".dbg-payload").last()).toHaveText("newest frame");
    await page.keyboard.press("Escape");
    await expect(page.locator(".debug-console")).toHaveCount(0);
});

test("shows the files toolbar and opens the upload picker", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        window.__noxa.state.myChannelID = 42;
        window.__noxa.state.channels = [{ ChannelID: 42, Name: "Uploads" }];
    });

    await page.locator("#tab-files").click();
    await expect(page.locator("#files-pane")).toBeVisible();
    await expect(page.locator("#files-pane .fb-upload")).toBeVisible();
    await page.locator("#files-pane .fb-upload").click();
    await expect.poll(() => page.evaluate(() => window.__calls.PickUploadPaths || 0)).toBe(1);
});

test("saves chat attachments through the native bridge without a DOM data URL", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        const state = window.__noxa.state;
        state.myChannelID = 42;
        state.channels = [{ ChannelID: 42, Name: "Uploads" }];
        window.__saveAttachmentResult = "C:\\Downloads\\report.txt";
        let release;
        window.__saveAttachmentGate = new Promise((resolve) => { release = resolve; });
        window.__releaseSaveAttachment = release;
        for (const callback of window.__events.event || []) callback(JSON.stringify({
            type: "chat",
            data: {
                id: 99, channel_id: 42, from: "Alice", from_unique_id: "user-a",
                text: "[file:blob.vcx#dGVzdC1rZXk=#report.txt]",
            },
        }));
    });

    const chip = page.getByRole("button", { name: "📎 report.txt" });
    await expect(chip).toBeVisible();
    await chip.click();
    await expect(chip).toBeDisabled();
    await expect.poll(() => page.evaluate(() => window.__callArgs.SaveChatAttachment)).toEqual([
        [42, "blob.vcx", "dGVzdC1rZXk=", "report.txt"],
    ]);
    await expect(page.locator('a[href^="data:application/octet-stream;base64,"]')).toHaveCount(0);

    await page.evaluate(() => {
        window.__releaseSaveAttachment();
        window.__saveAttachmentGate = null;
    });
    await expect(chip).toBeEnabled();
});

test("opens About links externally once and ignores a late rejected version lookup", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        let release;
        window.__clientVersionGate = new Promise((resolve) => { release = resolve; });
        window.__releaseClientVersion = release;
        window.__clientVersionReject = true;
        window.__browserOpenThrow = true;
        window.__unhandled = [];
        window.addEventListener("unhandledrejection", (event) => window.__unhandled.push(String(event.reason)));
    });
    const help = page.locator("#menubar > .menu-item").filter({ hasText: /^Help/ });
    await help.click();
    await page.getByRole("menuitem", { name: /About noXa/ }).click();
    const about = page.locator(".dlg-overlay", { hasText: "About noXa" });
    const project = about.getByRole("link", { name: "project" });
    await expect(project).toHaveAttribute("href", "https://github.com/arumes31/noxa");
    await expect(project).toHaveAttribute("rel", "noopener noreferrer");
    await expect(about.getByRole("link", { name: "issues" })).toHaveAttribute("href", "https://github.com/arumes31/noxa/issues");

    const before = page.url();
    await page.evaluate(() => {
        const overlay = [...document.querySelectorAll(".dlg-overlay")]
            .find((el) => el.textContent.includes("About noXa"));
        overlay.querySelector(".about-links a").click();
        overlay.querySelector(".dlg-ok").click();
        window.__releaseClientVersion();
    });
    await expect.poll(() => page.evaluate(() => window.__browserURLs)).toEqual(["https://github.com/arumes31/noxa"]);
    expect(page.url()).toBe(before);
    await expect(about).toHaveCount(0);
    await page.waitForTimeout(0);
    expect(await page.evaluate(() => window.__unhandled)).toEqual([]);
});

test("retains failed chat drafts and only retries attachments that were not sent", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        window.__noxa.state.myChannelID = 42;
        window.__noxa.state.channels = [{ ChannelID: 42, Name: "Uploads" }];
    });
    await page.evaluate(() => {
        window.__noxaChat.addChat({
            id: 701, channel_id: 42, from: "Bob", from_unique_id: "user-b", text: "reply parent",
        });
    });
    await page.locator("#chat-log .msg").hover();
    await expect(page.locator("button[title=reply]")).toBeVisible();
    await page.locator("button[title=reply]").click();
    await page.locator("#chat-file").setInputFiles({ name: "once.txt", mimeType: "text/plain", buffer: Buffer.from("once") });
    await expect(page.locator("#file-preview-row")).not.toHaveClass(/hidden/);
    await page.locator("#chat-text").fill("draft reply");

    await page.evaluate(() => { window.__sendChatReplyResult = "slow mode"; });
    await page.locator("#chat-send").click();
    await expect(page.locator("#chat-text")).toHaveValue("draft reply");
    await expect(page.locator("#reply-bar")).not.toHaveClass(/hidden/);
    await expect(page.locator("#file-preview-row")).toHaveClass(/hidden/);
    expect(await page.evaluate(() => ({ upload: window.__calls.UploadChatAttachment, send: window.__calls.SendChat }))).toEqual({ upload: 1, send: 1 });

    await page.evaluate(() => {
        window.__sendChatReplyResult = "";
        window.__sendChatReplyReject = true;
    });
    await page.locator("#chat-send").click();
    await expect(page.locator("#chat-text")).toHaveValue("draft reply");
    await expect(page.locator("#reply-bar")).not.toHaveClass(/hidden/);

    await page.evaluate(() => { window.__sendChatReplyReject = false; });
    await page.locator("#chat-send").click();
    await expect(page.locator("#chat-text")).toHaveValue("");
    await expect(page.locator("#reply-bar")).toHaveClass(/hidden/);
    expect(await page.evaluate(() => ({
        upload: window.__calls.UploadChatAttachment,
        send: window.__calls.SendChat,
        reply: window.__calls.SendChatReply,
    }))).toEqual({ upload: 1, send: 1, reply: 3 });
});

test("requeues only upload and attachment-token failures without resending successful attachments", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        const state = window.__noxa.state;
        state.myChannelID = 42;
        state.channels = [{ ChannelID: 42, Name: "Uploads" }];
        window.__noxaChat.addChat({ id: 704, channel_id: 42, from: "Bob", text: "reply parent" });
        window.__uploadAttempts = {};
        window.__attachmentSendAttempts = {};
        window.__uploadAttachmentHandler = (_channelID, name) => {
            const count = (window.__uploadAttempts[name] || 0) + 1;
            window.__uploadAttempts[name] = count;
            if (name.startsWith("upload-once_") && count === 1) throw new Error("upload unavailable");
            return `[file:${name}.vcx#dGVzdA==#${name}]`;
        };
        window.__sendChatHandler = (_scope, _target, token) => {
            const name = token.match(/#([^#]+)\]$/)?.[1] || token;
            const count = (window.__attachmentSendAttempts[name] || 0) + 1;
            window.__attachmentSendAttempts[name] = count;
            return name.startsWith("token-once_") && count === 1 ? "token send failed" : "";
        };
        window.__sendChatReplyResult = "reply unavailable";
    });
    await page.locator("#chat-log .msg").hover();
    await page.locator("button[title=reply]").click();
    await page.locator("#chat-file").setInputFiles([
        { name: "upload-once.txt", mimeType: "text/plain", buffer: Buffer.from("upload") },
        { name: "token-once.txt", mimeType: "text/plain", buffer: Buffer.from("token") },
        { name: "successful.txt", mimeType: "text/plain", buffer: Buffer.from("success") },
    ]);
    await expect(page.locator("#file-preview-row .file-preview")).toHaveCount(3);
    await page.locator("#chat-text").fill("keep this reply draft");

    await page.locator("#chat-send").click();
    await expect(page.locator("#chat-text")).toHaveValue("keep this reply draft");
    await expect(page.locator("#reply-bar")).not.toHaveClass(/hidden/);
    await expect(page.locator("#file-preview-row .file-preview")).toHaveCount(2);
    await expect(page.locator("#file-preview-row")).toContainText(/upload-once_/);
    await expect(page.locator("#file-preview-row")).toContainText(/token-once_/);
    await expect(page.locator("#file-preview-row")).not.toContainText(/successful_/);

    await page.locator("#chat-send").click();
    await expect(page.locator("#chat-text")).toHaveValue("keep this reply draft");
    await expect(page.locator("#reply-bar")).not.toHaveClass(/hidden/);
    await expect(page.locator("#file-preview-row")).toHaveClass(/hidden/);

    expect(await page.evaluate(() => ({
        uploads: Object.entries(window.__uploadAttempts).map(([name, count]) => [name.replace(/_[^_]+_[^.]+(?=\.txt$)/, ""), count]).sort(),
        sends: Object.entries(window.__attachmentSendAttempts).map(([name, count]) => [name.replace(/_[^_]+_[^.]+(?=\.txt$)/, ""), count]).sort(),
    }))).toEqual({
        uploads: [["successful.txt", 1], ["token-once.txt", 2], ["upload-once.txt", 2]],
        sends: [["successful.txt", 1], ["token-once.txt", 2], ["upload-once.txt", 1]],
    });

    await page.evaluate(() => { window.__sendChatReplyResult = ""; });
    await page.locator("#chat-send").click();
    await expect(page.locator("#chat-text")).toHaveValue("");
    await expect(page.locator("#reply-bar")).toHaveClass(/hidden/);
    expect(await page.evaluate(() => Object.entries(window.__uploadAttempts)
        .map(([name, count]) => [name.replace(/_[^_]+_[^.]+(?=\.txt$)/, ""), count]).sort()))
        .toEqual([["successful.txt", 1], ["token-once.txt", 2], ["upload-once.txt", 2]]);
});

test("discards stale inline attachment previews and configures lazy image and video media", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        const state = window.__noxa.state;
        state.myChannelID = 42;
        state.channels = [{ ChannelID: 42, Name: "Uploads" }];
        window.__noxaChat.onMyChannelChanged();
        let release;
        window.__attachmentGate = new Promise((resolve) => { release = resolve; });
        window.__releaseAttachment = release;
        window.__attachmentData = "aGVsbG8=";
        window.__noxaChat.addChat({ id: 702, channel_id: 42, from: "Bob", text: "[file:stale.vcx#dGVzdA==#stale.png]" });
    });
    await page.evaluate(() => {
        window.__noxa.state.serverGeneration++;
        window.__releaseAttachment();
    });
    await page.waitForTimeout(0);
    await expect(page.locator(".msg-file img, .msg-file video")).toHaveCount(0);
    await expect(page.locator("[src^='data:image/'], [src^='data:video/']")).toHaveCount(0);

    await page.evaluate(() => {
        window.__attachmentGate = null;
        window.__noxaChat.addChat({ id: 703, channel_id: 42, from: "Bob", text: "[file:photo.vcx#dGVzdA==#photo.png] [file:clip.vcx#dGVzdA==#clip.webm]" });
    });
    const image = page.locator(".msg-file img.msg-img");
    const video = page.locator(".msg-file video.msg-video");
    await expect(image).toHaveAttribute("loading", "lazy");
    await expect(image).toHaveAttribute("decoding", "async");
    await expect(video).toHaveAttribute("preload", "none");
    await expect(video).not.toHaveAttribute("loading");
    await image.click();
    const imageLightbox = page.locator(".lightbox img");
    await expect(imageLightbox).toBeVisible();
    await expect(imageLightbox).toHaveAttribute("loading", "lazy");
    await expect(imageLightbox).toHaveAttribute("decoding", "async");
    await page.locator(".lightbox").click({ position: { x: 1, y: 1 } });
    await expect(page.locator(".lightbox")).toHaveCount(0);
    await page.locator(".msg-file:has(video) .media-zoom").click();
    await expect(page.locator(".lightbox video[controls][autoplay]")).toBeVisible();
});

test("does not construct stale attachment data URLs after channel or view changes", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        const state = window.__noxa.state;
        state.myChannelID = 42;
        state.channels = [
            { ChannelID: 42, Name: "Original" },
            { ChannelID: 43, Name: "Moved" },
        ];
        window.__attachmentDataURLWrites = [];
        window.__attachmentRequests = {};
        const instrument = (prototype) => {
            const descriptor = Object.getOwnPropertyDescriptor(prototype, "src");
            Object.defineProperty(prototype, "src", {
                configurable: true,
                enumerable: descriptor.enumerable,
                get: descriptor.get,
                set(value) {
                    if (String(value).startsWith("data:")) window.__attachmentDataURLWrites.push(String(value));
                    return descriptor.set.call(this, value);
                },
            });
            return descriptor;
        };
        const imageSrc = instrument(HTMLImageElement.prototype);
        const videoSrc = instrument(HTMLMediaElement.prototype);
        window.__restoreAttachmentSrc = () => {
            Object.defineProperty(HTMLImageElement.prototype, "src", imageSrc);
            Object.defineProperty(HTMLMediaElement.prototype, "src", videoSrc);
        };
        window.__downloadAttachmentHandler = (_channelID, storage) => new Promise((resolve, reject) => {
            window.__attachmentRequests[storage] = { resolve, reject };
        });
        window.__noxaChat.addChat({
            id: 705, channel_id: 42, from: "Bob", text: "[file:stale-resolve.vcx#dGVzdA==#photo.png]",
        });
    });
    await expect.poll(() => page.evaluate(() => Object.keys(window.__attachmentRequests))).toEqual(["stale-resolve.vcx"]);
    await page.evaluate(() => {
        window.__noxa.state.myChannelID = 43;
        window.__attachmentRequests["stale-resolve.vcx"].resolve("aGVsbG8=");
    });
    await page.waitForTimeout(0);
    expect(await page.evaluate(() => window.__attachmentDataURLWrites)).toEqual([]);

    await page.evaluate(() => {
        window.__noxaChat.addChat({
            id: 706, channel_id: 43, from: "Bob", text: "[file:stale-reject.vcx#dGVzdA==#photo.png]",
        });
    });
    await expect.poll(() => page.evaluate(() => Object.keys(window.__attachmentRequests).sort())).toEqual([
        "stale-reject.vcx", "stale-resolve.vcx",
    ]);
    await page.evaluate(() => {
        window.__noxaChat.openPM("user-b", "Bob");
        window.__attachmentRequests["stale-reject.vcx"].reject(new Error("download rejected"));
    });
    await page.waitForTimeout(0);
    expect(await page.evaluate(() => window.__attachmentDataURLWrites)).toEqual([]);
    expect(await page.evaluate(() => document.querySelectorAll(".msg-file img, .msg-file video").length)).toBe(0);
    await page.evaluate(() => {
        window.__restoreAttachmentSrc();
        delete window.__downloadAttachmentHandler;
    });
});

test("contains disconnect and ICE-candidate rejections and reports ICE exhaustion once per outage", async ({ page }) => {
    await page.evaluate(async () => {
        window.__unhandled = [];
        window.addEventListener("unhandledrejection", (event) => window.__unhandled.push(String(event.reason)));
        window.__noxa.state.settings.notify_connection = true;
        document.getElementById("conn-pill").classList.add("up");
        window.__disconnectReject = true;
        await window.__noxa.disconnect();

        const originalSetTimeout = window.setTimeout;
        const originalClearTimeout = window.clearTimeout;
        const iceTimers = [];
        const delays = [];
        window.setTimeout = (callback, delay, ...args) => {
            if ([1000, 2000, 5000, 15000].includes(delay)) {
                const timer = { delay, ran: false, cancelled: false, callback: () => callback(...args) };
                delays.push(delay);
                iceTimers.push(timer);
                return timer;
            }
            return originalSetTimeout(callback, delay, ...args);
        };
        window.clearTimeout = (timer) => {
            if (timer && iceTimers.includes(timer)) {
                timer.cancelled = true;
                return;
            }
            return originalClearTimeout(timer);
        };
        window.__restoreTimeout = () => {
            window.setTimeout = originalSetTimeout;
            window.clearTimeout = originalClearTimeout;
        };
        const runNextICETimer = async () => {
            const timer = iceTimers.find((entry) => !entry.ran && !entry.cancelled);
            if (!timer) throw new Error("expected an ICE retry timer");
            timer.ran = true;
            await timer.callback();
        };
        const audio = new AudioContext();
        window.__iceAudio = audio;
        const stream = audio.createMediaStreamDestination().stream;
        navigator.mediaDevices.getUserMedia = async () => stream;
        class FakePeerConnection {
            constructor() {
                this.senders = [];
                this.transceivers = [];
                this.iceConnectionState = "connected";
            }
            addTransceiver(track, options = {}) {
                const sender = {
                    track,
                    getParameters: () => ({ encodings: [{}] }),
                    setParameters: async () => {},
                    replaceTrack: async (next) => { sender.track = next; },
                };
                const transceiver = { sender, receiver: { track: null }, direction: options.direction || "sendrecv" };
                this.senders.push(sender);
                this.transceivers.push(transceiver);
                return transceiver;
            }
            getSenders() { return this.senders; }
            getTransceivers() { return this.transceivers; }
            async createOffer() { return { type: "offer", sdp: "ice-test" }; }
            async setLocalDescription() {}
            async setRemoteDescription() {}
            close() { this.iceConnectionState = "closed"; }
        }
        window.RTCPeerConnection = FakePeerConnection;
        const state = window.__noxa.state;
        state.myClientID = "client-a";
        state.myChannelID = 42;
        state.channels = [{ ChannelID: 42, Name: "Lobby" }];
        await window.__noxa.ensureVoiceForChannel();
        const oldPC = state.pc;
        window.__noxa.resetVoiceSession();
        await window.__noxa.ensureVoiceForChannel();
        const pc = state.pc;
        window.__iceSysMessagesBeforeRetries = document.querySelectorAll("#chat-log .msg.sys").length;
        window.__sendICECandidateReject = true;
        pc.onicecandidate({ candidate: { candidate: "candidate", sdpMid: "0", sdpMLineIndex: 0 } });
        pc.iceConnectionState = "failed";
        pc.oniceconnectionstatechange();
        const pendingBeforeOldEvents = iceTimers.filter((entry) => !entry.ran && !entry.cancelled).length;
        oldPC.iceConnectionState = "connected";
        oldPC.oniceconnectionstatechange();
        oldPC.iceConnectionState = "completed";
        oldPC.oniceconnectionstatechange();
        window.__oldPeerIsolation = {
            pendingBeforeOldEvents,
            pendingAfterOldEvents: iceTimers.filter((entry) => !entry.ran && !entry.cancelled).length,
        };
        for (let i = 0; i < 4; i++) await runNextICETimer();
        window.__terminalToastsBeforeRecovery = [...document.querySelectorAll("#toasts .toast")]
            .filter((toast) => toast.textContent.includes("Voice connection unstable")).length;
        pc.iceConnectionState = "connected";
        pc.oniceconnectionstatechange();
        pc.iceConnectionState = "failed";
        pc.oniceconnectionstatechange();
        for (let i = 0; i < 4; i++) await runNextICETimer();
        window.__iceRetryDelays = delays;
    });
    expect(await page.evaluate(() => window.__calls.Disconnect)).toBe(1);
    expect(await page.evaluate(() => window.__calls.SendICECandidate)).toBe(1);
    expect(await page.evaluate(() => window.__oldPeerIsolation)).toEqual({ pendingBeforeOldEvents: 1, pendingAfterOldEvents: 1 });
    expect(await page.evaluate(() => window.__iceRetryDelays)).toEqual([1000, 2000, 5000, 15000, 1000, 2000, 5000, 15000]);
    expect(await page.evaluate(() => window.__terminalToastsBeforeRecovery)).toBe(1);
    expect(await page.locator("#toasts .toast", { hasText: "Voice connection unstable" }).count()).toBe(2);
    expect(await page.locator("#toasts .toast", { hasText: "disconnect failed" }).count()).toBe(1);
    expect(await page.locator("#chat-log .msg.sys").count()).toBe(
        await page.evaluate(() => window.__iceSysMessagesBeforeRetries),
    );
    expect(await page.evaluate(() => window.__unhandled)).toEqual([]);
    await page.evaluate(() => {
        window.__restoreTimeout();
        window.__iceAudio?.close();
    });
});

test("does not let an old checksum restoration timer mutate a reset file view", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        window.__noxa.state.myChannelID = 42;
        window.__noxa.state.channels = [{ ChannelID: 42, Name: "Uploads" }];
        window.__fileListResponse = {
            entries: [{ name: "report.txt", size: 4, uploader: "user-a", uploaded_at: 1, sha256: "0123456789abcdef" }],
            folders: [], used_bytes: 4, quota_bytes: 100,
        };
    });
    await page.locator("#tab-files").click();
    const verify = page.locator(".fb-actions button[title='verify checksum (re-downloads and compares)']");
    await expect(verify).toBeVisible();
    await verify.click();
    const oldSHA = await page.evaluate(() => {
        const sha = document.querySelector(".fb-sha");
        window.__oldChecksumCell = sha;
        return sha.textContent;
    });
    expect(oldSHA).toBe("✓ ok");
    await page.evaluate(() => window.__noxaFiles.resetServerView());
    await page.waitForTimeout(4100);
    expect(await page.evaluate(() => window.__oldChecksumCell.textContent)).toBe("✓ ok");
});

test("keeps details contextual and opens it when a user is selected", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
    });
    await expect(page.locator("body")).toHaveClass(/details-collapsed/);
    await page.evaluate(() => {
        for (const cb of window.__events.snapshot || []) cb(JSON.stringify({
            root_channels: [{
                ChannelID: 1,
                ParentID: 0,
                Name: "Lobby",
                clients: [{
                    client_id: "client-a",
                    unique_id: "user-a",
                    nickname: "Alice",
                    channel_id: 1,
                    is_speaking: false,
                }],
                children: [],
            }],
        }));
    });
    await page.locator('.client[data-clid="client-a"]').click();
    await expect(page.locator("body")).not.toHaveClass(/details-collapsed/);
    await expect(page.locator("#client-card .card-nick")).toHaveText("Alice");
    await page.getByRole("button", { name: "Close details" }).click();
    await expect(page.locator("body")).toHaveClass(/details-collapsed/);
    await expect(page.locator("#details-toggle")).toBeVisible();
});

test("lets a guest redeem a privilege key and promotes the live session", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.state.myClientID = "guest-client";
        window.__noxa.state.isGuest = true;
        window.__noxaPerms.openTokenRedeem();
    });
    await expect(page.locator(".tk-use-input")).toBeVisible();
    await page.locator(".tk-use-input").fill("bootstrap-key");
    await page.getByRole("button", { name: "Redeem", exact: true }).click();
    await expect.poll(() => page.evaluate(() => window.__calls.TokenUse || 0)).toBe(1);

    await page.evaluate(() => {
        const event = JSON.stringify({
            type: "token_used",
            data: { client_id: "guest-client", group_id: 5, promoted: true },
        });
        for (const cb of window.__events.event || []) cb(event);
    });
    await expect.poll(() => page.evaluate(() => window.__noxa.state.isGuest)).toBe(false);
    await expect(page.locator(".toast")).toContainText("privilege key redeemed");
});

test("nests connected members below channels and offers them as direct-message targets", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        window.__noxa.state.myClientID = "client-a";
        window.__noxa.state.myChannelID = 1;
        for (const cb of window.__events.snapshot || []) cb(JSON.stringify({
            root_channels: [
                {
                    ChannelID: 1, ParentID: 0, Name: "Lobby",
                    clients: [
                        { client_id: "client-a", unique_id: "user-a", nickname: "Alice", channel_id: 1, is_speaking: false },
                        { client_id: "client-b", unique_id: "user-b", nickname: "Bob", channel_id: 1, is_speaking: true },
                    ],
                    children: [],
                },
                {
                    ChannelID: 2, ParentID: 0, Name: "Workshop",
                    clients: [
                        { client_id: "client-c", unique_id: "user-c", nickname: "Carol", channel_id: 2, is_speaking: true },
                    ],
                    children: [],
                },
            ],
        }));
    });

    const lobby = page.locator('.channel-node:has(> .channel[data-chid="1"])');
    await expect(lobby.locator(':scope > .channel-members > .client')).toHaveCount(2);
    await expect(page.locator('.channel[data-chid="1"] .client')).toHaveCount(0);
    await expect(page.locator('.client[data-clid="client-b"] .client-voice-state')).toBeVisible();
    await expect(page.locator('.client[data-clid="client-c"] .client-voice-state')).toHaveCount(0);

    await page.locator("#chat-scope").selectOption("direct");
    await page.locator("#chat-target").focus();
    await expect(page.locator("#chat-target-options .target-option")).toHaveCount(2);
    await page.locator("#chat-target-options .target-option", { hasText: "Carol" }).click();
    await expect(page.locator("#chat-target")).toHaveValue("user-c");
});

test("uses the B3 console composition without losing responsive navigation", async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        window.__tabs = [{
            id: "tab-a", addr: "127.0.0.1:12333", nickname: "Test",
            active: true, connected: true, unread: 0, mentions: 0,
        }];
        for (const cb of window.__events.tab_update || []) cb(structuredClone(window.__tabs));
        window.__noxa.state.myClientID = "client-a";
        window.__noxa.state.myChannelID = 1;
        for (const cb of window.__events.snapshot || []) cb(JSON.stringify({
            root_channels: [{
                ChannelID: 1, ParentID: 0, Name: "Main Lounge",
                clients: [
                    { client_id: "client-a", unique_id: "user-a", nickname: "Daniel", channel_id: 1, is_speaking: false },
                    { client_id: "client-b", unique_id: "user-b", nickname: "Benedikt", channel_id: 1, is_speaking: true },
                ],
                children: [{ ChannelID: 2, ParentID: 1, Name: "Alpha Squad", clients: [], children: [] }],
            }],
        }));
    });

    const desktop = await page.evaluate(() => ({
        accent: getComputedStyle(document.documentElement).getPropertyValue("--accent").trim(),
        appColumns: getComputedStyle(document.getElementById("app")).gridTemplateColumns,
        railDirection: getComputedStyle(document.getElementById("server-tabs")).flexDirection,
        texture: getComputedStyle(document.getElementById("center"), "::before").backgroundImage,
    }));
    expect(desktop.accent).toBe("#4ad8ed");
    expect(desktop.appColumns).toBe("1280px");
    expect(desktop.railDirection).toBe("row");
    expect(desktop.texture).toBe("none");
    const mainLounge = page.locator('.channel-node:has(> .channel[data-chid="1"])');
    await expect(mainLounge.locator(":scope > .channel-children")).toHaveAttribute("role", "group");
    await expect(mainLounge.locator(":scope > .channel-children")).toHaveAttribute("aria-label", "Main Lounge subchannels");
    await expect(page.locator("#server-tabs .srv-tab.active")).toBeVisible();
    await page.locator('.client[data-clid="client-a"]').click();

    await page.setViewportSize({ width: 700, height: 800 });
    await expect.poll(() => page.evaluate(
        () => getComputedStyle(document.getElementById("server-tabs")).flexDirection,
    )).toBe("row");
    await expect(page.locator("#center")).toBeVisible();
    await page.getByRole("button", { name: "Close details", exact: true }).click();
    await page.getByRole("button", { name: "Show channels", exact: true }).click();
    await expect(page.locator('.channel[data-chid="1"]')).toBeVisible();
});

test("exposes named landmarks, controls, live regions, and a visible focus ring", async ({ page }) => {
    await expect(page.getByRole("dialog", { name: "noxa" })).toBeVisible();
    await expect(page.getByRole("textbox", { name: /^server$/i })).toBeVisible();
    await expect(page.getByRole("button", { name: "Connect" })).toBeVisible();
    await expect(page.locator("#login-error")).toHaveAttribute("role", "alert");
    await expect(page.locator("#toasts")).toHaveAttribute("aria-label", "Notifications");
    await expect(page.locator("#voice-status")).toHaveAttribute("role", "status");
    await expect(page.locator("#chat-log")).toHaveAttribute("aria-live", "off");
    await expect(page.locator("#chat-announcer")).toHaveAttribute("aria-live", "polite");
    await expect(page.locator("#alert-announcer")).toHaveAttribute("aria-live", "assertive");
    await expect(page.locator("#conn-pill")).not.toHaveAttribute("aria-live", /.+/);

    await page.locator("#login-addr").focus();
    await expect.poll(() => page.locator("#login-addr").evaluate((el) => {
        const style = getComputedStyle(el);
        return `${style.outlineStyle} ${style.outlineWidth}`;
    })).toBe("solid 2px");

    await expect(page.locator(".skip-link")).toBeHidden();
    await page.evaluate(() => window.__noxa.showWorkspace(false));
    await page.locator(".skip-link").focus();
    await expect(page.locator(".skip-link")).toBeVisible();
    await page.evaluate(() => {
        const message = JSON.stringify({
            type: "chat",
            data: { id: 101, from: "Bob", from_unique_id: "user-b", text: "hello from Bob", channel_id: 0 },
        });
        for (const cb of window.__events.event || []) cb(message);
    });
    await expect(page.locator("#chat-announcer")).toHaveText("Bob: hello from Bob");
    await expect(page.locator("#alert-announcer")).toBeEmpty();
    await expect(page.locator("#toasts [role=status], #toasts [role=alert]")).toHaveCount(0);
    await expect(page.locator("#toasts .toast").first()).toHaveAttribute("aria-hidden", "true");
    await page.evaluate(() => { document.getElementById("chat-log").innerHTML = "<p>rerendered history</p>"; });
    await expect(page.locator("#chat-announcer")).toHaveText("Bob: hello from Bob");
});

test("@a11y audits primary login, workspace, settings, and permission-dialog states", async ({ page }) => {
    await auditAccessibility(page, "login");

    await page.evaluate(() => window.__noxa.showWorkspace(false));
    await auditAccessibility(page, "connected workspace");

    await page.evaluate(() => window.__noxa.openSettings("application"));
    await expect(page.getByRole("dialog", { name: "Settings" })).toBeVisible();
    await auditAccessibility(page, "settings dialog");
    await page.keyboard.press("Escape");

    await page.evaluate(() => {
        window.__noxa.state.isAdmin = true;
        window.__groups = { groups: [{ id: 7, name: "Operators", member_count: 0, color: "" }] };
        window.__permEntries = { entries: [{ key: "b_channel_join_permanent", value: 1, grant: 1, skip: false, negate: false }] };
        window.__noxaPerms.openPermissionManager();
    });
    await page.locator(".pm-target", { hasText: "Operators" }).click();
    await expect(page.locator(".pm-edit-grid")).toBeVisible();
    await auditAccessibility(page, "permission manager dialog");
});

test("does not delete a same-named file in a new channel after user_moved during confirmation", async ({ page }) => {
    await page.evaluate(() => {
        const state = window.__noxa.state;
        window.__noxa.showWorkspace(false);
        state.myClientID = "client-a";
        state.myChannelID = 1;
        state.channels = [
            { ChannelID: 1, ParentID: 0, Name: "Original" },
            { ChannelID: 2, ParentID: 0, Name: "New channel" },
        ];
        state.clients = [{ client_id: "client-a", unique_id: "user-a", nickname: "Alice", channel_id: 1 }];
        // Both channels deliberately contain this name. A stale confirmation
        // must not turn the old row into a delete for the new channel.
        window.__fileListResponse = {
            entries: [{ name: "same-name.txt", size: 1, uploaded_at: 0, uploader: "user-a", sha256: "abc" }],
            folders: [], used_bytes: 1, quota_bytes: 0,
        };
    });
    await page.locator("#tab-files").click();
    await expect(page.locator("#files-pane .fb-name")).toHaveText("same-name.txt");
    await page.locator('#files-pane .fb-actions button[title="delete"]').click();
    await expect(page.getByRole("dialog", { name: "Delete file?" })).toBeVisible();

    await page.evaluate(() => {
        const moved = JSON.stringify({ type: "user_moved", data: { client_id: "client-a", channel_id: 2 } });
        for (const callback of window.__events.event || []) callback(moved);
    });
    await page.getByRole("button", { name: "Delete file" }).click();
    await expect.poll(() => page.evaluate(() => window.__calls.FileDelete || 0)).toBe(0);
});

test("does not export a different channel after its passphrase dialog is left open", async ({ page }) => {
    await page.evaluate(() => {
        const state = window.__noxa.state;
        window.__noxa.showWorkspace(false);
        state.myClientID = "client-a";
        state.myChannelID = 1;
        state.channels = [
            { ChannelID: 1, ParentID: 0, Name: "Original" },
            { ChannelID: 2, ParentID: 0, Name: "New channel" },
        ];
        state.clients = [{ client_id: "client-a", unique_id: "user-a", nickname: "Alice", channel_id: 1 }];
    });
    await page.getByLabel("More channel actions", { exact: true }).click();
    await page.getByRole("button", { name: "Export chat history" }).click();
    await expect(page.getByRole("dialog", { name: "Export chat" })).toBeVisible();
    await page.locator('input[placeholder^="passphrase"]').fill("encrypted-export");
    await page.evaluate(() => {
        const moved = JSON.stringify({ type: "user_moved", data: { client_id: "client-a", channel_id: 2 } });
        for (const callback of window.__events.event || []) callback(moved);
    });
    await page.getByRole("button", { name: "Export", exact: true }).click();
    await expect.poll(() => page.evaluate(() => window.__calls.ChatExportHistory || 0)).toBe(0);
});

test("runtime boundaries ignore malformed payloads and never answer a stale offer", async ({ page }) => {
    const result = await page.evaluate(async () => {
        const errors = [];
        const onUnhandled = (event) => {
            errors.push(String(event.reason));
            event.preventDefault();
        };
        window.addEventListener("unhandledrejection", onUnhandled);
        const state = window.__noxa.state;
        state.myClientID = "client-a";
        state.clients = [];
        state.channels = [];
        state.serverGeneration = 40;
        let releaseOffer;
        const oldPeer = {
            ice: 0, remote: 0, answers: 0, local: 0,
            addIceCandidate: async () => { oldPeer.ice++; },
            setRemoteDescription: async () => {
                oldPeer.remote++;
                await new Promise((resolve) => { releaseOffer = resolve; });
            },
            createAnswer: async () => { oldPeer.answers++; return { type: "answer", sdp: "old-answer" }; },
            setLocalDescription: async () => { oldPeer.local++; },
        };
        state.pc = oldPeer;
        const emit = (name, payload) => {
            for (const callback of window.__events[name] || []) callback(payload);
        };
        for (const name of ["snapshot", "channellist", "event", "ice", "offer"]) {
            emit(name, "{");
            emit(name, "null");
            emit(name, "[]");
        }
        emit("snapshot", JSON.stringify({ root_channels: [] }));
        emit("channellist", JSON.stringify({ channels: [{ id: 7, name: "Valid channel" }] }));
        emit("event", JSON.stringify({ type: "user_joined", data: {
            client_id: "client-b", unique_id: "user-b", nickname: "Bob", channel_id: 7,
        } }));
        emit("ice", JSON.stringify({ candidate: "candidate", sdp_mid: "0", sdp_mline_index: 0 }));
        emit("offer", JSON.stringify({ sdp: "old-offer" }));
        await new Promise((resolve) => setTimeout(resolve, 0));
        state.serverGeneration++;
        state.pc = { replacement: true };
        releaseOffer();
        await new Promise((resolve) => setTimeout(resolve, 0));
        const staleAnswers = window.__calls.WebRTCAnswer || 0;
        for (const stage of ["remote", "answer", "local", "bridge"]) {
            state.serverGeneration++;
            window.__webRTCAnswerReject = stage === "bridge";
            state.pc = {
                addIceCandidate: async () => {},
                setRemoteDescription: async () => {
                    if (stage === "remote") throw new Error("remote rejected");
                },
                createAnswer: async () => {
                    if (stage === "answer") throw new Error("answer rejected");
                    return { type: "answer", sdp: "answer" };
                },
                setLocalDescription: async () => {
                    if (stage === "local") throw new Error("local rejected");
                },
            };
            emit("offer", JSON.stringify({ sdp: `${stage}-offer` }));
            await new Promise((resolve) => setTimeout(resolve, 0));
        }
        window.__webRTCAnswerReject = false;
        window.removeEventListener("unhandledrejection", onUnhandled);
        return {
            hasValidChannel: state.channels.some((channel) => channel.ChannelID === 7),
            hasValidEvent: state.clients.some((client) => client.client_id === "client-b"),
            ice: oldPeer.ice,
            remote: oldPeer.remote,
            local: oldPeer.local,
            staleAnswers,
            answers: window.__calls.WebRTCAnswer || 0,
            errors,
        };
    });
    expect(result).toEqual({
        hasValidChannel: true,
        hasValidEvent: true,
        ice: 1,
        remote: 1,
        local: 1,
        staleAnswers: 0,
        answers: 1,
        errors: [],
    });
});

test("serializes live-region bursts without coalescing identical messages", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        window.__spokenAnnouncements = [];
        for (const id of ["chat-announcer", "alert-announcer"]) {
            const region = document.getElementById(id);
            new MutationObserver(() => {
                if (region.textContent) window.__spokenAnnouncements.push([id, region.textContent]);
            }).observe(region, { childList: true, characterData: true, subtree: true });
        }
        window.__noxa.announceLive("repeated update");
        window.__noxa.announceLive("repeated update");
        window.__noxa.announceLive("urgent update", "assertive");
    });

    await expect.poll(() => page.evaluate(() => window.__spokenAnnouncements)).toEqual([
        ["chat-announcer", "repeated update"],
        ["alert-announcer", "urgent update"],
        ["chat-announcer", "repeated update"],
    ]);
});

test("announces only eligible chat in the visible scope", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        const state = window.__noxa.state;
        state.myClientID = "client-a";
        state.myUniqueID = "user-a";
        state.myNickname = "Alice";
        state.myChannelID = 1;
        state.lastConnect = { addr: "voice.example:12333" };
        state.settings.chat_notification_level = "all";
        state.settings.notify_matrix = {};
        state.settings.channel_notify = {};
        window.__spokenChat = [];
        const region = document.getElementById("chat-announcer");
        new MutationObserver(() => {
            if (region.textContent) window.__spokenChat.push(region.textContent);
        }).observe(region, { childList: true, characterData: true, subtree: true });
        for (const cb of window.__events.snapshot || []) cb(JSON.stringify({
            root_channels: [
                { ChannelID: 1, ParentID: 0, Name: "Lobby", clients: [], children: [] },
                { ChannelID: 2, ParentID: 0, Name: "Elsewhere", clients: [], children: [] },
            ],
        }));
        const dispatch = (data) => {
            const message = JSON.stringify({ type: "chat", data });
            for (const callback of window.__events.event || []) callback(message);
        };
        dispatch({ id: 201, from: "Bob", from_unique_id: "user-b", text: "inactive scope", channel_id: 2 });
        state.settings.channel_notify["voice.example:12333#1"] = { muted: true };
        dispatch({ id: 202, from: "Bob", from_unique_id: "user-b", text: "muted", channel_id: 1 });
        delete state.settings.channel_notify["voice.example:12333#1"];
        state.settings.dnd_enabled = true;
        dispatch({ id: 203, from: "Bob", from_unique_id: "user-b", text: "dnd", channel_id: 1 });
        state.settings.dnd_enabled = false;
        state.settings.chat_notification_level = "direct";
        dispatch({ id: 204, from: "Bob", from_unique_id: "user-b", text: "category filtered", channel_id: 1 });
        state.settings.chat_notification_level = "all";
        state.settings.notify_matrix.channel_message = { toast: false, sound: false, flash: false, native: false };
        dispatch({ id: 205, from: "Bob", from_unique_id: "user-b", text: "matrix filtered", channel_id: 1 });
    });
    await page.waitForTimeout(450);
    expect(await page.evaluate(() => window.__spokenChat)).toEqual([]);

    await page.evaluate(() => {
        window.__noxa.state.settings.notify_matrix = {};
        const message = JSON.stringify({
            type: "chat",
            data: { id: 206, from: "Bob", from_unique_id: "user-b", text: "visible message", channel_id: 1 },
        });
        for (const callback of window.__events.event || []) callback(message);
    });
    await expect.poll(() => page.evaluate(() => window.__spokenChat)).toEqual(["Bob: visible message"]);
});

test("summarizes visible offline replay instead of announcing every message", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        window.__noxa.state.myUniqueID = "user-a";
        window.__noxa.state.myNickname = "Alice";
        window.__noxa.state.settings.notify_matrix = {};
        window.__noxa.state.settings.dnd_enabled = false;
        window.__noxaChat.openPM("user-b", "Bob");
        window.__offlineSpoken = [];
        const region = document.getElementById("chat-announcer");
        new MutationObserver(() => {
            if (region.textContent) window.__offlineSpoken.push(region.textContent);
        }).observe(region, { childList: true, characterData: true, subtree: true });
        const dispatch = (id, uid, from, text) => {
            const message = JSON.stringify({
                type: "chat",
                data: { id, from, from_unique_id: uid, text, e2e: true, offline: true, client_msg_id: `offline-${id}` },
            });
            for (const callback of window.__events.event || []) callback(message);
        };
        dispatch(301, "user-c", "Carol", "inactive offline message");
        dispatch(302, "user-b", "Bob", "one");
        dispatch(303, "user-b", "Bob", "two");
        dispatch(304, "user-b", "Bob", "three");
    });

    await expect.poll(() => page.evaluate(() => window.__offlineSpoken)).toEqual([
        "3 offline messages from Bob",
    ]);
});

test("summarizes a visible reconnect burst instead of announcing every message", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        const state = window.__noxa.state;
        state.myClientID = "client-a";
        state.myUniqueID = "user-a";
        state.myNickname = "Alice";
        state.myChannelID = 1;
        state.settings.chat_notification_level = "all";
        state.settings.notify_matrix = {};
        state.settings.channel_notify = {};
        state.settings.dnd_enabled = false;
        window.__reconnectSpoken = [];
        const region = document.getElementById("chat-announcer");
        new MutationObserver(() => {
            if (region.textContent) window.__reconnectSpoken.push(region.textContent);
        }).observe(region, { childList: true, characterData: true, subtree: true });
        for (const callback of window.__events.snapshot || []) callback(JSON.stringify({
            root_channels: [
                { ChannelID: 1, ParentID: 0, Name: "Lobby", clients: [], children: [] },
            ],
        }));
        window.__noxaChat.beginReconnectAnnouncementBatch(2000);
        for (let id = 401; id <= 403; id++) {
            const message = JSON.stringify({
                type: "chat",
                data: {
                    id,
                    from: "Bob",
                    from_unique_id: "user-b",
                    text: `replayed ${id}`,
                    channel_id: 1,
                },
            });
            for (const callback of window.__events.event || []) callback(message);
        }
    });

    await expect.poll(() => page.evaluate(() => window.__reconnectSpoken)).toEqual([
        "3 messages from Bob received after reconnect",
    ]);
});

test("cancels stale reconnect batches when switching server tabs", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        const state = window.__noxa.state;
        state.myClientID = "client-a";
        state.myUniqueID = "user-a";
        state.myNickname = "Alice";
        state.myChannelID = 1;
        state.settings.chat_notification_level = "all";
        state.settings.notify_matrix = {};
        state.settings.channel_notify = {};
        state.settings.dnd_enabled = false;
        window.__resetSpoken = [];
        const region = document.getElementById("chat-announcer");
        new MutationObserver(() => {
            if (region.textContent) window.__resetSpoken.push(region.textContent);
        }).observe(region, { childList: true, characterData: true, subtree: true });
        const snapshot = () => {
            for (const callback of window.__events.snapshot || []) callback(JSON.stringify({
                root_channels: [
                    { ChannelID: 1, ParentID: 0, Name: "Lobby", clients: [], children: [] },
                ],
            }));
        };
        const dispatch = (id, text) => {
            const message = JSON.stringify({
                type: "chat",
                data: { id, from: "Bob", from_unique_id: "user-b", text, channel_id: 1 },
            });
            for (const callback of window.__events.event || []) callback(message);
        };
        snapshot();
        window.__noxaChat.beginReconnectAnnouncementBatch(3000);
        dispatch(451, "old server replay");
        for (const callback of window.__events.tab_reset || []) callback("manual-switch");
        state.myClientID = "client-a";
        state.myChannelID = 1;
        snapshot();
        dispatch(452, "new server message");
    });

    await expect.poll(() => page.evaluate(() => window.__resetSpoken)).toEqual([
        "Bob: new server message",
    ]);
    await page.waitForTimeout(850);
    expect(await page.evaluate(() => window.__resetSpoken.some((text) => text.includes("after reconnect")))).toBe(false);
});

test("preserves reconnect batching across the tab created by a real reconnect", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        const state = window.__noxa.state;
        state.settings.chat_notification_level = "all";
        state.settings.notify_matrix = {};
        state.settings.channel_notify = {};
        state.settings.dnd_enabled = false;
        window.__preservedReconnectSpoken = [];
        const region = document.getElementById("chat-announcer");
        new MutationObserver(() => {
            if (region.textContent) window.__preservedReconnectSpoken.push(region.textContent);
        }).observe(region, { childList: true, characterData: true, subtree: true });
        window.__noxaChat.beginReconnectAnnouncementBatch(3000);
        state.reconnectInFlight = true;
        for (const callback of window.__events.tab_reset || []) callback("reconnected-tab");
        state.reconnectInFlight = false;
        state.myClientID = "client-a";
        state.myUniqueID = "user-a";
        state.myNickname = "Alice";
        state.myChannelID = 1;
        for (const callback of window.__events.snapshot || []) callback(JSON.stringify({
            root_channels: [
                { ChannelID: 1, ParentID: 0, Name: "Lobby", clients: [], children: [] },
            ],
        }));
        for (let id = 461; id <= 462; id++) {
            const message = JSON.stringify({
                type: "chat",
                data: { id, from: "Bob", from_unique_id: "user-b", text: `replayed ${id}`, channel_id: 1 },
            });
            for (const callback of window.__events.event || []) callback(message);
        }
    });

    await expect.poll(() => page.evaluate(() => window.__preservedReconnectSpoken)).toEqual([
        "2 messages from Bob received after reconnect",
    ]);
});

test("keeps reconnect countdown changes visual and announces the failure once", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        delete window.__noxa.state.settings.reconnect_on_loss;
        window.__noxa.state.settings.notify_connection = true;
        window.__noxa.state.lastConnect = { addr: "voice.example:12333", nick: "Alice", pw: "", spw: "" };
        for (const callback of window.__events.disconnected || []) callback();
    });
    await expect(page.locator("#conn-pill")).toContainText("retry 1/5 in 5s");
    await expect(page.locator("#alert-announcer")).toHaveText("Connection lost");
    await expect(page.locator("#conn-pill")).not.toHaveAttribute("aria-live", /.+/);
    await page.waitForTimeout(1100);
    await expect(page.locator("#conn-pill")).toContainText("retry 1/5 in 4s");
    await expect(page.locator("#alert-announcer")).toHaveText("Connection lost");
});

test("dispatches one DM notification only for actual E2EE direct messages", async ({ page }) => {
    await page.evaluate(() => {
        const originalNotify = window.__noxaNotify.notify;
        window.__notificationDispatches = [];
        window.__noxaNotify.notify = (event, text, context) => {
            window.__notificationDispatches.push(event);
            return originalNotify(event, text, context);
        };
        Object.defineProperty(document, "hasFocus", { configurable: true, value: () => false });
        window.__noxa.state.myNickname = "Alice";
        window.__noxa.state.myUniqueID = "user-a";
        window.__noxa.state.clients = [
            { client_id: "client-b", unique_id: "user-b", nickname: "Bob", channel_id: 0 },
            { client_id: "client-c", unique_id: "user-c", nickname: "Carol", channel_id: 0 },
        ];

        const direct = JSON.stringify({
            type: "chat",
            data: {
                id: 801, from: "Bob", from_client_id: "client-b", from_unique_id: "user-b",
                text: "private hello", e2e: true, client_msg_id: "dm-801",
            },
        });
        const global = JSON.stringify({
            type: "chat",
            data: {
                id: 802, from: "Carol", from_client_id: "client-c", from_unique_id: "user-c",
                text: "global hello", channel_id: 0, e2e: false,
            },
        });
        for (const callback of window.__events.event || []) callback(direct);
        for (const callback of window.__events.event || []) callback(global);
    });

    await expect.poll(() => page.evaluate(() => window.__notificationDispatches)).toEqual([
        "dm",
        "channel_message",
    ]);
    expect(await page.evaluate(() => window.__notificationDispatches.filter((event) => event === "dm").length)).toBe(1);
    expect(await page.evaluate(() => window.__calls.TrayMention || 0)).toBe(1);
    expect(await page.evaluate(() => window.__noxa.state.lastWhispererUID)).toBe("user-b");
});

test("renders hostile update, permission, and image metadata as inert data", async ({ page }) => {
    const attack = `<img src=x onerror="document.body.dataset.remoteXss='yes'">`;
    await page.evaluate(async (payload) => {
        window.__permissions = [{
            key: payload,
            value: 7,
            skip: true,
            negate: false,
            inherited: true,
            source_tier: payload,
        }];
        window.__updateInfo = { available: true, version: payload, size: 1048576 };
        await window.__noxa.refreshPermissions();
        await window.__noxa.checkForUpdatesInteractive();
    }, attack);

    expect(await page.evaluate(() => document.body.dataset.remoteXss || "")).toBe("");
    await expect(page.locator("#perm-area tbody .mono")).toHaveText(attack);
    await expect(page.locator("#perm-area tbody tr")).toHaveAttribute("title", `effective from ${attack} (inherited)`);
    await expect(page.locator(".upd-status")).toHaveText(`update available: ${attack} (1.0 MiB)`);

    await page.evaluate(async (payload) => {
        const host = document.createElement("span");
        host.className = "avatar hostile-avatar";
        host.dataset.uid = "hostile-user";
        document.body.appendChild(host);
        window.__avatarResponse = {
            content_type: `image/png\" onerror=\"document.body.dataset.remoteXss='image'`,
            data_base64: "AAAA",
        };
        await window.__noxa.fetchAvatar("hostile-user");
        window.__noxa.state.avatars.delete("valid-user");
        window.__noxa.state.avatarPending.delete("valid-user");
        const validHost = document.createElement("span");
        validHost.className = "avatar valid-avatar";
        validHost.dataset.uid = "valid-user";
        document.body.appendChild(validHost);
        window.__avatarResponse = { content_type: "image/png", data_base64: "AAAA" };
        await window.__noxa.fetchAvatar("valid-user");
        void payload;
    }, attack);

    expect(await page.evaluate(() => document.body.dataset.remoteXss || "")).toBe("");
    await expect(page.locator(".hostile-avatar img")).toHaveCount(0);
    expect(await page.evaluate(() => window.__noxa.state.avatars.get("hostile-user"))).toBe(null);
    await expect(page.locator(".valid-avatar img")).toHaveAttribute("src", "data:image/png;base64,AAAA");
});

test("channel permissions show the channel join requirement and edit its real setting", async ({ page }, testInfo) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        window.__noxa.state.isAdmin = true;
        for (const cb of window.__events.snapshot || []) cb(JSON.stringify({
            root_channels: [
                { ChannelID: 2, Name: "Member1", NeededJoinPower: 30, clients: [], children: [] },
                { ChannelID: 3, Name: "Public", NeededJoinPower: 0, clients: [], children: [] },
            ],
        }));
        // An old permission override must not mask the enforced channel setting.
        window.__permEntries = { entries: [{ key: "i_channel_needed_join_power", value: 99, grant: 99 }] };
        window.__noxaPerms.openPermissionManager();
    });
    const manager = page.getByRole("dialog", { name: "Permission Manager", exact: true });
    await manager.getByRole("button", { name: "Channel", exact: true }).click();
    await manager.locator(".pm-target", { hasText: "Member1" }).click();
    await manager.getByPlaceholder("filter permissions…").fill("i_channel_needed_join_power");
    const row = manager.locator(".pm-edit-grid tbody tr").filter({ has: page.locator("td.mono", { hasText: "i_channel_needed_join_power" }) });
    await expect(row.locator("td").nth(1)).toHaveText("30");
    await expect(row.locator("td").nth(2)).toHaveText("—");
    await expect(row).toContainText("channel setting");
    await manager.locator(".pm-target", { hasText: "Public" }).click();
    await expect(row.locator("td").nth(1)).toHaveText("0");
    await manager.locator(".pm-target", { hasText: "Member1" }).click();
    await row.click();
    await expect(manager.locator(".pe-set, .pe-unset, .pe-grant")).toHaveCount(0);
    await manager.screenshot({ path: testInfo.outputPath("channel-join-permission.png") });
    await manager.getByRole("button", { name: "Edit channel…", exact: true }).click();
    const editor = page.getByRole("dialog", { name: "Edit channel", exact: true });
    await expect(editor.getByLabel("Required join power", { exact: true })).toHaveValue("30");
    await expect(editor.getByLabel("Required join power", { exact: true })).toBeFocused();
    await editor.getByLabel("Required join power", { exact: true }).fill("40");
    await editor.getByRole("button", { name: "Save changes" }).click();
    await expect(editor).toHaveCount(0);
    expect(await page.evaluate(() => window.__callArgs.ChannelEditTree[0])).toEqual([2, "join_power", 40, 0, 0, false]);
    expect(await page.evaluate(() => window.__calls.PermSet || 0)).toBe(0);
    await page.evaluate(() => {
        for (const cb of window.__events.event || []) cb(JSON.stringify({
            type: "channel_updated", data: { channel_id: 2, needed_join_power: 40 },
        }));
    });
    await expect(row.locator("td").nth(1)).toHaveText("40");
    await page.evaluate(() => {
        window.__noxa.state.isAdmin = false;
        window.__noxa.state.myPerms = new Map([["b_permission_manage", { value: 1 }]]);
        for (const cb of window.__events.event || []) cb(JSON.stringify({
            type: "channel_updated", data: { channel_id: 2, needed_join_power: 40, inherit_permissions: true },
        }));
    });
    await expect(manager.getByText(/Parent join-power requirements also apply/)).toBeVisible();
    await expect(manager.getByRole("button", { name: "Edit channel…", exact: true })).toBeDisabled();
});

test("renders editable permission keys as inert text", async ({ page }) => {
    const key = '<span data-permission-key-injection="true">unexpected node</span>';
    await page.evaluate(({ permissionKey }) => {
        window.__noxa.state.isAdmin = true;
        window.__groups = { groups: [{ id: 7, name: "Operators", member_count: 0, color: "" }] };
        window.__permEntries = {
            entries: [{ key: permissionKey, value: 7, grant: 5, skip: true, negate: false }],
        };
        window.__noxaPerms.openPermissionManager();
    }, { permissionKey: key });

    await page.locator(".pm-target", { hasText: "Operators" }).click();
    const keyCell = page.locator(".pm-edit-grid tbody tr.set td.mono");
    await expect(keyCell).toHaveText(key);
    await expect(page.locator('[data-permission-key-injection="true"]')).toHaveCount(0);

    await keyCell.click();
    await expect(page.locator(".pm-editor-row .pe-value")).toHaveValue("7");
    await expect(page.locator(".pm-editor-row .pe-grant")).toHaveValue("5");
    await page.locator(".pm-editor-row .pe-set").click();
    await expect.poll(() => page.evaluate(() => window.__lastPermSet?.[4])).toBe(key);
});

test("invalidates server dialogs and delayed responses when the active tab changes", async ({ page }) => {
    const oldKey = "old_server_permission";
    const newKey = "new_server_permission";
    await page.evaluate(({ staleKey }) => {
        window.__noxa.state.isAdmin = true;
        window.__groups = { groups: [{ id: 7, name: "Old Operators", member_count: 0, color: "" }] };
        window.__permEntries = { entries: [{ key: staleKey, value: 7, grant: 7, skip: false, negate: false }] };
        let releasePermList;
        window.__permListGate = new Promise((resolve) => { releasePermList = resolve; });
        window.__releaseOldPermList = releasePermList;
        let releaseClientInfo;
        window.__clientInfoGate = new Promise((resolve) => { releaseClientInfo = resolve; });
        window.__releaseOldClientInfo = releaseClientInfo;
        window.__clientInfoResponse = {
            nickname: "Old Alice", unique_id: "old-user", connected_at: Date.now() / 1000 - 60,
            idle_seconds: 1, ping_ms: 20, ip: "127.0.0.7", port: 12333, bytes_in: 7, bytes_out: 7,
        };
        window.__noxaPerms.openPermissionManager();
    }, { staleKey: oldKey });

    await page.locator(".pm-target", { hasText: "Old Operators" }).click();
    await expect.poll(() => page.evaluate(() => window.__calls.PermList || 0)).toBe(1);
    await page.evaluate(() => {
        window.__noxa.openClientInfo({ client_id: "old-client", unique_id: "old-user", nickname: "Old Alice" });
        window.__noxaPerms.openTokenManager();
        window.__noxaPerms.openAuditViewer();
        window.__noxaPerms.openBanList();
    });
    await expect.poll(() => page.evaluate(() => window.__calls.GetClientInfo || 0)).toBeGreaterThan(0);
    await expect(page.locator(".dlg-overlay").filter({ hasText: "Permission Manager" })).toHaveCount(1);
    await expect(page.locator(".dlg-overlay").filter({ hasText: "Privilege Keys" })).toHaveCount(1);
    await expect(page.locator(".dlg-overlay").filter({ hasText: "Audit Log" })).toHaveCount(1);
    await expect(page.locator(".dlg-overlay").filter({ hasText: "Bans" })).toHaveCount(1);
    await expect(page.locator(".dlg-overlay").filter({ hasText: "Connection Info" })).toHaveCount(1);

    await page.evaluate(({ freshKey }) => {
        window.__groups = { groups: [{ id: 9, name: "New Operators", member_count: 0, color: "" }] };
        window.__permEntries = { entries: [{ key: freshKey, value: 9, grant: 9, skip: false, negate: false }] };
        window.__clientInfoResponse = {
            nickname: "New Bob", unique_id: "new-user", connected_at: Date.now() / 1000 - 30,
            idle_seconds: 2, ping_ms: 9, ip: "127.0.0.9", port: 12333, bytes_in: 9, bytes_out: 9,
        };
        for (const callback of window.__events.tab_reset || []) callback("tab-b");
        window.__noxa.state.isAdmin = true;
    }, { freshKey: newKey });

    await expect(page.locator(".dlg-overlay")).toHaveCount(0);
    await page.evaluate(() => window.__noxaPerms.openPermissionManager());
    await page.locator(".pm-target", { hasText: "New Operators" }).click();
    await expect(page.locator(".pm-edit-grid tbody tr.set td.mono")).toHaveText(newKey);
    await page.evaluate(() => {
        window.__noxa.openClientInfo({ client_id: "new-client", unique_id: "new-user", nickname: "New Bob" });
    });
    await expect(page.getByRole("dialog", { name: "Connection Info" }).locator('[data-f="nick"]')).toHaveText("New Bob");

    await page.evaluate(() => {
        window.__releaseOldPermList();
        window.__releaseOldClientInfo();
    });
    await page.waitForTimeout(100);
    await expect(page.getByRole("dialog", { name: "Connection Info" }).locator('[data-f="nick"]')).toHaveText("New Bob");
    await page.getByRole("dialog", { name: "Connection Info" }).getByRole("button", { name: "Close" }).click();
    await expect(page.locator(".pm-edit-grid tbody tr.set td.mono")).toHaveText(newKey);
    await expect(page.locator(".pm-edit-grid tbody tr.set td.mono")).not.toHaveText(oldKey);
    await page.locator(".pm-edit-grid tbody tr.set td.mono").click();
    await page.locator(".pm-editor-row .pe-set").click();
    await expect.poll(() => page.evaluate(() => window.__lastPermSet)).toEqual([
        "server_group", 9, "", 0, newKey, 9, 9, false, false,
    ]);
});

test("cancels server-bound image actions across active-tab resets", async ({ page }) => {
    const image = {
        name: "one-pixel.png",
        mimeType: "image/png",
        buffer: Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=", "base64"),
    };
    const prompts = [];
    page.on("dialog", async (dialog) => {
        prompts.push(dialog.message());
        await dialog.accept("late-emoji");
    });
    const connect = () => page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        window.__noxa.state.myClientID = "client-a";
        window.__noxa.state.isAdmin = true;
    });
    const reset = (tabID) => page.evaluate((id) => {
        for (const callback of window.__events.tab_reset || []) callback(id);
    }, tabID);
    const selfMenu = page.locator("#menubar > .menu-item > span").filter({ hasText: /^Self$/ }).locator("..");
    const chooseFromSelf = async (name) => {
        await selfMenu.click();
        const pending = page.waitForEvent("filechooser");
        await page.getByRole("menuitem", { name }).click();
        return pending;
    };

    // A crop dialog that already exists is scoped to the old server and closes
    // as part of the reset. Closing resolves the picker as cancelled.
    await connect();
    const cropChooser = await chooseFromSelf(/^Set avatar/);
    await cropChooser.setFiles(image);
    await expect(page.getByRole("dialog", { name: "Set avatar" })).toBeVisible();
    await reset("avatar-dialog-reset");
    await expect(page.getByRole("dialog", { name: "Set avatar" })).toHaveCount(0);
    await expect.poll(() => page.evaluate(() => window.__calls.SetAvatar || 0)).toBe(0);

    // A reset while the native picker is open must not allow the crop dialog
    // to mount late under the new generation.
    await connect();
    const lateCropChooser = await chooseFromSelf(/^Set avatar/);
    await reset("avatar-picker-reset");
    await lateCropChooser.setFiles(image);
    await page.waitForTimeout(300);
    await expect(page.getByRole("dialog", { name: "Set avatar" })).toHaveCount(0);
    expect(await page.evaluate(() => window.__calls.SetAvatar || 0)).toBe(0);

    // Server icon compression and quick-emoji upload have no DOM dialog after
    // file selection, so their caller-owned generation tokens block the write.
    await connect();
    const iconChooser = await chooseFromSelf(/^Set server icon/);
    await reset("server-icon-reset");
    await iconChooser.setFiles(image);
    await page.waitForTimeout(300);
    expect(await page.evaluate(() => window.__calls.ServerIconSet || 0)).toBe(0);

    await connect();
    await page.locator("#chat-emoji").click();
    const emojiChooserPromise = page.waitForEvent("filechooser");
    await page.locator(".emoji-upload").click();
    const emojiChooser = await emojiChooserPromise;
    await reset("emoji-picker-reset");
    await emojiChooser.setFiles(image);
    await page.waitForTimeout(300);
    expect(prompts).toEqual([]);
    expect(await page.evaluate(() => window.__calls.EmojiUpload || 0)).toBe(0);
});

test("drops late ban-lift responses without refreshing the new server", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        window.__noxa.state.isAdmin = true;
        window.__bans = { bans: [{
            id: 17,
            value: "old-user",
            reason: "old server ban",
            banned_by: "old-admin",
            expires_at: 0,
        }] };
        window.__banRemoveResult = "old server rejected the lift";
        let release;
        window.__banRemoveGate = new Promise((resolve) => { release = resolve; });
        window.__releaseBanRemove = release;
        window.__noxaPerms.openBanList();
    });
    await expect(page.locator(".ban-lift")).toHaveCount(1);
    await page.locator(".ban-lift").click();
    await expect(page.getByRole("dialog", { name: "Lift ban" })).toBeVisible();
    await page.evaluate(() => {
        for (const callback of window.__events.tab_reset || []) callback("ban-confirm-reset");
    });
    await expect(page.locator(".dlg-overlay")).toHaveCount(0);
    expect(await page.evaluate(() => window.__calls.BanRemove || 0)).toBe(0);

    // Once an old-server write is already in flight it cannot be cancelled,
    // but its response must not toast or schedule a list read on the new tab.
    await page.evaluate(() => {
        window.__noxa.state.isAdmin = true;
        let release;
        window.__banRemoveGate = new Promise((resolve) => { release = resolve; });
        window.__releaseBanRemove = release;
        window.__noxaPerms.openBanList();
    });
    await expect(page.locator(".ban-lift")).toHaveCount(1);
    await page.locator(".ban-lift").click();
    await page.getByRole("dialog", { name: "Lift ban" }).getByRole("button", { name: "Lift", exact: true }).click();
    await expect.poll(() => page.evaluate(() => window.__calls.BanRemove || 0)).toBe(1);
    await expect.poll(() => page.evaluate(() => window.__calls.BanList || 0)).toBe(2);

    await page.evaluate(() => {
        for (const callback of window.__events.tab_reset || []) callback("ban-reset");
        window.__releaseBanRemove();
    });
    await expect(page.locator(".dlg-overlay")).toHaveCount(0);
    await page.waitForTimeout(600);
    expect(await page.evaluate(() => window.__calls.BanList || 0)).toBe(2);
    await expect(page.locator("#toasts")).not.toContainText("old server rejected the lift");
});

test("supports keyboard menus and restores focus after a trapped modal", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
    });

    const tools = page.locator("#menubar > .menu-item > span").filter({ hasText: /^Tools$/ }).locator("..");
    await tools.focus();
    await page.keyboard.press("Enter");
    await expect(tools).toHaveAttribute("aria-expanded", "true");
    await expect(page.getByRole("menuitem", { name: /^Settings/ })).toBeFocused();
    await page.keyboard.press("Enter");

    const dialog = page.getByRole("dialog", { name: "Settings" });
    await expect(dialog).toBeVisible();
    await expect.poll(() => page.locator("#app").evaluate((el) => el.inert)).toBe(true);
    await expect(page.locator("#settings-page-application")).toBeFocused();

    await page.keyboard.press("Shift+Tab");
    await expect(page.locator("#set-apply")).toBeFocused();
    await page.keyboard.press("Tab");
    await expect(page.locator("#settings-page-application")).toBeFocused();

    await page.keyboard.press("Escape");
    await expect(dialog).toHaveCount(0);
    await expect(tools).toBeFocused();
    await expect.poll(() => page.locator("#app").evaluate((el) => el.inert)).toBe(false);
});

test("removes an unfinished hotkey capture when settings closes", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        window.__noxa.openSettings("hotkeys");
    });
    const capture = page.locator("#settings-content .hotkey-capture").first();
    await capture.click();
    await expect(capture).toHaveClass(/capturing/);
    await page.keyboard.press("Escape");
    await expect(page.getByRole("dialog", { name: "Settings" })).toHaveCount(0);

    const prevented = await page.evaluate(() => {
        const event = new KeyboardEvent("keydown", { key: "K", bubbles: true, cancelable: true });
        return !document.dispatchEvent(event);
    });
    expect(prevented).toBe(false);
});

test("activates tree rows and workspace views from the keyboard with loading feedback", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        window.__noxa.state.myClientID = "client-a";
        window.__noxa.state.myChannelID = 1;
        for (const cb of window.__events.snapshot || []) cb(JSON.stringify({
            root_channels: [{
                ChannelID: 1, ParentID: 0, Name: "Lobby",
                clients: [{ client_id: "client-a", unique_id: "user-a", nickname: "Alice", channel_id: 1 }],
                children: [],
            }],
        }));
    });

    const client = page.locator('.client[data-clid="client-a"]');
    await client.focus();
    await page.keyboard.press("Space");
    await expect(page.locator("body")).not.toHaveClass(/details-collapsed/);
    await expect(client).toHaveAttribute("aria-selected", "true");
    await expect(client).toBeFocused();

    const tablist = page.getByRole("tablist", { name: "Workspace views" });
    await expect(tablist).toBeVisible();
    expect(await tablist.locator("#tab-transfers").count()).toBe(0);
    await expect(page.locator("#tab-transfers")).toHaveAttribute("aria-haspopup", "dialog");
    await expect(page.locator("#tab-chat")).toHaveAttribute("aria-controls", "chat-pane");
    await expect(page.locator("#tab-chat")).toHaveAttribute("aria-selected", "true");
    await expect(page.locator("#tab-chat")).toHaveAttribute("tabindex", "0");
    await expect(page.locator("#tab-files")).toHaveAttribute("aria-controls", "files-pane");
    await expect(page.locator("#tab-files")).toHaveAttribute("aria-selected", "false");
    await expect(page.locator("#tab-files")).toHaveAttribute("tabindex", "-1");
    await expect(page.locator("#chat-pane")).toHaveAttribute("role", "tabpanel");
    await expect(page.locator("#chat-pane")).toHaveAttribute("aria-labelledby", "tab-chat");
    await expect(page.locator("#files-pane")).toHaveAttribute("role", "tabpanel");
    await expect(page.locator("#files-pane")).toHaveAttribute("aria-labelledby", "tab-files");

    await page.evaluate(() => {
        let release;
        window.__fileListGate = new Promise((resolve) => { release = resolve; });
        window.__releaseFileList = release;
    });
    await page.locator("#tab-chat").focus();
    await page.keyboard.press("ArrowRight");
    await expect(page.locator("#tab-files")).toHaveAttribute("aria-selected", "true");
    await expect(page.locator("#tab-files")).toHaveAttribute("tabindex", "0");
    await expect(page.locator("#tab-chat")).toHaveAttribute("tabindex", "-1");
    expect(await page.evaluate(() => ({
        chatSelected: document.getElementById("tab-chat").getAttribute("aria-selected"),
        filesSelected: document.getElementById("tab-files").getAttribute("aria-selected"),
        chatHidden: document.getElementById("chat-pane").hidden,
        filesHidden: document.getElementById("files-pane").hidden,
    }))).toEqual({ chatSelected: "false", filesSelected: "true", chatHidden: true, filesHidden: false });
    await expect(page.locator("#files-pane .fb-list")).toHaveAttribute("aria-busy", "true");
    await expect(page.locator('#files-pane .fb-list [role="status"]')).toContainText("Loading channel files");

    await page.evaluate(() => {
        window.__releaseFileList();
        window.__fileListGate = null;
    });
    await expect(page.locator("#files-pane .fb-list")).not.toHaveAttribute("aria-busy", "true");
    await expect(page.locator("#files-pane .empty-state")).toContainText("Empty folder");
    await page.keyboard.press("ArrowLeft");
    await expect(page.locator("#tab-chat")).toHaveAttribute("aria-selected", "true");
    await expect(page.locator("#chat-pane")).toBeVisible();
    await expect(page.locator("#files-pane")).toBeHidden();
    await page.keyboard.press("End");
    await expect(page.locator("#tab-files")).toHaveAttribute("aria-selected", "true");
    await page.keyboard.press("Home");
    await expect(page.locator("#tab-chat")).toHaveAttribute("aria-selected", "true");
});

test("notification center updates live and resumes unread counting after every close path", async ({ page }) => {
    await page.evaluate(() => window.__noxa.showWorkspace(false));
    const bell = page.locator("#notif-bell");
    const rows = page.locator(".nc-row");
    for (const close of ["button", "escape", "backdrop", "remove"]) {
        await bell.click();
        await page.getByRole("button", { name: "Clear all notifications" }).click();
        await expect(page.locator(".nc-list")).toHaveText("no notifications");
        await page.evaluate(() => window.__noxaPolish.recordNotification("message", "new arrival"));
        await expect(rows).toHaveCount(1);
        await expect(rows.first()).toContainText("new arrival");
        await expect(bell).toHaveAttribute("aria-label", "Notifications, 0 unread");
        await expect(page.locator("#notif-badge")).toHaveClass(/hidden/);
        if (close === "button") await page.getByRole("button", { name: "Close notifications" }).click();
        if (close === "escape") await page.keyboard.press("Escape");
        if (close === "backdrop") await page.locator(".dlg-overlay").click({ position: { x: 2, y: 2 } });
        await page.evaluate((close) => {
            if (close === "remove") document.querySelector(".notif-center").closest(".dlg-overlay").remove();
            // Direct removal and arrival can happen before lifecycle observers run.
            window.__noxaPolish.recordNotification("message", "after closing");
        }, close);
        await expect(page.locator(".notif-center")).toHaveCount(0);
        await expect(bell).toHaveAttribute("aria-label", "Notifications, 1 unread");
    }
});

test("live notifications preserve focused rows and scrolling while limiting history to 50", async ({ page }, testInfo) => {
    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        for (let i = 0; i < 50; i++) window.__noxaPolish.recordNotification("message", `arrival ${i}`, { uid: `user-${i}` });
    });
    await page.locator("#notif-bell").click();
    const focused = page.locator(".nc-row").filter({ has: page.locator(".nc-text", { hasText: /^arrival 30$/ }) });
    await focused.focus();
    const before = await focused.evaluate(row => row.getBoundingClientRect().top);
    await page.evaluate(() => window.__noxaPolish.recordNotification("message", "latest arrival", { uid: "latest" }));
    await expect(page.locator(".nc-row")).toHaveCount(50);
    await expect(page.locator(".nc-row").first()).toContainText("latest arrival");
    await expect(focused).toBeFocused();
    expect(Math.abs(await focused.evaluate(row => row.getBoundingClientRect().top) - before)).toBeLessThan(2);
    await expect(page.locator(".nc-text").filter({ hasText: /^arrival 0$/ })).toHaveCount(0);
    await page.screenshot({ path: testInfo.outputPath("notifications-live.png") });
    await page.locator(".nc-row").last().focus();
    await page.evaluate(() => window.__noxaPolish.recordNotification("message", "one more arrival"));
    await expect(page.getByRole("button", { name: "Close notifications" })).toBeFocused();
});

test("keeps unread notification labels exact while the visual badge is capped", async ({ page }) => {
    await page.evaluate(() => window.__noxa.showWorkspace(false));
    const bell = page.locator("#notif-bell");
    const badge = page.locator("#notif-badge");
    await expect(bell).toHaveAttribute("aria-label", "Notifications, 0 unread");
    await expect(badge).toHaveClass(/hidden/);

    await page.evaluate(() => window.__noxaPolish.recordNotification("message", "one"));
    await expect(bell).toHaveAttribute("aria-label", "Notifications, 1 unread");
    await expect(badge).toHaveText("1");

    await page.evaluate(() => {
        for (let i = 2; i <= 12; i++) window.__noxaPolish.recordNotification("message", String(i));
    });
    await expect(bell).toHaveAttribute("aria-label", "Notifications, 12 unread");
    await expect(badge).toHaveText("9+");

    await bell.click();
    await expect(page.locator(".notif-center")).toBeVisible();
    await expect(bell).toHaveAttribute("aria-label", "Notifications, 0 unread");
    await expect(badge).toHaveClass(/hidden/);
    await page.getByRole("button", { name: "Clear all notifications" }).click();
    await expect(bell).toHaveAttribute("aria-label", "Notifications, 0 unread");
    await expect(badge).toHaveText("");
});

test("keeps global announcements available in Files and restores chat for compact and zen modes", async ({ page }) => {
    await page.evaluate(() => window.__noxa.showWorkspace(false));
    const workspaceState = () => page.evaluate(() => ({
        chatSelected: document.getElementById("tab-chat").getAttribute("aria-selected"),
        filesSelected: document.getElementById("tab-files").getAttribute("aria-selected"),
        chatTabIndex: document.getElementById("tab-chat").tabIndex,
        filesTabIndex: document.getElementById("tab-files").tabIndex,
        chatHidden: document.getElementById("chat-pane").hidden,
        filesHidden: document.getElementById("files-pane").hidden,
    }));
    const chatActive = {
        chatSelected: "true", filesSelected: "false",
        chatTabIndex: 0, filesTabIndex: -1,
        chatHidden: false, filesHidden: true,
    };
    const activeElement = () => page.evaluate(() => {
        const active = document.activeElement;
        const style = active ? getComputedStyle(active) : null;
        return {
            id: active?.id || "",
            visible: Boolean(active && active !== document.body && active.isConnected && !active.hidden && !active.closest("[hidden]") &&
                style?.display !== "none" && style?.visibility !== "hidden" && active.getClientRects().length),
            unselectedFilesTab: active?.id === "tab-files" && active.tabIndex === -1,
        };
    });
    const expectRecoveredFocus = async () => {
        expect(await activeElement()).toEqual({ id: "voice-mute", visible: true, unselectedFilesTab: false });
    };

    await page.locator("#tab-files").click();
    await expect(page.locator("#files-pane")).toBeVisible();
    await expect(page.locator("#tab-files")).toBeFocused();
    await page.evaluate(() => {
        window.__noxa.announceLive("Connection warning while browsing files", "assertive");
        window.__noxa.announceLive("Status while browsing files", "polite");
    });
    await expect(page.locator("#alert-announcer")).toHaveText("Connection warning while browsing files");
    await expect(page.locator("#chat-announcer")).toHaveText("Status while browsing files");
    await expect(page.locator("#alert-announcer")).toBeVisible();
    expect(await page.evaluate(() => {
        const chatPane = document.getElementById("chat-pane");
        const filesPane = document.getElementById("files-pane");
        return ["chat-announcer", "alert-announcer"].every((id) => {
            const region = document.getElementById(id);
            return !chatPane.contains(region) && !filesPane.contains(region) && !region.hidden;
        });
    })).toBe(true);

    await page.evaluate(() => window.__noxa.toggleCompact());
    await expect(page.locator("body")).toHaveClass(/compact/);
    await expect(page.locator("#voice-bar")).toBeVisible();
    expect(await workspaceState()).toEqual(chatActive);
    await expectRecoveredFocus();
    await page.evaluate(() => window.__noxa.toggleCompact());
    await expect(page.locator("body")).not.toHaveClass(/compact/);
    expect(await workspaceState()).toEqual(chatActive);
    await expectRecoveredFocus();

    await page.locator("#tab-files").click();
    await expect(page.locator("#files-pane .fb-upload")).toBeVisible();
    await page.locator("#files-pane .fb-upload").focus();
    await expect(page.locator("#files-pane .fb-upload")).toBeFocused();
    await page.evaluate(() => window.__noxaPolish.toggleZen());
    await expect(page.locator("body")).toHaveClass(/zen/);
    await expect(page.locator("#voice-bar")).toBeVisible();
    expect(await workspaceState()).toEqual(chatActive);
    await expectRecoveredFocus();
    await page.evaluate(() => window.__noxaPolish.toggleZen());
    await expect(page.locator("body")).not.toHaveClass(/zen/);
    expect(await workspaceState()).toEqual(chatActive);
    await expectRecoveredFocus();
});

test("moves focus explicitly between login and the connected workspace", async ({ page }, testInfo) => {
    await expect(page.locator("#login-addr")).toBeFocused();
    await expect(page.locator(".skip-link")).toBeHidden();
    await expect(page.locator("#login-serverpw")).toHaveAttribute("autocomplete", "off");
    await expect(page.locator(".login-card input[type=password]")).toHaveCount(1);
    await expect(page.locator("#login-serverpw")).toHaveAccessibleName("SERVER PASSWORD");
    await page.locator(".login-card").screenshot({ path: testInfo.outputPath("login.png") });

    await page.locator("#login-nick").fill("Alice");
    await page.locator("#login-serverpw").fill("server-secret");
    await page.getByRole("button", { name: "Connect" }).click();
    await expect(page.locator("#center")).toBeFocused();
    expect(await page.evaluate(() => window.__callArgs.ConnectBookmarkTabWithID[0])).toEqual([
        "", "127.0.0.1:12333", "Alice", "", "server-secret",
    ]);
    await expect(page.locator("#app")).toHaveAttribute("aria-hidden", "false");
    await expect(page.locator(".skip-link")).toBeAttached();

    await page.evaluate(() => window.__noxa.showLogin());
    await expect(page.locator("#login-addr")).toBeFocused();
    await expect(page.locator("#app")).toHaveAttribute("aria-hidden", "true");

    await page.evaluate(() => {
        window.__tabs = [{
            id: "auto-tab", addr: "auto.example:12333", nickname: "Alice",
            active: true, connected: true, unread: 0, mentions: 0,
        }];
        for (const callback of window.__events.tab_update || []) callback(structuredClone(window.__tabs));
    });
    await expect(page.locator("#center")).toBeFocused();
    await expect(page.locator("#app")).toHaveAttribute("aria-hidden", "false");
});

test("computes names for settings and generated dialog controls", async ({ page }) => {
    await page.evaluate(() => window.__noxa.openSettings("application"));
    await expect(page.locator('#settings-content input[type="number"]').first()).toHaveAccessibleName("Chat max lines");
    await expect(page.locator("#settings-content select").first()).toHaveAccessibleName("Language");
    await expect(page.getByRole("combobox", { name: "Theme", exact: true })).toBeVisible();
    await expect(page.locator('#settings-content input[type="range"]').first()).toHaveAccessibleName("UI font size");
    await page.keyboard.press("Escape");

    await page.evaluate(() => window.__noxa.showWorkspace());
    await page.locator("#channel-create-btn").click();
    const create = page.getByRole("dialog", { name: "Create channel" });
    await expect(create.locator(".cc-name")).toHaveAccessibleName("Name");
    await expect(create.locator(".cc-type")).toHaveAccessibleName("Type");
    await expect(create.locator(".cc-maxclients")).toHaveAccessibleName("Max clients (0 = unlimited)");
    await page.keyboard.press("Escape");
});

test("keeps long channel dialogs within a small window and scrolls to their actions", async ({ page }) => {
    // Layout regression from the native 1024x768 window. Bindings are mocked;
    // this checks the shared dialog layout, not real channel creation.
    await page.setViewportSize({ width: 1024, height: 730 });
    await page.evaluate(() => window.__noxa.showWorkspace());
    await page.getByRole("button", { name: "Create channel", exact: true }).click();
    const dialog = page.getByRole("dialog", { name: "Create channel" });
    const panel = dialog.locator(".dlg");
    await panel.evaluate(async (element) => {
        await document.fonts.ready;
        await Promise.all(element.getAnimations().map((animation) => animation.finished));
    });
    const bounds = await panel.boundingBox();
    expect(bounds.y).toBeGreaterThanOrEqual(0);
    expect(bounds.y + bounds.height).toBeLessThanOrEqual(730);
    await expect(dialog.getByRole("heading", { name: "Create channel" })).toBeInViewport({ ratio: 1 });
    const name = dialog.getByRole("textbox", { name: "Name", exact: true });
    await name.fill("Small window room");
    const create = dialog.getByRole("button", { name: "Create", exact: true });
    const cancel = dialog.getByRole("button", { name: "Cancel", exact: true });
    await page.keyboard.press("Shift+Tab");
    await expect(cancel).toBeFocused();
    await expect(cancel).toBeInViewport({ ratio: 1 });
    await page.keyboard.press("Tab");
    await expect(name).toBeFocused();
    await expect(name).toBeInViewport({ ratio: 1 });
    await page.keyboard.press("Shift+Tab");
    await page.keyboard.press("Shift+Tab");
    await expect(create).toBeFocused();
    await expect(create).toBeInViewport({ ratio: 1 });
    await expect(cancel).toBeInViewport({ ratio: 1 });
    await expect.poll(() => panel.evaluate((element) => element.scrollTop)).toBeGreaterThan(0);
    await page.keyboard.press("Enter");
    await expect(dialog).toHaveCount(0);
    await expect.poll(() => page.evaluate(() => window.__callArgs.CreateChannel?.[0]?.[0])).toBe("Small window room");
});

async function openChannelEditor(page) {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        for (const cb of window.__events.snapshot || []) cb(JSON.stringify({
            root_channels: [{ ChannelID: 2, Name: "Public", Topic: "Everyone welcome", ParentID: 0,
                OpusBitrate: 32000, OpusFEC: true, OpusDTX: true, OrderIndex: 1, clients: [], children: [] }],
        }));
    });
    if (!(await page.locator("#sidebar").isVisible())) await page.getByRole("button", { name: "Show channels", exact: true }).click();
    await page.locator('.channel[data-chid="2"]').click({ button: "right" });
    await page.getByText("Edit channel", { exact: true }).click();
    return page.getByRole("dialog", { name: "Edit channel", exact: true });
}

test("channel editor keeps its title and actions visible at small window sizes @a11y", async ({ page }) => {
    for (const viewport of [{ width: 1000, height: 730 }, { width: 640, height: 480 }]) {
        await page.setViewportSize(viewport);
        const dialog = await openChannelEditor(page);
        await expect(dialog.getByRole("heading", { name: "Edit channel" })).toBeInViewport({ ratio: 1 });
        await expect(dialog.getByRole("button", { name: "Save changes" })).toBeInViewport({ ratio: 1 });
        await expect(dialog.getByRole("button", { name: "Cancel", exact: true })).toBeInViewport({ ratio: 1 });
        for (const summary of await dialog.locator("summary").all()) await summary.click();
        await dialog.locator(".ce-order").fill("3");
        await expect(dialog.getByRole("heading", { name: "Edit channel" })).toBeInViewport({ ratio: 1 });
        await expect(dialog.getByRole("button", { name: "Save changes" })).toBeInViewport({ ratio: 1 });
        const dimensions = await dialog.locator(".channel-edit").evaluate((el) => ({ width: el.clientWidth, scrollWidth: el.scrollWidth }));
        expect(dimensions.scrollWidth).toBeLessThanOrEqual(dimensions.width);
        await auditAccessibility(page, "channel editor");
        await page.keyboard.press("Escape");
        await expect(dialog).toHaveCount(0);
    }
});

test("channel editor preserves input on failure and prevents duplicate saves", async ({ page }) => {
    const dialog = await openChannelEditor(page);
    await dialog.locator(".ce-topic").fill("New topic");
    await dialog.getByText("Access & placement", { exact: true }).click();
    await dialog.locator(".ce-order").fill("4");
    await page.evaluate(() => { window.__channelEditError = "Permission denied"; });
    await dialog.getByRole("button", { name: "Save changes" }).click();
    await expect(dialog.getByRole("alert")).toContainText("Permission denied");
    await expect(dialog.locator(".ce-topic")).toHaveValue("New topic");
    expect(await page.evaluate(() => window.__calls.ChannelEditTree || 0)).toBe(0);
    await page.evaluate(() => {
        window.__channelEditError = "";
        window.__channelEditGate = new Promise((resolve) => { window.__releaseChannelEdit = resolve; });
    });
    await dialog.getByRole("button", { name: "Save changes" }).click();
    await expect(dialog.getByRole("button", { name: "Saving…" })).toBeDisabled();
    await page.evaluate(() => { window.__releaseChannelEdit(); });
    await expect(dialog).toHaveCount(0);
    expect(await page.evaluate(() => window.__calls.ChannelEdit)).toBe(2);
    expect(await page.evaluate(() => window.__callArgs.ChannelEditTree[0])).toEqual([2, "order", 0, 4, 0, false]);
});

test("channel editor saves presets without rewriting unchanged placement", async ({ page }) => {
    const dialog = await openChannelEditor(page);
    await dialog.locator(".ce-preset").selectOption("music");
    await dialog.getByRole("button", { name: "Save changes" }).click();
    await expect(dialog).toHaveCount(0);
    expect(await page.evaluate(() => window.__callArgs.ChannelEdit[0])).toEqual([2, "Everyone welcome", 0, 128000, true, false, true, "", 0]);
    expect(await page.evaluate(() => window.__calls.ChannelEditTree || 0)).toBe(0);
});

test("closes menus when keyboard focus exits and keeps expansion state in sync", async ({ page }) => {
    await page.evaluate(() => window.__noxa.showWorkspace(false));
    const tools = page.locator("#menubar > .menu-item").filter({ hasText: /^Tools/ });
    await tools.focus();
    await page.keyboard.press("Enter");
    await expect(tools).toHaveAttribute("aria-expanded", "true");
    await expect(page.getByRole("menuitem", { name: /^Settings/ })).toBeFocused();

    await page.keyboard.press("Tab");
    await expect(tools).toHaveAttribute("aria-expanded", "false");
    await expect(tools.locator(".menu-dropdown")).not.toHaveClass(/open/);
});

test("uses standard Left and Right behavior in the channel tree", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        window.__noxa.state.myClientID = "client-a";
        for (const cb of window.__events.snapshot || []) cb(JSON.stringify({
            root_channels: [{
                ChannelID: 1, ParentID: 0, Name: "Parent", clients: [],
                children: [{
                    ChannelID: 2, ParentID: 1, Name: "Child",
                    clients: [{ client_id: "client-a", unique_id: "user-a", nickname: "Alice", channel_id: 2 }],
                    children: [],
                }],
            }],
        }));
    });

    const parent = page.locator('.channel[data-chid="1"]');
    const child = page.locator('.channel[data-chid="2"]');
    await parent.focus();
    await page.keyboard.press("ArrowLeft");
    await expect(parent).toHaveAttribute("aria-expanded", "false");
    await expect(child).toHaveCount(0);

    await page.keyboard.press("ArrowRight");
    await expect(parent).toHaveAttribute("aria-expanded", "true");
    await page.keyboard.press("ArrowRight");
    await expect(child).toBeFocused();
    await page.keyboard.press("ArrowLeft");
    await expect(child).toHaveAttribute("aria-expanded", "false");
    await page.keyboard.press("ArrowLeft");
    await expect(parent).toBeFocused();
});

test("maintains a nested dialog stack across media, rerenders, and zero-control dialogs", async ({ page }) => {
    await page.evaluate(async () => {
        window.__noxa.showWorkspace(false);
        const { mountDialog } = await import("/src/modal.js");
        const launcher = document.getElementById("chat-info-btn");
        launcher.closest("details").open = true;
        launcher.classList.remove("hidden");
        launcher.focus();

        const outer = document.createElement("div");
        outer.className = "dlg-overlay";
        const renderOuter = (step) => {
            outer.innerHTML = `<div class="dlg"><h3>Outer step ${step}</h3><button class="open-media">Open media</button><button class="next-step">Next</button></div>`;
            outer.querySelector(".open-media").onclick = () => {
                const inner = document.createElement("div");
                inner.className = "dlg-overlay";
                inner.innerHTML = '<div class="dlg"><h3>Media preview</h3><video controls aria-label="Preview media"></video></div>';
                mountDialog(inner);
            };
            outer.querySelector(".next-step").onclick = () => renderOuter(step + 1);
        };
        renderOuter(1);
        mountDialog(outer);
    });

    const outer = page.locator('.dlg-overlay[aria-labelledby]:has-text("Outer step")');
    await expect(page.getByRole("dialog", { name: "Outer step 1" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Open media" })).toBeFocused();
    await page.getByRole("button", { name: "Open media" }).click();
    await expect(page.getByRole("dialog", { name: "Media preview" })).toBeVisible();
    await expect(page.getByLabel("Preview media")).toBeFocused();
    await expect(outer).toHaveJSProperty("inert", true);

    await page.keyboard.press("Escape");
    await expect(page.getByRole("dialog", { name: "Media preview" })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Open media" })).toBeFocused();
    await page.getByRole("button", { name: "Next" }).click();
    await expect(page.getByRole("dialog", { name: "Outer step 2" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Open media" })).toBeFocused();

    await page.keyboard.press("Escape");
    await expect(page.locator("#chat-info-btn")).toBeFocused();
    await page.evaluate(async () => {
        const { mountDialog } = await import("/src/modal.js");
        const empty = document.createElement("div");
        empty.className = "dlg-overlay";
        empty.innerHTML = '<div class="dlg"><h3>Working</h3><p>Please wait.</p></div>';
        mountDialog(empty);
    });
    const zero = page.getByRole("dialog", { name: "Working" });
    await expect(zero).toBeFocused();
    await page.keyboard.press("Escape");
    await expect(zero).toHaveCount(0);
    await expect(page.locator("#chat-info-btn")).toBeFocused();
});

test("falls back from invalid modal focus and restores login after a workspace transition", async ({ page }) => {
    await page.evaluate(async () => {
        window.__noxa.showWorkspace(false);
        const { mountDialog } = await import("/src/modal.js");
        const launcher = document.getElementById("chat-info-btn");
        launcher.closest("details").open = true;
        launcher.classList.remove("hidden");
        launcher.focus();
        const overlay = document.createElement("div");
        overlay.className = "dlg-overlay transition-dialog";
        overlay.innerHTML = `
            <div class="dlg">
                <h3>Transition focus</h3>
                <div hidden><button class="hidden-target">Hidden target</button></div>
                <button class="visible-target">Visible target</button>
            </div>`;
        mountDialog(overlay, { launcher, initialFocus: ".hidden-target" });
    });
    await expect(page.locator(".visible-target")).toBeFocused();

    await page.evaluate(() => window.__noxa.showLogin());
    await page.keyboard.press("Escape");
    await expect(page.locator(".transition-dialog")).toHaveCount(0);
    await expect(page.locator("#login-addr")).toBeFocused();
});

test("finalizes a dialog removed inside an ancestor subtree exactly once", async ({ page }) => {
    await page.evaluate(async () => {
        window.__noxa.showWorkspace(false);
        const { mountDialog } = await import("/src/modal.js");
        window.__subtreeDialogCloses = 0;
        const wrapper = document.createElement("section");
        wrapper.id = "dialog-wrapper";
        document.body.appendChild(wrapper);
        const overlay = document.createElement("div");
        overlay.className = "dlg-overlay subtree-dialog";
        overlay.innerHTML = '<div class="dlg"><h3>Subtree dialog</h3><button>Ready</button></div>';
        mountDialog(overlay, { onClose: () => { window.__subtreeDialogCloses++; } });
        wrapper.appendChild(overlay);
    });
    await expect(page.getByRole("dialog", { name: "Subtree dialog" })).toBeVisible();
    await page.evaluate(() => document.getElementById("dialog-wrapper").remove());
    await expect(page.locator(".subtree-dialog")).toHaveCount(0);
    await expect.poll(() => page.evaluate(() => window.__subtreeDialogCloses)).toBe(1);
    await page.waitForTimeout(100);
    expect(await page.evaluate(() => window.__subtreeDialogCloses)).toBe(1);
});

test("keeps a blocking gate visually and semantically above deferred dialogs", async ({ page }) => {
    await page.evaluate(async () => {
        window.__noxa.showWorkspace(false);
        for (const callback of window.__events.server_rules || []) {
            callback(JSON.stringify({ hash: "rules-v1", text: "Be excellent to each other." }));
        }
        const { mountDialog } = await import("/src/modal.js");
        const deferred = document.createElement("div");
        deferred.className = "dlg-overlay deferred-dialog";
        deferred.innerHTML = '<div class="dlg"><h3>Deferred reminder</h3><button>Continue</button></div>';
        mountDialog(deferred);
    });

    const gate = page.locator(".server-rules-gate");
    const deferred = page.locator(".deferred-dialog");
    await expect(gate).toHaveAttribute("aria-modal", "true");
    await expect(gate).not.toHaveAttribute("aria-hidden", "true");
    await expect(deferred).toHaveAttribute("aria-hidden", "true");
    await expect.poll(() => gate.evaluate((element) => element.inert)).toBe(false);
    await expect.poll(() => deferred.evaluate((element) => element.inert)).toBe(true);
    const [gateZ, deferredZ, skipLinkZ] = await page.evaluate(() => [
        Number(getComputedStyle(document.querySelector(".server-rules-gate")).zIndex),
        Number(getComputedStyle(document.querySelector(".deferred-dialog")).zIndex),
        Number(getComputedStyle(document.querySelector(".skip-link")).zIndex),
    ]);
    expect(gateZ).toBeGreaterThan(deferredZ);
    expect(gateZ).toBeGreaterThan(skipLinkZ);
    await expect(page.getByRole("button", { name: "Decline and disconnect" })).toBeFocused();

    await page.evaluate(() => window.__noxaNotify.resetServerRules());
    await expect(gate).toHaveCount(0);
    await expect(deferred).toHaveAttribute("aria-modal", "true");
    await expect(page.getByRole("button", { name: "Continue" })).toBeFocused();
    await page.keyboard.press("Escape");
});

test("Escape runs polling-dialog cleanup and allows stateful dialogs to reopen", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.showWorkspace(false);
        window.__noxa.state.myClientID = "client-a";
        window.__noxa.openClientInfo({ client_id: "client-a", unique_id: "user-a", nickname: "Alice" });
    });
    await expect(page.getByRole("dialog", { name: "Connection Info" })).toBeVisible();
    await expect.poll(() => page.evaluate(() => window.__calls.GetClientInfo || 0)).toBeGreaterThan(0);
    await page.keyboard.press("Escape");
    const clientInfoCalls = await page.evaluate(() => window.__calls.GetClientInfo || 0);
    await page.waitForTimeout(2200);
    expect(await page.evaluate(() => window.__calls.GetClientInfo || 0)).toBe(clientInfoCalls);

    await page.evaluate(() => window.__noxaFiles.openTransfers());
    await expect(page.getByRole("dialog", { name: "Transfers" })).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(page.getByRole("dialog", { name: "Transfers" })).toHaveCount(0);
    await page.evaluate(() => window.__noxaFiles.openTransfers());
    await expect(page.getByRole("dialog", { name: "Transfers" })).toBeVisible();
    await page.keyboard.press("Escape");

    await page.evaluate(() => window.__noxaMeta.openStatsPage());
    await expect(page.getByRole("dialog", { name: "Server information" })).toBeVisible();
    await page.keyboard.press("Escape");
    const statsCalls = await page.evaluate(() => window.__calls.GetClientInfo || 0);
    await page.waitForTimeout(1200);
    expect(await page.evaluate(() => window.__calls.GetClientInfo || 0)).toBe(statsCalls);
    await page.evaluate(() => window.__noxaMeta.openStatsPage());
    await expect(page.getByRole("dialog", { name: "Server information" })).toBeVisible();
});

test("keeps onboarding semantics and focus when each step rerenders", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.state.settings.onboarding_done = false;
        window.__noxaMeta.maybeOnboard();
        window.__noxaMeta.maybeOnboard();
    });
    await expect(page.locator(".onboarding")).toHaveCount(1);
    await expect(page.getByRole("dialog", { name: "Welcome to noXa" })).toBeVisible();
    await expect(page.locator(".ob-nick")).toBeFocused();
    await page.getByRole("button", { name: "Next" }).click();
    await expect(page.getByRole("dialog", { name: "Microphone check" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Open capture settings" })).toBeFocused();
    await page.keyboard.press("Escape");
    await expect(page.locator(".onboarding")).toHaveCount(0);
    await expect.poll(() => page.evaluate(() => window.__noxa.state.settings.onboarding_done)).toBe(true);
});

test("debounces keyboard pane persistence and refreshes separator values", async ({ page }) => {
    await page.evaluate(() => window.__noxa.showWorkspace(false));
    const handle = page.getByRole("separator", { name: "Resize channels pane" });
    await handle.focus();
    const before = await page.evaluate(() => window.__calls.SaveSettings || 0);
    await page.keyboard.press("ArrowRight");
    await page.keyboard.press("ArrowRight");
    await page.keyboard.press("ArrowRight");
    await expect.poll(() => page.evaluate(() => window.__calls.SaveSettings || 0)).toBe(before + 1);
    await expect.poll(() => handle.evaluate((element) =>
        Number(element.getAttribute("aria-valuenow")) - Math.round(element.parentElement.getBoundingClientRect().width),
    )).toBe(0);
});

test("uses grouped, distinct action sounds without replaying historical tab activity", async ({ page }) => {
    const result = await page.evaluate(async () => {
        const tones = [];
        const media = [];
        const { soundEngine } = window.__noxa;
        await soundEngine.preload();
        await soundEngine.resume();
        if (soundEngine.buffers.size !== 50 || soundEngine.ctx.state !== "running") throw new Error(JSON.stringify({ buffers: soundEngine.buffers.size, state: soundEngine.ctx.state, warnings: [...soundEngine.warnings] }));
        let clock = 0;
        soundEngine.now = () => clock += 1000;
        const originalSource = soundEngine.ctx.createBufferSource.bind(soundEngine.ctx);
        soundEngine.ctx.createBufferSource = () => {
            const source = originalSource();
            const start = source.start.bind(source);
            source.start = (...args) => {
                const name = [...soundEngine.buffers].find(([, buffer]) => buffer === source.buffer)?.[0];
                if (name === "own_channel_join") media.push(name);
                else tones.push(name);
                start(...args);
            };
            return source;
        };
        const state = window.__noxa.state;
        state.settings = {
            ...state.settings,
            activation_mode: "ptt",
            ptt_release_delay_ms: 0,
            event_sounds: {},
            notify_matrix: {},
            custom_sounds: {},
        };
        state.myClientID = "client-a";
        state.myChannelID = 1;
        state.clients = [
            { client_id: "client-a", unique_id: "user-a", nickname: "Alice", channel_id: 1 },
            { client_id: "client-b", unique_id: "user-b", nickname: "Bob", channel_id: 2 },
        ];
        const emit = (name, payload) => {
            for (const callback of window.__events[name] || []) callback(payload);
        };
        const move = (channelID) => emit("event", JSON.stringify({
            type: "user_moved", data: { client_id: "client-b", channel_id: channelID },
        }));
        const collect = (fn) => {
            for (const entry of soundEngine.active) soundEngine.release(entry);
            tones.length = 0;
            fn();
            return [...tones];
        };

        const moveIn = collect(() => move(1));
        const moveOut = collect(() => move(2));
        // Legacy custom beeps no longer override the authored action cue.
        state.settings.notify_matrix.join_leave = { toast: true, sound: false, flash: false, native: false };
        const matrixOff = collect(() => move(1));
        state.settings.notify_matrix.join_leave.sound = true;
        const custom = collect(() => move(2));
        state.replayingTabID = "tab-a";
        const replay = collect(() => move(1));
        emit("tab_replay_done", "tab-a");
        state.settings.event_sounds.user_move_out = false;
        const disabledSpecific = collect(() => move(2));
        state.settings.event_sounds.user_move_out = true;
        const afterReplay = collect(() => move(1));
        state.myUniqueID = "user-a";
        state.lastConnect = { addr: "sound.example:12333" };
        state.settings.chat_notification_level = "all";
        state.settings.keywords = { "sound.example:12333": ["urgent"] };
        const chat = (id, text) => emit("event", JSON.stringify({
            type: "chat", data: {
                id, from: "Bob", from_unique_id: "user-b", text, channel_id: 1,
            },
        }));
        const keywordChat = collect(() => chat(901, "urgent request"));
        const roleChat = collect(() => chat(902, "@admin urgent request"));
        const ordinaryChat = collect(() => chat(903, "ordinary request"));
        state.myChannelID = 0;
        state.clients = [{ client_id: "client-a", unique_id: "user-a", nickname: "Alice", channel_id: 7 }];
        const mediaBeforeOwnJoin = media.length;
        const ownJoin = collect(() => window.__noxa.syncOwnChannel());
        const ownJoinMedia = media.length - mediaBeforeOwnJoin;
        state.clients[0].channel_id = 8;
        const ownSwitch = collect(() => window.__noxa.syncOwnChannel());
        state.myChannelID = 9;
        state.clients = [{ client_id: "client-a", unique_id: "user-a", nickname: "Alice", channel_id: 9 }];
        state.channels = [{ ChannelID: 9, ParentID: 0, Name: "Deleted" }];
        const channelDeletion = collect(() => {
            emit("event", JSON.stringify({ type: "channel_deleted", data: { channel_id: 9 } }));
            emit("event", JSON.stringify({ type: "user_moved", data: { client_id: "client-a", channel_id: 0 } }));
        });
        state.settings.activation_mode = "vad";
        const vadPTT = collect(() => window.__noxa.setPTT(true));
        window.__noxa.setPTT(false);
        state.settings.activation_mode = "ptt";
        const ptt = collect(() => window.__noxa.setPTT(true));
        window.__noxa.setPTT(false);
        const deafen = collect(() => window.__noxa.setDeafened(true));
        state.settings.bookmarks = [{ name: "Guest", addr: "guest.example:12333", nickname: "Guest" }];
        window.__tabs = [{
            id: "guest-tab", addr: "guest.example:12333", nickname: "Guest",
            active: true, connected: true, unread: 0, mentions: 0,
        }];
        window.__guestConnectHandler = async () => {
            // Emulate Go's connect/activate ordering: reset, replayed state,
            // then replay completion, all before the bridge resolves.
            emit("tab_reset", "guest-tab");
            emit("snapshot", JSON.stringify({ root_channels: [{
                ChannelID: 15, ParentID: 0, Name: "Guest channel", clients: [{
                    client_id: "client-a", unique_id: "user-a", nickname: "Alice", channel_id: 15,
                }], children: [],
            }] }));
            // Let ClientID resolve and record the replayed channel while cues
            // remain suppressed, then finish the replay.
            await Promise.resolve();
            await Promise.resolve();
            emit("tab_replay_done", "guest-tab");
            return { tab_id: "guest-tab", error: "" };
        };
        const mediaBeforeGuest = media.length;
        tones.length = 0;
        await window.__noxaTabs.quickConnectLast();
        const guestConnect = [...tones];
        const guestInitialJoinMedia = media.length - mediaBeforeGuest;
        const guestInitialCueCleared = state.pendingInitialChannelCueTabID === "";

        // Exercise the opposite race too: replay completes before ClientID.
        // syncOwnChannel must then play the pending initial join when identity
        // arrives, without replay history getting its own cue.
        let releaseReplayFirstIdentity;
        window.__clientIDGate = new Promise((resolve) => { releaseReplayFirstIdentity = resolve; });
        window.__tabs = [{
            id: "replay-first-tab", addr: "replay.example:12333", nickname: "Replay",
            active: true, connected: true, unread: 0, mentions: 0,
        }];
        emit("tab_reset", "replay-first-tab");
        emit("snapshot", JSON.stringify({ root_channels: [{
            ChannelID: 16, ParentID: 0, Name: "Replay channel", clients: [{
                client_id: "client-a", unique_id: "user-a", nickname: "Alice", channel_id: 16,
            }], children: [],
        }] }));
        emit("tab_replay_done", "replay-first-tab");
        const mediaBeforeReplayFirstIdentity = media.length;
        releaseReplayFirstIdentity();
        await new Promise((resolve) => setTimeout(resolve, 0));
        window.__clientIDGate = null;
        const replayFirstIdentityMedia = media.length - mediaBeforeReplayFirstIdentity;
        const replayFirstCueCleared = state.pendingInitialChannelCueTabID === "";

        // A channel-0 replay leaves its initial marker armed. Its first live
        // self-move must consume that marker, so the following equality sync
        // cannot duplicate the channel cue.
        window.__tabs = [{
            id: "live-move-tab", addr: "live.example:12333", nickname: "Live",
            active: true, connected: true, unread: 0, mentions: 0,
        }];
        emit("tab_reset", "live-move-tab");
        emit("snapshot", JSON.stringify({ root_channels: [{
            ChannelID: 18, ParentID: 0, Name: "No channel", clients: [{
                client_id: "client-a", unique_id: "user-a", nickname: "Alice", channel_id: 0,
            }], children: [],
        }] }));
        await new Promise((resolve) => setTimeout(resolve, 0));
        emit("tab_replay_done", "live-move-tab");
        const mediaBeforeLiveMove = media.length;
        emit("event", JSON.stringify({
            type: "user_moved", data: { client_id: "client-a", channel_id: 17 },
        }));
        window.__noxa.syncOwnChannel();
        const liveMoveInitialMedia = media.length - mediaBeforeLiveMove;
        const liveMoveCueCleared = state.pendingInitialChannelCueTabID === "";
        window.__noxa.openSettings("notifications");
        const groups = [...document.querySelectorAll("#settings-content .set-subhead")].map((element) => element.querySelector("span")?.textContent || element.textContent);
        return { moveIn, moveOut, matrixOff, custom, replay, disabledSpecific, afterReplay,
            keywordChat, roleChat, ordinaryChat,
            ownJoin, ownJoinMedia, ownSwitch, channelDeletion, vadPTT, ptt, deafen, guestConnect,
            guestInitialJoinMedia, guestInitialCueCleared, replayFirstIdentityMedia,
            replayFirstCueCleared, liveMoveInitialMedia, liveMoveCueCleared, groups };
    });

    expect(result.moveIn).toEqual(["user_move_in"]);
    expect(result.moveOut).toEqual(["user_move_out"]);
    expect(result.matrixOff).toEqual([]);
    expect(result.custom).toEqual(["user_move_out"]);
    expect(result.replay).toEqual([]);
    expect(result.disabledSpecific).toEqual([]);
    expect(result.afterReplay).toEqual(["user_move_in"]);
    expect(result.keywordChat).toEqual(["keyword"]);
    expect(result.roleChat).toEqual(["mention"]);
    expect(result.ordinaryChat).toEqual(["channel_message"]);
    expect(result.ownJoin).toEqual([]);
    expect(result.ownJoinMedia).toBe(1);
    expect(result.ownSwitch).toEqual(["own_channel_switch"]);
    expect(result.channelDeletion).toEqual(["own_channel_leave"]);
    expect(result.vadPTT).toEqual([]);
    expect(result.ptt).toEqual(["ptt_on"]);
    expect(result.deafen).toEqual(["deafen_on"]);
    expect(result.guestConnect).toEqual(["connection_connected"]);
    expect(result.guestInitialJoinMedia).toBe(1);
    expect(result.guestInitialCueCleared).toBe(true);
    expect(result.replayFirstIdentityMedia).toBe(1);
    expect(result.replayFirstCueCleared).toBe(true);
    expect(result.liveMoveInitialMedia).toBe(1);
    expect(result.liveMoveCueCleared).toBe(true);
    expect(result.groups).toEqual(expect.arrayContaining([
        "Connection", "Your channel", "Other users", "Voice controls", "Notifications",
    ]));
    await expect(page.getByText("Channel message", { exact: true })).toBeVisible();
});

test("scopes connection failures and active-tab close sounds", async ({ page }) => {
    const result = await page.evaluate(async () => {
        const tones = [];
        const { soundEngine } = window.__noxa;
        await soundEngine.preload();
        await soundEngine.resume();
        if (soundEngine.buffers.size !== 50 || soundEngine.ctx.state !== "running") throw new Error(JSON.stringify({ buffers: soundEngine.buffers.size, state: soundEngine.ctx.state, warnings: [...soundEngine.warnings] }));
        let clock = 0;
        soundEngine.now = () => clock += 1000;
        const originalSource = soundEngine.ctx.createBufferSource.bind(soundEngine.ctx);
        soundEngine.ctx.createBufferSource = () => {
            const source = originalSource();
            const start = source.start.bind(source);
            source.start = (...args) => {
                const name = [...soundEngine.buffers].find(([, buffer]) => buffer === source.buffer)?.[0];
                tones.push(name);
                start(...args);
            };
            return source;
        };
        const state = window.__noxa.state;
        state.settings = { ...state.settings, event_sounds: {}, notify_matrix: {} };
        const emit = (name, payload) => {
            for (const callback of window.__events[name] || []) callback(payload);
        };
        window.__tabs = [
            { id: "tab-a", addr: "a.example:12333", nickname: "Alice", active: true, connected: true, unread: 0, mentions: 0 },
            { id: "tab-b", addr: "b.example:12333", nickname: "Bob", active: false, connected: true, unread: 0, mentions: 0 },
        ];
        emit("tab_reset", "tab-a");
        emit("tab_replay_done", "tab-a");
        document.getElementById("login-addr").value = "slow.example:12333";
        document.getElementById("login-nick").value = "Alice";
        window.__connectBookmarkGate = new Promise((resolve) => { window.__releaseSlowConnect = resolve; });
        const slowLogin = window.__noxa.connectFromLogin();
        await Promise.resolve();
        emit("tab_reset", "tab-b");
        emit("tab_replay_done", "tab-b");
        window.__connectBookmarkResult = "server unavailable";
        window.__releaseSlowConnect();
        await slowLogin;
        const staleFailure = [...tones];

        tones.length = 0;
        window.__connectBookmarkGate = null;
        window.__connectBookmarkResult = "still unavailable";
        await window.__noxa.connectFromLogin();
        const currentFailure = [...tones];
        window.__connectBookmarkResult = "";

        tones.length = 0;
        window.__disconnectHandler = async () => {
            // Match App.Disconnect -> closeTab(true): the Go-owned edge is
            // emitted before replacement tab replay and bridge resolution.
            emit("intentional_disconnect", "tab-b");
            window.__tabs = [
                { id: "tab-a", addr: "a.example:12333", nickname: "Alice", active: true, connected: true, unread: 0, mentions: 0 },
                { id: "tab-b", addr: "b.example:12333", nickname: "Bob", active: false, connected: true, unread: 0, mentions: 0 },
            ];
            emit("tab_reset", "tab-a");
            emit("tab_replay_done", "tab-a");
            return "";
        };
        emit("tray_disconnect");
        await new Promise((resolve) => setTimeout(resolve, 0));
        const menuDisconnect = [...tones];

        emit("tab_update", structuredClone(window.__tabs));
        tones.length = 0;
        document.querySelector('.srv-tab[data-tab-id="tab-b"] .srv-tab-x').click();
        await new Promise((resolve) => setTimeout(resolve, 0));
        const backgroundClose = [...tones];

        window.__disconnectTabHandler = async (tabID) => {
            if (tabID === "tab-a") {
                emit("intentional_disconnect", "tab-a");
                window.__tabs = [{
                    id: "tab-b", addr: "b.example:12333", nickname: "Bob",
                    active: true, connected: true, unread: 0, mentions: 0,
                }];
                emit("tab_reset", "tab-b");
                emit("tab_replay_done", "tab-b");
            }
            return "";
        };
        tones.length = 0;
        document.querySelector('.srv-tab[data-tab-id="tab-a"] .srv-tab-x').click();
        await new Promise((resolve) => setTimeout(resolve, 0));
        const activeClose = [...tones];

        window.__tabs = [{
            id: "tab-b", addr: "b.example:12333", nickname: "Bob",
            active: true, connected: false, unread: 0, mentions: 0,
        }];
        emit("tab_update", structuredClone(window.__tabs));
        tones.length = 0;
        document.querySelector('.srv-tab[data-tab-id="tab-b"] .srv-tab-x').click();
        await new Promise((resolve) => setTimeout(resolve, 0));
        const activeOfflineClose = [...tones];

        tones.length = 0;
        window.__disconnectHandler = null;
        emit("tray_disconnect");
        await new Promise((resolve) => setTimeout(resolve, 0));
        const offlineMenuDisconnect = [...tones];
        return {
            staleFailure, currentFailure, menuDisconnect, backgroundClose, activeClose,
            activeOfflineClose, offlineMenuDisconnect,
        };
    });

    expect(result.staleFailure).toEqual([]);
    expect(result.currentFailure).toEqual(["connection_failed"]);
    expect(result.menuDisconnect).toEqual(["connection_disconnected"]);
    expect(result.backgroundClose).toEqual([]);
    expect(result.activeClose).toEqual(["connection_disconnected"]);
    expect(result.activeOfflineClose).toEqual([]);
    expect(result.offlineMenuDisconnect).toEqual([]);
});
