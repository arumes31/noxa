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
            if (method === 'SessionInfoForTab') return window.__mobileInfo || { connected: true, mobile_available: true, mobile_account_key: 'server|rowan' };
            if (method === 'DevicesForTab') return window.__devicePromise || window.__devices;
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
    await page.evaluate(() => Object.assign(window.__noxa.state, { activeTabID: 'tab-1', myClientID: 'self', myNickname: 'Rowan', isGuest: false }));
    await page.evaluate(() => {
        document.getElementById('login-overlay').classList.add('hidden');
        document.getElementById('login-overlay').setAttribute('aria-hidden', 'true');
        document.getElementById('app').classList.remove('hidden');
        document.getElementById('app').setAttribute('aria-hidden', 'false');
    });
});


async function refresh(page) { await page.evaluate(() => window.__noxaMobile.refresh()); }
test('compact banner opens platform install guide only on demand', async ({ page }, testInfo) => {
    await page.setViewportSize({width:1024,height:768});
    await refresh(page);
    const banner=page.locator('#mobile-announcement');
    await expect(banner).toBeVisible();
    expect((await banner.boundingBox()).height).toBeLessThan(85);
    expect(await page.evaluate(() => window.__calls.filter(c=>c[0]==='CreateDeviceLinkForTab'))).toEqual([]);
    await banner.getByRole('button',{name:'Set up',exact:true}).click();
    const dialog=page.locator('.mobile-quick-guide');
    await expect(dialog.getByRole('img',{name:'QR code that links your phone to this account'})).toBeVisible();
    await dialog.locator('summary').filter({hasText:'Install as an app'}).click();
    await expect(dialog).toContainText('Add to Home Screen');
    await dialog.getByRole('button',{name:'Android',exact:true}).click();
    await expect(dialog).toContainText('Install and create shortcut');
    await dialog.getByRole('button',{name:'macOS',exact:true}).click();
    await expect(dialog).toContainText('Add to Dock');
    await page.setViewportSize({width:650,height:768});
    await expect(dialog).toHaveCSS('max-height', '686px');
    await dialog.getByRole('button',{name:'Close',exact:true}).scrollIntoViewIfNeeded();
    await expect(dialog.getByRole('button',{name:'Close',exact:true})).toBeInViewport();
    await page.screenshot({path:testInfo.outputPath('mobile-quick-guide.png')});
    await page.keyboard.press('Escape');
    await expect(dialog).toHaveCount(0);
    await banner.getByRole('button',{name:'Not now'}).click();
    await page.evaluate(()=>{window.__noxa.state.myClientID='new-connection';});
    await refresh(page);
    await expect(banner).toBeHidden();
});
for(const mode of ['linked','guest','unsupported']) test(`announcement hidden for ${mode}`, async ({page})=>{
    await page.evaluate(mode=>{
        if(mode==='linked') window.__devices.push({kind:'web'});
        if(mode==='guest') window.__noxa.state.isGuest=true;
        if(mode==='unsupported') window.__mobileInfo={connected:true,mobile_available:false};
    },mode);
    await refresh(page);
    await expect(page.locator('#mobile-announcement')).toBeHidden();
});
test('linked event hides banner and stale responses cannot cross tabs', async ({page})=>{
    await refresh(page);
    await expect(page.locator('#mobile-announcement')).toBeVisible();
    await page.evaluate(()=>{window.__devices.push({kind:'web'});for(const f of window.__events.device_event) f({event:'linked'});});
    await expect(page.locator('#mobile-announcement')).toBeHidden();
    await page.evaluate(()=>{window.__devices=[];window.__devicePromise=new Promise(r=>window.__releaseDevices=r);window.__pendingRefresh=window.__noxaMobile.refresh();});
    await page.waitForFunction(()=>window.__calls.filter(c=>c[0]==='DevicesForTab').length>=3);
    await page.evaluate(async()=>{window.__noxa.state.activeTabID='other';window.__noxaMobile.clear();window.__releaseDevices([]);await window.__pendingRefresh;});
    await expect(page.locator('#mobile-announcement')).toBeHidden();
});

test('small sidebar banner stays available for linked accounts and opens QR setup', async ({ page }, testInfo) => {
    await page.evaluate(() => window.__devices.push({ kind: 'web' }));
    await refresh(page);
    const shortcut = page.locator('#mobile-setup-banner');
    await expect(shortcut).toBeVisible();
    await expect(page.locator('#mobile-announcement')).toBeHidden();
    expect((await shortcut.boundingBox()).height).toBeLessThan(40);
    await page.screenshot({ path: testInfo.outputPath('mobile-sidebar-banner.png') });
    await shortcut.click();
    await expect(page.locator('.mobile-quick-guide').getByRole('img', { name: 'QR code that links your phone to this account' })).toBeVisible();
    await page.keyboard.press('Escape');
    await page.evaluate(() => { for (const f of window.__events.disconnected) f(); });
    await expect(shortcut).toBeHidden();
});

test('dismissing during a pending device refresh keeps the promotion hidden', async ({ page }) => {
    await refresh(page);
    await page.evaluate(() => {
        window.__devicePromise = new Promise(resolve => { window.__releaseDevices = resolve; });
        window.__pendingRefresh = window.__noxaMobile.refresh();
    });
    await page.waitForFunction(() => window.__calls.filter(c => c[0] === 'DevicesForTab').length >= 2);
    await page.locator('#mobile-announcement').getByRole('button', { name: 'Not now' }).click();
    await page.evaluate(async () => { window.__releaseDevices([]); await window.__pendingRefresh; });
    await expect(page.locator('#mobile-announcement')).toBeHidden();
    await expect(page.locator('#mobile-setup-banner')).toBeVisible();
});
