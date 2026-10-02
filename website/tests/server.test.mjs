import assert from 'node:assert/strict';
import { after, before, test } from 'node:test';
import { createPreviewServer } from '../scripts/serve.mjs';
import { readdir, lstat } from 'node:fs/promises';
import { extname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

let server;
let origin;
before(async () => {
  server = createPreviewServer();
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  origin = `http://127.0.0.1:${server.address().port}`;
});
after(() => new Promise(resolve => server.close(resolve)));

test('serves identical static HTML at root and project paths', async () => {
  const root = await fetch(`${origin}/`);
  const project = await fetch(`${origin}/noxa/`);
  assert.equal(root.status, 200);
  assert.match(root.headers.get('content-type'), /text\/html/);
  assert.equal(await root.text(), await project.text());
});

test('redirects the bare project path and serves HEAD without a body', async () => {
  const redirect = await fetch(`${origin}/noxa`, { redirect: 'manual' });
  assert.equal(redirect.status, 308);
  assert.equal(redirect.headers.get('location'), '/noxa/');
  const head = await fetch(`${origin}/noxa/`, { method: 'HEAD' });
  assert.equal(head.status, 200);
  assert.equal(await head.text(), '');
});

test('does not expose tooling, unavailable resources, or parent files', async () => {
  for (const path of ['/package.json', '/scripts/serve.mjs', '/noxa/missing.png', '/%2e%2e%5cpackage.json']) {
    const response = await fetch(`${origin}${path}`);
    assert.ok([403, 404].includes(response.status), `${path}: ${response.status}`);
  }
  assert.equal((await fetch(`${origin}/`, { method: 'POST' })).status, 405);
});

test('Pages artifact contains only public website assets', async () => {
  const root = fileURLToPath(new URL('../public/', import.meta.url));
  const extensions = new Set(['.html', '.css', '.js', '.png', '.jpg', '.jpeg', '.webp', '.svg', '.ico', '.woff2']);
  const allowedDirectories = new Set(['assets', 'css', 'js', 'branding', 'screenshots', 'fonts']);
  const found = [];
  async function check(directory) {
    for (const name of await readdir(directory)) {
      const path = join(directory, name);
      const info = await lstat(path);
      assert.equal(info.isSymbolicLink(), false, `Artifact contains symlink: ${name}`);
      assert.ok(!name.startsWith('.') || (directory === root && name === '.nojekyll'), `Unexpected dotfile: ${name}`);
      if (info.isDirectory()) {
        assert.ok(allowedDirectories.has(name), `Unexpected public directory: ${name}`);
        await check(path);
      } else {
        assert.ok(name === '.nojekyll' || extensions.has(extname(name)), `Unexpected public file: ${name}`);
        found.push(name);
      }
    }
  }
  await check(root);
  assert.ok(found.includes('index.html'));
  assert.ok(found.includes('.nojekyll'));
});
