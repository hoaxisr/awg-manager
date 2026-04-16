<script lang="ts">
	import type { DiagMode, DiagRouteMode, TunnelListItem } from '$lib/types';

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
</script>

<div class="controls">
	<div class="controls-bar">
		<select
			class="controls-select"
			bind:value={routeMode}
			disabled={disabled}
		>
			<option value="direct">Через текущий маршрут по умолчанию</option>
			<option value="tunnel">Через выбранный туннель</option>
		</select>

		{#if routeMode === 'tunnel'}
			<select
				class="controls-select"
				bind:value={routeTunnelId}
				disabled={disabled || runningTunnels.length === 0}
			>
				{#if runningTunnels.length === 0}
					<option value="">Нет запущенных туннелей</option>
				{:else}
					{#each runningTunnels as t}
						<option value={t.id}>{t.name} ({t.interfaceName ?? t.id})</option>
					{/each}
				{/if}
			</select>
		{/if}

		<div class="controls-buttons">
			<button
				class="btn btn-secondary"
				onclick={() => onStart('quick', false, routeMode, routeTunnelId)}
				disabled={startDisabled}
			>
				Быстрый тест
			</button>
			<button
				class="btn btn-primary"
				onclick={() => onStart('quick', true, routeMode, routeTunnelId)}
				disabled={startDisabled}
			>
				Полная диагностика
			</button>
			<button
				class="btn btn-ghost"
				onclick={() => onStart('full', true, routeMode, routeTunnelId)}
				disabled={startDisabled}
			>
				Сформировать отчёт
			</button>
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
		padding: 0.375rem 0.625rem;
		border: 1px solid var(--border);
		border-radius: 6px;
		background: var(--bg-primary);
		color: var(--text-primary);
		font-size: 0.8125rem;
		font-family: inherit;
		width: auto;
		max-width: 280px;
	}

	.controls-select:focus {
		outline: none;
		border-color: var(--accent);
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
