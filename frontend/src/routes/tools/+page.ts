import { redirect } from '@sveltejs/kit';
import type { PageLoad } from './$types';

// У пользователей есть закладки на /tools?tab=… — раздаём их по новым
// маршрутам, чтобы старые ссылки не приводили в 404.
const TAB_ROUTES: Record<string, string> = {
	logs: '/logs',
	monitoring: '/monitoring',
	connections: '/connections',
	checks: '/diagnostics?tab=checks',
	about: '/diagnostics?tab=about',
	awgConfig: '/diagnostics?tab=awgConfig',
	dns: '/diagnostics?tab=dns',
	// Значения времён вкладки «Журнал» с рейлом здоровья.
	tests: '/diagnostics?tab=checks',
	dnscheck: '/diagnostics?tab=checks',
};

export const load: PageLoad = ({ url }) => {
	const tab = url.searchParams.get('tab');
	redirect(308, (tab && TAB_ROUTES[tab]) || '/logs');
};
