# noXa product website

A static product and Windows-download page using the client's OLED Midnight
palette, hosted at <https://noxa.wowcraft.pw/>. The same origin serves the PWA
at `/app/` and redirects `/dl/noxa-client-windows-amd64.exe` to the latest
Windows release on GitHub, through HTTPS port 443. The desktop public test
server address is `wowcraft.pw`.

## Files and local preview

Only `public/` is deployed. It contains semantic HTML, CSS, a small browser
script and local branding/screenshots. There is no application
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
`5442eb2e4275fe2c6c651dcc0cea955fc5dfdd36`. They use fictional local demo
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

`public/assets/screenshots/pwa-chat.png` captures the actual mobile chat components
at 390 × 844 with fictional local data. Run `node website/scripts/capture-mobile.mjs`
after installing `mobile/` dependencies and Chromium to refresh it. Its source
revision and browser are recorded in `scripts/capture-mobile-manifest.json`.
External requests and service workers are blocked during both capture workflows.

## Download contract

Every Windows download points to:

```text
https://noxa.wowcraft.pw/dl/noxa-client-windows-amd64.exe
```

The release marked **Latest** must contain an asset named exactly
`noxa-client-windows-amd64.exe`. This is the Windows x64 client, not the server.
The reverse proxy returns a temporary redirect to
`https://github.com/arumes31/noxa/releases/latest/download/noxa-client-windows-amd64.exe`.
The executable stays on GitHub; no server-side copy is maintained. Existing authenticated
file transfers under `/dl/` keep their capability checks; the public Windows
installer is an exact route exception.

Changing the release asset name requires updating the page and its download
tests together. No release version is embedded in the page, and download links
do not require browser-side API calls or JavaScript.

The page links to [all releases](https://github.com/arumes31/noxa/releases),
[latest release details](https://github.com/arumes31/noxa/releases/latest), and
the current [new-server setup guide](https://github.com/arumes31/noxa#new-server-setup).
“Latest” does not imply stable; the product remains labeled Public alpha.

## Hosting and validation

GitHub Pages is disabled. The [Website workflow](../.github/workflows/website.yml)
validates pull requests and website changes; it does not publish a site or
create deployments. Host `public/` at the root of the public HTTPS origin.
Keep the `/app/`, `/ws`, API and file-transfer routes directed to the noXa web
listener, and the exact Windows download route redirected to GitHub Releases.
The website files are separate from the noXa Docker image.

Validate before copying the static files to the host. Keep a copy of the
previous public directory for rollback; deploy only `public/`, never the
repository, test fixtures or operator files.

## Updating the page

Keep the Signature composition and product claims grounded in the current
client and repository documentation. Check desktop and mobile screenshots
after layout changes. Keep actual app captures distinct from illustrative UI,
and inspect every published image for private messages, production hostnames,
tokens, and personal information. A new screenshot must document its source,
application commit, capture method, and refresh procedure.

The canonical URL and social metadata use <https://noxa.wowcraft.pw/>.
Update those values and browser tests together if the public domain changes.
