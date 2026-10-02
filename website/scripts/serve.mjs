import { createServer } from 'node:http';
import { readFile } from 'node:fs/promises';
import { extname, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

const publicDirectory = fileURLToPath(new URL('../public/', import.meta.url));
const mimeTypes = {
  '.html': 'text/html; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.png': 'image/png',
  '.jpg': 'image/jpeg',
  '.jpeg': 'image/jpeg',
  '.webp': 'image/webp',
  '.svg': 'image/svg+xml',
  '.ico': 'image/x-icon',
  '.woff2': 'font/woff2',
};

// Both locations intentionally serve the same output to catch project-site URL bugs.
export function createPreviewServer() {
  return createServer(async (request, response) => {
    if (request.method !== 'GET' && request.method !== 'HEAD') {
      response.writeHead(405, { Allow: 'GET, HEAD' }).end();
      return;
    }
    let pathname;
    try {
      pathname = decodeURIComponent(new URL(request.url, 'http://localhost').pathname);
    } catch {
      response.writeHead(400).end('Bad request');
      return;
    }
    if (pathname === '/noxa') {
      response.writeHead(308, { Location: '/noxa/' }).end();
      return;
    }
    if (pathname.startsWith('/noxa/')) pathname = pathname.slice('/noxa'.length);
    if (pathname.endsWith('/')) pathname += 'index.html';
    const file = resolve(publicDirectory, `.${pathname}`);
    if (!file.startsWith(resolve(publicDirectory) + sep)) {
      response.writeHead(403).end('Forbidden');
      return;
    }
    try {
      const contents = await readFile(file);
      response.writeHead(200, {
        'Content-Type': mimeTypes[extname(file)] || 'application/octet-stream',
        'Content-Length': contents.length,
        'Cache-Control': 'no-store',
      });
      response.end(request.method === 'HEAD' ? undefined : contents);
    } catch (error) {
      response.writeHead(error.code === 'ENOENT' || error.code === 'EISDIR' ? 404 : 500).end();
    }
  });
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const port = Number(process.env.PORT || 4173);
  createPreviewServer().listen(port, '127.0.0.1', () => {
    console.log(`noXa website preview: http://127.0.0.1:${port}/noxa/`);
  });
}
