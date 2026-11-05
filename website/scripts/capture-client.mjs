// Capture the real client frontend with fictional, local-only native API data.
// Run from any directory: node website/scripts/capture-client.mjs
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { realpathSync } from 'node:fs';
import { copyFile, mkdir, mkdtemp, readdir, rm, writeFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const sourceRoot = path.resolve(process.env.NOXA_CAPTURE_SOURCE_ROOT || root);
// Validate provenance before opening a browser or replacing any output file.
const sourceCommit = execFileSync('git', ['rev-parse', 'HEAD'], { cwd: sourceRoot, encoding: 'utf8' }).trim();
// HEAD alone does not identify uncommitted frontend edits that were captured.
const sourceDirty = execFileSync('git', ['status', '--porcelain', '--', 'client/frontend'], { cwd: sourceRoot, encoding: 'utf8' }).trim() !== '';
const frontend = path.join(sourceRoot, 'client/frontend');
const require = createRequire(path.join(root, 'client/frontend/package.json'));
const dependencyDirectory = realpathSync(path.join(root, 'client/frontend/node_modules'));
const { chromium } = require('@playwright/test');
const { createServer } = await import(pathToFileURL(require.resolve('vite')).href);
const output = path.join(root, 'website/public/assets/screenshots');
// Screenshots land in a scratch directory and replace the published ones only
// after the whole run finished without runtime errors.
const staging = await mkdtemp(path.join(os.tmpdir(), 'noxa-capture-'));
const guidesOnly = process.argv.includes('--guides-only');
const server = await createServer({ root: frontend, logLevel: 'error', server: {
  host: '127.0.0.1', port: 12367, strictPort: true,
  fs: { allow: [frontend, dependencyDirectory] },
} });
await mkdir(output, { recursive: true });
let browser;
try {
  await server.listen();
  browser = await chromium.launch({ executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH || undefined });
  const context = await browser.newContext({ viewport: { width: 1280, height: 800 },
    deviceScaleFactor: 1, locale: 'en-GB', timezoneId: 'UTC', reducedMotion: 'reduce' });
  // Never connect the fixture to a public server or third-party asset host.
  await context.route('**/*', route => new URL(route.request().url()).hostname === '127.0.0.1'
    ? route.continue() : route.abort());
  const page = await context.newPage();
  page.setDefaultNavigationTimeout(120000);
  await page.clock.setFixedTime(new Date('2026-10-24T18:35:00Z'));
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  page.on('response', response => {
    if (response.status() >= 400) errors.push(`${response.status()} ${response.url()}`);
  });
  await page.addInitScript(() => {
    const settings = { settings_version: 4, capture_device_id: '', playback_device_id: '',
      activation_mode: 'vad', vad_threshold: 25, volume: 100, chat_max_lines: 200,
      window_opacity: 100, camera_fps: 30, sound_volume: 0, auto_away_minutes: 0,
      notification_matrix: {}, bookmarks: [], onboarding_done: true, alpha_dismissed: 'demo',
      language: 'en', display_name: 'Rowan', ui_font: 'outfit', ui_font_size: 14,
      notify_matrix: { channel_message: { toast: false, sound: false, flash: false, native: false } } };
    const tabs = [{ id: 'demo-server', addr: 'community.example', name: 'The Common Room',
      nickname: 'Rowan', connected: true, active: true }];
    const capabilities = [
      { key: 'view_channel', group: 'access', en: 'View channel', channel: true },
      { key: 'connect', group: 'voice', en: 'Connect to voice', requires: ['view_channel'], channel: true },
      { key: 'speak', group: 'voice', en: 'Speak', requires: ['connect'], channel: true },
      { key: 'share_screen', group: 'voice', en: 'Share screen', requires: ['connect'], channel: true },
      { key: 'send_messages', group: 'text', en: 'Send messages', requires: ['view_channel'], channel: true },
      { key: 'manage_channels', group: 'access', en: 'Manage channels', requires: ['view_channel'], channel: true },
      { key: 'manage_roles', group: 'server', en: 'Manage roles' },
    ];
    window.__captureEvents = {};
    window.runtime = { EventsOn(name, callback) {
      (window.__captureEvents[name] ||= []).push(callback);
      return () => { window.__captureEvents[name] = window.__captureEvents[name].filter(item => item !== callback); };
    }, EventsEmit() {}, WindowIsFullscreen: async () => false, BrowserOpenURL() {} };
    window.go = { main: { App: new Proxy({}, { get(_target, method) {
      return async () => {
        if (method === 'GetSettings') return structuredClone(settings);
        if (method === 'ListTabs') return structuredClone(tabs);
        if (method === 'IdentityUID') return 'fictional-rowan';
        if (method === 'ClientVersionShort' || method === 'ClientVersion') return 'demo';
        if (method === 'Connected') return false;
        if (method === 'IsAdmin') return true;
        if (method === 'IsGuest') return false;
        if (method === 'SessionInfoForTab') return { connected: true, client_id: 'rowan',
          unique_id: 'fictional-rowan', is_admin: true, is_guest: false, authorization_model: 'roles-v1' };
        if (method === 'CheckForUpdate') return { available: false, version: 'demo', size: 0 };
        if (method === 'ServerInfoForTab') return { name: 'The Common Room', version: 'Demo data',
          clients_online: 5, max_clients: 100, channels_online: 5, uptime_seconds: 3600 };
        if (method === 'GetClientInfoForTab') return { ping_ms: 24 };
        if (method === 'RoleStateForTab') return { actor_id: 1, capabilities,
          grantable_capabilities: capabilities.map(item => item.key), manageable_role_ids: [10, 20, 30, 40],
          policy: { revision: 1, owner_id: 1, everyone_id: 10, default_member_role_id: 20,
            channels: [{ channel_id: 2, parent_id: 1, name: 'The lounge', synced: false, overrides: [
              { role_id: 10, capability: 'view_channel', effect: 'deny' },
              { role_id: 20, capability: 'view_channel', effect: 'allow' },
              { role_id: 20, capability: 'connect', effect: 'allow' },
              { role_id: 20, capability: 'speak', effect: 'allow' },
              { role_id: 20, capability: 'send_messages', effect: 'allow' },
            ] }], members: [], roles: [
              { id: 10, name: '@everyone', position: 0, permissions: ['view_channel', 'connect'] },
              { id: 20, name: 'Member', position: 1, color: '#a0e6e8', permissions: ['view_channel', 'connect', 'speak', 'share_screen', 'send_messages'] },
              { id: 30, name: 'Moderator', position: 2, color: '#bfa6ed', permissions: ['view_channel', 'connect', 'speak', 'manage_channels'] },
              { id: 40, name: 'Admin', position: 3, color: '#f1ba6e', deletion_protected: true, permissions: ['manage_roles', 'manage_channels'] },
            ] } };
        if (method === 'ConversationForTab') return { conversations: [], messages: [] };
        if (method === 'GetAvatarForTab' || method === 'ServerIconGetForTab' || method === 'ServerBannerGetForTab' || method === 'ChannelIconGetForTab' || method === 'IdentityInfo') return {};
        return '';
      };
    } }) } };
    Object.defineProperty(navigator, 'mediaDevices', { configurable: true, value: {
      enumerateDevices: async () => [],
      getUserMedia: async () => { throw new Error('Screenshot fixture does not capture devices'); },
    } });
  });
  await page.goto('http://127.0.0.1:12367/');
  await page.waitForFunction(() => !!window.__noxa?.openSettings);
  if (guidesOnly) {
    await page.setViewportSize({ width: 1280, height: 1100 });
    await page.evaluate(() => window.__noxa.showLogin());
    await page.locator('#login-addr').fill('community.example');
    await page.locator('#login-nick').fill('Rowan');
    await page.locator('#login-options').evaluate(node => { node.open = true; });
    await page.evaluate(() => document.fonts.ready);
    await page.screenshot({ path: path.join(staging, 'app-connect.jpg'), type: 'jpeg', quality: 85, animations: 'disabled' });
    await page.setViewportSize({ width: 1280, height: 800 });
  }
  await page.evaluate(async () => {
    for (const callback of window.__captureEvents.tab_update) callback(await window.go.main.App.ListTabs());
    for (const callback of window.__captureEvents.tab_reset) callback('demo-server');
  });
  await page.waitForFunction(() => document.getElementById('conn-pill').classList.contains('up'));
  await page.evaluate(async () => {
    const app = window.__noxa;
    Object.assign(app.state, { myClientID: 'rowan', myUniqueID: 'fictional-rowan', myNickname: 'Rowan',
      myChannelID: 2, isGuest: false, authorizationModel: 'roles-v1', activeTabID: 'demo-server',
      channels: [
        { ChannelID: 1, Name: 'Lobby', ParentID: 0, SystemKind: "home", ChatDisabled: true, VoiceDisabled: true },
        { ChannelID: 2, Name: 'The lounge', ParentID: 1, Topic: 'A good place to catch up.' },
        { ChannelID: 3, Name: 'Game night', ParentID: 1 },
        { ChannelID: 4, Name: 'Quiet corner', ParentID: 1 },
        { ChannelID: 5, Name: 'Planning room', ParentID: 0, HasPassword: true },
      ],
      clients: ['Rowan', 'Avery', 'Sage', 'Morgan', 'Quinn'].map((nickname, index) => ({
        client_id: nickname.toLowerCase(), unique_id: 'fictional-' + nickname.toLowerCase(),
        nickname, channel_id: index < 3 ? 2 : 3, is_speaking: nickname === 'Avery',
      })),
    });
    app.showWorkspace(false);
    app.renderTree();
    window.__noxaChat.onMyChannelChanged();
    await window.__noxaSocial.refreshNews();
    const messages = [
      ['Avery', 'Anyone around for a catch-up before game night?'],
      ['Rowan', 'I’m here! Bring your best stories from the week.'],
      ['Avery', 'I have one involving a very ambitious houseplant. 🌱'],
      ['Sage', 'That definitely needs an explanation.'],
    ];
    for (const [index, [from, text]] of messages.entries()) window.__noxaChat.addChat({
      id: index + 1, channel_id: 2, enc_verified: true, from, from_unique_id: 'fictional-' + from.toLowerCase(),
      text, sent_at: Date.parse('2026-10-24T18:30:00Z') + index * 60000,
    });
    await document.fonts.ready;
  });
  await page.locator('#chat-log [data-msg-id="4"]').waitFor();
  await page.locator('#toasts .toast').waitFor({ state: 'hidden' });
  await page.mouse.move(1270, 790);
  if (!guidesOnly) await page.screenshot({ path: path.join(staging, 'app-chat.png'), animations: 'disabled' });
  await page.setViewportSize({ width: 1280, height: 1100 });
  if (guidesOnly) {
    await page.evaluate(() => window.__noxa.openSettings('capture'));
    await page.locator('#settings-overlay .settings-dialog').waitFor();
    await page.evaluate(() => document.fonts.ready);
    await page.locator('#settings-overlay .settings-dialog').screenshot({ path: path.join(staging, 'app-capture.png'), animations: 'disabled' });
    await page.keyboard.press('Escape');
    await page.evaluate(() => window.__noxa.openSettings('connection'));
    await page.locator('#settings-overlay .settings-dialog').waitFor();
    await page.evaluate(() => document.fonts.ready);
    await page.locator('#settings-overlay .settings-dialog').screenshot({ path: path.join(staging, 'app-connection.png'), animations: 'disabled' });
    await page.keyboard.press('Escape');
    await page.evaluate(async () => { const { openChannelAccess } = await import('/src/channel-access-ui.js'); openChannelAccess(2); });
    await page.locator('.channel-access-dialog .channel-access-content select').first().waitFor();
    await page.locator('.channel-access-dialog').screenshot({ path: path.join(staging, 'app-channel-access.png'), animations: 'disabled' });
    await page.keyboard.press('Escape');
    await page.evaluate(async () => { const { openStudio } = await import('/src/studio-ui.js'); openStudio(); });
    await page.locator('.studio-overlay .studio').waitFor();
    await page.locator('.studio-overlay .studio').screenshot({ path: path.join(staging, 'app-studio.png'), animations: 'disabled' });
    await page.keyboard.press('Escape');
  }
  await page.evaluate(async () => {
    const { openRolesManager } = await import('/src/roles-ui.js');
    openRolesManager();
  });
  const roles = page.getByRole('dialog', { name: 'Roles', exact: true });
  await roles.getByRole('button', { name: 'Member', exact: true }).click();
  await roles.locator('.roles-editor').evaluate(node => { node.scrollTop = 0; });
  await roles.locator('.roles-dialog').screenshot({ path: path.join(staging, guidesOnly ? 'app-roles-guide.png' : 'app-roles.png'), animations: 'disabled' });
  assert.deepEqual(errors, [], 'Client screenshot runtime errors');
  for (const name of await readdir(staging)) await copyFile(path.join(staging, name), path.join(output, name));
  const manifest = { sourceCommit, sourceDirty,
    // Vite, Playwright and node_modules come from this checkout, even when
    // NOXA_CAPTURE_SOURCE_ROOT points the captured source elsewhere.
    ...(sourceRoot !== root ? { dependencyCommit: execFileSync('git', ['rev-parse', 'HEAD'], { cwd: root, encoding: 'utf8' }).trim() } : {}),
    browser: browser.version(), viewport: '1280x800', rolesViewport: '1280x1100', scale: 1, locale: 'en-GB', timezone: 'UTC',
    fixtureTime: '2026-10-24T18:35:00Z',
    fixture: 'website/scripts/capture-client.mjs', data: 'Fictional local-only demo data',
    appChat: 'Full actual frontend viewport', appRoles: 'Actual Roles dialog crop',
    generatedAt: new Date().toISOString() };
  if (guidesOnly) {
    manifest.appConnect = 'Actual connection screen, fictional community.example address';
    manifest.appCapture = 'Actual Audio & voice settings dialog, no hardware access';
    manifest.appConnection = 'Actual Connection diagnostics, no network tests running';
    manifest.appChannelAccess = 'Actual Channel access dialog, fictional policy';
    manifest.appStudio = 'Actual Studio dialog, no captures or broadcast';
    manifest.appRoles = 'app-roles-guide.png: Actual Roles dialog crop';
    manifest.connectViewport = '1280x1100';
  }
  await writeFile(path.join(root, guidesOnly ? 'website/scripts/capture-guides-manifest.json' : 'website/scripts/capture-manifest.json'), JSON.stringify(manifest, null, 2) + '\n');
  console.log('Captured actual client screenshots with fictional data:', manifest);
} finally {
  await browser?.close();
  await server.close();
  await rm(staging, { recursive: true, force: true });
}
