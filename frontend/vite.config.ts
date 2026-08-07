import { sveltekit } from '@sveltejs/kit/vite';
import tailwindcss from '@tailwindcss/vite';
import { svelteTesting } from '@testing-library/svelte/vite';
import { readFileSync } from 'node:fs';
import { fileURLToPath, URL } from 'node:url';
import { defineConfig, loadEnv, type Plugin } from 'vite';

/**
 * In mock-dev (`VITE_API_STRIP_PREFIX=true`) the router-relative
 * /site.webmanifest references in SvelteKit pages produce child-URL
 * requests like /tunnels/site.webmanifest, /singbox/site.webmanifest,
 * /subscriptions/site.webmanifest, /awg/servers/site.webmanifest instead of the real root path.
 * This Vite middleware intercepts those requests and serves the actual
 * manifest file from frontend/static/ so the PWA install prompt works
 * on nested routes during mock-dev on Windows.
 * Does NOT apply in production builds or real API mode.
 */
const serveNestedManifestInMockDev = (enabled: boolean): Plugin => ({
	name: 'serve-nested-site-webmanifest-in-mock-dev',
	apply: 'serve',
	enforce: 'pre',
	configureServer(server) {
		if (!enabled) return;

		server.middlewares.use((req, res, next) => {
			const pathname = (req.url ?? '').split('?')[0];

			const nestedManifestPaths = new Set([
				'/tunnels/site.webmanifest',
				'/singbox/site.webmanifest',
				'/subscriptions/site.webmanifest',
				'/awg/servers/site.webmanifest',
			]);

			const isNestedManifest = nestedManifestPaths.has(pathname);

			if (!isNestedManifest) {
				next();
				return;
			}

			if (req.method !== 'GET' && req.method !== 'HEAD') {
				next();
				return;
			}

			const body = readFileSync(
				fileURLToPath(new URL('./static/site.webmanifest', import.meta.url)),
				'utf8',
			);

			res.statusCode = 200;
			res.setHeader('Content-Type', 'application/manifest+json; charset=utf-8');
			res.setHeader('Cache-Control', 'no-store, max-age=0');
			res.end(req.method === 'HEAD' ? undefined : body);
		});
	},
});

/**
 * Strip /routes/dev/* contents during production build so dev-only
 * Storybook pages do not ship in the IPK bundle.
 *
 * The +page.ts load() in those pages throws 404 in production as a
 * runtime gate; this plugin removes the demo page chunk entirely so
 * the bundle stays minimal.
 */
const stubDevRoutes = (): Plugin => ({
	name: 'stub-dev-routes',
	enforce: 'pre',
	apply: 'build',
	load(id) {
		const norm = id.replace(/\\/g, '/');
		if (!norm.includes('/src/routes/dev/')) return null;
		if (norm.endsWith('+page.svelte')) {
			return '<script lang="ts"></script>';
		}
		if (norm.endsWith('+page.ts') || norm.endsWith('+page.js')) {
			return [
				"import { error } from '@sveltejs/kit';",
				'export const prerender = false;',
				'export const ssr = false;',
				'export function load() { error(404, "Not Found"); }',
				''
			].join('\n');
		}
		if (norm.endsWith('.css')) {
			return '';
		}
		return null;
	}
});

export default defineConfig(({ mode }) => {
	const env = loadEnv(mode, process.cwd(), '');
	const envValue = (key: string) => env[key] ?? process.env[key] ?? '';
	const isTruthy = (value: string) => ['1', 'true', 'yes', 'on'].includes(value.trim().toLowerCase());
	const apiTarget = envValue('VITE_API_TARGET') || 'http://127.0.0.1:8080';
	const useMockRewrite = isTruthy(envValue('VITE_API_STRIP_PREFIX'));

	return {
		plugins: [
			serveNestedManifestInMockDev(useMockRewrite),
			stubDevRoutes(),
			tailwindcss(),
			sveltekit(),
			svelteTesting(),
		],
		test: {
			environment: 'jsdom',
			include: ['src/**/*.test.ts'],
			// Форк на ядро (дефолт vitest) — это 200-400 МБ на форк, то есть
			// ~2.5 ГБ на прогон при 12 ядрах. На машине разработчика этого
			// хватало, чтобы забить ОЗУ и своп: OOM killer выбирал не vitest,
			// а соседние процессы, и падения выглядели чем угодно, только не
			// нехваткой памяти. Два — потолок, при котором прогон переживает
			// параллельно открытый браузер и мок-стенд.
			// Поднять разово можно флагом: --maxWorkers=N. Именно maxWorkers, а
			// не poolOptions.forks.maxForks: vitest 4 убрал poolOptions и
			// молча игнорировал предел — печатал DEPRECATED и поднимал форк
			// на ядро, то есть ограничения не было вовсе.
			maxWorkers: 2,
		},
		resolve: {
			alias: {
				// Filesystem-absolute paths so esbuild's optimize-deps can
				// resolve the shim during pre-bundle. The previous "/src/..."
				// pseudo-root only works through Vite's own resolver and
				// crashed esbuild with "Cannot read file: /src/...".
				'node:dns/promises': fileURLToPath(new URL('./src/lib/shims/node-dns-promises.ts', import.meta.url)),
				'dns/promises': fileURLToPath(new URL('./src/lib/shims/node-dns-promises.ts', import.meta.url))
			}
		},
		server: {
			proxy: {
				'/api': {
					target: apiTarget,
					changeOrigin: true,
					ws: true,
					rewrite: useMockRewrite ? (p) => p.replace(/^\/api/, '') : undefined
				}
			}
		}
	};
});
