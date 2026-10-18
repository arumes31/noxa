import { expect, test } from './fixtures.js';

// App shell (design 45): top-bar search and ping, sidebar Voice/Text/Direct
// sections, the server menu and the voice card.
test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
        window.__events = {};
        window.__calls = [];
        window.runtime = { EventsOn: (name, fn) => { (window.__events[name] ||= []).push(fn); return () => {}; }, EventsEmit() {}, WindowIsFullscreen: async () => false };
        window.go = { main: { App: new Proxy({}, { get(_target, method) { return async (...args) => {
            window.__calls.push([method, ...args]);
            if (method === 'GetSettings') return { language: window.__language || 'en', onboarding_done: true, alpha_dismissed: 'test', effects_enabled: false, bookmarks: [], chat_max_lines: 200 };
            if (['ListTabs', 'GetPermissions', 'ListStreamsForTab'].includes(method)) return [];
            if (['IdentityInfo', 'GetAvatar', 'GetMediaLimitsForTab'].includes(method)) return {};
            if (['Connected', 'IsGuest', 'GamingOverlayAvailable'].includes(method)) return false;
            if (['ClientVersion', 'ClientVersionShort'].includes(method)) return 'test';
            if (method === 'ConversationForTab') return { conversations: [{ id: 'g1', name: 'Raid leads', unread_count: 0, members: [] }] };
            return '';
        }; } }) } };
    });
    await page.goto('/');
    await page.waitForFunction(() => !!window.__noxaShell);
    await page.evaluate(() => {
        const v = window.__noxa;
        Object.assign(v.state, { activeTabID: 'shell-test', myClientID: 'self', myUniqueID: 'u-self', myNickname: 'Daniel', ownAuthority: 'owner' });
        v.showWorkspace(false);
        const c = (id, nick, channel, extra = {}) => ({ client_id: id, unique_id: 'u-' + id, nickname: nick, channel_id: channel, ...extra });
        const snapshot = { root_channels: [
            { ChannelID: 1, Name: 'Lobby', clients: [c('oren', 'Oren', 1)] },
            { ChannelID: 2, Name: 'Raid Night', clients: [c('self', 'Daniel', 2), c('jinx', 'Jinx', 2, { sharing: true }), c('sable', 'Sable', 2, { self_muted: true })] },
            { ChannelID: 3, Name: 'War Room', access: { restricted: true, can_connect: true }, clients: [] },
            { ChannelID: 11, Name: 'general', TextOnly: true, clients: [] },
            { ChannelID: 12, Name: 'loot-council', TextOnly: true, OrderIndex: 1, clients: [] },
        ] };
        v.state.myChannelID = 2;
        for (const callback of window.__events.snapshot) callback(JSON.stringify(snapshot));
    });
});

test('text channels list under Text, never in the voice tree, and open their chat', async ({ page }) => {
    await expect(page.locator('#channel-tree .channel[data-chid="11"]')).toHaveCount(0);
    await expect(page.locator('#channel-tree .channel[data-chid="2"]')).toBeVisible();
    const rows = page.locator('#text-channels .text-channel');
    await expect(rows).toHaveText(['general', 'loot-council']);
    await expect(page.getByRole('heading', { name: 'Text', exact: true })).toBeVisible();
    await expect(rows.first()).toHaveAccessibleName('Text channel general');
    await rows.first().click();
    await expect.poll(() => page.evaluate(() => window.__calls.filter(([method]) => method === 'SubscribeChannelsForTab').map(([, tab, ids, on]) => [tab, ids, on])))
        .toEqual([['shell-test', [11], true]]);
    expect(await page.evaluate(() => window.__calls.some(([method, , id]) => method === 'JoinChannelForTab' && id === 11))).toBe(false);
});

test('voice tree shows LIVE, lock and muted state in text and icons', async ({ page }) => {
    const jinx = page.locator('#channel-tree .client[data-clid="jinx"]');
    await expect(jinx.locator('.client-stream-state')).toHaveText('LIVE');
    await expect(jinx).toHaveAccessibleName('Jinx, Sharing a stream');
    await expect(page.locator('#channel-tree .channel[data-chid="3"] .ch-access-lock')).toHaveCount(1);
    await expect(page.locator('#channel-tree .client[data-clid="sable"] .status-icons')).toHaveAttribute('aria-label', /muted/i);
});

test('search field and Ctrl+K open the same accessible quick switcher', async ({ page }) => {
    await page.locator('#quick-switch-open').click();
    const input = page.getByRole('combobox', { name: 'Search or jump to' });
    await expect(input).toBeFocused();
    await expect(page.getByRole('listbox')).toBeVisible();
    await input.fill('loot');
    await expect(page.getByRole('option', { name: /loot-council/ })).toHaveAttribute('aria-selected', 'true');
    await expect(input).toHaveAttribute('aria-activedescendant', 'qs-option-0');
    await input.fill('raid lea');
    await expect(page.getByRole('option', { name: /Raid leads/ })).toBeVisible();
    await input.fill('notif');
    await expect(page.getByRole('option', { name: /Notifications/ }).first()).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(page.locator('.qs-overlay')).toHaveCount(0);
    await page.keyboard.press('Control+k');
    await expect(page.getByRole('combobox', { name: 'Search or jump to' })).toBeFocused();
});

test('server menu opens from the chevron and works with the keyboard', async ({ page }) => {
    const chevron = page.locator('#server-menu-btn');
    await chevron.focus();
    await page.keyboard.press('Enter');
    await expect(chevron).toHaveAttribute('aria-expanded', 'true');
    const menu = page.getByRole('menu', { name: 'Server menu' });
    await expect(menu.getByRole('menuitem', { name: 'Server information' })).toBeFocused();
    await expect(menu.getByRole('menuitem', { name: 'Create text channel…' })).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(chevron).toHaveAttribute('aria-expanded', 'false');
    await expect(chevron).toBeFocused();
});

test('Messages counts direct messages and opens the direct view', async ({ page }) => {
    await page.evaluate(() => {
        window.__noxaChat.addChat({ direct: true, enc_verified: true, from_unique_id: 'u-mira', to_unique_id: 'u-self', from: 'Mira', client_msg_id: 'm1', text: 'hi' });
        window.__noxa.trayUnread.set('groups', 2);
    });
    const messages = page.locator('#direct-messages');
    // The count is the DM view's own DM + private-group total when that view
    // is present, else the shell's fallback; either way Mira's DM is unread.
    await expect(messages.locator('.side-count')).toHaveText(/^[1-9]\d*$/);
    await expect(messages).toHaveAccessibleName(/^Messages, [1-9]\d* unread$/);
    await messages.click();
    // The row opens the dedicated direct-messages view without opening (and
    // sending read receipts for) a conversation: the select hint shows until
    // the user picks Mira's conversation.
    await expect.poll(() => page.evaluate(() => window.__noxa.isDirectMessagesOpen?.())).toBe(true);
    await expect(page.locator('#dm-thread-empty')).toBeVisible();
    await page.locator('#dm-conversations .dm-row').filter({ hasText: 'Mira' }).click();
    await expect(page.locator('#dm-head-name')).toContainText('Mira');
});

test('voice card names the channel and the streaming identity', async ({ page }) => {
    await page.evaluate(() => { window.__noxa.state.screenSharing = true; window.__noxa.renderTree(); });
    await expect(page.locator('#voice-channel-name')).toHaveText('Raid Night');
    await expect(page.locator('#voice-self .voice-self-name')).toHaveText('Daniel');
    await expect(page.locator('#voice-self .voice-live-badge')).toHaveText('LIVE');
    // Icon-only controls keep their accessible names and their order.
    for (const id of ['voice-mute', 'voice-deafen', 'voice-screen', 'voice-video', 'voice-leave-channel']) {
        await expect(page.locator('#' + id)).toBeVisible();
        await expect(page.locator('#' + id)).toHaveAccessibleName(/\S/);
    }
    await expect(page.locator('#voice-leave-channel')).toHaveAccessibleName('Leave voice channel');
    await expect(page.locator('#voice-options > summary')).toHaveAccessibleName(/\S/);
    const left = id => page.locator(id).evaluate(element => element.getBoundingClientRect().left);
    const order = await Promise.all(['#voice-mute', '#voice-deafen', '#voice-screen', '#voice-video', '#voice-options > summary', '#voice-leave-channel'].map(left));
    expect([...order].sort((a, b) => a - b)).toEqual(order);
});

test('ping readout lives in the top bar and keeps its server-information action', async ({ page }) => {
    await page.evaluate(() => {
        const latency = document.getElementById('voice-latency');
        latency.hidden = false; latency.textContent = '24 ms'; latency.dataset.quality = 'good';
    });
    await expect(page.locator('#topbar #voice-latency')).toHaveText('24 ms');
    await expect(page.locator('#voice-bar')).toHaveAttribute('data-quality', 'good');
});

test('German shell strings', async ({ page }) => {
    await page.evaluate(() => {
        window.__noxa.state.settings.language = 'de';
        window.__noxa.applyAppearance();
    });
    await expect(page.locator('#voice-heading')).toHaveText('Sprache');
    await expect(page.locator('#direct-heading')).toHaveText('Direkt');
    await expect(page.locator('#quick-switch-open')).toHaveAccessibleName('Suchen oder springen');
    await expect(page.locator('#direct-messages')).toHaveAccessibleName('Nachrichten');
});
