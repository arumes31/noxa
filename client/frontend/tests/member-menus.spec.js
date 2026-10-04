import { test, expect } from "./fixtures.js";

test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
        window.__events = {}; window.__moves = []; window.__joins = []; window.__links = [];
        window.__saved = { language: "en", activation_mode: "ptt", volume: 100, user_volumes: {}, bookmarks: [], onboarding_done: true, alpha_dismissed: "0.5.0-dev+gabc123", chat_max_lines: 200, window_opacity: 100 };
        window.runtime = { EventsOn: (name, fn) => { (window.__events[name] ||= []).push(fn); return () => {}; }, EventsEmit() {}, WindowIsFullscreen: async () => false, BrowserOpenURL: url => window.__links.push(url) };
        window.go = { main: { App: new Proxy({}, { get(_target, method) { return async (...args) => {
            if (method === "GetSettings") return structuredClone(window.__saved);
            if (method === "SaveSettings") { if (window.__saveWait) await window.__saveWait; if (window.__saveFail) return "save failed"; window.__saved = structuredClone(args[0]); return ""; }
            if (method === "MoveClientForTab") { window.__moves.push(args); return window.__moveError || ""; }
            if (method === "JoinChannelForTab") { window.__joins.push(args); return ""; }
            if (["ListTabs", "GetPermissions", "SubscriptionsForTab", "DMHistoryLoadForContext"].includes(method)) return [];
            if (["IdentityInfo", "GetAvatar"].includes(method)) return {};
            if (method === "DMHistoryContextForTab") return { tab_id: "one", identity_uid: "me", activation: "0", identity_revision: "0" };
            if (method === "Connected" || method === "IsGuest") return false;
            if (method === "ClientVersion" || method === "ClientVersionShort") return "0.5.0-dev+gabc123";
            return "";
        }; } }) } };
        Object.defineProperty(navigator, "mediaDevices", { configurable: true, value: { enumerateDevices: async () => [], addEventListener() {} } });
    });
    await page.goto("/");
    await page.waitForFunction(() => !!window.__noxa?.openSettings);
    await page.evaluate(async () => {
        const v = window.__noxa; v.showWorkspace(false);
        Object.assign(v.state, { activeTabID: "one", myClientID: "self", myUniqueID: "me", myChannelID: 1, selectedClientID: "alice",
            channels: [{ ChannelID: 1, Name: "Lobby" }, { ChannelID: 2, Name: "Team" }],
            clients: [{ client_id: "self", unique_id: "me", nickname: "Me", channel_id: 1 }, { client_id: "alice", unique_id: "alice-uid", nickname: "Alice", channel_id: 1 }] });
        v.renderTree();
        window.__shareAvailable = true;
        v.shareAudioCtl = { get: id => id === "alice" && window.__shareAvailable ? { muted: false, volume: 100 } : null };
        window.__gain = { gain: { value: 1 } };
        window.__mute = { gain: { value: 1 } };
        (await import("/src/audio.js")).registerUserChain("alice-uid", window.__gain, window.__mute);
    });
});

test("visible member menu exposes separate personal microphone and screen-share volumes", async ({ page }) => {
    const trigger = page.locator('#channel-tree .client[data-clid="alice"]');
    await trigger.click({ button: 'right' });
    const microphone = page.getByRole('slider', { name: 'Voice volume', exact: true });
    const share = page.getByRole('slider', { name: 'Screen-share audio', exact: true });
    await expect(microphone).toHaveValue('100');
    if (process.env.NOXA_UI_SCREENSHOTS) await page.screenshot({ path: '.cache/ui-improvements-member-menu.png' });
    await share.fill('45');
    await share.dispatchEvent('change');
    await expect.poll(() => page.evaluate(() => window.__saved.user_share_volumes?.['alice-uid'])).toBe(45);
    await expect(microphone).toHaveValue('100');
    expect(await page.evaluate(() => window.__gain.gain.value)).toBe(1);
    await page.keyboard.press('Escape');
    await expect(trigger).toBeFocused();
    await page.keyboard.press('Shift+F10');
    await expect(share).toHaveValue('45');
});

test("exact percentages, amplification, and mute preserve independent audio preferences", async ({ page }) => {
    await page.locator('#channel-tree .client[data-clid="alice"]').click({ button: 'right' });
    const voice = page.getByRole('spinbutton', { name: 'Voice volume percentage', exact: true });
    const share = page.getByRole('spinbutton', { name: 'Screen-share audio percentage', exact: true });
    await voice.fill('145'); await voice.press('Enter');
    await expect.poll(() => page.evaluate(() => window.__saved.user_volumes['alice-uid'])).toBe(145);
    await expect(page.getByRole('slider', { name: 'Voice volume', exact: true })).toHaveAttribute('aria-valuetext', '145% · Amplified');
    await share.fill('40'); await share.press('Enter');
    await expect.poll(() => page.evaluate(() => window.__saved.user_share_volumes['alice-uid'])).toBe(40);
    await page.getByRole('menuitem', { name: 'Mute voice for me', exact: true }).click();
    await expect(page.getByRole('menu')).toBeVisible();
    await expect(page.getByRole('menuitem', { name: 'Unmute voice for me', exact: true })).toHaveAttribute('aria-pressed', 'true');
    await expect(voice).toHaveValue('145'); await expect(share).toHaveValue('40');
    await page.getByRole('menuitem', { name: 'Reset volume to 100%', exact: true }).click();
    await expect(voice).toHaveValue('100');
    expect(await page.evaluate(() => window.__saved.muted_users)).toEqual(['alice-uid']);
    await share.fill('201'); await share.press('Enter');
    await expect(share).toHaveValue('40');
    expect(await page.evaluate(() => window.__saved.user_share_volumes['alice-uid'])).toBe(40);
});

test("screen-share controls explain unavailable audio and follow stream changes", async ({ page }) => {
    await page.evaluate(() => { window.__shareAvailable = false; });
    await page.locator('#channel-tree .client[data-clid="alice"]').click({ button: 'right' });
    const share = page.getByRole('slider', { name: 'Screen-share audio', exact: true });
    await expect(page.getByText('No screen-share audio', { exact: true })).toBeVisible();
    await expect(share).toBeDisabled();
    await expect(page.getByRole('menuitem', { name: 'Mute screen-share audio', exact: true })).toBeDisabled();
    await expect(page.getByRole('slider', { name: 'Voice volume', exact: true })).toBeEnabled();
    await page.evaluate(() => { window.__shareAvailable = true; });
    await expect(share).toBeEnabled();
    await expect(page.getByText('No screen-share audio', { exact: true })).toBeHidden();
    await page.evaluate(() => { window.__shareAvailable = false; });
    await expect(share).toBeDisabled();
});

for (const language of ['en', 'de']) test(`compact audio groups keep hierarchy and fit in ${language}`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width: 644, height: 480 });
    await page.evaluate(async language => { (await import('/src/i18n.js')).setLanguage(language); }, language);
    await page.locator('#workspace-sidebar-toggle').click();
    await page.locator('#channel-tree .client[data-clid="alice"]').click({ button: 'right' });
    const menu = page.getByRole('menu');
    const order = await menu.locator(':scope > a').evaluateAll(items => items.map(item => item.dataset.act));
    expect(order.slice(0, 2)).toEqual(['pm', 'info']); expect(order.at(-1)).toBe('copy');
    await expect(menu.locator('.ctx-audio-baseline')).toHaveCount(2);
    const bounds = await menu.boundingBox(); expect(bounds.x + bounds.width).toBeLessThanOrEqual(644);
    expect(await menu.evaluate(node => node.scrollWidth <= node.clientWidth)).toBe(true);
    await page.screenshot({ path: testInfo.outputPath(`member-audio-${language}.png`) });
});

test("voice More keeps secondary actions available and restores keyboard focus", async ({ page }) => {
    await page.setViewportSize({ width: 640, height: 480 });
    const more = page.locator('#voice-options > summary');
    await expect(more).toHaveAccessibleName('More voice options');
    await expect(page.locator('#voice-disconnect')).toBeHidden();
    await expect(page.locator('#voice-leave-channel')).toBeVisible();
    await more.click();
    await expect(page.locator('#voice-disconnect')).toBeVisible();
    // The details toggle and device refresh position the menu asynchronously.
    await expect(async () => {
        const bounds = await page.locator('.voice-options-menu').boundingBox();
        expect(bounds.x).toBeGreaterThanOrEqual(0);
        expect(bounds.y).toBeGreaterThanOrEqual(0);
        expect(bounds.x + bounds.width).toBeLessThanOrEqual(640);
        expect(bounds.y + bounds.height).toBeLessThanOrEqual(480);
    }).toPass();
    if (process.env.NOXA_UI_SCREENSHOTS) await page.screenshot({ path: '.cache/ui-improvements-compact.png' });
    await page.locator('#voice-settings').focus();
    await page.keyboard.press('Escape');
    await expect(page.locator('#voice-disconnect')).toBeHidden();
    await expect(more).toBeFocused();
    await more.click();
    await page.locator('#voice-settings').click();
    await expect(page.locator('#settings-overlay')).toBeVisible();
    await expect(page.locator('#voice-disconnect')).toBeHidden();
});

test("personal screen-share volume restores on failed save and resets independently", async ({ page }) => {
    await page.locator('#channel-tree .client[data-clid="alice"]').click({ button: 'right' });
    const share = page.getByRole('slider', { name: 'Screen-share audio', exact: true });
    await page.evaluate(() => { window.__saveFail = true; });
    await share.fill('25'); await share.dispatchEvent('change');
    await expect(share).toHaveValue('100');
    await expect(share).toBeEnabled();
    await page.evaluate(() => { window.__saveFail = false; });
    await share.fill('65'); await share.dispatchEvent('change');
    await expect.poll(() => page.evaluate(() => window.__saved.user_share_volumes?.['alice-uid'])).toBe(65);
    await page.getByRole('menuitem', { name: 'Reset screen-share audio to 100%', exact: true }).click();
    await expect.poll(() => page.evaluate(() => window.__saved.user_share_volumes?.['alice-uid'])).toBe(100);
    expect(await page.evaluate(() => window.__gain.gain.value)).toBe(1);
});

test("member volume previews without saving and resets from the menu", async ({ page }) => {
    await page.locator('.client[data-clid="alice"]').click({ button: "right" });
    const slider = page.locator(".ctx-menu .ctx-volume input[type=range]");
    await slider.evaluate(input => { input.value = "150"; input.dispatchEvent(new Event("input", { bubbles: true })); });
    expect(await page.evaluate(() => window.__gain.gain.value)).toBe(1.5);
    expect(await page.evaluate(() => window.__saved.user_volumes["alice-uid"])).toBeUndefined();
    await slider.dispatchEvent("change");
    await expect.poll(() => page.evaluate(() => window.__saved.user_volumes["alice-uid"])).toBe(150);
    await page.getByRole("menuitem", { name: "Reset volume to 100%" }).click();
    await expect.poll(() => page.evaluate(() => window.__gain.gain.value)).toBe(1);
});

test("keyboard member menu fits a short window and restores focus", async ({ page }) => {
    await page.setViewportSize({ width: 780, height: 350 });
    const member = page.locator('.client[data-clid="alice"]');
    await member.focus(); await page.keyboard.press("Shift+F10");
    const menu = page.locator(".ctx-menu"); await expect(menu).toBeVisible();
    const box = await menu.boundingBox();
    expect(box.y).toBeGreaterThanOrEqual(0); expect(box.y + box.height).toBeLessThanOrEqual(350);
    await page.keyboard.press("Escape"); await expect(menu).toHaveCount(0); await expect(member).toBeFocused();
});

test("member menu moves through a searchable destination dialog", async ({ page }) => {
    await page.locator('.client[data-clid="alice"]').click({ button: "right" });
    await page.getByRole("menuitem", { name: "Move to channel…", exact: true }).click();
    await page.getByRole("searchbox", { name: "Search channels" }).fill("Team");
    await page.getByRole("button", { name: "Team", exact: true }).click();
    await expect.poll(() => page.evaluate(() => window.__moves)).toEqual([["one", "alice", 2]]);
});

test("channel menu has explicit join and leave actions", async ({ page }) => {
    await page.locator('.channel[data-chid="2"]').click({ button: "right" });
    await page.getByRole("menuitem", { name: "Join voice", exact: true }).click();
    await expect.poll(() => page.evaluate(() => window.__joins)).toEqual([["one", 2]]);
    await page.locator('.channel[data-chid="1"]').click({ button: "right" });
    await page.getByRole("menuitem", { name: "Leave voice", exact: true }).click();
    await expect.poll(() => page.evaluate(() => window.__joins)).toEqual([["one", 2], ["one", 0]]);
});

test("failed volume save and cancelled drag restore the saved level", async ({ page }) => {
    await page.locator('.client[data-clid="alice"]').click({ button: "right" });
    const slider = page.locator(".ctx-menu .ctx-volume input[type=range]");
    await slider.evaluate(input => { input.value = "25"; input.dispatchEvent(new Event("input", { bubbles: true })); });
    expect(await page.evaluate(() => window.__gain.gain.value)).toBe(0.25);
    await page.keyboard.press("Escape");
    expect(await page.evaluate(() => window.__gain.gain.value)).toBe(1);
    await page.evaluate(() => { window.__saveFail = true; });
    await page.locator('.client[data-clid="alice"]').click({ button: "right" });
    await slider.evaluate(input => { input.value = "175"; input.dispatchEvent(new Event("input", { bubbles: true })); input.dispatchEvent(new Event("change", { bubbles: true })); });
    await expect(slider).toHaveValue("100");
    expect(await page.evaluate(() => window.__gain.gain.value)).toBe(1);
});

test("member card exposes the same menu and DM right-click does not delete history", async ({ page }) => {
    await page.evaluate(() => window.__noxa.setDetailsOpen(true));
    await page.locator(".card-nick").click({ button: "right" });
    await expect(page.getByRole("menuitem", { name: "Reset volume to 100%" })).toBeVisible();
    await page.keyboard.press("Escape");
    await page.evaluate(() => window.__noxaChat.openPM("alice-uid", "Alice"));
    const tab = page.locator(".pm-tab");
    await tab.focus(); await page.keyboard.press("Shift+F10");
    await expect(page.getByRole("menuitem", { name: "Open conversation", exact: true })).toBeVisible();
    await expect(page.getByRole("button", { name: "Delete history", exact: true })).toHaveCount(0);
    await page.getByRole("menuitem", { name: "Delete local history…", exact: true }).click();
    await expect(page.getByRole("button", { name: "Delete history", exact: true })).toBeVisible();
});

test("denied member move shows an error and leaves the member in place", async ({ page }) => {
    await page.evaluate(() => { window.__moveError = "Permission denied"; });
    await page.locator('.client[data-clid="alice"]').click({ button: "right" });
    await page.getByRole("menuitem", { name: "Move to channel…", exact: true }).click();
    await page.getByRole("button", { name: "Team", exact: true }).click();
    await expect(page.locator(".dlg .role-error")).toContainText("Permission denied");
    expect(await page.evaluate(() => window.__noxa.state.clients.find(c => c.client_id === "alice").channel_id)).toBe(1);
});

test("authors and mentions share the member menu and messages support keyboard actions", async ({ page }) => {
    await page.evaluate(() => {
        for (const callback of window.__events.event || []) callback(JSON.stringify({ type: "chat", data: {
            id: 501, channel_id: 1, from: "Alice", from_unique_id: "alice-uid", text: "Hello @Alice",
        } }));
    });
    const row = page.locator('.msg[data-msg-id="501"]');
    for (const target of [row.locator(".msg-from"), row.locator(".mention-tok")]) {
        await target.click();
        await expect(page.getByRole("slider", { name: "Voice volume" })).toBeVisible();
        await page.keyboard.press("Escape");
        await expect(target).toBeFocused();
    }
    await row.focus(); await page.keyboard.press("Shift+F10");
    await page.getByRole("menuitem", { name: "reply", exact: true }).click();
    await expect(page.locator("#reply-bar")).toContainText("Alice");
});

test("closing the member drawer cancels a live drag without saving", async ({ page }) => {
    await page.evaluate(() => window.__noxa.setDetailsOpen(true));
    const slider = page.locator("#member-volume");
    const box = await slider.boundingBox();
    await page.mouse.move(box.x + box.width * 0.75, box.y + box.height / 2);
    await page.mouse.down();
    await expect.poll(() => page.evaluate(() => window.__gain.gain.value)).toBeGreaterThan(1.2);
    await page.keyboard.press("Escape");
    await expect.poll(() => page.evaluate(() => window.__gain.gain.value)).toBe(1);
    await page.mouse.up();
    expect(await page.evaluate(() => window.__saved.user_volumes["alice-uid"] ?? 100)).toBe(100);
});

test("recent channel labels render literally inside a bounded menu", async ({ page }) => {
    await page.evaluate(() => {
        const v = window.__noxa;
        v.state.channels[1].Name = '<img src=x onerror="window.__injected=true">';
        v.state.lastConnect = { addr: "test" };
        v.state.settings.recent_channels = { test: [2] };
    });
    await page.locator('.channel[data-chid="1"]').click({ button: "right" });
    await expect(page.locator('.ctx-menu [data-act="recent-2"]')).toContainText("<img src=x");
    expect(await page.evaluate(() => !!window.__injected)).toBe(false);
});

test("video tile menu supports keyboard navigation within a short viewport", async ({ page }) => {
    await page.evaluate(async () => {
        const canvas = document.createElement("canvas"); canvas.width = canvas.height = 32;
        const stream = canvas.captureStream(1), track = stream.getVideoTracks()[0];
        (await import("/src/video.js")).videoTrackAdded(track.id, stream, "alice");
    });
    await page.setViewportSize({ width: 780, height: 350 });
    const tile = page.locator(".vtile");
    await tile.focus(); await page.keyboard.press("Shift+F10");
    const menu = page.getByRole("menu"); await expect(menu).toBeVisible();
    const bounds = await menu.boundingBox();
    expect(bounds.y + bounds.height).toBeLessThanOrEqual(350);
    await page.keyboard.press("End"); await page.keyboard.press("Escape");
    await expect(menu).toHaveCount(0); await expect(tile).toBeFocused();
});

test("keyboard volume adjustment can continue to reset and other menu actions", async ({ page }) => {
    const member = page.locator('.client[data-clid="alice"]');
    await member.focus(); await page.keyboard.press("Shift+F10");
    const slider = page.locator(".ctx-menu .ctx-volume input[type=range]"); await slider.focus();
    await page.keyboard.press("ArrowRight");
    await expect.poll(() => page.evaluate(() => window.__saved.user_volumes["alice-uid"])).toBeGreaterThan(100);
    await page.keyboard.press("ArrowUp");
    await expect(page.getByRole("menuitem", { name: "Reset volume to 100%" })).toBeFocused();
    await page.keyboard.press("Enter");
    await expect.poll(() => page.evaluate(() => window.__saved.user_volumes["alice-uid"])).toBe(100);
});

test("member names inside links preserve link navigation", async ({ page }) => {
    await page.evaluate(() => {
        for (const callback of window.__events.event || []) callback(JSON.stringify({ type: "chat", data: {
            id: 502, channel_id: 1, from: "Alice", from_unique_id: "alice-uid", text: "https://example.com/@Alice",
        } }));
    });
    const link = page.locator('.msg[data-msg-id="502"] a.md-link');
    await expect(link.locator("[data-member-uid]")).toHaveCount(0);
    await link.click();
    expect(await page.evaluate(() => window.__links)).toEqual(["https://example.com/@Alice"]);
});

test('member card moderation preserves the selected session when identities match', async ({page}) => {
    await page.evaluate(() => {
        const v=window.__noxa;
        v.state.clients.push({client_id:'alice-phone',unique_id:'alice-uid',nickname:'Alice phone',channel_id:1});
        v.state.selectedClientID='alice-phone'; v.renderTree(); v.setDetailsOpen(true);
    });
    await page.locator('.card-nick').click({button:'right'});
    await page.getByRole('menuitem',{name:'Move to channel…',exact:true}).click();
    await page.getByRole('button',{name:'Team',exact:true}).click();
    expect(await page.evaluate(()=>window.__moves)).toEqual([['one','alice-phone',2]]);
});

test('member card reflects volume saved through the member menu', async ({page}) => {
    await page.evaluate(()=>window.__noxa.setDetailsOpen(true));
    await expect(page.locator('#member-volume')).toHaveValue('100');
    await page.locator('.client[data-clid="alice"]').click({button:'right'});
    const slider=page.locator('.ctx-menu .ctx-volume input[type=range]'); await slider.fill('160'); await slider.dispatchEvent('change');
    await expect.poll(()=>page.evaluate(()=>window.__saved.user_volumes['alice-uid'])).toBe(160);
    await page.keyboard.press('Escape');
    await page.evaluate(()=>window.__noxa.renderTree());
    await expect(page.locator('#member-volume')).toHaveValue('160');
});

test('member card retains keyboard focus across volume saves', async ({page}) => {
    await page.evaluate(()=>window.__noxa.setDetailsOpen(true));
    const slider=page.locator('#member-volume'); await slider.focus(); await page.keyboard.press('ArrowRight');
    await expect.poll(()=>page.evaluate(()=>window.__saved.user_volumes['alice-uid'])).toBe(105);
    await expect(slider).toBeFocused();
    await page.keyboard.press('ArrowRight');
    await expect.poll(()=>page.evaluate(()=>window.__saved.user_volumes['alice-uid'])).toBe(110);
});

test('channel member exposes the shared personal volume menu', async ({page}) => {
    const participant=page.locator('#channel-tree .client[data-clid="alice"]');
    await participant.click({button:'right'});
    await expect(page.getByRole('menuitem',{name:'Reset volume to 100%'})).toBeVisible();
});

test('narrow layout offers a visible channel-navigation control', async ({page}) => {
    await page.setViewportSize({width:644,height:730});
    await expect(page.locator('#workspace-sidebar-toggle')).toBeVisible();
    await page.locator('#workspace-sidebar-toggle').click();
    await expect(page.locator('#channel-tree')).toBeVisible();
});

test('menu restores keyboard focus after a live speaking update', async ({page}) => {
    const member=page.locator('.client[data-clid="alice"]'); await member.focus(); await page.keyboard.press('Shift+F10');
    await page.evaluate(()=>{
        for(const callback of window.__events.event||[]) callback(JSON.stringify({type:'speaking_changed',data:{client_id:'alice',speaking:true}}));
    });
    await page.keyboard.press('Escape');
    await expect(member).toBeFocused();
});

test("identity-only author menu never picks an arbitrary connected device", async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.state.clients.push({ client_id: "alice-phone", unique_id: "alice-uid", nickname: "Alice phone", channel_id: 2 });
        for (const callback of window.__events.event || []) callback(JSON.stringify({ type: "chat", data: {
            id: 601, channel_id: 1, from: "Alice", from_unique_id: "alice-uid", text: "Two devices",
        } }));
    });
    await page.locator('.msg[data-msg-id="601"] .msg-from').click();
    await expect(page.getByRole("menuitem", { name: "Kick from server…", exact: true })).toHaveAttribute("aria-disabled", "true");
    await expect(page.getByRole("menuitem", { name: "Move to channel…", exact: true })).toHaveCount(0);
    await expect(page.getByRole("slider", { name: "Voice volume" })).toBeEnabled();
    await expect(page.locator('.ctx-menu [data-act="call"]')).not.toHaveAttribute("aria-disabled", "true");
    await expect(page.locator('.ctx-menu [data-act="call"]')).toBeEnabled();
});

test("a departed card session never falls back to another device", async ({ page }) => {
    await page.evaluate(() => {
        const v = window.__noxa;
        v.state.clients.push({ client_id: "alice-phone", unique_id: "alice-uid", nickname: "Alice phone", channel_id: 1 });
        v.state.selectedClientID = "alice-phone"; v.renderTree(); v.setDetailsOpen(true);
        v.state.clients = v.state.clients.filter(c => c.client_id !== "alice-phone");
    });
    await page.locator(".card-nick").click({ button: "right" });
    await expect(page.getByRole("menuitem", { name: "Move to channel…", exact: true })).toHaveCount(0);
    await expect(page.getByRole("menuitem", { name: "Kick from server…", exact: true })).toHaveAttribute("aria-disabled", "true");
});

test("settings and speaking updates preserve an active member-volume drag", async ({ page }) => {
    await page.evaluate(() => window.__noxa.setDetailsOpen(true));
    const slider = page.locator("#member-volume");
    await slider.evaluate(input => { input.value = "145"; input.dispatchEvent(new Event("input", { bubbles: true })); });
    await page.evaluate(() => {
        window.__saved.user_volumes["alice-uid"] = 70;
        for (const callback of window.__events.settings_update || []) callback(structuredClone(window.__saved));
        window.__noxa.renderTree();
    });
    await expect(slider).toHaveValue("145");
    await page.keyboard.press("Escape");
    expect(await page.evaluate(() => window.__gain.gain.value)).toBe(0.7);
});

test("channel member keyboard menu preserves its existing selection click", async ({ page }) => {
    const participant = page.locator('#channel-tree .client[data-clid="alice"]');
    await participant.click();
    await expect(page.locator(".card-nick")).toHaveText("Alice");
    await participant.focus(); await page.keyboard.press("Shift+F10");
    await expect(page.getByRole("menuitem", { name: "Reset volume to 100%" })).toBeVisible();
    await page.keyboard.press("Escape"); await expect(participant).toBeFocused();
});

test("live settings update refreshes both personal volume controls", async ({ page }) => {
    await page.locator('#channel-tree .client[data-clid="alice"]').click({ button: 'right' });
    await page.evaluate(() => {
        window.__saved.user_volumes['alice-uid'] = 65;
        window.__saved.user_share_volumes = { 'alice-uid': 25 };
        for (const callback of window.__events.settings_update || []) callback(structuredClone(window.__saved));
    });
    await expect(page.getByRole('slider', { name: 'Voice volume', exact: true })).toHaveValue('65');
    await expect(page.getByRole('slider', { name: 'Screen-share audio', exact: true })).toHaveValue('25');
});

test("keyboard mute returns focus to its member control", async ({ page }) => {
    const trigger = page.locator('#channel-tree .client[data-clid="alice"]');
    await trigger.focus(); await page.keyboard.press('Shift+F10');
    await page.getByRole('menuitem', { name: 'Mute voice for me', exact: true }).focus();
    await page.keyboard.press('Enter');
    await expect.poll(() => page.evaluate(() => window.__saved.muted_users)).toEqual(['alice-uid']);
    await expect(page.getByRole('menuitem', { name: 'Unmute voice for me', exact: true })).toBeFocused();
    await page.keyboard.press('Escape');
    await expect(trigger).toBeFocused();
});

test("member mute uses the current state after a live settings update", async ({ page }) => {
    const trigger = page.locator('#channel-tree .client[data-clid="alice"]');
    await trigger.click({ button: 'right' });
    await page.evaluate(() => {
        window.__saved.muted_users = ['alice-uid'];
        for (const callback of window.__events.settings_update || []) callback(structuredClone(window.__saved));
    });
    const mute = page.locator('.ctx-menu [data-act="mute"]');
    await expect(mute).toHaveAttribute("aria-pressed", "true");
    await expect(page.locator('.client[data-clid="alice"] .status-icons[title="muted locally"]')).toBeVisible();
    await mute.click();
    await expect.poll(() => page.evaluate(() => window.__saved.muted_users)).toEqual([]);
});

test("member Escape restores focus while a volume save is pending", async ({ page }) => {
    await page.evaluate(() => { window.__saveWait = new Promise(resolve => { window.__releaseSave = resolve; }); });
    const trigger = page.locator('#channel-tree .client[data-clid="alice"]');
    await trigger.click({ button: 'right' });
    const slider = page.getByRole('slider', { name: 'Voice volume', exact: true });
    await slider.focus(); await page.keyboard.press('ArrowRight');
    await expect(slider).toBeDisabled();
    await page.keyboard.press('Escape');
    await expect(page.getByRole('menu')).toHaveCount(0);
    await expect(trigger).toBeFocused();
    await page.evaluate(() => window.__releaseSave());
    await expect.poll(() => page.evaluate(() => window.__saved.user_volumes['alice-uid'])).toBe(101);
});

test("adjusting and resetting a muted microphone preserves its mute", async ({ page }) => {
    await page.evaluate(() => {
        window.__saved.muted_users = ['alice-uid'];
        for (const callback of window.__events.settings_update || []) callback(structuredClone(window.__saved));
    });
    await page.locator('#channel-tree .client[data-clid="alice"]').click({ button: 'right' });
    await expect(page.locator('.ctx-menu [data-act="mute"]')).toHaveAttribute("aria-pressed", "true");
    const slider = page.getByRole('slider', { name: 'Voice volume', exact: true });
    await slider.fill('140'); await slider.dispatchEvent('change');
    await expect.poll(() => page.evaluate(() => window.__saved.user_volumes['alice-uid'])).toBe(140);
    await page.getByRole('menuitem', { name: 'Reset volume to 100%', exact: true }).click();
    await expect(slider).toHaveValue('100');
    expect(await page.evaluate(() => window.__mute.gain.value)).toBe(0);
    await page.locator('.ctx-menu [data-act="mute"]').click();
    await expect.poll(() => page.evaluate(() => window.__mute.gain.value)).toBe(1);
});

test.describe('member controls on touch screens', () => {
    test.use({ hasTouch: true, viewport: { width: 644, height: 480 } });
    test('visible menu remains reachable and contained in a compact touch window', async ({ page }) => {
        await page.locator('#workspace-sidebar-toggle').tap();
        await page.locator('#channel-tree .client[data-clid="alice"]').tap();
        const trigger = page.locator('#client-card .card-nick');
        await trigger.tap();
        const menu = page.getByRole('menu');
        await expect(menu).toBeVisible();
        const box = await menu.boundingBox();
        expect(box.x).toBeGreaterThanOrEqual(0);
        expect(box.y).toBeGreaterThanOrEqual(0);
        expect(box.x + box.width).toBeLessThanOrEqual(644);
        expect(box.y + box.height).toBeLessThanOrEqual(480);
        const slider = page.getByRole('slider', { name: 'Screen-share audio', exact: true });
        await expect(slider).toBeVisible();
        if (process.env.NOXA_UI_AUDIT_SCREENSHOTS) await page.screenshot({ path: '.cache/ui-audit-members/member-menu-compact.png' });
        await page.keyboard.press('Escape');
        await expect(trigger).toBeFocused();
    });
});

test("member menu exposes and toggles the existing independent screen-share mute", async ({ page }) => {
    await page.evaluate(() => {
        window.__saved.muted_share_users = ['alice-uid'];
        for (const callback of window.__events.settings_update || []) callback(structuredClone(window.__saved));
    });
    const trigger = page.locator('#channel-tree .client[data-clid="alice"]');
    await trigger.click({ button: 'right' });
    const shareMute = page.locator('.ctx-menu [data-act="mute-share"]');
    await expect(shareMute).toHaveAttribute("aria-pressed", "true");
    expect(await page.evaluate(() => window.__mute.gain.value)).toBe(1);
    await shareMute.click();
    await expect.poll(() => page.evaluate(() => window.__saved.muted_share_users)).toEqual([]);
    await expect(page.getByRole('menu')).toBeVisible();
    await expect(shareMute).toHaveAttribute("aria-pressed", "false");
    await page.evaluate(() => { window.__saveFail = true; });
    await shareMute.click();
    await expect(page.locator('#toasts')).toContainText('save failed');
    await expect(page.getByRole('menu')).toBeVisible();
    await expect(shareMute).toHaveAttribute("aria-pressed", "false");
    expect(await page.evaluate(() => window.__saved.muted_users ?? [])).toEqual([]);
    expect(await page.evaluate(() => window.__saved.user_share_volumes ?? {})).toEqual({});
});

test("member mute immediately updates its visible channel state", async ({ page }) => {
    const trigger = page.locator('#channel-tree .client[data-clid="alice"]');
    await trigger.click({ button: 'right' });
    await page.locator('.ctx-menu [data-act="mute"]').click();
    await expect.poll(() => page.evaluate(() => window.__saved.muted_users)).toEqual(['alice-uid']);
    await expect(page.locator('.client[data-clid="alice"] .status-icons[title="muted locally"]')).toBeVisible();
});

test("another session of my account has session controls but cannot call its own account", async ({ page }) => {
    await page.evaluate(() => {
        const v = window.__noxa;
        v.state.myUniqueID = "local-storage-key";
        v.state.clients.push({ client_id: "self-phone", unique_id: "me", nickname: "My phone", channel_id: 1 });
        v.renderTree();
    });
    await page.locator('.client[data-clid="self-phone"]').click({ button: 'right' });
    await expect(page.locator('.ctx-menu [data-act="call"]')).toHaveCount(0);
    await expect(page.getByRole('menuitem', { name: 'Client Info', exact: true })).toBeVisible();
    await page.getByRole('menuitem', { name: 'Move to channel…', exact: true }).click();
    await page.getByRole('button', { name: 'Team', exact: true }).click();
    expect(await page.evaluate(() => window.__moves)).toEqual([["one", "self-phone", 2]]);
});
