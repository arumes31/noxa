import { test as base, expect } from '@playwright/test';

const downloadURL = 'https://github.com/arumes31/noxa/releases/latest/download/noxa-client-windows-amd64.exe';

// A marketing page needs no third-party runtime requests. Block them so network
// availability cannot make deterministic browser checks pass or fail.
const test = base.extend({
  page: async ({ page }, use) => {
    const issues = [];
    page.on('pageerror', error => issues.push(error.message));
    page.on('console', message => {
      if (message.type() === 'error') issues.push(message.text());
    });
    page.on('response', response => {
      if (response.status() >= 400) issues.push(`${response.status()} ${response.url()}`);
    });
    await page.context().route('**/*', route => {
      const url = new URL(route.request().url());
      if (url.hostname === '127.0.0.1' || url.protocol === 'data:') return route.continue();
      issues.push(`Unexpected external request: ${url.href}`);
      return route.abort();
    });
    await use(page);
    expect(issues, 'page errors, missing assets, or unexpected third-party requests').toEqual([]);
  },
});

// Exercise CSS scrolling consistently across local and Linux CI browser defaults.
test.use({ launchOptions: { args: ['--enable-smooth-scrolling'] } });

async function ready(page) {
  await page.goto('./');
  await page.locator('.hero img').waitFor();
  await page.evaluate(() => document.fonts.ready);
}

for (const width of [320, 390, 768, 1440, 1920]) {
  test(`responsive layout at ${width}px`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 844 });
    await ready(page);
    await expect(page.locator('h1')).toHaveCount(1);
    await expect(page.locator('h1')).toBeVisible();
    const dimensions = await page.evaluate(() => ({
      document: document.documentElement.scrollWidth,
      body: document.body.scrollWidth,
      viewport: innerWidth,
    }));
    expect(dimensions.document).toBeLessThanOrEqual(dimensions.viewport + 1);
    expect(dimensions.body).toBeLessThanOrEqual(dimensions.viewport + 1);
    if (width <= 390) {
      const cards = await page.locator('.features .feature').all();
      expect(cards).toHaveLength(3);
      let previous;
      for (const card of cards) {
        const bounds = await card.boundingBox();
        if (previous) {
          expect(Math.abs(bounds.x - previous.x)).toBeLessThanOrEqual(1);
          expect(bounds.y).toBeGreaterThanOrEqual(previous.y + previous.height);
        }
        previous = bounds;
      }
      const storyImage = await page.locator('.story .app-shot').boundingBox();
      const storyCopy = await page.locator('.story-copy').boundingBox();
      expect(storyCopy.y).toBeGreaterThanOrEqual(storyImage.y + storyImage.height);
    }
    for (const image of await page.locator('a[data-full] img').all()) {
      await image.scrollIntoViewIfNeeded();
      await expect(image).toBeVisible();
      const bounds = await image.boundingBox();
      expect(bounds.width).toBeGreaterThan(width < 768 ? width * 0.65 : 250);
      expect(bounds.x).toBeGreaterThanOrEqual(-1);
      expect(bounds.x + bounds.width).toBeLessThanOrEqual(width + 1);
    }
    if (width === 390) {
      await page.evaluate(() => scrollTo({ top: 0, left: 0, behavior: 'instant' }));
      const download = page.locator('.hero a').filter({ hasText: 'Download for Windows' });
      await expect(download).toBeVisible();
      const bounds = await download.boundingBox();
      expect(bounds.y).toBeGreaterThan(0);
      expect(bounds.y + bounds.height).toBeLessThanOrEqual(844);
      const heroImage = await page.locator('.hero img').boundingBox();
      expect(heroImage.y).toBeGreaterThan(bounds.y + bounds.height);
    }
    if (width === 390 || width === 1440) {
      await page.evaluate(() => scrollTo({ top: 0, left: 0, behavior: 'instant' }));
      await page.screenshot({ path: testInfo.outputPath(`signature-${width}.png`), fullPage: true });
    }
  });
}

test('download, navigation, and metadata destinations are consistent', async ({ page }) => {
  await ready(page);
  const downloads = page.getByRole('link', { name: /Download (?:for Windows|noXa)/ });
  expect(await downloads.count()).toBeGreaterThanOrEqual(3);
  for (const link of await downloads.all()) await expect(link).toHaveAttribute('href', downloadURL);
  await expect(page.getByRole('link', { name: 'All releases', exact: true })).toHaveAttribute('href', 'https://github.com/arumes31/noxa/releases');
  for (const link of await page.locator('a[href^="#"]').all()) {
    const id = (await link.getAttribute('href')).slice(1);
    expect(id, 'Fragment links must have a nonempty target').not.toBe('');
    await expect(page.locator(`[id="${id}"]`)).toHaveCount(1);
  }
  for (const link of await page.locator('a[target="_blank"]').all()) {
    expect((await link.getAttribute('rel') || '').split(/\s+/)).toContain('noopener');
  }
  await expect(page.locator('link[rel="canonical"]')).toHaveAttribute('href', 'https://arumes31.github.io/noxa/');
  await expect(page.locator('meta[property="og:image"]')).toHaveAttribute('content', /^https:\/\/arumes31\.github\.io\/noxa\//);
  await expect(page).toHaveTitle(/noXa/);
  expect(await page.locator('meta[name="description"]').getAttribute('content')).toBeTruthy();
  const robots = await page.locator('meta[name="robots"]').evaluateAll(nodes => nodes.map(node => node.content));
  expect(robots.join(' ')).not.toContain('noindex');
});

test('landmarks, headings, and the keyboard skip link are accessible', async ({ page }) => {
  await ready(page);
  await expect(page.getByRole('banner')).toHaveCount(1);
  await expect(page.getByRole('main')).toHaveCount(1);
  await expect(page.getByRole('contentinfo')).toHaveCount(1);
  await expect(page.getByRole('navigation', { name: 'Main navigation' })).toHaveCount(1);
  const levels = await page.locator('h1,h2,h3,h4,h5,h6').evaluateAll(nodes => nodes.map(node => Number(node.tagName.slice(1))));
  expect(levels[0]).toBe(1);
  for (let index = 1; index < levels.length; index++) expect(levels[index]).toBeLessThanOrEqual(levels[index - 1] + 1);
  await page.keyboard.press('Tab');
  const skip = page.getByRole('link', { name: 'Skip to content' });
  await expect(skip).toBeFocused();
  await expect(skip).toBeInViewport();
  await page.keyboard.press('Enter');
  await expect(page.locator('#overview')).toBeFocused();
});

test('all local assets and full-image links work beneath the selected base path', async ({ page, request, baseURL }) => {
  await ready(page);
  // Trigger lazy images before checking their decode results.
  for (const image of await page.locator('img').all()) {
    if (await image.isVisible()) await image.scrollIntoViewIfNeeded();
  }
  const assets = await page.evaluate(() => [
    ...Array.from(document.querySelectorAll('img[src],script[src]'), node => node.getAttribute('src')),
    ...Array.from(document.querySelectorAll('link[rel="stylesheet"],link[rel~="icon"],link[rel="apple-touch-icon"],a[data-full]'), node => node.getAttribute('href')),
  ]);
  expect(assets.length).toBeGreaterThanOrEqual(6);
  for (const asset of new Set(assets.filter(Boolean))) {
    const url = new URL(asset, baseURL);
    expect(url.origin).toBe(new URL(baseURL).origin);
    expect(url.pathname.startsWith(new URL(baseURL).pathname)).toBe(true);
    const response = await request.get(url.href);
    expect(response.status(), asset).toBe(200);
  }
  for (const image of await page.locator('a[data-full] img').all()) {
    await expect.poll(() => image.evaluate(node => node.complete && node.naturalWidth > 0)).toBe(true);
    expect(await image.getAttribute('alt')).toBeTruthy();
    const dimensions = await image.evaluate(node => ({
      width: Number(node.getAttribute('width')), height: Number(node.getAttribute('height')),
      actualWidth: node.naturalWidth, actualHeight: node.naturalHeight,
    }));
    expect(dimensions.width).toBe(dimensions.actualWidth);
    expect(dimensions.height).toBe(dimensions.actualHeight);
  }
});

test('mobile menu supports keyboard, Escape, and section navigation', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await ready(page);
  const button = page.locator('.menu-button');
  const navigation = page.locator('#navigation');
  await expect(button).toBeVisible();
  await expect(button).toHaveAttribute('aria-controls', 'navigation');
  await expect(button).toHaveAttribute('aria-expanded', 'false');
  await expect(navigation).toBeHidden();
  await button.focus();
  await page.keyboard.press('Enter');
  await expect(button).toHaveAttribute('aria-expanded', 'true');
  await expect(navigation).toBeVisible();
  await page.keyboard.press('Tab');
  expect(await navigation.evaluate(node => node.contains(document.activeElement))).toBe(true);
  await page.keyboard.press('Escape');
  await expect(button).toHaveAttribute('aria-expanded', 'false');
  await expect(button).toBeFocused();
  await button.click();
  await navigation.getByRole('link', { name: 'Features', exact: true }).click();
  await expect(page).toHaveURL(/#features$/);
  await expect(button).toHaveAttribute('aria-expanded', 'false');
});

test('both product images enlarge with Escape, close button, and contained keyboard focus', async ({ page }) => {
  await ready(page);
  const dialog = page.locator('dialog.lightbox');
  const links = page.locator('a[data-full]');
  expect(await links.count()).toBeGreaterThanOrEqual(2);
  for (const link of await links.all()) {
    await link.click();
    await expect(dialog).toBeVisible();
    await expect(dialog).toHaveAttribute('open', '');
    const close = dialog.getByRole('button', { name: /close/i });
    await expect(close).toBeVisible();
    expect(await dialog.evaluate(node => node.contains(document.activeElement))).toBe(true);
    for (const key of ['Tab', 'Tab', 'Shift+Tab', 'Shift+Tab']) {
      await page.keyboard.press(key);
      expect(await dialog.evaluate(node => node.contains(document.activeElement))).toBe(true);
    }
    await page.keyboard.press('Escape');
    await expect(dialog).not.toBeVisible();
    await expect(link).toBeFocused();
    await link.click();
    await close.click();
    await expect(dialog).not.toBeVisible();
    await expect(link).toBeFocused();
  }
});

test('reduced motion removes smooth scrolling and decorative motion', async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' });
  await ready(page);
  const motion = await page.evaluate(() => ({
    scroll: getComputedStyle(document.documentElement).scrollBehavior,
    animations: Array.from(document.querySelectorAll('body *')).flatMap(node => {
      const style = getComputedStyle(node);
      return [...style.animationDuration.split(','), ...style.transitionDuration.split(',')].map(Number.parseFloat);
    }),
  }));
  expect(motion.scroll).not.toBe('smooth');
  expect(Math.max(...motion.animations)).toBeLessThanOrEqual(0.01);
});

test.describe('without JavaScript', () => {
  test.use({ javaScriptEnabled: false, viewport: { width: 390, height: 844 } });
  test('navigation, downloads, and full images remain real links', async ({ page }) => {
    await page.goto('./');
    const navigation = page.locator('#navigation');
    await expect(navigation).toBeVisible();
    await navigation.getByRole('link', { name: 'Features', exact: true }).click();
    await expect(page).toHaveURL(/#features$/);
    for (const link of await page.getByRole('link', { name: /Download (?:for Windows|noXa)/ }).all()) {
      await expect(link).toHaveAttribute('href', downloadURL);
    }
    const fullImage = page.locator('a[data-full]').first();
    const target = new URL(await fullImage.getAttribute('href'), page.url()).href;
    // Exercise native keyboard activation even while the section scroll animates.
    await fullImage.focus();
    await expect(fullImage).toBeFocused();
    await page.keyboard.press('Enter');
    await expect(page).toHaveURL(target);
    await expect(page.locator('img')).toBeVisible();
  });
});
