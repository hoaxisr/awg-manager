import { sveltekit } from '@sveltejs/kit/vite';
import tailwindcss from '@tailwindcss/vite';
import { defineConfig, type Plugin } from 'vite';

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

export default defineConfig({
	plugins: [stubDevRoutes(), tailwindcss(), sveltekit()],
	server: {
		proxy: {
			'/api': {
				target: 'http://127.0.0.1:8080',
				changeOrigin: true
			}
		}
	}
});
