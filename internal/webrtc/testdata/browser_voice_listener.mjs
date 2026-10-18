// Opens the voice-listener page served by the Go integration test in
// headless Chromium (fake microphone) and keeps it alive until killed.
// Run by TestBrowserVoiceListenerSurvivesSpeakerChurn; needs Playwright.
import { createRequire } from 'node:module';

const require = createRequire(import.meta.url);
let playwright;
for (const candidate of [process.env.NOXA_PLAYWRIGHT_MODULE, 'playwright', 'playwright-core', '@playwright/test'].filter(Boolean)) {
    try { playwright = require(candidate); break; } catch { /* try the next one */ }
}
if (!playwright) throw new Error('playwright is not installed');
const url = process.argv[2];
const browser = await playwright.chromium.launch({
    headless: true,
    args: ['--use-fake-ui-for-media-stream', '--use-fake-device-for-media-stream', '--autoplay-policy=no-user-gesture-required'],
});
const page = await browser.newPage();
page.on('console', message => console.log(`[page] ${message.text()}`));
page.on('pageerror', error => console.log(`[page error] ${error}`));
await page.goto(url);
process.on('SIGTERM', async () => { await browser.close(); process.exit(0); });
await new Promise(() => {});
