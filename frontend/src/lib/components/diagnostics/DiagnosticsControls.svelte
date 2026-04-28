<script lang="ts">
	import type { DiagMode, DiagRouteMode, TunnelListItem } from '$lib/types';
	import { Button, Dropdown, type DropdownOption } from '$lib/components/ui';

	interface Props {
		onStart: (mode: DiagMode, restart: boolean, routeMode: DiagRouteMode, routeTunnelId: string) => void;
		disabled: boolean;
		tunnels: TunnelListItem[];
	}

	let { onStart, disabled, tunnels }: Props = $props();
	let routeMode = $state<DiagRouteMode>('direct');
	let routeTunnelId = $state('');

	const runningTunnels = $derived(tunnels.filter((t) => t.status === 'running'));

	$effect(() => {
		if (routeMode === 'tunnel' && !routeTunnelId && runningTunnels.length > 0) {
			routeTunnelId = runningTunnels[0].id;
		}
	});

	const startDisabled = $derived(disabled || (routeMode === 'tunnel' && !routeTunnelId));

	const routeOptions: DropdownOption<DiagRouteMode>[] = [
		{ value: 'direct', label: 'Через текущий маршрут по умолчанию' },
		{ value: 'tunnel', label: 'Через выбранный туннель' },
	];
	const tunnelOptions = $derived<DropdownOption[]>(
		runningTunnels.length === 0
			? [{ value: '', label: 'Нет запущенных туннелей', disabled: true }]
			: runningTunnels.map((t) => ({ value: t.id, label: `${t.name} (${t.interfaceName ?? t.id})` })),
	);
</script>

<div class="controls">
	<div class="controls-bar">
		<div class="controls-select">
			<Dropdown
				bind:value={routeMode}
				options={routeOptions}
				disabled={disabled}
				fullWidth
			/>
		</div>

		{#if routeMode === 'tunnel'}
			<div class="controls-select">
				<Dropdown
					bind:value={routeTunnelId}
					options={tunnelOptions}
					disabled={disabled || runningTunnels.length === 0}
					fullWidth
				/>
			</div>
		{/if}

		<div class="controls-buttons">
			<Button
				variant="secondary"
				size="md"
				onclick={() => onStart('quick', false, routeMode, routeTunnelId)}
				disabled={startDisabled}
			>
				Быстрый тест
			</Button>
			<Button
				variant="primary"
				size="md"
				onclick={() => onStart('quick', true, routeMode, routeTunnelId)}
				disabled={startDisabled}
			>
				Полная диагностика
			</Button>
			<Button
				variant="ghost"
				size="md"
				onclick={() => onStart('full', true, routeMode, routeTunnelId)}
				disabled={startDisabled}
			>
				Сформировать отчёт
			</Button>
		</div>
	</div>

	<p class="controls-hint">
		Полная диагностика включает тест перезапуска туннелей. Соединение будет прервано на ~15 сек.
		Отчёт собирает полную информацию о системе, туннелях и маршрутах в JSON-файл.
	</p>
</div>

<style>
	.controls {
		background: var(--bg-secondary, var(--bg-card));
		border: 1px solid var(--border);
		border-radius: var(--radius, 8px);
		padding: 1rem;
	}

	.controls-bar {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		flex-wrap: wrap;
	}

	.controls-select {
		width: auto;
		max-width: 280px;
		flex: 0 1 280px;
	}

	.controls-buttons {
		display: flex;
		gap: 0.5rem;
		margin-left: auto;
	}

	.controls-hint {
		color: var(--text-muted);
		font-size: 0.75rem;
		line-height: 1.5;
		margin-top: 0.75rem;
	}

	@media (max-width: 640px) {
		.controls-bar {
			flex-direction: column;
			align-items: stretch;
		}

		.controls-select {
			width: 100%;
			max-width: none;
		}

		.controls-buttons {
			margin-left: 0;
			flex-direction: column;
			width: 100%;
		}

		.controls-buttons :global(.btn) {
			width: 100%;
		}
	}
</style>
