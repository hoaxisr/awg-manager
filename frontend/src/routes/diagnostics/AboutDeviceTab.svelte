<script lang="ts">
	import { onMount } from 'svelte';
	import { get } from 'svelte/store';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { systemInfo } from '$lib/stores/system';
	import { settings, usageLevel, reloadSettings } from '$lib/stores/settings';
	import { auth } from '$lib/stores/auth';
	import { theme } from '$lib/stores/theme';
	import { singboxStatus } from '$lib/stores/singbox';
	import {
		isRoutingSubTabVisible,
		isSectionVisible,
		type UsageLevel,
	} from '$lib/types/usageLevel';
	import type {
		AccessPolicy,
		DeviceProxyConfig,
		DeviceProxyRuntime,
		DnsCheckStartResponse,
		HydraRouteStatus,
		PolicyDevice,
		Subscription,
	} from '$lib/types';
	import { Button } from '$lib/components/ui';
	import { AboutInfoSection } from '$lib/components/diagnostics';
	import { NdmsPolicyHintBanner } from '$lib/components/dnsroutes';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import {
		awgmServicesRows,
		browserSnapshotRows,
		buildAwgmServicesSnapshot,
		buildPolicyNameLookup,
		buildRouterClientContext,
		collectBrowserSnapshot,
		formatAboutReport,
		routerClientRows,
		routerStaticRows,
		type AwgmServicesSnapshot,
		type BrowserSnapshot,
		type RouterClientContext,
	} from '$lib/utils/about-device';

	let refreshing = $state(false);
	let browserSnap = $state<BrowserSnapshot | null>(null);
	let routerClient = $state<RouterClientContext | null>(null);
	let awgmSnap = $state<AwgmServicesSnapshot | null>(null);
	let lastAwgmCounts: Partial<AwgmCounts> = {};

	const sysInfo = $derived($systemInfo.data);
	const isOS5 = $derived(sysInfo?.isOS5 ?? false);

	type AwgmCounts = {
		hydra?: HydraRouteStatus | null;
		hydraLoaded?: boolean;
		deviceProxy?: DeviceProxyConfig | null;
		deviceProxyRuntime?: DeviceProxyRuntime | null;
		dnsRoutesTotal?: number;
		dnsRoutesEnabled?: number;
		dnsRoutesLoaded?: boolean;
		awgRunning?: number;
		awgTotal?: number;
		awgCountsLoaded?: boolean;
		subscriptionsEnabled?: number;
		subscriptionsTotal?: number;
		subscriptionsLoaded?: boolean;
	};

	function patchAwgmFromStores(counts?: Partial<AwgmCounts>) {
		if (counts) {
			lastAwgmCounts = { ...lastAwgmCounts, ...counts };
		}
		const c = lastAwgmCounts;
		const level = get(usageLevel);
		awgmSnap = buildAwgmServicesSnapshot({
			level,
			theme: get(theme),
			settings: get(settings),
			authDisabled: get(auth).authDisabled,
			authenticated: get(auth).authenticated,
			login: get(auth).login,
			singbox: get(singboxStatus).data,
			hydra: c.hydra ?? null,
			hydraLoaded: c.hydraLoaded ?? false,
			showHydra: isRoutingSubTabVisible(level, 'hrNeo'),
			deviceProxy: c.deviceProxy ?? null,
			deviceProxyRuntime: c.deviceProxyRuntime ?? null,
			dnsRoutesTotal: c.dnsRoutesTotal ?? 0,
			dnsRoutesEnabled: c.dnsRoutesEnabled ?? 0,
			dnsRoutesLoaded: c.dnsRoutesLoaded ?? false,
			showDnsRoutes: isRoutingSubTabVisible(level, 'dnsRoutes'),
			awgRunning: c.awgRunning ?? 0,
			awgTotal: c.awgTotal ?? 0,
			awgCountsLoaded: c.awgCountsLoaded ?? false,
			subscriptionsEnabled: c.subscriptionsEnabled ?? 0,
			subscriptionsTotal: c.subscriptionsTotal ?? 0,
			subscriptionsLoaded: c.subscriptionsLoaded ?? false,
		});
	}

	async function fetchAccessPolicies(): Promise<AccessPolicy[]> {
		if (!isSectionVisible(get(usageLevel), 'routing')) {
			return [];
		}
		try {
			const res = await fetch('/api/routing/access-policies');
			if (!res.ok) return [];
			const body = await res.json();
			return (body.data ?? []) as AccessPolicy[];
		} catch {
			return [];
		}
	}

	/** Fast path first; fall back to full dns-check/start on older backends. */
	async function fetchClientContext(): Promise<DnsCheckStartResponse | null> {
		try {
			return await api.getDnsCheckClient();
		} catch {
			try {
				return await api.startDnsCheck();
			} catch {
				return null;
			}
		}
	}

	function loadHydraStatus(level: UsageLevel) {
		if (!isRoutingSubTabVisible(level, 'hrNeo')) return;
		void api
			.getHydraRouteStatus()
			.then((hydra) => {
				patchAwgmFromStores({ hydra, hydraLoaded: true });
			})
			.catch(() => {
				patchAwgmFromStores({ hydra: null, hydraLoaded: true });
			});
	}

	async function loadRemoteContext() {
		const level = get(usageLevel);

		// Клиент в сети — приоритет, без ожидания HydraRoute и тяжёлых счётчиков.
		const [dns, policies, devices] = await Promise.all([
			fetchClientContext(),
			fetchAccessPolicies(),
			isRoutingSubTabVisible(level, 'clientRoutes')
				? api.listPolicyDevices().catch(() => null)
				: Promise.resolve(null),
		]);

		routerClient = buildRouterClientContext(dns, devices, buildPolicyNameLookup(policies));

		// AWGM: счётчики и интеграции подтягиваем по мере готовности.
		void api
			.getTunnelsAll()
			.then((snap) => {
				const list = snap.tunnels ?? [];
				patchAwgmFromStores({
					awgTotal: list.length,
					awgRunning: list.filter((t) => t.status === 'running').length,
					awgCountsLoaded: true,
				});
			})
			.catch(() => {});

		if (isRoutingSubTabVisible(level, 'dnsRoutes')) {
			void fetch('/api/routing/dns-routes')
				.then(async (res) => {
					if (!res.ok) return;
					const body = await res.json();
					const lists = (body.data ?? []) as { enabled?: boolean }[];
					patchAwgmFromStores({
						dnsRoutesTotal: lists.length,
						dnsRoutesEnabled: lists.filter((l) => l.enabled).length,
						dnsRoutesLoaded: true,
					});
				})
				.catch(() => {});
		}

		if (isRoutingSubTabVisible(level, 'clientRoutes')) {
			void Promise.all([
				api.getDeviceProxyConfig().catch(() => null),
				api.getDeviceProxyRuntime().catch(() => null),
			]).then(([cfg, rt]) => {
				patchAwgmFromStores({
					deviceProxy: cfg,
					deviceProxyRuntime: rt,
				});
			});
		}

		if (level !== 'basic') {
			void api
				.listSubscriptions()
				.then((subs: Subscription[]) => {
					patchAwgmFromStores({
						subscriptionsTotal: subs.length,
						subscriptionsEnabled: subs.filter((s) => s.enabled).length,
						subscriptionsLoaded: true,
					});
				})
				.catch(() => {});
		}

		loadHydraStatus(level);
	}

	async function refresh() {
		refreshing = true;
		browserSnap = collectBrowserSnapshot();
		patchAwgmFromStores();

		try {
			await Promise.all([
				systemInfo.refetch(),
				singboxStatus.refetch(),
				reloadSettings(),
				loadRemoteContext(),
			]);
			patchAwgmFromStores();
		} finally {
			refreshing = false;
		}
	}

	onMount(() => {
		browserSnap = collectBrowserSnapshot();
		patchAwgmFromStores();
		void loadRemoteContext();
	});

	const routerRows = $derived(sysInfo ? routerStaticRows(sysInfo, $usageLevel) : []);
	const browserRows = $derived(browserSnap ? browserSnapshotRows(browserSnap) : []);
	const clientRows = $derived(routerClientRows(routerClient));
	const servicesRows = $derived(awgmSnap ? awgmServicesRows(awgmSnap) : []);

	async function copyReport() {
		const text = buildAboutReportText();
		const ok = await copyToClipboard(text);
		if (ok) {
			notifications.success('Отчёт скопирован в буфер обмена');
		} else {
			notifications.error('Не удалось скопировать');
		}
	}

	function buildAboutReportText(): string {
		const sections = [
			{ title: 'Роутер', rows: routerRows },
			{ title: 'Браузер', rows: browserRows },
			{ title: 'Клиент в сети роутера', rows: clientRows },
			{ title: 'AWGM', rows: servicesRows },
		];
		return formatAboutReport(sections);
	}

</script>

<div class="about-toolbar">
	<Button variant="secondary" size="sm" onclick={() => refresh()} loading={refreshing}>
		Обновить
	</Button>
	<Button variant="ghost" size="sm" onclick={copyReport} disabled={refreshing || !sysInfo}>
		Скопировать данные
	</Button>
</div>

<NdmsPolicyHintBanner {isOS5} />

{#if !sysInfo && $systemInfo.status === 'loading'}
	<p class="about-hint">Загрузка данных о роутере…</p>
{:else if !sysInfo && $systemInfo.status === 'error'}
	<p class="about-hint about-hint-warn">Не удалось загрузить информацию о роутере.</p>
{/if}

<div class="about-grid">
	{#if sysInfo}
		<AboutInfoSection title="Роутер" rows={routerRows} loading={refreshing && !sysInfo} />
	{/if}
	<AboutInfoSection
		title="Клиент в сети роутера"
		rows={clientRows}
		loading={refreshing && routerClient === null}
	/>
	<AboutInfoSection
		title="Браузер"
		rows={browserRows}
		loading={refreshing && !browserSnap}
	/>
	{#if awgmSnap}
		<AboutInfoSection title="AWGM" rows={servicesRows} />
	{/if}
</div>

<style>
	.about-toolbar {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
		margin-bottom: 0.75rem;
	}

	.about-hint {
		margin: 0 0 0.75rem;
		font-size: 0.8125rem;
		color: var(--color-text-muted, var(--text-muted));
	}

	.about-hint-warn {
		color: var(--color-warning, var(--warning));
	}

	.about-grid {
		--about-gap: 0.765rem;
		column-count: 3;
		column-gap: var(--about-gap);
	}

	.about-grid > :global(.about-section) {
		break-inside: avoid;
		margin-bottom: var(--about-gap);
		width: 100%;
	}

	@media (max-width: 1100px) {
		.about-grid {
			column-count: 2;
		}
	}

	@media (max-width: 640px) {
		.about-grid {
			column-count: 1;
		}
	}
</style>
