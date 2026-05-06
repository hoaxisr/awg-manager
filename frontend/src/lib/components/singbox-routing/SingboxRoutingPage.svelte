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
	import { notifications } from '$lib/stores/notifications';
	import { api } from '$lib/api/client';

	type ViewMode = 'simple' | 'advanced';
	type SubTab =
		| 'engine'
		| 'presets'
		| 'rules'
		| 'rulesets'
		| 'outbounds'
		| 'dns'
		| 'deviceproxy'
		| 'connections';

	const advancedOrder: SubTab[] = [
		'engine',
		'presets',
		'rules',
		'rulesets',
		'outbounds',
		'dns',
		'deviceproxy',
		'connections',
	];
	const simpleOrder: SubTab[] = ['engine', 'presets', 'rulesets', 'rules', 'outbounds', 'dns'];

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

	let mode = $state<ViewMode>('simple');
	let active = $state<SubTab>('engine');
	let drawerOpen = $state(false);
	let inspectorOpen = $state(false);
	let enabling = $state(false);

	function readModeFromURL(): ViewMode {
		const value = $page.url.searchParams.get('mode');
		return value === 'advanced' ? 'advanced' : 'simple';
	}

	function readSubFromURL(): SubTab {
		const v = $page.url.searchParams.get('sub');
		const allowed = mode === 'advanced' ? advancedOrder : simpleOrder;
		return allowed.includes(v as SubTab) ? (v as SubTab) : 'engine';
	}

	function setMode(next: ViewMode) {
		if (next === mode) return;
		mode = next;
		const sp = new URLSearchParams($page.url.search);
		sp.set('mode', next);
		sp.set('tab', 'singbox');
		if (next === 'simple') {
			sp.delete('sub');
		} else {
			sp.set('sub', active);
		}
		goto(`?${sp.toString()}`, { replaceState: true, keepFocus: true, noScroll: true });
	}

	function setSub(next: SubTab) {
		if (next === active) return;
		active = next;
		const sp = new URLSearchParams($page.url.search);
		sp.set('sub', next);
		sp.set('tab', 'singbox');
		sp.set('mode', mode);
		goto(`?${sp.toString()}`, { replaceState: true, keepFocus: true, noScroll: true });
	}

	// Subscribe to the cold-tier sing-box status polling store so the
	// header badge reflects real running/version state. The store is
	// shared with the rest of the app — subscribing here just keeps it
	// hot while this page is open.
	let unsubStatus: (() => void) | undefined;
	onMount(() => {
		unsubStatus = singboxStatus.subscribe(() => {});
		singboxRouter.reloadStatus();
	});
	onDestroy(() => {
		unsubStatus?.();
	});

	$effect(() => {
		mode = readModeFromURL();
		active = readSubFromURL();
	});

	const statusState = $derived($singboxStatus);
	const status = $derived(statusState.data);
	const statusReady = $derived(statusState.lastFetchedAt > 0 || statusState.status === 'error');
	const running = $derived(status?.running ?? false);
	const version = $derived(status?.version ?? '—');
	const statusLabel = $derived(
		!statusReady ? 'получение данных…' : running ? `v${version}` : 'остановлен',
	);
	const routerStatusStore = singboxRouter.status;
	const routerStatus = $derived($routerStatusStore);
	const routerInstalled = $derived(routerStatus?.installed ?? false);
	const routerNetfilterReady = $derived(routerStatus?.netfilterAvailable ?? false);
	const routerNetfilterName = $derived(routerStatus?.netfilterComponentName ?? 'Компонент netfilter');
	const tabsItems = $derived(
		(mode === 'advanced' ? advancedOrder : simpleOrder).map((id) => ({ id, label: labels[id] })),
	);

	const wizRulesStore = singboxRouter.rules;
	const wizOutboundsStore = singboxRouter.outbounds;
	const wizSettingsStore = singboxRouter.settings;
	const isEmpty = $derived(
		($wizRulesStore?.length ?? 0) === 0 &&
		($wizOutboundsStore?.length ?? 0) === 0 &&
		(!$wizSettingsStore?.policyName || $wizSettingsStore.policyName === '')
	);

	async function ensureRouterEnabled() {
		if (enabling) return;
		enabling = true;
		try {
			await api.singboxRouterEnable();
			await singboxRouter.reloadStatus();
			notifications.success('Sing-box маршрутизация включена');
		} catch (e) {
			const message = e instanceof Error ? e.message : String(e);
			notifications.error(`Не удалось включить Sing-box маршрутизацию: ${message}`);
		} finally {
			enabling = false;
		}
	}
</script>

<header class="page-header">
	<div class="header-left">
		<div class="mode-switch" role="tablist" aria-label="Режим sing-box маршрутизации">
			<button
				type="button"
				role="tab"
				class:active={mode === 'simple'}
				aria-selected={mode === 'simple'}
				onclick={() => setMode('simple')}
			>
				Простой режим
			</button>
			<button
				type="button"
				role="tab"
				class:active={mode === 'advanced'}
				aria-selected={mode === 'advanced'}
				onclick={() => setMode('advanced')}
			>
				Advanced
			</button>
		</div>
	</div>
	<div class="header-right">
		<span class="status-badge" class:running={statusReady && running}>
			<span class="status-dot"></span>
			sing-box · {statusLabel}
		</span>
		<Button size="sm" variant="primary" onclick={() => singboxWizard.start()}>Мастер</Button>
		<Button size="sm" variant="ghost" onclick={() => (inspectorOpen = true)}>Инспектор</Button>
		<Button size="sm" variant="ghost" onclick={() => (drawerOpen = true)}>Конфиг</Button>
	</div>
</header>

{#if isEmpty}
	<WizardEntry />
{/if}

{#if mode === 'simple'}
	<section class="simple-note">
		<div class="note-title">Рекомендуем для России</div>
		<div class="note-text">
			Начните с пресетов Telegram / YouTube / OpenAI / GitHub, затем уточняйте через rule-set и DNS.
		</div>
	</section>
{/if}

{#if !routerInstalled && mode === 'simple'}
	<section class="router-onboarding">
		<h3>Sing-box маршрутизация ещё не активирована</h3>
		<p>
			Базовый sing-box установлен, но модуль маршрутизации пока не инициализирован.
			Нажми кнопку ниже, чтобы подготовить policy/netfilter и открыть управление правилами.
		</p>
		{#if !routerNetfilterReady}
			<p class="warn">
				Проверь установку компонента: <strong>{routerNetfilterName}</strong>.
			</p>
		{/if}
		<Button variant="primary" size="sm" onclick={ensureRouterEnabled} loading={enabling} disabled={enabling}>
			Включить маршрутизацию Sing-box
		</Button>
	</section>
{:else}
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
{/if}

<JsonConfigDrawer open={drawerOpen} onClose={() => (drawerOpen = false)} />
<RouteInspector open={inspectorOpen} onClose={() => (inspectorOpen = false)} />
<WizardModal />

<style>
	.page-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.75rem;
		margin-bottom: 0.75rem;
		flex-wrap: wrap;
	}
	.header-left {
		display: flex;
		align-items: center;
	}
	.header-right {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex-wrap: wrap;
		justify-content: flex-end;
	}
	.mode-switch {
		display: inline-flex;
		border: 1px solid var(--color-border);
		border-radius: 999px;
		overflow: hidden;
		background: var(--color-bg-secondary);
	}
	.mode-switch button {
		border: none;
		background: transparent;
		color: var(--color-text-muted);
		padding: 0.45rem 0.9rem;
		font-size: 0.85rem;
		cursor: pointer;
	}
	.mode-switch button.active {
		background: color-mix(in srgb, var(--color-accent, #5b8cff) 18%, transparent);
		color: var(--color-text-primary);
		font-weight: 600;
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
		background: var(--color-error);
		box-shadow: 0 0 0 3px color-mix(in srgb, var(--color-error) 22%, transparent);
	}
	.status-badge.running .status-dot {
		background: var(--color-success);
		box-shadow: 0 0 0 3px color-mix(in srgb, var(--color-success) 28%, transparent);
	}
	.sub-content {
		margin-top: 1rem;
	}
	.simple-note {
		margin: 0.2rem 0 0.8rem;
		padding: 0.65rem 0.8rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		background: var(--color-bg-secondary);
	}
	.note-title {
		font-size: 0.75rem;
		text-transform: uppercase;
		letter-spacing: 0.06em;
		color: var(--color-success, #22c55e);
		margin-bottom: 0.2rem;
		font-weight: 700;
	}
	.note-text {
		font-size: 0.86rem;
		color: var(--color-text-secondary);
	}
	.router-onboarding {
		margin-top: 0.9rem;
		padding: 1rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		background: var(--color-bg-secondary);
		display: grid;
		gap: 0.65rem;
		max-width: 760px;
	}
	.router-onboarding h3 {
		margin: 0;
		font-size: 1rem;
		font-weight: 600;
		color: var(--color-text-primary);
	}
	.router-onboarding p {
		margin: 0;
		font-size: 0.9rem;
		color: var(--color-text-secondary);
	}
	.router-onboarding .warn {
		color: var(--color-warning, #f59e0b);
	}
</style>
