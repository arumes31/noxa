// Capture the real client frontend with fictional, local-only native API data.
// Run from any directory: node website/scripts/capture-client.mjs
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdir, writeFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const frontend = path.join(root, 'client/frontend');
const require = createRequire(path.join(frontend, 'package.json'));
const { chromium } = require('@playwright/test');
const { createServer } = await import(pathToFileURL(require.resolve('vite')).href);
const output = path.join(root, 'website/public/assets/screenshots');
const server = await createServer({ root: frontend, logLevel: 'error', server: {
  host: '127.0.0.1', port: 12367, strictPort: true,
} });
await mkdir(output, { recursive: true });
let browser;
try {
  await server.listen();
  browser = await chromium.launch();
  const context = await browser.newContext({ viewport: { width: 1280, height: 800 },
    deviceScaleFactor: 1, locale: 'en-GB', timezoneId: 'UTC', reducedMotion: 'reduce' });
  // Never connect the fixture to a public server or third-party asset host.
  await context.route('**/*', route => new URL(route.request().url()).hostname === '127.0.0.1'
    ? route.continue() : route.abort());
  const page = await context.newPage();
  await page.clock.setFixedTime(new Date('2026-10-02T18:35:00Z'));
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
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
            channels: [], members: [], roles: [
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
        { ChannelID: 1, Name: 'Welcome', ParentID: 0 },
        { ChannelID: 2, Name: 'The lounge', ParentID: 0, Topic: 'A good place to catch up.' },
        { ChannelID: 3, Name: 'Game night', ParentID: 0 },
        { ChannelID: 4, Name: 'Quiet corner', ParentID: 0 },
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
      id: index + 1, channel_id: 2, from, from_unique_id: 'fictional-' + from.toLowerCase(),
      text, sent_at: Date.parse('2026-10-02T18:30:00Z') + index * 60000,
    });
    await document.fonts.ready;
  });
  await page.locator('#chat-log [data-msg-id="4"]').waitFor();
  await page.getByRole('button', { name: 'Private groups', exact: true }).click();
  await page.locator('#toasts .toast').waitFor({ state: 'hidden' });
  await page.mouse.move(1270, 790);
  await page.screenshot({ path: path.join(output, 'app-chat.png'), animations: 'disabled' });
  await page.evaluate(async () => {
    const { openRolesManager } = await import('/src/roles-ui.js');
    openRolesManager();
  });
  const roles = page.getByRole('dialog', { name: 'Roles', exact: true });
  await roles.getByRole('button', { name: 'Member', exact: true }).click();
  await roles.getByLabel('Search permissions', { exact: true }).fill('voice');
  await roles.locator('.roles-dialog').screenshot({ path: path.join(output, 'app-roles.png'), animations: 'disabled' });
  assert.deepEqual(errors, [], 'Client screenshot runtime errors');
  const manifest = { sourceCommit: execFileSync('git', ['rev-parse', 'HEAD'], { cwd: root, encoding: 'utf8' }).trim(),
    browser: browser.version(), viewport: '1280x800', scale: 1, locale: 'en-GB', timezone: 'UTC',
    fixtureTime: '2026-10-02T18:35:00Z',
    fixture: 'website/scripts/capture-client.mjs', data: 'Fictional local-only demo data',
    appChat: 'Full actual frontend viewport', appRoles: 'Actual Roles dialog crop',
    generatedAt: new Date().toISOString() };
  await writeFile(path.join(root, 'website/scripts/capture-manifest.json'), JSON.stringify(manifest, null, 2) + '\n');
  console.log('Captured actual client screenshots with fictional data:', manifest);
} finally {
  await browser?.close();
  await server.close();
}
