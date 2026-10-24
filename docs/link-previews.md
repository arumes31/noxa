# Server-mediated link previews

Clients request metadata through their authenticated control connection (198/199,
`server_link_previews_v1`). Unsupported servers show plain links; clients never
fall back to fetching metadata or thumbnails themselves. The desktop renders
text with textContent and only accepts JPEG data URLs for previews. Its image CSP
also disallows external HTTP(S) sources. The PWA currently displays message URLs
as text and does not independently fetch link previews.

The server performs ordinary bounded HTTPS GETs, parses selected JSON/HTML fields,
and decodes/re-encodes raster thumbnails to JPEG. There is no browser, JavaScript
engine, subprocess, plugin, or downloaded executable involved. Provider embed HTML,
scripts, SVG, credentials, cookies and source image metadata are never returned.
Only HTML/JSON metadata and JPEG/PNG/GIF/WebP image MIME types are accepted.

Requests require login and accepted rules. A separate per-session budget, four
concurrent server fetches, eight-second deadlines, bounded headers/bodies (256 KiB
HTML, 64 KiB provider JSON, 2 MiB input images), a four-megapixel decode limit,
640-pixel thumbnails and 200 KiB output cap bound resource use. Results have a
64-entry in-memory cache with a one-minute reuse lifetime; nothing is written to disk.

HTTPS port 443 is required. User info, local/reserved IP ranges and local hostnames
are rejected. All DNS results must be public; the dialer connects to a validated
IP directly while TLS verifies the original hostname. Every redirect is checked;
provider metadata additionally retains the provider-specific redirect policy.
Environment HTTP proxies are disabled so they cannot bypass this address policy.

Enabling previews discloses the preview URL to the connected server, including
URLs in encrypted conversations; message encryption remains unchanged. This is
explained beside the preview preference. Explicitly opening a link in the user's
browser is a separate action and naturally visits the destination.
