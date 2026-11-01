import { test, expect } from '@playwright/test';

test('signal field responds, pauses by keyboard, and stops when off screen', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.addInitScript(() => {
    window.signalPaints = 0;
    const getContext = HTMLCanvasElement.prototype.getContext;
    HTMLCanvasElement.prototype.getContext = function (type, ...args) {
      const context = getContext.call(this, type, ...args);
      if (type === 'webgl' && context) {
        const draw = context.drawArrays;
        context.drawArrays = function (...values) { window.signalPaints++; return draw.apply(this, values); };
      }
      return context;
    };
  });
  await page.goto('./');
  const field = page.locator('.signal-field');
  await expect(field).toHaveAttribute('data-renderer', /webgl|static/);
  test.skip(await field.getAttribute('data-renderer') !== 'webgl', 'This browser has no WebGL context; static behavior is covered separately.');
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  const before = await page.evaluate(() => window.signalPaints);
  const bounds = await page.locator('.hero-art').boundingBox();
  await page.mouse.move(bounds.x + bounds.width * .8, bounds.y + bounds.height * .25);
  await expect.poll(() => page.evaluate(() => window.signalPaints)).toBeGreaterThan(before);
  const toggle = page.locator('.signal-toggle');
  await expect(toggle).toHaveAccessibleName('Pause visual effect');
  await toggle.focus();
  await page.keyboard.press('Space');
  await expect(toggle).toHaveAttribute('aria-pressed', 'true');
  const paused = await page.evaluate(() => window.signalPaints);
  await page.mouse.move(bounds.x + 20, bounds.y + 20);
  await page.waitForTimeout(250);
  expect(await page.evaluate(() => window.signalPaints)).toBe(paused);
  await page.keyboard.press('Space');
  await expect(toggle).toHaveAttribute('aria-pressed', 'false');
  await expect.poll(() => page.evaluate(() => window.signalPaints)).toBeGreaterThan(paused);
  await page.evaluate(() => dispatchEvent(new PageTransitionEvent('pagehide', { persisted: true })));
  const cached = await page.evaluate(() => window.signalPaints);
  await page.waitForTimeout(150);
  expect(await page.evaluate(() => window.signalPaints)).toBe(cached);
  await page.evaluate(() => dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true })));
  await expect.poll(() => page.evaluate(() => window.signalPaints)).toBeGreaterThan(cached);
  await page.locator('footer').scrollIntoViewIfNeeded();
  await page.waitForTimeout(250);
  const offscreen = await page.evaluate(() => window.signalPaints);
  await page.waitForTimeout(250);
  expect(await page.evaluate(() => window.signalPaints)).toBe(offscreen);
  expect(errors).toEqual([]);
});

for (const mode of ['reduced-motion', 'touch', 'no-webgl']) {
  test(`signal room keeps the complete page with ${mode}`, async ({ browser, baseURL }) => {
    const context = await browser.newContext({
      viewport: { width: mode === 'touch' ? 390 : 1440, height: 900 },
      reducedMotion: mode === 'reduced-motion' ? 'reduce' : 'no-preference',
      hasTouch: mode === 'touch',
      isMobile: mode === 'touch',
    });
    if (mode === 'no-webgl') await context.addInitScript(() => {
      const getContext = HTMLCanvasElement.prototype.getContext;
      HTMLCanvasElement.prototype.getContext = function (type, ...args) { return type === 'webgl' ? null : getContext.call(this, type, ...args); };
    });
    const page = await context.newPage();
    await page.goto(baseURL);
    await expect(page.locator('.signal-field')).toHaveAttribute('data-renderer', 'static');
    await expect(page.locator('.signal-still')).toBeVisible();
    await expect(page.locator('.signal-canvas')).toBeHidden();
    await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
    await expect(page.locator('.hero a.btn').first()).toBeVisible();
    await expect(page.locator('.signal-toggle')).toBeHidden();
    await context.close();
  });
}
