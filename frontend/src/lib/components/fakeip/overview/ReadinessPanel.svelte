<!--
  Составной индикатор готовности FakeIP (FE-spec §12.2). НЕ один тумблер: список
  строк «метка + точка состояния + короткий текст». Три строки — живые сигналы
  бэкенда, две (DNS .2 / Egress) бэкендом пока не сообщаются и честно помечены
  «не сообщается движком» (§2: зелёное не выдумываем).

  Презентационный компонент: получает уже разрешённые значения пропами, своих
  подписок/запросов не делает.
-->
<script lang="ts">
	import { Card } from '$lib/components/ui';
	import { readinessRows, type ReadinessState } from './readinessRows';

	interface Props {
		/** sing-box процесс запущен ($singboxStatus.data?.running). */
		running: boolean;
		/** NDMS auto-маршрут пула присутствует (SingboxRouterStatus.active). */
		active: boolean;
		/** Сохранение source (SingboxRouterStatus.sourcePreserved), tri-state. */
		sourcePreserved?: boolean;
	}

	let { running, active, sourcePreserved }: Props = $props();

	const rows = $derived(readinessRows({ running, active, sourcePreserved }));

	// Mapping state → CSS class. Verified tokens: --color-success / --color-warning
	// / --color-error exist in app.css; muted dot uses --text-muted.
	function dotClass(state: ReadinessState): string {
		return `dot dot-${state}`;
	}
</script>

<Card padding="md">
	{#snippet header()}
		<h3 class="panel-title">Готовность</h3>
	{/snippet}

	<ul class="rows">
		{#each rows as row (row.key)}
			<li class="row">
				<span class={dotClass(row.state)} aria-hidden="true"></span>
				<span class="row-label">{row.label}</span>
				<span class="row-text" class:muted={row.state === 'unknown'}>{row.text}</span>
			</li>
		{/each}
	</ul>
</Card>

<style>
	.panel-title {
		margin: 0;
		font-size: 0.9375rem;
		font-weight: 600;
		color: var(--text-primary);
	}

	.rows {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: 0.625rem;
	}

	.row {
		display: grid;
		grid-template-columns: auto minmax(8rem, max-content) 1fr;
		align-items: center;
		gap: 0.625rem;
		font-size: 0.875rem;
	}

	.dot {
		width: 0.625rem;
		height: 0.625rem;
		border-radius: 50%;
		flex-shrink: 0;
	}

	.dot-ok {
		background: var(--color-success);
	}

	.dot-warn {
		background: var(--color-warning);
	}

	.dot-error {
		background: var(--color-error);
	}

	.dot-unknown {
		background: var(--text-muted);
		opacity: 0.6;
	}

	.row-label {
		color: var(--text-primary);
		font-weight: 500;
	}

	.row-text {
		color: var(--text-secondary);
	}

	.row-text.muted {
		color: var(--text-muted);
		font-style: italic;
	}
</style>
