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
	<div class="routing">
		<label class="field">
			<span class="field-label">Маршрут тестов</span>
			<select
				class="select"
				bind:value={routeMode}
				disabled={disabled}
			>
				<option value="direct">Через текущий маршрут по умолчанию</option>
				<option value="tunnel">Через выбранный туннель</option>
			</select>
		</label>

		{#if routeMode === 'tunnel'}
			<label class="field">
				<span class="field-label">Туннель для тестов</span>
				<select
					class="select"
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
			</label>
		{/if}
	</div>

	<div class="buttons">
		<button
			class="btn btn-secondary"
			onclick={() => onStart('quick', false, routeMode, routeTunnelId)}
			disabled={startDisabled}
		>
			Быстрый тест
		</button>
		<button
			class="btn btn-secondary"
			onclick={() => onStart('quick', true, routeMode, routeTunnelId)}
			disabled={startDisabled}
		>
			Полная диагностика
		</button>
		<button
			class="btn btn-primary"
			onclick={() => onStart('full', true, routeMode, routeTunnelId)}
			disabled={startDisabled}
		>
			Сформировать отчёт
		</button>
	</div>

	<p class="controls-hint">
		Полная диагностика включает тест перезапуска туннелей. Соединение будет прервано на ~15 сек.
		Отчёт собирает полную информацию о системе, туннелях и маршрутах в JSON-файл.
	</p>
</div>

<style>
	.controls {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}

	.routing {
		display: grid;
		gap: 10px;
		grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
	}

	.field {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}

	.field-label {
		font-size: 13px;
		color: var(--text-secondary);
	}

	.select {
		height: 36px;
		padding: 0 10px;
		border-radius: 8px;
		border: 1px solid var(--border-primary);
		background: var(--bg-elevated);
		color: var(--text-primary);
	}

	.buttons {
		display: flex;
		flex-wrap: wrap;
		gap: 8px;
	}

	.controls-hint {
		color: var(--text-tertiary);
		font-size: 13px;
		line-height: 1.4;
	}
</style>
