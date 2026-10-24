import { expect, test } from './fixtures.js';

test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
        window.__events = {};
        window.__calls = [];
        window.runtime = { EventsOn: (name, fn) => { (window.__events[name] ||= []).push(fn); return () => {}; }, EventsEmit() {}, WindowIsFullscreen: async () => false, BrowserOpenURL: url => window.__calls.push(['BrowserOpenURL', url]) };
        window.__devices = [
            { device_id: 'dev-desktop', kind: 'desktop', name: 'Desktop', fingerprint: 'aaaa bbbb cccc dddd', current: true, first_device: true, created_at: 1, last_seen_at: Math.floor(Date.now() / 1000) },
        ];
        window.go = { main: { App: new Proxy({}, { get(_target, method) { return async (...args) => {
            window.__calls.push([method, ...args]);
            if (method === 'GetSettings') return { language: 'en', onboarding_done: true, alpha_dismissed: 'test', effects_enabled: false,
                play_sounds: false, bookmarks: [], chat_max_lines: 200 };
            if (method === 'CreateDeviceLinkForTab') return { link_id: 'link-1', url: window.__linkURL || 'https://chat.example/link#id=link-1&k=x', code: 'K7QM-4XPA',
                expires_at: Math.floor(Date.now() / 1000) + 120, qr: 'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==' };
            if (method === 'DevicesForTab') return window.__devices;
            if (method === 'DecideDeviceLinkForTab') {
                window.__devices = [...window.__devices, { device_id: 'dev-phone', kind: 'web', name: 'iPhone · Safari', fingerprint: '1111 2222 3333 4444', created_at: 2, last_seen_at: Math.floor(Date.now() / 1000) }];
                return args[2] ? { action: 'link_decide', state: 'approved', device: { name: 'iPhone · Safari' } } : { action: 'link_decide', state: 'denied' };
            }
            if (['ListTabs', 'GetPermissions', 'ListStreamsForTab'].includes(method)) return [];
            if (['IdentityInfo', 'GetAvatar', 'GetMediaLimitsForTab'].includes(method)) return {};
            if (['Connected', 'IsGuest', 'GamingOverlayAvailable'].includes(method)) return false;
            if (['ClientVersion', 'ClientVersionShort'].includes(method)) return 'test';
            return '';
        }; } }) } };
    });
    await page.goto('/');
    await page.waitForFunction(() => !!window.__noxa?.state);
    await page.evaluate(() => Object.assign(window.__noxa.state, { activeTabID: 'tab-1', myClientID: 'self', myNickname: 'Rowan' }));
    await page.evaluate(() => {
        document.getElementById('login-overlay').classList.add('hidden');
        document.getElementById('login-overlay').setAttribute('aria-hidden', 'true');
        document.getElementById('app').classList.remove('hidden');
        document.getElementById('app').setAttribute('aria-hidden', 'false');
    });
});

test('PWA / Mobile login shows a QR code with a hidden code and approves after comparison', async ({ page }) => {
    await page.locator('#menubar > .menu-item').filter({ hasText: /^Connections/ }).click();
    await page.getByRole('menuitem', { name: 'PWA / Mobile login…', exact: true }).click();

    const dialog = page.getByRole('dialog', { name: 'PWA / Mobile login' });
    await expect(dialog.getByRole('img', { name: 'QR code that links your phone to this account' })).toBeVisible();
    const pwa = dialog.getByRole('link', { name: 'Open PWA', exact: true });
    await expect(pwa).toHaveAttribute('href', 'https://chat.example/app/');
    await expect(dialog.getByText('https://chat.example/app/', { exact: true })).toBeVisible();
    await pwa.click();
    expect(await page.evaluate(() => window.__calls.filter(call => call[0] === 'BrowserOpenURL'))).toEqual([['BrowserOpenURL', 'https://chat.example/app/']]);
    await expect(pwa).toBeVisible();
    const code = dialog.locator('.device-link-code');
    await expect(code).toHaveText('K7QM-4XPA');
    await expect(code).toHaveAttribute('data-hidden', 'true');
    await dialog.getByRole('button', { name: 'Show code' }).click();
    await expect(code).toHaveAttribute('data-hidden', 'false');
    await expect(dialog.getByText(/Expires in \d+ s/)).toBeVisible();
    await expect(dialog.locator('.device-row strong')).toHaveText(['Desktop']);

    await page.evaluate(() => {
        for (const fn of window.__events.device_event) fn({ event: 'link_claimed', link_id: 'link-1', platform: 'iPhone · Safari',
            ed25519_public_key: 'phone-key', x25519_public_key: 'phone-x', via: 'qr', sas: '47', fingerprint: '1111 2222 3333 4444' });
    });
    await expect(dialog.getByText('iPhone · Safari wants to sign in as Rowan.')).toBeVisible();
    await expect(dialog.locator('.device-link-sas')).toHaveText('47');
    await expect(dialog.getByText('Device fingerprint: 1111 2222 3333 4444')).toBeVisible();
    await dialog.getByRole('button', { name: 'Approve' }).click();

    await expect(dialog.getByText('iPhone · Safari is linked. It can now sign in on its own.')).toBeVisible();
    await expect(dialog.locator('.device-row')).toHaveCount(2);
    const decide = await page.evaluate(() => window.__calls.find(call => call[0] === 'DecideDeviceLinkForTab'));
    expect(decide).toEqual(['DecideDeviceLinkForTab', 'tab-1', 'link-1', true, 'phone-key', 'phone-x']);
});

test('PWA link does not open an unsafe server-supplied URL', async ({ page }) => {
    await page.evaluate(async () => {
        window.__linkURL = 'javascript:alert(1)';
        (await import('/src/device-link-ui.js')).openDeviceLinkDialog();
    });
    const dialog = page.getByRole('dialog', { name: 'PWA / Mobile login' });
    await expect(dialog.locator('.device-link-code')).toHaveText('K7QM-4XPA');
    await expect(dialog.getByRole('link', { name: 'Open PWA' })).toHaveCount(0);
});

test('claims for another link are ignored', async ({ page }) => {
    await page.evaluate(async () => (await import('/src/device-link-ui.js')).openDeviceLinkDialog());
    const dialog = page.getByRole('dialog', { name: 'PWA / Mobile login' });
    await expect(dialog.locator('.device-link-code')).toHaveText('K7QM-4XPA');
    await page.evaluate(() => {
        for (const fn of window.__events.device_event) fn({ event: 'link_claimed', link_id: 'other', platform: 'Attacker', sas: '12' });
    });
    await expect(dialog.getByText('Waiting for your phone…')).toBeVisible();
    await expect(dialog.locator('.device-link-sas')).toHaveCount(0);
});
