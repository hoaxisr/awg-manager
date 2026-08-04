// fakeipConfig — DNS-часть fakeip-слота. Правила, наборы и outbound'ы живут в
// общем слоте маршрутизации (21-routing.json) и обслуживаются singboxRouter:
// fakeip-табы читают их оттуда же, что и sb-router. Здесь остался только DNS —
// он в fakeip режимный (свой fakeip-сервер, свои DNS-правила).
import { writable } from 'svelte/store';
import { api } from '$lib/api/client';
import type {
	SingboxRouterDNSServer,
	SingboxRouterDNSRule,
	SingboxRouterDNSGlobals,
} from '$lib/types';

function createFakeipConfigStore() {
	const dnsServers = writable<SingboxRouterDNSServer[]>([]);
	const dnsRules = writable<SingboxRouterDNSRule[]>([]);
	const dnsGlobals = writable<SingboxRouterDNSGlobals>({ final: '', strategy: '' });
	const loading = writable(false);
	const initialized = writable(false);
	const error = writable<string | null>(null);

	async function loadAll(): Promise<void> {
		loading.set(true);
		error.set(null);
		try {
			const [ds, dr, dg] = await Promise.all([
				api.singboxFakeIPListDNSServers(),
				api.singboxFakeIPListDNSRules(),
				api.singboxFakeIPGetDNSGlobals(),
			]);
			dnsServers.set(ds);
			dnsRules.set(dr);
			dnsGlobals.set(dg);
		} catch (e) {
			error.set(e instanceof Error ? e.message : 'Не удалось загрузить fakeip-конфиг');
		} finally {
			loading.set(false);
			initialized.set(true);
		}
	}

	function applyDNSServers(data: SingboxRouterDNSServer[]): void {
		dnsServers.set(data);
	}

	function applyDNSRules(data: SingboxRouterDNSRule[]): void {
		dnsRules.set(data);
	}

	function applyDNSGlobals(data: SingboxRouterDNSGlobals): void {
		dnsGlobals.set(data);
	}

	return {
		dnsServers: { subscribe: dnsServers.subscribe },
		dnsRules: { subscribe: dnsRules.subscribe },
		dnsGlobals: { subscribe: dnsGlobals.subscribe },
		loading: { subscribe: loading.subscribe },
		initialized: { subscribe: initialized.subscribe },
		error: { subscribe: error.subscribe },
		loadAll,
		applyDNSServers,
		applyDNSRules,
		applyDNSGlobals,
	};
}

export const fakeipConfig = createFakeipConfigStore();
