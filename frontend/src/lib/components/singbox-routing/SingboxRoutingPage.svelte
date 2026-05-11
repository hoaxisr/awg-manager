<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { page } from '$app/stores';
	import { goto } from '$app/navigation';
	import { Tabs, Button } from '$lib/components/ui';
	import { singboxStatus } from '$lib/stores/singbox';
	import JsonConfigDrawer from './JsonConfigDrawer.svelte';
	import RouteInspector from './RouteInspector.svelte';
	import EngineSubTab from './EngineSubTab.svelte';
	import PresetsSubTab from './PresetsSubTab.svelte';
	import RulesSubTab from './RulesSubTab.svelte';
	import RuleSetsSubTab from './RuleSetsSubTab.svelte';
	import OutboundsSubTab from './OutboundsSubTab.svelte';
	import DnsSubTab from './DnsSubTab.svelte';
	import DeviceProxySubTab from './DeviceProxySubTab.svelte';
	import { ConnectionsSubTab } from '$lib/components/routing/singboxRouter';
	import { WizardModal, WizardEntry } from './wizard';
	import { singboxWizard } from '$lib/stores/singboxWizard';
	import { singboxRouter } from '$lib/stores/singboxRouter';
	import { stripAnsi } from '$lib/utils/ansi';
	import { StagingBanner } from '.';

	type SubTab =
		| 'engine'
		| 'presets'
		| 'rules'
		| 'rulesets'
		| 'outbounds'
		| 'dns'
		| 'deviceproxy'
		| 'connections';

	const order: SubTab[] = [
		'engine',
		'presets',
		'rules',
		'rulesets',
		'outbounds',
		'dns',
		'deviceproxy',
		'connections',
	];

	const labels: Record<SubTab, string> = {
		engine: 'Движок',
		presets: 'Пресеты',
		rules: 'Правила',
		rulesets: 'Наборы',
		outbounds: 'Outbounds',
		dns: 'DNS',
		deviceproxy: 'Прокси',
		connections: 'Соединения'
	};

	let active = $state<SubTab>('engine');
	let drawerOpen = $state(false);
	let inspectorOpen = $state(false);

	function readSubFromURL(): SubTab {
		const v = $page.url.searchParams.get('sub');
		return order.includes(v as SubTab) ? (v as SubTab) : 'engine';
	}

	function setSub(next: SubTab) {
		if (next === active) return;
		active = next;
		const sp = new URLSearchParams($page.url.search);
		sp.set('sub', next);
		sp.set('tab', 'singbox');
		goto(`?${sp.toString()}`, { replaceState: true, keepFocus: true, noScroll: true });
	}

	// Subscribe to the cold-tier sing-box status polling store so the
	// header badge reflects real running/version state. The store is
	// shared with the rest of the app — subscribing here just keeps it
	// hot while this page is open. Also kick off router-status reload
	// so the badge has both inputs available on first paint.
	let unsubStatus: (() => void) | undefined;
	onMount(() => {
		unsubStatus = singboxStatus.subscribe(() => {});
		singboxRouter.reloadStatus();
	});
	onDestroy(() => {
		unsubStatus?.();
	});

	$effect(() => {
		active = readSubFromURL();
	});

	const statusState = $derived($singboxStatus);
	const status = $derived(statusState.data);
	const statusReady = $derived(statusState.lastFetchedAt > 0 || statusState.status === 'error');
	const singboxInstalled = $derived(status?.installed ?? false);
	const running = $derived(status?.running ?? false);
	const version = $derived(status?.version ?? status?.currentVersion ?? '—');
	const singboxLastError = $derived(stripAnsi(status?.lastError).trim());

	const routerStatusStore = singboxRouter.status;
	const routerStatus = $derived($routerStatusStore);
	const routerNetfilterReady = $derived(routerStatus?.netfilterAvailable ?? false);
	const routerNetfilterName = $derived(routerStatus?.netfilterComponentName ?? 'Компонент netfilter');
	const routerEnabled = $derived(routerStatus?.enabled ?? false);
	const routerPolicyOK = $derived(routerStatus?.policyExists ?? false);
	const routerIssuesCount = $derived((routerStatus?.issues ?? []).length);

	type HeaderState = 'loading' | 'ready' | 'warn' | 'error';
	const headerState = $derived.by<HeaderState>(() => {
		if (!statusReady) return 'loading';
		if (!singboxInstalled) return 'error';
		if (!running) return 'error';
		if (!routerEnabled) return 'warn';
		if (!routerNetfilterReady || !routerPolicyOK || routerIssuesCount > 0) return 'warn';
		return 'ready';
	});
	const headerLabel = $derived.by(() => {
		if (!statusReady) return 'получение данных…';
		if (!singboxInstalled) return 'не установлен';
		if (!running) return `v${version} · остановлен`;
		if (!routerEnabled) return `v${version} · роутинг выключен`;
		if (!routerPolicyOK) return `v${version} · нет policy`;
		if (!routerNetfilterReady) return `v${version} · нет netfilter`;
		if (routerIssuesCount > 0) {
			const word =
				routerIssuesCount === 1
					? 'проблема'
					: routerIssuesCount < 5
						? 'проблемы'
						: 'проблем';
			return `v${version} · ${routerIssuesCount} ${word}`;
		}
		return `v${version} · готов`;
	});
	const headerReason = $derived.by(() => {
		if (!statusReady) return '';
		if (singboxLastError) return singboxLastError;
		if (!singboxInstalled) return 'Базовый sing-box не установлен.';
		if (!running) return 'Процесс sing-box не запущен.';
		if (!routerEnabled) return 'Модуль маршрутизации выключен.';
		if (!routerPolicyOK) return 'Не настроена/не найдена policy для маршрутизации.';
		if (!routerNetfilterReady) return `Недоступен компонент: ${routerNetfilterName}.`;
		if (routerIssuesCount > 0) return routerStatus?.issues?.[0]?.message ?? '';
		return '';
	});

	const tabsItems = $derived(order.map((id) => ({ id, label: labels[id] })));

	const wizRulesStore = singboxRouter.rules;
	const wizOutboundsStore = singboxRouter.outbounds;
	const wizSettingsStore = singboxRouter.settings;
	// Settings is null until loadAll() (called from sub-tabs onMount) has
	// completed at least once. Gate the WizardEntry on that to avoid a
	// brief flash on initial page paint when stores still hold defaults.
	const isEmpty = $derived(
		$wizSettingsStore !== null &&
		($wizRulesStore?.length ?? 0) === 0 &&
		($wizOutboundsStore?.length ?? 0) === 0 &&
		(!$wizSettingsStore.policyName || $wizSettingsStore.policyName === '')
	);
</script>

<header class="page-header">
	<div class="header-right">
		<span
			class="status-badge"
			class:loading={headerState === 'loading'}
			class:running={headerState === 'ready'}
			class:warn={headerState === 'warn'}
			class:error={headerState === 'error'}
			title={headerReason}
		>
			<span class="status-dot"></span>
			sing-box · {headerLabel}
		</span>
		<Button size="sm" variant="primary" onclick={() => singboxWizard.start()}>Мастер</Button>
		<Button size="sm" variant="ghost" onclick={() => (inspectorOpen = true)}>Инспектор</Button>
		<Button size="sm" variant="ghost" onclick={() => (drawerOpen = true)}>Конфиг</Button>
	</div>
</header>

{#if isEmpty}
	<WizardEntry />
{/if}

<StagingBanner />

<Tabs tabs={tabsItems} active={active} onchange={(id) => setSub(id as SubTab)} />

<section class="sub-content">
	{#if active === 'engine'}
		<EngineSubTab />
	{:else if active === 'presets'}
		<PresetsSubTab />
	{:else if active === 'rules'}
		<RulesSubTab />
	{:else if active === 'rulesets'}
		<RuleSetsSubTab />
	{:else if active === 'outbounds'}
		<OutboundsSubTab />
	{:else if active === 'dns'}
		<DnsSubTab />
	{:else if active === 'deviceproxy'}
		<DeviceProxySubTab />
	{:else if active === 'connections'}
		<ConnectionsSubTab />
	{/if}
</section>

<JsonConfigDrawer open={drawerOpen} onClose={() => (drawerOpen = false)} />
<RouteInspector open={inspectorOpen} onClose={() => (inspectorOpen = false)} />
<WizardModal />

<style>
	.page-header {
		display: flex;
		align-items: center;
		justify-content: flex-end;
		gap: 0.75rem;
		margin-bottom: 0.75rem;
	}
	.header-right {
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}
	.status-badge {
		display: inline-flex;
		align-items: center;
		gap: 0.4rem;
		font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, 'Liberation Mono', monospace;
		font-size: 12px;
		color: var(--color-text-secondary);
	}
	.status-dot {
		width: 7px;
		height: 7px;
		border-radius: 999px;
		background: var(--color-text-muted);
		box-shadow: 0 0 0 3px color-mix(in srgb, var(--color-text-muted) 22%, transparent);
	}
	.status-badge.loading .status-dot {
		background: var(--color-accent, #6ea8ff);
		box-shadow: 0 0 0 3px color-mix(in srgb, var(--color-accent, #6ea8ff) 25%, transparent);
		animation: status-pulse 1.15s ease-in-out infinite;
	}
	.status-badge.running .status-dot {
		background: var(--color-success);
		box-shadow: 0 0 0 3px color-mix(in srgb, var(--color-success) 28%, transparent);
	}
	.status-badge.warn .status-dot {
		background: var(--color-warning);
		box-shadow: 0 0 0 3px color-mix(in srgb, var(--color-warning) 28%, transparent);
	}
	.status-badge.error .status-dot {
		background: var(--color-error);
		box-shadow: 0 0 0 3px color-mix(in srgb, var(--color-error) 28%, transparent);
	}
	@keyframes status-pulse {
		0%, 100% { opacity: 0.55; }
		50% { opacity: 1; }
	}
	.sub-content {
		margin-top: 1rem;
	}
</style>
