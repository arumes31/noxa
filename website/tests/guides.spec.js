import { test, expect } from '@playwright/test';
import { readFile } from 'node:fs/promises';

const sitemap = await readFile(new URL('../public/sitemap.xml', import.meta.url), 'utf8');
const pages = [...sitemap.matchAll(/<loc>(.*?)<\/loc>/g)].map(match => new URL(match[1]).pathname.slice(1));

test('every canonical page is crawlable with complete metadata, images, and local links', async ({ page, request, baseURL }) => {
  test.setTimeout(120000);
  const errors = [];
  const targets = new Map();
  page.on('pageerror', error => errors.push(error.message));
  for (const path of pages) {
    await page.goto(path || './');
    await expect(page.locator('h1')).toHaveCount(1);
    await expect(page.locator('main')).toHaveCount(1);
    await expect(page.locator('meta[name="description"]')).toHaveAttribute('content', /\S.{40}/);
    await expect(page.locator('link[rel="canonical"]')).toHaveAttribute('href', `https://noxa.wowcraft.pw/${path}`);
    const schema = await page.locator('script[type="application/ld+json"]').allTextContents();
    expect(schema.length, path).toBeGreaterThan(0);
    for (const data of schema) expect(JSON.parse(data)['@context']).toBe('https://schema.org');
    const ids = await page.locator('[id]').evaluateAll(nodes => nodes.map(node => node.id));
    expect(new Set(ids).size, `Duplicate IDs: ${path}`).toBe(ids.length);
    const resources = await page.locator('a[href],img[src],link[rel="stylesheet"]').evaluateAll(nodes => nodes.map(node => ({ value: node.getAttribute('href') || node.getAttribute('src'), image: node.tagName === 'IMG', alt: node.getAttribute('alt'), width: node.getAttribute('width'), height: node.getAttribute('height') })));
    for (const resource of resources) {
      if (resource.image) {
        expect(resource.alt, path).toBeTruthy();
        expect(Number(resource.width), path).toBeGreaterThan(0);
        expect(Number(resource.height), path).toBeGreaterThan(0);
      }
      if (/^(?:https?:|mailto:)/.test(resource.value)) continue;
      const url = new URL(resource.value, page.url());
      if (url.hash && url.pathname === new URL(page.url()).pathname) {
        expect(ids, `${path}: ${url.hash}`).toContain(decodeURIComponent(url.hash.slice(1)));
      } else {
        const target = new URL(url);
        target.hash = '';
        if (!targets.has(target.href)) {
          const response = await request.get(target.href);
          expect(response.status(), `${path}: ${resource.value}`).toBe(200);
          const ids = response.headers()['content-type']?.includes('text/html')
            ? await page.evaluate(html => [...new DOMParser().parseFromString(html, 'text/html').querySelectorAll('[id]')].map(node => node.id), await response.text())
            : [];
          targets.set(target.href, ids);
        }
        if (url.hash) expect(targets.get(target.href), `${path}: ${resource.value}`).toContain(decodeURIComponent(url.hash.slice(1)));
      }
    }
    for (const width of [320, 390, 1440]) {
      await page.setViewportSize({ width, height: 844 });
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), `${path}: overflow at ${width}px`).toBe(true);
    }
    for (const image of await page.locator('img[src]').all()) {
      await image.scrollIntoViewIfNeeded();
      await expect.poll(() => image.evaluate(node => node.complete && node.naturalWidth > 0)).toBe(true);
      expect(await image.evaluate(node => Number(node.getAttribute('width')) === node.naturalWidth && Number(node.getAttribute('height')) === node.naturalHeight), `${path}: image dimensions`).toBe(true);
    }
    if (['guides/', 'guides/server-setup/', 'guides/studio/'].includes(path)) {
      await page.evaluate(() => scrollTo({ top: 0, behavior: 'instant' }));
      await page.screenshot({ path: test.info().outputPath(`${path.replaceAll('/', '-') || 'home'}.png`), fullPage: true });
    }
  }
  expect(errors).toEqual([]);
});

test('guide browsing follows reading order and enlarged screenshots keep their captions', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto('guides/mobile/');
  const details = page.locator('.docs-sidebar details');
  await expect(details).not.toHaveAttribute('open', '');
  const sidebar = await details.boundingBox();
  const article = await page.locator('main').boundingBox();
  expect(sidebar.y + sidebar.height).toBeLessThan(article.y);
  await page.keyboard.press('Tab');
  await expect(page.getByRole('link', { name: 'Skip to content' })).toBeFocused();
  await page.keyboard.press('Enter');
  await expect(page.locator('main')).toBeFocused();
  const figure = page.locator('figure').filter({ has: page.locator('img[src$="pwa-connect.png"]') });
  const caption = await figure.locator('figcaption').textContent();
  const link = figure.getByRole('link');
  await link.focus();
  await page.keyboard.press('Enter');
  await expect(page.getByRole('dialog')).toBeVisible();
  await expect(page.locator('#image-caption')).toHaveText(caption);
  await page.keyboard.press('Escape');
  await expect(link).toBeFocused();
  await page.setViewportSize({ width: 1440, height: 1000 });
  await expect(details).toHaveAttribute('open', '');
});

test('server setup works without JavaScript and guide images open with keyboard', async ({ browser, baseURL }) => {
  const context = await browser.newContext({ javaScriptEnabled: false, viewport: { width: 390, height: 844 } });
  const page = await context.newPage();
  await page.goto(new URL('guides/server-setup/', baseURL).href);
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Set up your own noXa server');
  await page.getByRole('navigation', { name: 'On this page' }).getByRole('link').first().click();
  await expect(page).toHaveURL(/#before-you-start$/);
  const image = page.locator('figure a').first();
  const target = new URL(await image.getAttribute('href'), page.url()).href;
  await image.focus();
  await page.keyboard.press('Enter');
  await expect(page).toHaveURL(target);
  await context.close();
});
