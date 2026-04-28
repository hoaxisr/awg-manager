import { sveltekit } from '@sveltejs/kit/vite';
import tailwindcss from '@tailwindcss/vite';
import { defineConfig } from 'vite';

export default defineConfig({
	plugins: [tailwindcss(), sveltekit()],
	server: {
		proxy: {
			'/api': {
				target: 'http://127.0.0.1:8080',
				changeOrigin: true
			}
		}
	},
	build: {
		rollupOptions: {
			external: ['node:dns/promises'],
			onwarn(warning, warn) {
				// Ignore node:dns/promises externalized warning from @vinejs/vine
				if (warning.message.includes('node:dns/promises')) return;
				warn(warning);
			}
		}
	}
});
