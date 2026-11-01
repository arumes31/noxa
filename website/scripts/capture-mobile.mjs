import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { realpathSync } from 'node:fs';
import { mkdir, writeFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const sourceRoot = path.resolve(process.env.NOXA_CAPTURE_SOURCE_ROOT || root);
// Resolve provenance before any browser or output changes.
const sourceCommit = execFileSync('git', ['rev-parse', 'HEAD'], { cwd: sourceRoot, encoding: 'utf8' }).trim();
const mobile = path.join(sourceRoot, 'mobile');
const require = createRequire(path.join(root, 'mobile/package.json'));
const dependencyDirectory = realpathSync(path.join(root, 'mobile/node_modules'));
const { chromium } = require('@playwright/test');
const { createServer } = await import(pathToFileURL(require.resolve('vite')).href);
const output = path.join(root, 'website/public/assets/screenshots');
const server = await createServer({ root: mobile, logLevel: 'error', server: { host: '127.0.0.1', port: 12368, strictPort: true, fs: { allow: [mobile, dependencyDirectory] } } });
let browser;
try {
  await server.listen();
  browser = await chromium.launch({ executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH || undefined });
  const context = await browser.newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 1, locale: 'en-GB', timezoneId: 'UTC', reducedMotion: 'reduce', serviceWorkers: 'block' });
  await context.route('**/*', route => new URL(route.request().url()).hostname === '127.0.0.1' ? route.continue() : route.abort());
  // Render actual UI components through the local layout fixture with a short,
  // fictional conversation. No credentials, server connection or native media.
  await context.route('**/layout-fixture.js', async route => {
    const response = await route.fetch();
    let source = await response.text();
    source = source.replaceAll('Daniel', 'Rowan').replaceAll('Public1', 'Lounge')
      .replaceAll('A very long channel name that should fit on a phone', 'Planning room')
      .replace('length: 60', 'length: 4').replaceAll('1700000000 + i', '1792953000 + i * 60')
      .replaceAll('"A sample message for the layout check."', '["Anyone around before game night?", "Bring your best stories from the week!", "I have one about a very ambitious houseplant. 🌱", "See you in the lounge at seven."][i]');
    await route.fulfill({ response, body: source });
  });
  const page = await context.newPage();
  page.setDefaultNavigationTimeout(120000);
  await page.clock.setFixedTime(new Date('2026-10-25T18:35:00Z'));
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  page.on('response', response => { if (response.status() >= 400) errors.push(`${response.status()} ${response.url()}`); });
  await mkdir(output, { recursive: true });
  const inViewport = async locator => {
    const bounds = await locator.boundingBox();
    assert.ok(bounds && bounds.y >= 0 && bounds.y + bounds.height <= 844 && bounds.x >= 0 && bounds.x + bounds.width <= 391, 'Screenshot controls must fit entirely inside the viewport');
  };
  await page.goto('http://127.0.0.1:12368/app/ui-tests/layout.html?view=chat');
  await page.locator('html[data-ready="true"]').waitFor();
  await page.locator('.msg').last().waitFor();
  await page.evaluate(() => document.fonts.ready);
  assert.equal(await page.locator('.msg').count(), 4);
  // Show an actual short, unsent draft rather than a wrapped placeholder.
  await page.locator('.composer textarea').fill('See you soon!');
  await inViewport(page.locator('.composer'));
  await inViewport(page.getByRole('button', { name: 'Send', exact: true }));
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
  await page.screenshot({ path: path.join(output, 'pwa-chat.png'), animations: 'disabled' });
  await page.goto('http://127.0.0.1:12368/app/ui-tests/layout.html?view=you');
  await page.locator('html[data-ready="true"]').waitFor();
  await page.getByRole('navigation', { name: 'Main', exact: true }).waitFor();
  await page.evaluate(() => document.fonts.ready);
  await inViewport(page.getByRole('navigation', { name: 'Main', exact: true }));
  await page.screenshot({ path: path.join(output, 'pwa-you.png'), animations: 'disabled' });
  await page.route('**/api/v1/web/info', route => route.fulfill({ json: { server_name: 'The Common Room', protocol_version: 1 } }));
  await page.goto('http://127.0.0.1:12368/app/');
  await page.getByLabel('Code from your desktop', { exact: true }).waitFor();
  await page.evaluate(() => document.fonts.ready);
  await inViewport(page.getByRole('button', { name: 'Continue', exact: true }));
  await page.screenshot({ path: path.join(output, 'pwa-connect.png'), animations: 'disabled' });
  assert.deepEqual(errors, []);
  const manifest = { sourceCommit, browser: browser.version(), viewport: '390x844', fixture: 'Actual Connect screen with local server-info response; mobile/ui-tests/layout-fixture.js Chat and You with fictional capture data', fixtureTime: '2026-10-25T18:35:00Z', screenshots: ['pwa-chat.png', 'pwa-you.png', 'pwa-connect.png'], generatedAt: new Date().toISOString() };
  await writeFile(path.join(root, 'website/scripts/capture-mobile-manifest.json'), JSON.stringify(manifest, null, 2) + '\n');
  console.log(manifest);
} finally { await browser?.close(); await server.close(); }
