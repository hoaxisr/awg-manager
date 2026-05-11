import { sveltekit } from '@sveltejs/kit/vite';
import tailwindcss from '@tailwindcss/vite';
import { svelteTesting } from '@testing-library/svelte/vite';
import { fileURLToPath, URL } from 'node:url';
import { defineConfig, loadEnv, type Plugin } from 'vite';

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
	const apiTarget = env.VITE_API_TARGET || 'http://127.0.0.1:8080';
	const useMockRewrite = env.VITE_API_STRIP_PREFIX === '1';

	return {
		plugins: [stubDevRoutes(), tailwindcss(), sveltekit(), svelteTesting()],
		test: {
			environment: 'jsdom',
			include: ['src/**/*.test.ts'],
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
