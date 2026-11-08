import adapter from '@sveltejs/adapter-static';
import { sveltekit } from '@sveltejs/kit/vite';
import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';
import { createHash } from 'node:crypto';
import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { defineConfig } from 'vite';

// Deterministic app version = hash of the sources. SvelteKit's default is
// Date.now(), which changes the inline bootstrap script (and with it the CSP
// hash, version.json and index.html) on EVERY build; with a content hash an
// unchanged frontend rebuilds to a byte-identical, committed dist/.
function sourceVersion() {
  const root = fileURLToPath(new URL('.', import.meta.url));
  const h = createHash('sha256');
  const walk = (dir) => {
    for (const e of readdirSync(dir, { withFileTypes: true }).sort((a, b) =>
      a.name < b.name ? -1 : 1
    )) {
      const p = join(dir, e.name);
      if (e.isDirectory()) walk(p);
      else h.update(p.slice(root.length)).update('\0').update(readFileSync(p)).update('\0');
    }
  };
  walk(join(root, 'src'));
  for (const f of ['package-lock.json', 'vite.config.js'])
    h.update(f).update(readFileSync(join(root, f)));
  return h.digest('hex').slice(0, 12);
}

// SvelteKit 3: the project configuration is passed to the plugin (there is no
// svelte.config.js any more); options SvelteKit does not know go to
// vite-plugin-svelte.
export default defineConfig({
  plugins: [
    sveltekit({
      preprocess: vitePreprocess(),
      version: { name: sourceVersion() },
      // CSP without 'unsafe-inline' scripts. SvelteKit's static build emits ONE
      // inline bootstrap <script> per HTML page; with mode 'hash' the build adds a
      // <meta http-equiv="content-security-policy"> carrying that script's
      // sha256. Keep these directives equal to the header the Go server sends
      // (internal/adapters/rest/middleware.go, withCommonHeaders): a page must
      // satisfy BOTH policies, so a directive that is looser here buys nothing and
      // one that is stricter here breaks the app. frame-ancestors (and sandbox /
      // report-uri) are ignored in <meta> and stay header-only.
      csp: {
        mode: 'hash',
        directives: {
          'default-src': ['self'],
          'img-src': ['self', 'data:'],
          'media-src': ['self', 'data:'],
          'style-src': ['self', 'unsafe-inline'],
          'script-src': ['self'],
          'connect-src': ['self'],
          'object-src': ['none'],
          'form-action': ['self'],
          'base-uri': ['self']
        }
      },
      // Build a static SPA straight into the Go server's embed directory.
      adapter: adapter({
        pages: '../internal/webui/dist',
        assets: '../internal/webui/dist',
        fallback: 'index.html',
        precompress: false,
        strict: false
      })
    })
  ],
  server: {
    // Dev convenience: proxy API calls to the Go server on :8080 (override
    // with API_PROXY=http://host:port to point at another instance).
    proxy: {
      '/api': process.env.API_PROXY || 'http://localhost:8080'
    }
  }
});
