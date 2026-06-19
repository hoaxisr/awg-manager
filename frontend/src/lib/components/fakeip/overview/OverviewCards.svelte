<!--
  Верхний ряд «Обзора» по мокапу dash3 (`.oprow`): три карточки в одной панели
  с тонкими разделителями — движок · память sing-box · активные выборы
  (composite). Без cfg-бейджей: зелёная точка на карточке движка — обычный
  status-индикатор.

    1. Движок — «● <статус>» из engineState + sub «gvisor» (стек честно; индекс
       opkgtun в DTO отсутствует, не выдумываем).
    2. Память sing-box — процессная RSS в байтах из поля `memory` Clash
       /connections WebSocket (singbox:memory SSE); 0 пока движок не запущен
       или первый снимок ещё не пришёл.
    3. Активные выборы (composite) — для каждого selector/urltest/loadbalance
       активный участник (resolveCompositeOutboundView через activeComposites).
       Живой блок: вне live — честный empty-state.

  Презентационный: значения приходят пропами, своих подписок нет.
-->
<script lang="ts">
	import { formatBytes } from '$lib/utils/format';
	import type { ActiveCompositeRow } from './activeComposites';

	interface Props {
		/** Дериватив состояния движка (для текста статуса/точки). */
		engineLive: boolean;
		/** Текст статуса движка (работает / остановлен / clash ↯). */
		engineLabel: string;
		/** Память процесса sing-box в байтах (Clash /connections `memory`). */
		memoryBytes: number;
		/** Причина не-live состояния для текста empty-state composite-блока. */
		notLiveReason?: 'stopped' | 'clash-down';
		/** Строки активных composite-выборов (из proxies/list, уже разрешены). */
		composites: ActiveCompositeRow[];
	}

	let { engineLive, engineLabel, memoryBytes, notLiveReason, composites }: Props = $props();

	const notLiveText = $derived(
		notLiveReason === 'clash-down'
			? 'Clash-runtime недоступен — активные выборы временно недоступны.'
			: 'Движок остановлен — активные выборы недоступны.',
	);
</script>

<div class="oprow">
	<!-- Движок -->
	<div class="otile">
		<div class="v" class:g={engineLive}>
			{#if engineLive}<span class="d" aria-hidden="true"></span>{/if}
			{engineLabel}
		</div>
		<div class="l">движок</div>
		<div class="s">gvisor</div>
	</div>

	<!-- Память sing-box -->
	<div class="otile">
		<div class="v">{memoryBytes > 0 ? formatBytes(memoryBytes) : '—'}</div>
		<div class="l">память sing-box</div>
		<div class="s">Clash /connections</div>
	</div>

	<!-- Активные выборы (composite) -->
	<div class="ocomp">
		<div class="l">Активные выборы (composite) — какой outbound где активен</div>

		{#if !engineLive}
			<p class="empty">{notLiveText}</p>
		{:else if composites.length === 0}
			<p class="empty">Composite-outbounds не настроены.</p>
		{:else}
			{#each composites as row (row.tag)}
				<div class="grp">
					<span class="gn">{row.groupTitle}</span>
					<span class="ty">{row.compositeType}</span>
					<span class="arr">&rarr;</span>
					<span class="act">{row.activeMemberLabel || '—'}</span>
					{#if row.otherCount > 0}
						<span class="meta">+{row.otherCount}</span>
					{/if}
					<span class="d" aria-hidden="true"></span>
				</div>
			{/each}
		{/if}
	</div>
</div>

<style>
	/* 2 узких тайла + широкая composite-карточка (dash3 `.oprow`). */
	.oprow {
		display: grid;
		grid-template-columns: 1fr 1fr 2.4fr;
		gap: 1px;
		background: var(--color-border);
		border: 1px solid var(--color-border);
		border-radius: var(--radius, 12px);
		overflow: hidden;
	}

	.otile,
	.ocomp {
		background: var(--color-bg-secondary);
		padding: 0.875rem;
	}

	.otile .v {
		color: var(--color-accent);
		font: 800 1.5rem/1.1 var(--font-sans);
		letter-spacing: -0.02em;
	}

	.otile .v.g {
		color: var(--color-success);
		font-size: 1.0625rem;
		display: flex;
		align-items: center;
		gap: 0.4375rem;
	}

	.otile .v.g .d {
		width: 9px;
		height: 9px;
		border-radius: 50%;
		background: var(--color-success);
		flex-shrink: 0;
	}

	.otile .l,
	.ocomp .l {
		color: var(--text-secondary);
		font-size: 0.875rem;
		margin-top: 0.3125rem;
	}

	.otile .s {
		color: var(--text-muted);
		font-size: 0.8125rem;
		margin-top: 0.25rem;
	}

	.ocomp .l {
		margin-top: 0;
		margin-bottom: 0.5rem;
	}

	.empty {
		margin: 0;
		font-size: 0.8125rem;
		color: var(--text-muted);
	}

	.grp {
		display: flex;
		align-items: center;
		gap: 0.625rem;
		font-size: 0.875rem;
		padding: 0.25rem 0;
	}

	.grp .gn {
		color: var(--text-primary);
		min-width: 7.5rem;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.grp .ty {
		font-size: 0.75rem;
		border-radius: 4px;
		padding: 1px 5px;
		border: 1px solid var(--color-border);
		color: var(--text-secondary);
		flex-shrink: 0;
	}

	.grp .arr {
		color: var(--text-muted);
	}

	.grp .act {
		color: var(--color-accent);
		font-family: var(--font-mono);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.grp .meta {
		color: var(--text-muted);
		font-size: 0.8125rem;
	}

	.grp .d {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		background: var(--color-success);
		margin-left: auto;
		flex-shrink: 0;
	}

	@media (max-width: 760px) {
		.oprow {
			grid-template-columns: 1fr 1fr;
		}
		.ocomp {
			grid-column: 1 / -1;
		}
	}
</style>
