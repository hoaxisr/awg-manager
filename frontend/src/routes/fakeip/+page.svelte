<script lang="ts">
	import { onMount } from 'svelte';
	import { get } from 'svelte/store';
	import { PageContainer, PageHeader } from '$lib/components/layout';
	import { Tabs } from '$lib/components/ui';
	import { singboxRouter } from '$lib/stores/singboxRouter';
	import { singboxStatus } from '$lib/stores/singbox';
	import {
		NotEnabledScreen,
		ConfirmSwitch,
		SwitchProgress,
		ReadinessPanel,
		EngineSettingsCard,
		SegmentsDelivery,
		deriveFakeIPEngineState,
	} from '$lib/components/fakeip';
	import { fakeipTransition } from '$lib/stores/fakeipTransition';
	import { notifications } from '$lib/stores/notifications';
	import { api } from '$lib/api/client';

	onMount(() => {
		// routingMode comes from the router SETTINGS, which are only fetched by
		// loadAll(). On direct navigation to /fakeip the store may still be cold
		// (settings === null → routingMode undefined → 'not-fakeip'), so prime it
		// once. Idempotent; refreshes status too.
		if (!get(singboxRouter.initialized)) void singboxRouter.loadAll();
	});

	// FE-spec §3: fixed order + labels of the 9 FakeIP sub-pages. Badges are
	// intentionally omitted here — real chip counters arrive in task 11.2.
	// `live` marks chips whose content depends on the running engine / Clash
	// runtime (FE-spec §12.1): they show the "движок остановлен" empty-state or
	// a clash-down banner. Config-oriented chips render regardless of state.
	const CHIPS: { id: string; label: string; live: boolean }[] = [
		{ id: 'overview', label: 'Обзор', live: false },
		{ id: 'inbounds', label: 'Inbounds', live: false },
		{ id: 'outbounds', label: 'Outbounds', live: false },
		{ id: 'rulesets', label: 'Rule sets', live: false },
		{ id: 'dns', label: 'DNS', live: false },
		{ id: 'routes', label: 'Маршруты', live: false },
		{ id: 'devices', label: 'Устройства', live: false },
		{ id: 'connections', label: 'Соединения', live: true },
		{ id: 'logs', label: 'Журнал', live: true }
	];

	let activeTab = $state('overview');

	let activeChip = $derived(CHIPS.find((c) => c.id === activeTab) ?? CHIPS[0]);

	// singboxRouter is a composite store: `settings` and `status` are exposed as
	// separate sub-stores, not a single subscribe value. routingMode lives in
	// SETTINGS, not status (verified against backend). Absent on legacy payloads
	// → 'tproxy' default, handled inside the pure helper.
	const settings = singboxRouter.settings;
	const status = singboxRouter.status;
	const routingMode = $derived($settings?.routingMode);
	const running = $derived($singboxStatus.data?.running ?? false);

	// Composite-readiness live signals (1E.7 / FE-spec §12.2). `active` == pool
	// auto-route present for fakeip-tun; `sourcePreserved` is the tri-state SNAT
	// verdict (undefined → не определено). Both honest, from the Status DTO.
	const routerActive = $derived($status?.active ?? false);
	const sourcePreserved = $derived($status?.sourcePreserved);

	// Engine toggle ON-state: persisted fakeip-tun AND enabled. Drives the card
	// toggle; flipping it routes through the existing ConfirmSwitch flow.
	const engineOn = $derived(
		$settings?.enabled === true && $settings?.routingMode === 'fakeip-tun',
	);

	// TODO(1E.7/slice3): derive from live-block fetch errors. Live blocks are
	// still stubs, so there is no robust Clash-reachability signal yet — assume
	// reachable rather than fabricate a probe.
	const clashReachable = true;

	const engineState = $derived(
		deriveFakeIPEngineState({ routingMode, running, clashReachable }),
	);

	// ConfirmSwitch state. `fromMode` is the honest current mode: when the engine
	// is disabled the source is 'off', otherwise the persisted routingMode (legacy
	// payloads without routingMode default to 'tproxy', mirroring engineState).
	let confirmOpen = $state(false);
	let switchBusy = $state(false);
	let progressOpen = $state(false);
	// Target mode of the pending switch. The NotEnabledScreen CTA and the engine
	// toggle both feed this: CTA → 'fakeip-tun', toggle-off → 'off'. Defaults to
	// 'fakeip-tun' (the enable direction).
	let toMode = $state<'off' | 'tproxy' | 'fakeip-tun'>('fakeip-tun');
	const fromMode = $derived<'off' | 'tproxy' | 'fakeip-tun'>(
		$settings?.enabled ? ($settings?.routingMode ?? 'tproxy') : 'off',
	);

	// Live progress stream (1E.6). The store is fed by the SSE handler wired in
	// +layout.svelte (`onSingboxRouterTransition`); the page only opens the modal,
	// kicks the POST and renders the accumulated steps.
	const transition = fakeipTransition;

	function handleEnableRequested(): void {
		toMode = 'fakeip-tun';
		confirmOpen = true;
	}

	// Engine-card toggle: ON→OFF requests a switch to 'off'; OFF→ON re-enables
	// fakeip-tun. Both open ConfirmSwitch (direction-aware copy already in 1E.5).
	function handleToggleEngine(turnOn: boolean): void {
		toMode = turnOn ? 'fakeip-tun' : 'off';
		confirmOpen = true;
	}

	function handleCancelSwitch(): void {
		confirmOpen = false;
	}

	async function handleRestart(): Promise<void> {
		try {
			await api.singboxControl('restart');
			notifications.success('Перезапуск sing-box запущен');
		} catch (e) {
			const msg = e instanceof Error ? e.message : 'Не удалось перезапустить sing-box';
			notifications.error(msg);
		}
	}

	async function handleConfirmSwitch(): Promise<void> {
		// Open the live progress screen immediately (before the first SSE event)
		// and hand off to the SSE stream. The POST drives the backend; events fold
		// into the store and render step-by-step during the await.
		switchBusy = true;
		fakeipTransition.begin(fromMode, toMode);
		confirmOpen = false;
		progressOpen = true;
		try {
			await api.singboxRouterSwitchMode(toMode);
			// On resolve the store is already (or imminently) `done` via SSE; the
			// modal renders the success/rollback summary. No notify on success —
			// the modal is the primary signal.
		} catch (e) {
			// Network/POST failure: surface it into the progress view so the modal
			// never spins forever, plus a secondary toast.
			const msg = e instanceof Error ? e.message : 'Не удалось переключить режим';
			fakeipTransition.fail(msg);
			notifications.error(msg);
		} finally {
			switchBusy = false;
		}
	}

	async function handleProgressClose(): Promise<void> {
		progressOpen = false;
		fakeipTransition.reset();
		// Re-derive engine state off the new mode (leaves NotEnabledScreen on
		// success; on rollback the page stays where it was).
		await singboxRouter.loadAll();
	}
</script>

<PageContainer>
	<PageHeader title="FakeIP" description="Режим маршрутизации fakeip-tun" />

	<!--
		B3 flicker guard (1D.4 handoff): while a switch is in flight, intermediate
		`singbox-router:status` SSE events re-derive `engineState` and would thrash
		the underlying NotEnabledScreen/chip content beneath the modal. Freeze the
		main content render until the progress modal is closed.
	-->
	{#if progressOpen}
		<!-- main content frozen behind SwitchProgress -->
	{:else if engineState === 'not-fakeip'}
		<NotEnabledScreen onEnableRequested={handleEnableRequested} />
	{:else}
		<!--
			Hero slot (FE-spec): каждая под-страница показывает hero из config.json
			+ «Инспектор маршрутов». Это реальные компоненты из более поздних задач
			Slice 1E+ — здесь оставляем пустой слот, не строим фейковый просмотрщик.
		-->

		<Tabs
			tabs={CHIPS}
			active={activeTab}
			onchange={(id) => (activeTab = id)}
			urlParam="chip"
			defaultTab="overview"
		/>

		{#if activeTab === 'overview'}
			<!--
				Обзор (1E.7 MVP): составной readiness (3 живых сигнала + 2 deferred)
				+ карта движка. Полный дашборд (hero/live-traffic/active-selects)
				отложен в Slice 3+/2.2.
			-->
			<section class="overview">
				<ReadinessPanel running={running} active={routerActive} {sourcePreserved} />
				<EngineSettingsCard
					{engineOn}
					wanAutoDetect={$settings?.wanAutoDetect ?? true}
					wanInterface={$settings?.wanInterface}
					snifferEnabled={$settings?.snifferEnabled ?? false}
					toggleBusy={switchBusy}
					onToggleEngine={handleToggleEngine}
					onRestart={handleRestart}
				/>
				<SegmentsDelivery />
			</section>
		{:else}
			<section class="chip-stub">
				<h2 class="chip-stub-title">{activeChip.label}</h2>

				{#if activeChip.live && engineState === 'stopped'}
					<p class="chip-stub-note chip-stub-empty">
						Движок остановлен — живые данные недоступны.
					</p>
				{:else if activeChip.live && engineState === 'clash-down'}
					<p class="chip-stub-note chip-stub-error">
						Clash-runtime недоступен — живые блоки временно не работают.
						Конфигурация по-прежнему доступна.
					</p>
				{:else}
					<p class="chip-stub-note">Раздел в разработке (Slice 1E+)</p>
				{/if}
			</section>
		{/if}
	{/if}
</PageContainer>

<ConfirmSwitch
	open={confirmOpen}
	from={fromMode}
	to={toMode}
	busy={switchBusy}
	onConfirm={handleConfirmSwitch}
	onCancel={handleCancelSwitch}
/>

<SwitchProgress open={progressOpen} state={$transition} onClose={handleProgressClose} />

<style>
	.overview {
		display: grid;
		grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
		gap: 1rem;
		align-items: start;
	}

	@media (max-width: 720px) {
		.overview {
			grid-template-columns: 1fr;
		}
	}

	.chip-stub {
		padding: 2rem;
		border: 1px dashed var(--border);
		border-radius: var(--radius);
		text-align: center;
	}

	.chip-stub-title {
		margin: 0 0 0.5rem;
		font-size: 1rem;
		font-weight: 600;
		color: var(--text-primary);
	}

	.chip-stub-note {
		margin: 0;
		font-size: 0.875rem;
		color: var(--text-muted);
	}

	.chip-stub-error {
		color: var(--color-error, var(--text-primary));
	}
</style>
