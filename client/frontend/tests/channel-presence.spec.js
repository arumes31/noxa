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
        await expect(row.locator('.client-voice-state')).toHaveCount(1);
        await page.evaluate(flag => {
            const { state, renderTree } = window.__noxa;
            state.clients.find(c => c.client_id === 'peer')[flag] = true;
            renderTree();
        }, flag);
        await expect(row.locator('.client-voice-state')).toHaveCount(0);
        await page.evaluate(() => window.__activity(true));
        await expect(row).not.toHaveClass(/speaking/);
        expect(await page.evaluate(() => window.__noxa.state.clients.find(c => c.client_id === 'peer').is_speaking)).toBe(false);
        await page.evaluate(flag => { window.__noxa.state.clients.find(c => c.client_id === 'peer')[flag] = false; window.__activity(true); }, flag);
        await expect(row.locator('.client-voice-state')).toHaveCount(1);
        await page.evaluate(flag => window.__membership(7, { [flag]: true, is_speaking: true }), flag);
        await expect(row.locator('.client-voice-state')).toHaveCount(0);
        expect(await page.evaluate(() => window.__noxa.state.clients.find(c => c.client_id === 'peer').is_speaking)).toBe(false);
    });
}

test('own mute immediately hides speaking before the server reply', async ({ page }) => {
    await page.evaluate(() => {
        const { state, renderTree } = window.__noxa;
        state.clients.find(c => c.client_id === 'self').is_speaking = true;
        renderTree();
    });
    const row = page.locator('.client[data-clid="self"]');
    await expect(row.locator('.client-voice-state')).toHaveCount(1);
    await page.evaluate(() => document.getElementById('voice-mute').click());
    await expect(row.locator('.client-voice-state')).toHaveCount(0);
});
