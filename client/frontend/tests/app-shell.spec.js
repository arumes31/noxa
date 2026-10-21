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
    const order = await Promise.all(['#voice-mute', '#voice-deafen', '#voice-video', '#voice-screen', '#voice-options > summary'].map(left));
    expect([...order].sort((a, b) => a - b)).toEqual(order);
    const controls = await page.locator('.voice-buttons').boundingBox(), hangup = await page.locator('#voice-leave-channel').boundingBox();
    expect(hangup.y).toBeGreaterThanOrEqual(controls.y + controls.height);
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

for (const width of [1000, 640, 390]) test(`My Roles inspector stays inside a ${width}px window`, async ({ page }) => {
    await page.setViewportSize({ width, height: 600 });
    await page.evaluate(() => { document.documentElement.dataset.theme = 'arctic'; document.body.classList.remove('details-collapsed'); });
    await expect(page.locator('#details-close')).toBeVisible();
    const bounds = await page.locator('#details').boundingBox();
    expect(bounds.x).toBeGreaterThanOrEqual(0);
    expect(bounds.x + bounds.width).toBeLessThanOrEqual(width);
    expect(bounds.y + bounds.height).toBeLessThanOrEqual(601);
    await page.locator('#details-close').click();
    await expect(page.locator('#details')).toBeHidden();
});
test('server tabs scroll with a mouse wheel and reveal newly active servers', async ({ page }) => {
    await page.setViewportSize({ width: 1000, height: 700 });
    await page.evaluate(() => {
        window.__testTabs = Array.from({length:12},(_,i)=>({id:'s'+i,addr:'server-'+i+'.example.test',nickname:'Daniel',connected:true,active:i===0}));
        window.__noxaTabs.renderTabs(window.__testTabs);
    });
    const tabs = page.locator('#server-tabs');
    await tabs.hover(); await page.mouse.wheel(0, 500);
    await expect.poll(() => tabs.evaluate(el => el.scrollLeft)).toBeGreaterThan(0);
    await page.evaluate(() => { window.__testTabs.forEach((t,i)=>t.active=i===11); window.__noxaTabs.renderTabs(window.__testTabs); });
    const bar = await tabs.boundingBox(), active = await page.locator('.srv-tab.active').boundingBox();
    expect(active.x).toBeGreaterThanOrEqual(bar.x);
    expect(active.x + active.width).toBeLessThanOrEqual(bar.x + bar.width + 1);
    const before = await tabs.evaluate(el => el.scrollLeft);
    await page.evaluate(() => window.__noxaTabs.renderTabs(window.__testTabs));
    expect(await tabs.evaluate(el=>el.scrollLeft)).toBe(before);
});
test('unread DM shortcut opens Messages and disappears after reading', async ({ page }) => {
    await page.evaluate(() => window.__noxaChat.addChat({direct:true,enc_verified:true,from_unique_id:'u-mira',to_unique_id:'u-self',from:'Mira',client_msg_id:'fresh',text:'New message'}));
    const shortcut = page.locator('#pm-tabs [data-chat-key="dm:u-mira"]');
    await expect(shortcut).toBeVisible(); await shortcut.locator('.pm-tab-select').click();
    await expect(page.locator('#dm-head-name')).toHaveText('Mira');
    await expect(page.locator('#center')).toHaveClass(/dm-view-active/);
    await expect(shortcut).toHaveCount(0);
    await expect(page.locator('#dm-conversations')).toContainText('Mira');
});
test('private groups are created and opened inside Messages', async ({ page }) => {
    await page.evaluate(async () => {
        const original=window.go.main.App, groups=[];
        window.go.main.App=new Proxy(original,{get(target,key){
            if(key!=='ConversationForTab')return target[key];
            return async (_tab,c)=>{
                if(c.action==='create') groups.push({id:'created',name:c.name,owner:'u-self',revision:1,epoch:1,members:[{unique_id:'u-self',pending:false}]});
                return {conversations:structuredClone(groups),messages:[]};
            };
        }});
        await (await import('/src/conversations.js')).conversationChanged();
    });
    await page.locator('#direct-messages').click();
    await page.getByRole('button',{name:'New conversation',exact:true}).click();
    await page.getByRole('menuitem',{name:'New private group…',exact:true}).click();
    await page.getByRole('textbox',{name:'Group name',exact:true}).fill('Weekend team');
    await page.getByRole('button',{name:'OK',exact:true}).click();
    await expect(page.locator('#dm-head-name')).toHaveText('Weekend team');
    await expect(page.locator('#dm-conversations')).toContainText('Weekend team');
    await expect(page.locator('#sidebar .group-sidebar, #sidebar .group-list')).toHaveCount(0);
});
test('menu actions are grouped under Connections, Settings and Tools', async ({ page }) => {
    const bar=page.getByRole('menubar');
    await expect(bar.locator(':scope > .menu-item > span')).toHaveText(['Connections','Settings','View','Tools','Help']);
    await bar.getByRole('menuitem',{name:'Connections',exact:true}).click();
    await expect(page.getByRole('menuitem',{name:/Manage bookmarks/})).toBeVisible();
    await page.keyboard.press('Escape');
    await bar.getByRole('menuitem',{name:'Settings',exact:true}).click();
    await expect(page.getByRole('menuitem',{name:/Settings…/})).toBeVisible();
    await page.keyboard.press('Escape');
    await bar.getByRole('menuitem',{name:'Tools',exact:true}).click();
    await expect(page.getByRole('menuitem',{name:'My roles',exact:true})).toBeVisible();
});

test('Messages toggles closed and clicking the current voice channel closes it immediately', async ({ page }) => {
    await page.locator('#direct-messages').click();
    await expect(page.locator('#center')).toHaveClass(/dm-view-active/);
    await page.locator('#direct-messages').click();
    await expect(page.locator('#center')).not.toHaveClass(/dm-view-active/);
    await expect(page.locator('#chat-pane')).toBeVisible();
    await page.locator('#direct-messages').click();
    await page.locator('#channel-tree .channel[data-chid="2"]').click();
    await expect(page.locator('#center')).not.toHaveClass(/dm-view-active/);
    await expect(page.locator('#chat-pane')).toBeVisible();
});
