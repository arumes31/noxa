import { expect, test } from './fixtures.js';

test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
        window.__events = {};
        window.runtime = { EventsOn: (name, fn) => { (window.__events[name] ||= []).push(fn); return () => {}; }, EventsEmit() {}, WindowIsFullscreen: async () => false };
        const settings = { language: 'de', onboarding_done: true, alpha_dismissed: 'test', hotkey_ptt: 'Ctrl+M', bookmarks: [], chat_max_lines: 200 };
        window.go = { main: { App: new Proxy({}, { get(_target, method) { return async (...args) => {
            if (method === 'GetSettings') return structuredClone(settings);
            if (method === 'SaveSettings') { Object.assign(settings, args[0]); window.__savedHotkeys = structuredClone(args[0]); return ''; }
            if (['ListTabs', 'GetPermissions'].includes(method)) return [];
            if (['IdentityInfo', 'GetAvatar'].includes(method)) return {};
            if (['Connected', 'IsGuest', 'GamingOverlayAvailable'].includes(method)) return false;
            if (['ClientVersion', 'ClientVersionShort'].includes(method)) return 'test';
            return '';
        }; } }) } };
    });
    await page.goto('/');
    await page.waitForFunction(() => !!window.__noxa?.openSettings);
    await page.evaluate(() => window.__noxa.openSettings('hotkeys'));
});

const capture = page => page.locator('[data-hotkey-action="ptt"] .hotkey-capture');
const apply = async page => { await page.locator('#set-apply').click(); await expect(page.locator('#set-apply')).toBeEnabled(); };

test('Bild up/down and arrows capture, apply and stay in the settings dialog', async ({ page }) => {
    for (const [key, spec, label] of [['Control+PageUp', 'Ctrl+PageUp', 'Ctrl+Bild ↑'], ['PageDown', 'PageDown', 'Bild ↓'], ['ArrowDown', 'Down', 'Down'], ['Escape', 'Escape', 'Escape'], ['Tab', 'Tab', 'Tab']]) {
        await capture(page).click(); await page.keyboard.press(key);
        await expect(capture(page)).toHaveText(label);
        await apply(page);
        expect(await page.evaluate(() => window.__savedHotkeys.hotkey_ptt)).toBe(spec);
    }
});

test('DE and EN symbols, dead keys and numpad remain distinct in saved specs', async ({ page }) => {
    for (const [key, code, spec] of [['ö', 'Semicolon', 'Semicolon'], ['ä', 'Quote', 'Quote'], ['ü', 'BracketLeft', 'BracketLeft'], ['+', 'BracketRight', 'BracketRight'], ['#', 'Backslash', 'Backslash'], ['.', 'Period', 'Period'], ['-', 'Slash', 'Slash'], [',', 'Comma', 'Comma'], ['Dead', 'Backquote', 'Backquote'], ['=', 'Equal', 'Equal'], ['7', 'Numpad7', 'Numpad7'], ['+', 'NumpadAdd', 'NumpadAdd'], [',', 'NumpadDecimal', 'NumpadDecimal']]) {
        await capture(page).click();
        await capture(page).dispatchEvent('keydown', { key, code, location: code.startsWith('Numpad') ? 3 : 0, ctrlKey: true, bubbles: true });
        await expect(capture(page)).not.toHaveClass(/capturing/);
        await apply(page);
        expect(await page.evaluate(() => window.__savedHotkeys.hotkey_ptt)).toBe(`Ctrl+${spec}`);
    }
});

test('unavailable keys give immediate feedback without replacing the draft', async ({ page }) => {
    await capture(page).click();
    await capture(page).dispatchEvent('keydown', { key: 'Unidentified', bubbles: true });
    await expect(page.locator('#hotkey-error-ptt')).toContainText('nicht als verwendbares Kürzel');
    await expect(capture(page)).toHaveClass(/capturing/);
    await capture(page).click();
    await expect(capture(page)).toHaveText('Ctrl+M');
    await apply(page);
    expect(await page.evaluate(() => window.__savedHotkeys.hotkey_ptt)).toBe('Ctrl+M');
});

test('switching pages cancels capture and current registration status clears errors', async ({ page }) => {
    await page.evaluate(() => window.__events.hotkey_status.forEach(cb => cb({ action: 'ptt', error: 'conflict' })));
    await expect(page.locator('#hotkey-error-ptt')).toHaveText('conflict');
    await page.evaluate(() => window.__events.hotkey_status.forEach(cb => cb({ action: 'ptt', registered: false })));
    await expect(page.locator('#hotkey-error-ptt')).toBeEmpty();
    await capture(page).click();
    await page.getByRole('tab', { name: 'Anwendung', exact: true }).click();
    await page.keyboard.press('PageDown');
    await page.getByRole('tab', { name: 'Tastenkürzel', exact: true }).click();
    await expect(capture(page)).toHaveText('Ctrl+M');
});
