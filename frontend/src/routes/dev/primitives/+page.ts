import { dev } from '$app/environment';
import { error } from '@sveltejs/kit';

export const prerender = false;
export const ssr = false;

export function load() {
	if (!dev) {
		error(404, 'Not Found');
	}
	return {};
}
