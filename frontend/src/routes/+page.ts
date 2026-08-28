import { redirect } from '@sveltejs/kit';
import { legacyProxyTabRedirect } from '$lib/utils/legacyProxyTab';
import type { PageLoad } from './$types';

// Санитайзер работает в load, а не в onMount: легаси-адрес обязан увести
// на /proxy ДО первого рендера главной (ia.md §1.1).
export const load: PageLoad = ({ url }) => {
	const target = legacyProxyTabRedirect(url);
	if (target) redirect(307, target);
};
