const repository = 'arumes31/noxa';
const expectedAsset = 'noxa-client-windows-amd64.exe';
const endpoint = `https://api.github.com/repos/${repository}/releases/latest`;

// This integration check reads release metadata only; it never downloads an executable.
let release;
try {
  const response = await fetch(endpoint, {
    headers: {
      Accept: 'application/vnd.github+json',
      'X-GitHub-Api-Version': '2022-11-28',
      'User-Agent': 'noxa-website-release-check',
      ...(process.env.GITHUB_TOKEN ? { Authorization: `Bearer ${process.env.GITHUB_TOKEN}` } : {}),
    },
    signal: AbortSignal.timeout(20_000),
  });
  if (!response.ok) throw new Error(`GitHub API returned HTTP ${response.status}`);
  release = await response.json();
} catch (error) {
  console.error(`Release verification unavailable: ${error.message}`);
  process.exit(2);
}

const asset = release.assets?.find(candidate => candidate.name === expectedAsset);
if (!asset || asset.state !== 'uploaded' || asset.size <= 0) {
  console.error(`Latest release ${release.tag_name || '(unknown)'} has no uploaded ${expectedAsset}.`);
  process.exit(1);
}
const prefix = `https://github.com/${repository}/releases/download/`;
if (!asset.browser_download_url?.startsWith(prefix) ||
    !asset.browser_download_url.endsWith(`/${expectedAsset}`)) {
  console.error('Latest Windows client asset has an unexpected download URL.');
  process.exit(1);
}
console.log(`Verified ${expectedAsset} in latest release ${release.tag_name}; executable not downloaded.`);
