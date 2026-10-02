# noXa product website

A static product and Windows-download page based on the supplied Signature
design (concept 25). The public site is intended for
<https://arumes31.github.io/noxa/>. GitHub Pages hosts the website; client
downloads remain on GitHub Releases, and noXa servers run separately.

## Files and local preview

Only `public/` is deployed. It contains semantic HTML, CSS, a small browser
script, `.nojekyll`, and local branding/screenshots. There is no application
server, build step, analytics, remote font request, or service worker.
Tests, tooling, this document, and design references are not published.

Use Node.js 24 (the CI version), or a supported Node.js version of at least 22:

```sh
cd website
npm ci
npx playwright install chromium
npm run preview
```

Open <http://127.0.0.1:4173/noxa/>. The same static content is served
at `/` and `/noxa/`; verify `/noxa/` before publishing. Links to assets use
relative paths so the page works at either base path. Downloads and navigation
remain ordinary links when JavaScript is disabled. Screenshot links open their
full-size images without JavaScript; JavaScript adds an enlargement dialog.

## Validation

```sh
npm test
npm run check:release
```

The deterministic suite validates the public artifact and uses Chromium for
both base paths. It covers 320, 390, 768, 1440, and 1920 pixel widths, including
the primary download in the 390 by 844 first viewport. It checks local
resources, anchors, repeated download links, overflow, menu and dialog keyboard
behavior, no-JavaScript fallbacks, reduced motion, and browser errors.
Linux CI installs Chromium dependencies with
`npx playwright install --with-deps chromium`.

`check:release` is a separate network integration check against GitHub's latest
release API. It checks the asset metadata without downloading the executable.
A missing expected asset fails the check; an unavailable network is reported
separately. The deterministic browser suite does not depend on that API.

Playwright keeps failures and traces under `test-results/`; its CI HTML report
is under `playwright-report/`. Those local outputs are ignored by Git and are
outside the published directory.

## Product screenshot provenance

`public/assets/screenshots/app-chat.png` and `app-roles.png` are captures of
the actual noXa frontend at application commit
`c2697e44a45172f9055063b90f00bac234657029`. They use fictional local demo
messages, members, channels, roles, and the reserved `community.example`
hostname. The page identifies the images as demo data. They are browser
captures with native API fixtures, not a connection to a production server
or proof of an active voice or encrypted session.

The chat image captures the 1280 by 800 client viewport. The roles image is a
crop of the real Roles dialog. `scripts/capture-manifest.json` records the
application source commit, Chromium version, viewport, locale, timezone, and
capture timestamp. The supplied concept mockups are not used as app captures.

To refresh from the checked-out application source, run from the repository
root:

```sh
npm --prefix client/frontend ci
npm --prefix client/frontend exec -- playwright install chromium
node website/scripts/capture-client.mjs
```

The capture script starts the actual frontend on `127.0.0.1:12367`, injects
the fictional native API fixtures, blocks external network requests, captures
the two images, and refreshes the manifest. Inspect both images and the
manifest before committing them, then run the website tests and compare
desktop/mobile previews. The script and fixture data stay outside `public/`.
Update the application commit recorded above when replacing these captures.

## Download contract

Every Windows download points to:

```text
https://github.com/arumes31/noxa/releases/latest/download/noxa-client-windows-amd64.exe
```

The release marked **Latest** must contain an asset named exactly
`noxa-client-windows-amd64.exe`. This is the Windows x64 client, not the server.
Changing the release asset name requires updating the page and its download
tests together. No release version is embedded in the page, and download links
do not require browser-side API calls or JavaScript.

The page links to [all releases](https://github.com/arumes31/noxa/releases),
[latest release details](https://github.com/arumes31/noxa/releases/latest), and
the current [new-server setup guide](https://github.com/arumes31/noxa#new-server-setup).
“Latest” does not imply stable; the product remains labeled Public alpha.

## GitHub Pages setup and deployment

One-time repository owner setup:

1. In **Settings → Pages → Build and deployment**, select **GitHub Actions**
   as the source.
2. In **Settings → Environments → github-pages**, restrict deployment branches
   to the selected branch `main` (not pull request refs or arbitrary branches).
   Keep any required deployment reviewers already configured.
3. Review the website PR and merge it through the repository's normal process.
   A website change pushed to `main` runs validation before publishing. To
   republish the current version, run the **Website** workflow manually with
   branch `main` selected.
4. Confirm the **Publish GitHub Pages** job succeeded and open its environment
   URL. The intended URL is <https://arumes31.github.io/noxa/>; a successful
   local test or uploaded artifact alone does not confirm a live deployment.

The dedicated [Pages workflow](../.github/workflows/pages.yml) validates every
pull request. Its **Website validation** job can be selected as a required
check without path filters leaving unrelated PRs pending. Push runs are
limited to `website/**` and the Pages workflow on `main`. Manual runs on other
branches can validate but cannot upload a deployment artifact or publish.
Publication also checks that `main` is still the default branch; update the
workflow and environment rules together if the default branch is renamed.

Only a successful validation job on an eligible default-branch run uploads
`website/public/`. The deployment job consumes that run's Pages artifact and
is the only job granted `pages: write` and `id-token: write`. It uses the
`github-pages` environment and serializes production deployments. PR runs
cannot cancel production deployments. Existing application CI and release
workflows are separate.

The workflow does not enable Pages itself or add a custom domain. If Pages is
not configured, complete step 1 and rerun the workflow on `main`. If an
environment approval is pending, an authorized reviewer must approve that
specific deployment. Do not loosen branch restrictions to publish a feature
branch. For rollback, revert the website change through the normal review
process and let the successful `main` run publish the reverted static files.

## Updating the page

Keep the Signature composition and product claims grounded in the current
client and repository documentation. Check desktop and mobile screenshots
after layout changes. Keep actual app captures distinct from illustrative UI,
and inspect every published image for private messages, production hostnames,
tokens, and personal information. A new screenshot must document its source,
application commit, capture method, and refresh procedure.

The page's canonical URL and social metadata deliberately use the production
project-site URL. If the repository or Pages domain changes, update those
values and the local-resource/base-path tests together.

The workflow action pins were verified against each official action's release
and Git tag on 2026-10-02: checkout v7.0.1, setup-node v7.0.0, configure-pages
v6.0.0, upload-pages-artifact v5.0.0, and deploy-pages v5.0.1. The repository's
existing GitHub Actions Dependabot configuration covers this workflow.
