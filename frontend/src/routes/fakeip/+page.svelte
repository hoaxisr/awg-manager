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
		deriveFakeIPEngineState,
	} from '$lib/components/fakeip';
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
	const routingMode = $derived($settings?.routingMode);
	const running = $derived($singboxStatus.data?.running ?? false);

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
	const fromMode = $derived<'off' | 'tproxy' | 'fakeip-tun'>(
		$settings?.enabled ? ($settings?.routingMode ?? 'tproxy') : 'off',
	);

	function handleEnableRequested(): void {
		confirmOpen = true;
	}

	function handleCancelSwitch(): void {
		confirmOpen = false;
	}

	async function handleConfirmSwitch(): Promise<void> {
		// TODO(1E.6): replace await+notify with SwitchProgress SSE stream.
		switchBusy = true;
		try {
			await api.singboxRouterSwitchMode('fakeip-tun');
			confirmOpen = false;
			// Refresh stores so the engine-state derivation re-renders the page off
			// NotEnabledScreen once routingMode/status reflect the new mode.
			await singboxRouter.loadAll();
			notifications.success('Режим FakeIP включён');
		} catch (e) {
			notifications.error(
				e instanceof Error ? e.message : 'Не удалось переключить режим',
			);
		} finally {
			switchBusy = false;
		}
	}
</script>

<PageContainer>
	<PageHeader title="FakeIP" description="Режим маршрутизации fakeip-tun" />

	{#if engineState === 'not-fakeip'}
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
</PageContainer>

<ConfirmSwitch
	open={confirmOpen}
	from={fromMode}
	to="fakeip-tun"
	busy={switchBusy}
	onConfirm={handleConfirmSwitch}
	onCancel={handleCancelSwitch}
/>

<style>
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
