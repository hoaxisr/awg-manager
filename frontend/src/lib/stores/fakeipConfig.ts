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

// Экспортируется ради тестов: они проверяют поведение loadAll на чистом сторе,
// а синглтон переживает весь файл тестов.
export function createFakeipConfigStore() {
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
			// initialized поднимается ТОЛЬКО при успехе — «данные есть», а не
			// «попытка была». Гейтить загрузку по нему нельзя (SSE стор не освежает,
			// см. loadOnce), но флаг остаётся честным признаком для всякого, кому
			// нужно отличить пустой список от незагруженного.
			initialized.set(true);
		} catch (e) {
			error.set(e instanceof Error ? e.message : 'Не удалось загрузить fakeip-конфиг');
		} finally {
			loading.set(false);
		}
	}

	// loadOnce — загрузка для onMount. Гейта по initialized тут НЕТ намеренно:
	// SSE этот стор не освежает, поэтому каждый монтаж обязан перечитать DNS.
	// Дедуп только по in-flight обещанию — на входе на страницу монтируются
	// каркас (ему нужен бейдж чипа «DNS») и активная вкладка сразу, и без него
	// оба слали бы свою тройку GET'ов.
	//
	// Мутации зовут loadAll напрямую: им нужен круг ПОСЛЕ записи, а не подписка
	// на круг, начатый до неё.
	let inFlight: Promise<void> | null = null;
	function loadOnce(): Promise<void> {
		if (!inFlight) {
			inFlight = loadAll().finally(() => {
				inFlight = null;
			});
		}
		return inFlight;
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
		loadOnce,
		applyDNSServers,
		applyDNSRules,
		applyDNSGlobals,
	};
}

export const fakeipConfig = createFakeipConfigStore();
