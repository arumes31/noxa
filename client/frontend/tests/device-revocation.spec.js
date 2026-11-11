import { expect, test } from './fixtures.js';

// (38) A revoked device of a contact or of this account: signed revocations
// (verified and pinned on the Go side) and server-only ones read differently.
test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
        window.__events = {};
        window.runtime = { EventsOn: (name, fn) => { (window.__events[name] ||= []).push(fn); return () => {}; }, EventsEmit() {}, WindowIsFullscreen: async () => false };
        window.go = { main: { App: new Proxy({}, { get(_target, method) { return async () => {
            if (method === 'GetSettings') return { language: 'en', onboarding_done: true, alpha_dismissed: 'test', effects_enabled: false, bookmarks: [], chat_max_lines: 200 };
            if (['ListTabs', 'GetPermissions', 'ListStreamsForTab'].includes(method)) return [];
            if (['IdentityInfo', 'GetAvatar', 'GetMediaLimitsForTab'].includes(method)) return {};
            if (['Connected', 'IsGuest', 'GamingOverlayAvailable'].includes(method)) return false;
            if (['ClientVersion', 'ClientVersionShort'].includes(method)) return 'test';
            return '';
        }; } }) } };
    });
    await page.goto('/');
    await page.waitForFunction(() => !!window.__noxaShell && !!window.__events.device_revoked);
    await page.evaluate(() => {
        Object.assign(window.__noxa.state, { activeTabID: 'rev-test', myClientID: 'self', myUniqueID: 'u-self', myNickname: 'Daniel' });
        window.__noxa.showWorkspace(false);
    });
});

const fire = (page, detail) => page.evaluate(d => { for (const callback of window.__events.device_revoked) callback(d); }, detail);

test('signed and server-only revocations read differently and reach the open conversation', async ({ page }) => {
    await page.evaluate(() => window.__noxaChat.openPM('u-jinx', 'Jinx'));
    await fire(page, { unique_id: 'u-jinx', device_id: 'phone', signed: true, signer_device_id: 'desktop' });
    await expect(page.getByText('signed out one of their devices. It was verified', { exact: false }).first()).toBeVisible();
    await expect(page.locator('#chat-log .device-revoked')).toHaveCount(1);
    await expect(page.locator('#chat-log .device-revoked')).toContainText('verified and no longer receives your messages');

    await fire(page, { unique_id: 'u-jinx', device_id: 'tablet', signed: false });
    await expect(page.locator('#chat-log .device-revoked')).toHaveCount(2);
    await expect(page.locator('#chat-log .device-revoked').last()).toContainText('did not sign this removal');

    // Another contact's revocation is a toast only, not a line here.
    await fire(page, { unique_id: 'u-sable', device_id: 'laptop', signed: false });
    await expect(page.locator('#chat-log .device-revoked')).toHaveCount(2);
});

test('this account\'s own revocations say so', async ({ page }) => {
    await fire(page, { unique_id: 'u-self', device_id: 'old-laptop', signed: true, signer_device_id: 'desktop' });
    await expect(page.getByText('One of your devices was signed out from another of your devices.').first()).toBeVisible();
    await fire(page, { unique_id: 'u-self', device_id: 'old-phone', signed: false });
    await expect(page.getByText('The server removed one of your devices.').first()).toBeVisible();
    // A malformed event is ignored.
    await fire(page, { device_id: 'x' });
});
