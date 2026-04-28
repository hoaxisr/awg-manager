<script lang="ts">
	import { onDestroy } from 'svelte';
	import { Button, Card, Dropdown, Toggle } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { diagnosticsStore } from '$lib/stores/diagnostics';
	import type { TunnelListItem, DiagMode, DiagRouteMode } from '$lib/types';

	interface Props {
		tunnels: TunnelListItem[];
	}

	let { tunnels }: Props = $props();

	let mode = $state<DiagMode>('quick');
	let includeRestart = $state(false);
	let routeMode = $state<DiagRouteMode>('direct');
	let routeTunnelId = $state('');

	let eventSource: EventSource | null = null;

	const modeOptions = [
		{ value: 'quick', label: 'Быстрая' },
		{ value: 'full', label: 'Полная' },
	];

	const routeOptions = [
		{ value: 'direct', label: 'Прямая' },
		{ value: 'tunnel', label: 'Через туннель' },
	];

	const tunnelOptions = $derived(tunnels.map((t) => ({ value: t.id, label: t.name })));

	function start() {
		diagnosticsStore.start();
		cleanup();

		eventSource = api.streamDiagnostics(
			mode,
			includeRestart,
			routeMode,
			routeTunnelId,
			(event) => {
				switch (event.type) {
					case 'phase':
						diagnosticsStore.setPhase(event.label ?? '');
						break;
					case 'test':
						if (event.test) diagnosticsStore.addTest(event.test);
						break;
					case 'done':
						if (event.summary) diagnosticsStore.finish(event.summary);
						cleanup();
						break;
					case 'error':
						diagnosticsStore.fail(event.message ?? 'Ошибка диагностики');
						cleanup();
						break;
				}
			},
			() => diagnosticsStore.fail('Соединение потеряно'),
		);
	}

	function cleanup() {
		if (eventSource) {
			eventSource.close();
			eventSource = null;
		}
	}

	onDestroy(cleanup);

	const running = $derived($diagnosticsStore.running);
	const hasReport = $derived(!!$diagnosticsStore.summary);
	let downloadingReport = $state(false);

	async function downloadReport() {
		downloadingReport = true;
		try {
			await api.downloadDiagnosticsReport();
		} catch (e) {
			diagnosticsStore.fail((e as Error).message);
		} finally {
			downloadingReport = false;
		}
	}
</script>

<Card variant="nested" padding="md">
	{#snippet header()}<strong>Проверки системы</strong>{/snippet}

	<div class="form">
		<Dropdown bind:value={mode} options={modeOptions} label="Режим" fullWidth />

		{#if mode === 'full'}
			<div class="restart-block">
				<Toggle
					checked={includeRestart}
					onchange={(v) => (includeRestart = v)}
					label="Включая restart-цикл"
					hint="Перезапустит туннели на 2-5 сек каждый"
				/>
			</div>
		{/if}

		<Dropdown bind:value={routeMode} options={routeOptions} label="Маршрут" fullWidth />

		{#if routeMode === 'tunnel'}
			<Dropdown
				bind:value={routeTunnelId}
				options={tunnelOptions}
				label="Туннель"
				placeholder="— выбрать —"
				fullWidth
			/>
		{/if}

		<Button
			variant="primary"
			fullWidth
			onclick={start}
			loading={running}
			disabled={routeMode === 'tunnel' && !routeTunnelId}
		>
			{running ? $diagnosticsStore.currentPhase || 'Запуск...' : 'Запустить проверки'}
		</Button>

		{#if hasReport && !running}
			<Button
				variant="secondary"
				fullWidth
				onclick={downloadReport}
				loading={downloadingReport}
			>
				Сформировать отчёт
			</Button>
		{/if}

		{#if $diagnosticsStore.errorMessage}
			<div class="error">{$diagnosticsStore.errorMessage}</div>
		{/if}
	</div>
</Card>

<style>
	.form {
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
	}

	.restart-block {
		padding: 0.5rem 0;
	}

	.error {
		margin-top: 0.5rem;
		color: var(--color-error);
		font-size: 12px;
	}
</style>
