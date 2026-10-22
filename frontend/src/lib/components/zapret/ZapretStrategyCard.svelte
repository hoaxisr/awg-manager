<script lang="ts">
	import { formatLocale, m } from '$lib/i18n';
	import type { ZapretStrategy } from '$lib/types';
	import { Badge, IconButton } from '$lib/components/ui';
	import { ArrowDown, ArrowUp, TriangleAlert, ListFilter } from 'lucide-svelte';
	import { zapretWarningText } from './warnings';

	/** Имя файла без пути — на карточке важны списки, а не где они лежат. */
	function baseName(p: string): string {
		return p.split('/').pop() ?? p;
	}

	interface Props {
		strategy: ZapretStrategy;
		total: number;
		busy?: boolean;
		/** Есть куда сдвинуть вверх / вниз — стрелки гасим на краях. */
		canMoveUp: boolean;
		canMoveDown: boolean;
		onmoveup: () => void;
		onmovedown: () => void;
	}

	let { strategy, total, busy = false, canMoveUp, canMoveDown, onmoveup, onmovedown }: Props =
		$props();

	// Краткая подпись профиля: порты + протокол L7. Имя в конфиге есть
	// далеко не всегда, поэтому подпись собираем из самих фильтров.
	let ports = $derived(strategy.filterTcp || strategy.filterUdp || '');
	let proto = $derived(strategy.filterUdp ? 'UDP' : 'TCP');

	let filterChips = $derived.by(() => {
		const out: { label: string; title: string }[] = [];
		if (strategy.filterTcp) {
			out.push({ label: `TCP ${strategy.filterTcp}`, title: m.zapret_strategy_filter_tcp_title() });
		}
		if (strategy.filterUdp) {
			out.push({ label: `UDP ${strategy.filterUdp}`, title: m.zapret_strategy_filter_udp_title() });
		}
		if (strategy.filterL7) {
			out.push({ label: strategy.filterL7, title: m.zapret_strategy_filter_l7_title() });
		}
		if (strategy.filterL3) {
			out.push({ label: strategy.filterL3, title: m.zapret_strategy_filter_l3_title() });
		}
		if (strategy.inRange || strategy.outRange) {
			const range = [strategy.inRange, strategy.outRange].filter(Boolean).join(' / ');
			out.push({ label: range, title: m.zapret_strategy_filter_packet_range_title() });
		}
		if (strategy.payload) {
			out.push({ label: strategy.payload, title: m.zapret_strategy_filter_payload_title() });
		}
		return out;
	});

	let poolKey = $derived.by(() => {
		for (const inst of strategy.luaDesync ?? []) {
			if (inst.key) return inst.key;
		}
		return '';
	});

	// Список, к которому привязан профиль, — самое частое, что нужно понять
	// пользователю: «эта карточка работает только по моему списку».
	// <HOSTLIST> разворачивается сразу в несколько файлов, поэтому
	// показываем их все: так видно и свой список, и исключения.
	let boundLists = $derived(
		[
			...(strategy.hostlists ?? []),
			...(strategy.hostlistExcludes ?? []),
			...(strategy.hostlistAuto ?? []),
		].filter((h) => h.exists)
	);

	let usesPlaceholder = $derived((strategy.placeholders?.length ?? 0) > 0);

	// Предупреждаем только когда не нашлось НИ ОДНОГО существующего файла:
	// <HOSTLIST> включает автосписок, которого до первого блока не будет.
	let hostlistMissing = $derived(
		(strategy.hostlists?.length ?? 0) > 0 && boundLists.length === 0
	);

	let hasWarnings = $derived((strategy.warnings?.length ?? 0) > 0);
</script>

<div class="zap-card" class:warned={hasWarnings}>
	<div class="card-head">
		<span class="prio" title={m.zapret_strategy_priority_title()}>
			{(strategy.index + 1).toLocaleString(formatLocale())}
		</span>
		<div class="head-text">
			<h3 title={strategy.name}>{strategy.name || `${proto} ${ports || m.zapret_strategy_no_filter()}`}</h3>
			{#if poolKey}
				<span class="pool" title={m.zapret_strategy_pool_key_title()}>
					key={poolKey}
				</span>
			{/if}
		</div>
		<div class="order-btns">
			<IconButton
				size="sm"
				ariaLabel={m.zapret_strategy_move_up_aria()}
				title={m.zapret_strategy_move_up_title()}
				disabled={!canMoveUp || busy}
				onclick={onmoveup}
			>
				<ArrowUp size={14} aria-hidden="true" />
			</IconButton>
			<IconButton
				size="sm"
				ariaLabel={m.zapret_strategy_move_down_aria()}
				title={m.zapret_strategy_move_down_title()}
				disabled={!canMoveDown || busy}
				onclick={onmovedown}
			>
				<ArrowDown size={14} aria-hidden="true" />
			</IconButton>
		</div>
	</div>

	<div class="chips">
		{#each filterChips as chip (chip.label)}
			<Badge size="xs" variant="muted" title={chip.title}>{chip.label}</Badge>
		{/each}
	</div>

	{#if usesPlaceholder || boundLists.length > 0 || hostlistMissing}
		<div class="lists">
			<ListFilter size={13} />
			<div class="list-body">
				{#if usesPlaceholder}
					<span class="placeholder" title={m.zapret_strategy_placeholder_title()}>
						{strategy.placeholders?.join(', ')}
					</span>
				{/if}
				{#if hostlistMissing}
					<span class="list-path missing" title={m.zapret_strategy_hostlists_missing_title()}>
						{m.zapret_strategy_hostlists_missing()}
					</span>
				{:else}
					{#each boundLists as list (list.path)}
						<span class="list-path" title={list.path}>
							{baseName(list.path)}
						</span>
					{/each}
				{/if}
			</div>
		</div>
	{/if}

	{#if hasWarnings}
		<ul class="warnings">
			{#each strategy.warnings ?? [] as w, index (`${w.code}-${index}`)}
				<li>
					<TriangleAlert size={13} />
					<span>{zapretWarningText(w)}</span>
				</li>
			{/each}
		</ul>
	{/if}

	<details class="raw">
		<summary>{m.zapret_strategy_raw_summary()}</summary>
		<pre>{strategy.raw}</pre>
		{#if total > 1}
			<p class="raw-note">
				{m.zapret_strategy_order_hint()}
			</p>
		{/if}
	</details>
</div>

<style>
	.zap-card {
		display: flex;
		flex-direction: column;
		gap: 8px;
		padding: 12px;
		border: 1px solid var(--border);
		border-radius: 8px;
		background: var(--bg-secondary);
		transition: border-color 0.2s;
	}

	.zap-card:hover {
		border-color: var(--border-hover);
	}

	.zap-card.warned {
		border-color: var(--warning, #f59e0b);
	}

	.card-head {
		display: flex;
		align-items: center;
		gap: 8px;
		min-width: 0;
	}

	/* Номер = приоритет. Приглушён, но всегда на своём месте: смысл
	   карточек в порядке, и без него список не читается. */
	.prio {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		min-width: 20px;
		height: 20px;
		padding: 0 5px;
		border-radius: 5px;
		background: var(--bg-primary);
		border: 1px solid var(--border);
		font-size: 0.625rem;
		font-variant-numeric: tabular-nums;
		color: var(--text-muted);
		flex-shrink: 0;
	}

	.head-text {
		display: flex;
		flex-direction: column;
		gap: 1px;
		min-width: 0;
		flex: 1;
	}

	.head-text h3 {
		margin: 0;
		font-size: 0.8125rem;
		font-weight: 600;
		color: var(--text-primary);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.pool {
		font-family: var(--font-mono);
		font-size: 0.625rem;
		color: var(--text-muted);
	}

	.order-btns {
		display: flex;
		gap: 2px;
		flex-shrink: 0;
	}

	.chips {
		display: flex;
		flex-wrap: wrap;
		gap: 4px;
	}

	.lists {
		display: flex;
		align-items: flex-start;
		gap: 5px;
		min-width: 0;
		color: var(--text-muted);
	}

	.list-body {
		display: flex;
		flex-wrap: wrap;
		gap: 2px 8px;
		min-width: 0;
	}

	.placeholder {
		font-family: var(--font-mono);
		font-size: 0.625rem;
		color: var(--accent);
	}

	.list-path {
		font-family: var(--font-mono);
		font-size: 0.625rem;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		min-width: 0;
	}

	.list-path.missing {
		color: var(--warning, #f59e0b);
	}

	.warnings {
		display: flex;
		flex-direction: column;
		gap: 4px;
		margin: 0;
		padding: 6px 8px;
		list-style: none;
		border-radius: 6px;
		background: color-mix(in srgb, var(--warning, #f59e0b) 12%, transparent);
	}

	.warnings li {
		display: flex;
		align-items: flex-start;
		gap: 5px;
		font-size: 0.6875rem;
		line-height: 1.35;
		color: var(--text-primary);
	}

	.warnings li :global(svg) {
		flex-shrink: 0;
		margin-top: 1px;
		color: var(--warning, #f59e0b);
	}

	.raw summary {
		font-size: 0.6875rem;
		color: var(--text-muted);
		cursor: pointer;
		user-select: none;
	}

	.raw summary:hover {
		color: var(--text-secondary);
	}

	.raw pre {
		margin: 6px 0 0;
		padding: 8px;
		border-radius: 6px;
		background: var(--bg-primary);
		border: 1px solid var(--border);
		font-family: var(--font-mono);
		font-size: 0.6875rem;
		line-height: 1.5;
		color: var(--text-secondary);
		white-space: pre-wrap;
		word-break: break-word;
	}

	.raw-note {
		margin: 6px 0 0;
		font-size: 0.6875rem;
		line-height: 1.4;
		color: var(--text-muted);
	}
</style>
