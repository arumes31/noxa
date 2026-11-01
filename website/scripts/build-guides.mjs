// Generate crawlable documentation. Only the resulting public/ files are hosted.
import { lstat, mkdir, realpath, rm, writeFile } from 'node:fs/promises';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import serverGuides from './guides/server.mjs';
import clientGuides from './guides/clients.mjs';

const origin = 'https://noxa.wowcraft.pw';
const publicDirectory = new URL('../public/', import.meta.url);
const guides = [...clientGuides, ...serverGuides];
const guideShot = (name, width, height, alt, caption) => {
  const file = `${name}.${name === 'app-connect' ? 'jpg' : 'png'}`;
  return `<figure class="guide-figure"><a href="../../assets/screenshots/${file}"><img src="../../assets/screenshots/${file}" width="${width}" height="${height}" loading="lazy" decoding="async" alt="${alt}"></a><figcaption>${caption} · Actual client interface with fictional local demo data.</figcaption></figure>`;
};
const guideScreenshots = {
  'getting-started:connect': guideShot('app-connect', 1280, 1100, 'noXa connection screen with server, account, display name and separate password fields', 'Connect to the address supplied by your community'),
  'server-setup:first-start': guideShot('app-connect', 1280, 1100, 'noXa connection screen showing separate account and server password fields', 'Use the owner account created during offline setup'),
  'voice-video:microphone': guideShot('app-capture', 1080, 760, 'noXa Audio and voice settings showing transmission modes, input selection and microphone testing', 'Audio and voice settings; this fixture has no connected microphone'),
  'voice-video:echo': guideShot('app-connection', 1080, 760, 'noXa Connection settings showing network echo test and connection benchmark diagnostics', 'Find the network echo test under Connection, in Diagnostics'),
  'mobile:sign-in': guideShot('pwa-connect', 390, 844, 'noXa mobile sign-in screen with passkey, Scan QR code and desktop code controls', 'Use Scan QR code or the complete QR link to prepare encrypted messaging on a new browser'),
  'mobile:install': guideShot('pwa-you', 390, 844, 'noXa You screen with profile, searchable settings and the bottom navigation', 'Open You to find this browser’s settings and account controls'),
  'roles-permissions:channel-access': guideShot('app-channel-access', 690, 1036, 'noXa channel permissions showing role-based voice, video, sharing, messages and file controls', 'Channel access in a fictional members-only lounge'),
  'server-setup:roles-and-channels': guideShot('app-channel-access', 690, 1036, 'noXa channel permissions matrix with Member and everyone role settings', 'Channel access uses a separate policy from global role grants'),
  'studio:open': guideShot('app-studio', 1180, 602, 'noXa Studio with Preview and Program monitors, scene controls and a Go live button', 'Studio opens before any capture or broadcast starts'),
};
const escape = value => String(value).replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;').replaceAll('"', '&quot;');
const json = value => JSON.stringify(value).replaceAll('<', '\\u003c');
const enhance = html => html
  .replace(/<table>/g, '<div class="table-scroll" role="region" aria-label="Reference table" tabindex="0"><table>')
  .replace(/<\/table>/g, '</table></div>')
  .replace(/<a href="([^"<>]+\.(?:png|webp|jpe?g))">(?=<img)/g, '<a class="image-link" href="$1" data-full>')
  .replace(/class="guide-figure"(?=><a[^>]+><img[^>]+pwa-)/g, 'class="guide-figure mobile-shot"');
const guideLink = guide => `<a href="${guide.slug}/"><span class="guide-category">${escape(guide.category)}</span><h2>${escape(guide.title)}</h2><p>${escape(guide.summary)}</p><span class="guide-card-link">Read guide <span aria-hidden="true">↗</span></span></a>`;

function shell({ title, description, path, depth, body, schema }) {
  const base = '../'.repeat(depth);
  return `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <meta name="theme-color" content="#03070a">
  <title>${escape(title)} | noXa Guides</title>
  <meta name="description" content="${escape(description)}">
  <link rel="canonical" href="${origin}${path}">
  <meta property="og:type" content="website">
  <meta property="og:site_name" content="noXa">
  <meta property="og:title" content="${escape(title)} | noXa Guides">
  <meta property="og:description" content="${escape(description)}">
  <meta property="og:url" content="${origin}${path}">
  <meta property="og:image" content="${origin}/assets/screenshots/app-chat.png">
  <meta property="og:image:width" content="1280">
  <meta property="og:image:height" content="800">
  <meta property="og:image:alt" content="noXa desktop channels and chat with fictional demo data">
  <meta name="twitter:card" content="summary_large_image">
  <link rel="icon" type="image/png" sizes="32x32" href="${base}assets/branding/favicon-32.png">
  <link rel="apple-touch-icon" href="${base}assets/branding/apple-touch-icon.png">
  <link rel="stylesheet" href="${base}assets/css/site.css">
  <link rel="stylesheet" href="${base}assets/css/guides.css">
  <script defer src="${base}assets/js/site.js"></script>
  <script type="application/ld+json">${json(schema)}</script>
</head>
<body class="documentation">
  <a class="skip" href="#content">Skip to content</a>
  <header class="site-header wrap">
    <a class="brand" href="${base}" aria-label="noXa home"><span class="wordmark">no<span>X</span>a</span><span class="alpha">Alpha</span></a>
    <nav class="guide-top-nav" aria-label="Main navigation">
      <a href="${base}guides/"${depth === 1 ? ' aria-current="page"' : ''}>Guides</a>
      <a href="${base}guides/server-setup/">Self-host</a>
      <a href="https://github.com/arumes31/noxa">GitHub</a>
    </nav>
    <a class="header-download" href="https://github.com/arumes31/noxa/releases/latest/download/noxa-client-windows-amd64.exe">Download noXa</a>
  </header>
  ${body}
  <footer class="wrap site-footer">
    <a class="footer-brand" href="${base}" aria-label="noXa home"><span class="wordmark">no<span>X</span>a</span></a>
    <nav aria-label="Footer navigation"><a href="${base}guides/">All guides</a><a href="${base}guides/server-setup/">Server setup</a><a href="https://github.com/arumes31/noxa/releases">Releases</a><a href="https://github.com/arumes31/noxa/issues">Report an issue</a></nav>
  </footer>
  <dialog class="lightbox" aria-label="Product screenshot" aria-describedby="image-caption"><button type="button" aria-label="Close image" autofocus>×</button><img alt=""><p id="image-caption">noXa client · Fictional demo data</p></dialog>
</body>
</html>
`;
}

// public/guides is exclusively generated. Refuse symlinks and verify its
// resolved parent before removing stale pages from a renamed/removed guide.
const publicPath = await realpath(publicDirectory);
const guidesPath = resolve(publicPath, 'guides');
if (dirname(guidesPath) !== publicPath) throw new Error('Guide output must be a direct child of public/');
const oldGuides = await lstat(guidesPath).catch(error => { if (error.code !== 'ENOENT') throw error; return null; });
if (oldGuides?.isSymbolicLink()) throw new Error('Refusing to replace a symlinked guide directory');
await rm(guidesPath, { recursive: true, force: true });

for (const guide of guides) {
  const path = `/guides/${guide.slug}/`;
  const sidebar = guides.map(item => `<a href="../${item.slug}/"${item.slug === guide.slug ? ' aria-current="page"' : ''}>${escape(item.title)}</a>`).join('\n');
  const body = `<div class="wrap docs-layout">
    <aside class="docs-sidebar"><details open><summary>Browse guides</summary><nav aria-label="Guide navigation"><a href="../">All guides</a>${sidebar}</nav></details></aside>
    <main class="guide-article" id="content" tabindex="-1">
      <nav class="breadcrumbs" aria-label="Breadcrumb"><a href="../../">Home</a><span aria-hidden="true">/</span><a href="../">Guides</a><span aria-hidden="true">/</span><span aria-current="page">${escape(guide.title)}</span></nav>
      <header class="guide-intro"><p class="eyebrow">${escape(guide.category)} · Public alpha</p><h1>${escape(guide.title)}</h1><p class="guide-summary">${escape(guide.summary)}</p></header>
      <nav class="guide-toc" aria-label="On this page"><h2>On this page</h2><ol>${guide.sections.map(section => `<li><a href="#${section.id}">${escape(section.title)}</a></li>`).join('')}</ol></nav>
      ${guide.sections.map(section => `<section id="${section.id}" aria-labelledby="${section.id}-heading"><h2 id="${section.id}-heading">${escape(section.title)}</h2>${enhance(section.body + (guideScreenshots[`${guide.slug}:${section.id}`] || ''))}</section>`).join('\n')}
      <section class="guide-related" aria-labelledby="next-heading"><h2 id="next-heading">Keep going</h2><ul>${guide.related.map(slug => { const item = guides.find(item => item.slug === slug); if (!item) throw new Error(`Unknown related guide: ${slug}`); return `<li><a href="../${item.slug}/">${escape(item.title)}</a></li>`; }).join('')}</ul></section>
      <aside class="guide-sources" aria-label="Source documentation"><p>Based on the current noXa documentation. Public alpha features and menu labels can change; use the client and server from the same release.</p><ul>${guide.sources.map(source => `<li><a href="${escape(source.href)}">${escape(source.label)}</a></li>`).join('')}</ul><a href="https://github.com/arumes31/noxa/issues">Suggest a correction</a></aside>
    </main>
  </div>`;
  const schema = { '@context': 'https://schema.org', '@graph': [
    { '@type': 'TechArticle', '@id': `${origin}${path}#article`, headline: guide.title, description: guide.summary, url: `${origin}${path}`, inLanguage: 'en', isPartOf: { '@id': `${origin}/#website` }, about: { '@id': `${origin}/#software` } },
    { '@type': 'BreadcrumbList', itemListElement: [{ '@type': 'ListItem', position: 1, name: 'Home', item: `${origin}/` }, { '@type': 'ListItem', position: 2, name: 'Guides', item: `${origin}/guides/` }, { '@type': 'ListItem', position: 3, name: guide.title, item: `${origin}${path}` }] },
  ] };
  const directory = new URL(`guides/${guide.slug}/`, publicDirectory);
  await mkdir(directory, { recursive: true });
  await writeFile(new URL('index.html', directory), shell({ title: guide.title, description: guide.summary, path, depth: 2, body, schema }));
}

const hubBody = `<main class="wrap guides-hub" id="content" tabindex="-1"><nav class="breadcrumbs" aria-label="Breadcrumb"><a href="../">Home</a><span aria-hidden="true">/</span><span aria-current="page">Guides</span></nav><header class="guide-intro"><p class="eyebrow">Learn your way around</p><h1>A home for every<br><em>how do I…?</em></h1><p class="guide-summary">Set up your server, bring your people together, and get more from noXa. Practical guides with real client screenshots and clear steps.</p></header><div class="guide-start"><div><span class="guide-category">New to noXa?</span><h2>Your first conversation starts here.</h2><p>Download the Windows client, choose a server, and get connected.</p></div><a class="btn" href="getting-started/">Get started <span aria-hidden="true">↗</span></a></div><div class="guide-grid">${guides.map(guideLink).join('\n')}</div><p class="guide-hub-note">noXa is in public alpha. Screenshots use fictional demo data. See the <a href="https://github.com/arumes31/noxa/releases">release notes</a> for changes in your version.</p></main>`;
await mkdir(new URL('guides/', publicDirectory), { recursive: true });
await writeFile(new URL('guides/index.html', publicDirectory), shell({ title: 'Setup, client & community guides', description: 'Learn to use and self-host noXa with step-by-step guides for server setup, voice, video, chat, roles, mobile, integrations, and troubleshooting.', path: '/guides/', depth: 1, body: hubBody, schema: { '@context': 'https://schema.org', '@type': 'CollectionPage', name: 'noXa Guides', url: `${origin}/guides/`, hasPart: guides.map(guide => ({ '@type': 'TechArticle', headline: guide.title, url: `${origin}/guides/${guide.slug}/` })) } }));
const urls = ['/', '/guides/', ...guides.map(guide => `/guides/${guide.slug}/`)];
await writeFile(new URL('sitemap.xml', publicDirectory), `<?xml version="1.0" encoding="UTF-8"?>\n<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">\n${urls.map(path => `  <url><loc>${origin}${path}</loc></url>`).join('\n')}\n</urlset>\n`);
console.log(`Generated ${guides.length} guides, the guide index, and sitemap in ${fileURLToPath(publicDirectory)}`);
