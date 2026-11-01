import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { copyFile, mkdir, mkdtemp, readFile, rm, stat, writeFile } from 'node:fs/promises';
import { existsSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { test } from 'node:test';

test('regeneration removes retired guides and preserves unrelated public files', async () => {
  const temporaryRoot = resolve(tmpdir());
  const fixture = await mkdtemp(join(temporaryRoot, 'noxa-guides-'));
  assert.equal(dirname(fixture), temporaryRoot);
  try {
    await mkdir(join(fixture, 'scripts/guides'), { recursive: true });
    await mkdir(join(fixture, 'public/guides/retired-guide'), { recursive: true });
    for (const file of ['build-guides.mjs', 'guides/clients.mjs', 'guides/server.mjs']) {
      await copyFile(new URL(`../scripts/${file}`, import.meta.url), join(fixture, 'scripts', file));
    }
    await copyFile(new URL('../public/index.html', import.meta.url), join(fixture, 'public/index.html'));
    await copyFile(new URL('../public/index.html', import.meta.url), join(fixture, 'public/guides/retired-guide/index.html'));
    const homepage = await readFile(join(fixture, 'public/index.html'), 'utf8');
    execFileSync(process.execPath, [join(fixture, 'scripts/build-guides.mjs')]);
    await assert.rejects(stat(join(fixture, 'public/guides/retired-guide')), { code: 'ENOENT' });
    assert.ok((await stat(join(fixture, 'public/guides/server-setup/index.html'))).isFile());
    assert.equal(await readFile(join(fixture, 'public/index.html'), 'utf8'), homepage);
    const sitemap = await readFile(join(fixture, 'public/sitemap.xml'), 'utf8');
    assert.match(sitemap, /\/guides\/server-setup\//);
    assert.doesNotMatch(sitemap, /retired-guide/);
  } finally {
    // mkdtemp's output is verified above to be a direct child of the OS temp dir.
    await rm(fixture, { recursive: true, force: true });
  }
});

for (const capture of ['capture-client.mjs', 'capture-mobile.mjs']) test(`${capture} rejects an invalid source before screenshot outputs change`, async () => {
  const temporaryRoot = resolve(tmpdir());
  const fixture = await mkdtemp(join(temporaryRoot, 'noxa-capture-'));
  assert.equal(dirname(fixture), temporaryRoot);
  try {
    const scripts = join(fixture, 'website/scripts');
    const output = join(fixture, 'website/public/assets/screenshots');
    await mkdir(scripts, { recursive: true });
    await mkdir(output, { recursive: true });
    await copyFile(new URL(`../scripts/${capture}`, import.meta.url), join(scripts, capture));
    const sentinel = join(output, 'existing.png');
    await writeFile(sentinel, 'existing screenshot');
    assert.throws(() => execFileSync(process.execPath, [join(scripts, capture), '--guides-only'], {
      env: { ...process.env, NOXA_CAPTURE_SOURCE_ROOT: fixture, GIT_CEILING_DIRECTORIES: temporaryRoot, LC_ALL: 'C' },
      stdio: 'pipe',
    }), error => {
      assert.match(String(error.stderr), /not a git repository/i);
      return true;
    });
    assert.equal(await readFile(sentinel, 'utf8'), 'existing screenshot');
    assert.equal(existsSync(join(output, 'app-connect.jpg')), false);
  } finally {
    await rm(fixture, { recursive: true, force: true });
  }
});
