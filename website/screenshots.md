# Product screenshot provenance

The public images are captures of the actual noXa frontend, not reconstructed
interfaces or the illustrative mockups supplied with the Signature concept.
Display the caption **Actual client · Demo data** beside each image on the site.

| File | Source and framing |
| --- | --- |
| `public/assets/screenshots/app-chat.png` | Full 1280 × 800 client viewport: channel tree, member strip and conversation. |
| `public/assets/screenshots/app-roles.png` | Actual Roles dialog, cropped to its 960 × 712 bounds. The Member role and permission search are selected through the UI. |

Both captures were generated from app commit
`c2697e44a45172f9055063b90f00bac234657029` on 2026-10-02 using Chromium
151.0.7922.34 through the client’s locked Playwright dependency. The browser used
an isolated profile, a 1280 × 800 viewport at scale 1, English locale, UTC timezone,
fixed fixture time of `2026-10-02T18:35:00Z` and reduced motion. Exact capture metadata is in
[`scripts/capture-manifest.json`](scripts/capture-manifest.json).

The fixture in [`scripts/capture-client.mjs`](scripts/capture-client.mjs) supplies
fictional people (Rowan, Avery, Sage, Morgan and Quinn), authored conversation
text, channels, role names and a reserved `community.example` address. It drives
the real `client/frontend/index.html`, application modules and styles. Native
Wails API responses are local fixture data, following the existing app browser
test harness. The roles fixture uses a small set of valid capabilities from
`internal/authorization/capabilities.go`; it is not a complete permission catalog.
Voice and latency indicators are fixture state, not measured service performance.
No server connection, microphone, camera, private profile or real conversation is
used. Browser network requests to non-local hosts are blocked.

The screenshots retain the app’s locally bundled Outfit typography. Its font
software is supplied by `@fontsource-variable/outfit` under the SIL Open Font
License 1.1 (`client/frontend/node_modules/@fontsource-variable/outfit/LICENSE`).
No font file is copied into the website by this capture process.

## Refresh

Capturing is optional maintenance, separate from website validation. From the
repository root, install the existing client’s locked development dependencies
and Playwright Chromium if needed:

```sh
cd client/frontend
npm ci
npx playwright install chromium
cd ../..
node website/scripts/capture-client.mjs
```

The script starts Vite on `127.0.0.1:12367`, captures the actual frontend, checks
for browser runtime errors, writes only the two PNGs and provenance manifest,
then closes the browser and Vite. The port must be available. The client source
and styles are not modified. If a future UI change breaks the fixture, update
the fixture to use the new actual UI; do not paint replacement controls over the
screenshots.

After refreshing, inspect both PNGs for complete, readable content and update
the commit/runtime and any changed crop dimensions in this document. Confirm
that only fictional data is visible and keep the public demo-data captions.
Only `website/public` is published; the fixture, this document and the manifest
remain outside the Pages artifact.
