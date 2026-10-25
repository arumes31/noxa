import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { writeFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const mobile = path.join(root, 'mobile');
const require = createRequire(path.join(mobile, 'package.json'));
const { chromium } = require('@playwright/test');
const { createServer } = await import(pathToFileURL(require.resolve('vite')).href);
const server = await createServer({ root: mobile, logLevel: 'error', server: { host: '127.0.0.1', port: 12368, strictPort: true } });
let browser;
try {
  await server.listen();
  browser = await chromium.launch();
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
  await page.clock.setFixedTime(new Date('2026-10-25T18:35:00Z'));
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  await page.goto('http://127.0.0.1:12368/app/ui-tests/layout.html?view=chat');
  await page.locator('html[data-ready="true"]').waitFor();
  await page.locator('.msg').last().waitFor();
  await page.evaluate(() => document.fonts.ready);
  assert.equal(await page.locator('.msg').count(), 4);
  assert.ok(await page.locator('.composer').isVisible());
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
  await page.screenshot({ path: path.join(root, 'website/public/assets/screenshots/pwa-chat.png'), animations: 'disabled' });
  assert.deepEqual(errors, []);
  const manifest = { sourceCommit: execFileSync('git', ['rev-parse', 'HEAD'], { cwd: root, encoding: 'utf8' }).trim(), browser: browser.version(), viewport: '390x844', fixture: 'mobile/ui-tests/layout-fixture.js with fictional capture data', fixtureTime: '2026-10-25T18:35:00Z', generatedAt: new Date().toISOString() };
  await writeFile(path.join(root, 'website/scripts/capture-mobile-manifest.json'), JSON.stringify(manifest, null, 2) + '\n');
  console.log(manifest);
} finally { await browser?.close(); await server.close(); }
