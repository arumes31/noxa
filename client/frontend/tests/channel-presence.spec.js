import { expect, test } from './fixtures.js';

test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
        window.__events = {};
        window.runtime = { EventsOn: (name, fn) => { (window.__events[name] ||= []).push(fn); return () => {}; }, EventsEmit() {}, WindowIsFullscreen: async () => false };
        window.go = { main: { App: new Proxy({}, { get(_target, method) { return async () => {
            if (method === 'GetSettings') return { language: 'en', onboarding_done: true, alpha_dismissed: 'test', effects_enabled: false,
                spoken_messages: true, speech_volume: 100, play_sounds: true, bookmarks: [], chat_max_lines: 200 };
            if (['ListTabs', 'GetPermissions', 'ListStreamsForTab'].includes(method)) return [];
            if (['IdentityInfo', 'GetAvatar', 'GetMediaLimitsForTab'].includes(method)) return {};
            if (['Connected', 'IsGuest', 'GamingOverlayAvailable'].includes(method)) return false;
            if (['ClientVersion', 'ClientVersionShort'].includes(method)) return 'test';
            return '';
        }; } }) } };
    });
    await page.goto('/');
    await page.waitForFunction(() => !!window.__noxa?.soundEngine);
    await page.evaluate(async () => {
        const { state, soundEngine, speechQueue } = window.__noxa;
        Object.assign(state, { activeTabID: 'presence-test', myClientID: 'self', myChannelID: 7, pc: new RTCPeerConnection() });
        await soundEngine.preload(); await soundEngine.resume(); speechQueue.clear();
        window.__spoken = [];
        const play = soundEngine.play.bind(soundEngine);
        soundEngine.play = (id, options) => {
            const played = play(id, options);
            if (played && id.startsWith('speech_')) window.__spoken.push(id);
            return played;
        };
        window.__membership = (peerChannel = 7, audioState = {}) => {
            const clients = [{ client_id: 'self', nickname: 'Self', channel_id: 7 }];
            if (peerChannel !== null) clients.push({ client_id: 'peer', nickname: 'Peer', channel_id: peerChannel, ...audioState });
            const snapshot = { root_channels: [7, 8].map(id => ({ ChannelID: id, Name: 'Channel ' + id,
                clients: clients.filter(client => client.channel_id === id) })), unassigned_clients: clients.filter(client => !client.channel_id) };
            for (const callback of window.__events.snapshot) callback(JSON.stringify(snapshot));
        };
        window.__membership();
    });
});

for (const language of ['en', 'de']) {
    for (const [channel, event] of [[null, 'user_disconnected'], [0, 'user_leave'], [8, 'user_moved']]) {
        test(`${language} live snapshot plays ${event} once`, async ({ page }) => {
            await page.evaluate(({ channel, language }) => { window.__noxa.state.settings.speech_language = language; window.__membership(channel); }, { channel, language });
            await expect.poll(() => page.evaluate(() => window.__spoken)).toEqual([`speech_${language}_${event}`]);
            await page.evaluate(channel => window.__membership(channel), channel);
            await page.waitForTimeout(300);
            expect(await page.evaluate(() => window.__spoken)).toEqual([`speech_${language}_${event}`]);
        });
    }
}

for (const condition of ['replay', 'reconnect', 'tab switch', 'notifications disabled']) {
    test(`membership announcements stay silent during ${condition}`, async ({ page }) => {
        await page.evaluate(condition => {
            const state = window.__noxa.state;
            if (condition === 'replay') state.replayingTabID = state.activeTabID;
            if (condition === 'reconnect') state.sessionGeneration++;
            if (condition === 'tab switch') state.activeTabID = 'other-tab';
            if (condition === 'notifications disabled') state.settings.notify_matrix = { join_leave: { sound: false } };
            window.__membership(null);
        }, condition);
        await page.waitForTimeout(400);
        expect(await page.evaluate(() => window.__spoken)).toEqual([]);
    });
}

for (const flag of ['self_muted', 'self_deafened', 'server_muted']) {
    test(`speaking indicator clears for ${flag}, including late activity`, async ({ page }) => {
        const row = page.locator('.client[data-clid="peer"]');
        await page.evaluate(() => {
            window.__activity = speaking => {
                for (const callback of window.__events.event) callback(JSON.stringify({ type: 'speaking_changed', data: { client_id: 'peer', channel_id: 7, speaking } }));
            };
            window.__activity(true);
        });
        await expect(row.locator('.speaking-avatar canvas')).toHaveCount(1);
        await page.evaluate(flag => {
            const { state, renderTree } = window.__noxa;
            state.clients.find(c => c.client_id === 'peer')[flag] = true;
            renderTree();
        }, flag);
        await expect(row.locator('.speaking-avatar canvas')).toHaveCount(0);
        await page.evaluate(() => window.__activity(true));
        await expect(row).not.toHaveClass(/speaking/);
        expect(await page.evaluate(() => window.__noxa.state.clients.find(c => c.client_id === 'peer').is_speaking)).toBe(false);
        await page.evaluate(flag => { window.__noxa.state.clients.find(c => c.client_id === 'peer')[flag] = false; window.__activity(true); }, flag);
        await expect(row.locator('.speaking-avatar canvas')).toHaveCount(1);
        await page.evaluate(flag => window.__membership(7, { [flag]: true, is_speaking: true }), flag);
        await expect(row.locator('.speaking-avatar canvas')).toHaveCount(0);
        expect(await page.evaluate(() => window.__noxa.state.clients.find(c => c.client_id === 'peer').is_speaking)).toBe(false);
    });
}

test('stream badges distinguish sharing from watching and clear stopped publications', async ({ page }, testInfo) => {
    await page.evaluate(async () => {
        window.__noxa.showWorkspace(false);
        window.__membership(7, { sharing: true });
        window.__screenCatalog = [{ publisher_id: 'peer', slot: 'screen', generation: '1', preview_at: 0, watch_revision: '0' }];
        const app = window.go.main.App;
        window.go.main.App = new Proxy(app, { get(target, method) {
            if (method !== 'VideoStreamControlForTab') return target[method];
            return async (_tab, request) => ({ ...request, streams: window.__screenCatalog, session: '1' });
        } });
        window.__streamControls = await import('/src/stream-controls.js');
        window.__streamControls.startStreamSession(window.__noxa.state.pc, () => null, () => {});
    });
    const member = page.locator('.client[data-clid="peer"]');
    await expect(member.getByRole('img', { name: 'Sharing a stream', exact: true })).toBeVisible();
    await expect(member.locator('.client-stream-state')).not.toHaveClass(/watching/);
    await page.getByRole('button', { name: 'Watch', exact: true }).click();
    await expect(member.getByRole('img', { name: 'You’re watching this stream', exact: true })).toBeVisible();
    await expect(member.locator('.client-stream-state')).toHaveClass(/watching/);
    await page.screenshot({ path: testInfo.outputPath('watching-stream.png') });
    await page.getByRole('button', { name: 'Stop watching', exact: true }).click();
    await expect(member.getByRole('img', { name: 'Sharing a stream', exact: true })).toBeVisible();
    await page.evaluate(() => { window.__screenCatalog = []; });
    await expect(member.locator('.client-stream-state')).toHaveCount(0);
    await page.evaluate(() => window.__streamControls.stopStreamSession());
});

test('own mute immediately hides speaking before the server reply', async ({ page }) => {
    await page.evaluate(() => {
        const { state, renderTree } = window.__noxa;
        state.clients.find(c => c.client_id === 'self').is_speaking = true;
        renderTree();
    });
    const row = page.locator('.client[data-clid="self"]');
    await expect(row.locator('.speaking-avatar canvas')).toHaveCount(1);
    await page.evaluate(() => document.getElementById('voice-mute').click());
    await expect(row.locator('.speaking-avatar canvas')).toHaveCount(0);
});

test('channel members stay alphabetical across snapshots and shift selection follows that order', async ({ page }) => {
    await page.evaluate(() => {
        window.__orderedMembers = [
            { client_id: 'self', unique_id: 'self', nickname: 'Zoe' },
            { client_id: 'bob', unique_id: 'bob', nickname: 'bob' },
            { client_id: 'alex-z', unique_id: 'z', nickname: 'alex' },
            { client_id: 'user10', unique_id: 'user10', nickname: 'User10' },
            { client_id: 'alice', unique_id: 'alice', nickname: 'Alice' },
            { client_id: 'user2', unique_id: 'user2', nickname: 'User2' },
            { client_id: 'alex-a', unique_id: 'a', nickname: 'Alex' },
        ].map(member => ({ ...member, channel_id: 7 }));
        window.__orderedSnapshot = () => {
            const snapshot = { root_channels: [{ ChannelID: 7, Name: 'Members', clients: window.__orderedMembers }] };
            for (const callback of window.__events.snapshot) callback(JSON.stringify(snapshot));
        };
        window.__orderedSnapshot();
    });
    const order = () => page.locator('.channel-members .client').evaluateAll(rows => rows.map(row => row.dataset.clid));
    const expected = ['alex-a', 'alex-z', 'alice', 'bob', 'user2', 'user10', 'self'];
    expect(await order()).toEqual(expected);
    await page.evaluate(() => {
        window.__orderedMembers.reverse();
        window.__orderedMembers.find(member => member.client_id === 'bob').is_speaking = true;
        window.__orderedMembers.find(member => member.client_id === 'alice').self_muted = true;
        window.__orderedSnapshot();
    });
    expect(await order()).toEqual(expected);
    await page.evaluate(() => {
        document.querySelector('.channel-members [data-clid="alice"]').click();
        document.querySelector('.channel-members [data-clid="user2"]').dispatchEvent(new MouseEvent('click', { bubbles: true, shiftKey: true }));
    });
    expect(await page.locator('.channel-members .client.selected').evaluateAll(rows => rows.map(row => row.dataset.clid))).toEqual(['alice', 'bob', 'user2']);
});
