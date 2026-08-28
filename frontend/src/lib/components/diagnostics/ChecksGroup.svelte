<script lang="ts" module>
	export type GroupLed = 'gray' | 'green' | 'yellow' | 'red' | 'running';

	interface KindStyle {
		label: string;
		color: string;
		bg: string;
	}

	const KIND_STYLES: Record<string, KindStyle> = {
		'wg':     { label: 'WG',      color: '#7aa2f7', bg: 'rgba(122,162,247,0.14)' },
		'awg':    { label: 'AWG',     color: '#7aa2f7', bg: 'rgba(122,162,247,0.14)' },
		'awg1.0': { label: 'AWG 1.0', color: '#7aa2f7', bg: 'rgba(122,162,247,0.14)' },
		'awg1.5': { label: 'AWG 1.5', color: '#7dcfff', bg: 'rgba(125,207,255,0.14)' },
		'awg2.0': { label: 'AWG 2.0', color: '#7dcfff', bg: 'rgba(125,207,255,0.14)' },
		'awg3':   { label: 'AWG 3.0', color: '#bb9af7', bg: 'rgba(187,154,247,0.14)' },
		'awg3.1': { label: 'AWG 3.1', color: '#bb9af7', bg: 'rgba(187,154,247,0.14)' },
		'xray':   { label: 'XRAY',    color: '#bb9af7', bg: 'rgba(187,154,247,0.14)' },
		'vless':  { label: 'VLESS',   color: '#bb9af7', bg: 'rgba(187,154,247,0.14)' },
		'hy2':    { label: 'HY2',     color: '#f7768e', bg: 'rgba(247,118,142,0.14)' },
		'ss':     { label: 'SS',      color: '#9ece6a', bg: 'rgba(158,206,106,0.14)' },
	};

	const PLANNED_GLOBAL = [
		'WAN связность',
		'NDMS здоровье',
		'Модуль ядра',
		'Синхронизация часов',
		'Прямая связность',
		'Sing-box runtime',
	];

	const PLANNED_AWG = [
		'Резолв эндпоинта',
		'Пинг эндпоинта',
		'Маршрут к эндпоинту',
		'AWG рукопожатие',
		'Связность туннеля',
		'Правила файрвола',
		'Парсинг конфига',
		'Состояние интерфейса',
		'MTU',
		'Здоровье прокси',
		'Пинг-чек',
		'rp_filter',
	];

	const PLANNED_SINGBOX = [
		'Состояние туннеля',
		'Proxy port (TCP)',
		'HTTP-check (gstatic)',
		'Задержка (RTT)',
		'Alt-check (Cloudflare)',
	];

	export function getPlannedTests(isGlobal: boolean, isSingbox: boolean): string[] {
		if (isGlobal) return PLANNED_GLOBAL;
		if (isSingbox) return PLANNED_SINGBOX;
		return PLANNED_AWG;
	}
</script>

<script lang="ts">
	import type { Snippet } from 'svelte';
	import type { DiagTestEvent } from '$lib/types';
	import { DiagnosticsTestItem } from './';
	import { Button } from '$lib/components/ui';

	interface Props {
		name: string;
		kind?: string;
		isGlobal?: boolean;
		subtitle?: string;
		led: GroupLed;
		summary: string;
		tests?: DiagTestEvent[];
		expanded: boolean;
		onToggle: () => void;
		/** Called when user clicks the per-group quick-run button. Omit to hide it. */
		onRun?: () => void;
		/** True only for this specific group's active run (controls button label). */
		groupRunning?: boolean;
		/** True when any diagnostic run is in progress (disables all run buttons). */
		anyRunning?: boolean;
		actions?: Snippet;
		body?: Snippet;
		highlight?: boolean;
	}

	let {
		name,
		kind = '',
		isGlobal = false,
		subtitle = '',
		led,
		summary,
		tests = [],
		expanded,
		onToggle,
		onRun,
		groupRunning = false,
		anyRunning = false,
		actions,
		body,
		highlight = false,
	}: Props = $props();

	const kindStyle = $derived(kind ? KIND_STYLES[kind] : null);
	const isSingbox = $derived(!isGlobal && !kind?.startsWith('awg') && kind !== 'wg');
	const plannedTests = $derived(getPlannedTests(isGlobal, isSingbox));
	const showPlanned = $derived(expanded && !body && tests.length === 0);
	const runBtnLabel = $derived(groupRunning ? 'Идёт' : 'Проверить');
</script>

<section class="group" class:highlight class:expanded>
	<header class="head">
		<button
			class="title-btn"
			type="button"
			onclick={onToggle}
			aria-expanded={expanded}
		>
			<span class="led led-{led}"></span>
			{#if kindStyle}
				<span
					class="kind-badge"
					style="color:{kindStyle.color};background:{kindStyle.bg};border-color:{kindStyle.color}22"
				>{kindStyle.label}</span>
			{/if}
			<span class="name">{name}</span>
			{#if subtitle}<span class="subtitle">{subtitle}</span>{/if}
		</button>
		<span class="summary">{summary}</span>
		{#if onRun}
			<Button
				variant="secondary"
				size="sm"
				onclick={(e) => { e.stopPropagation(); onRun?.(); }}
				disabled={anyRunning}
				loading={groupRunning}
			>
				{runBtnLabel}
			</Button>
		{/if}
		{#if actions}{@render actions()}{/if}
		<button
			class="chev"
			type="button"
			onclick={onToggle}
			aria-label={expanded ? 'Свернуть' : 'Развернуть'}
		>
			<span class:rotated={expanded}>›</span>
		</button>
	</header>

	{#if expanded && (body || tests.length > 0 || showPlanned)}
		<div class="body">
			{#if body}
				{@render body()}
			{:else if tests.length > 0}
				<div class="tests">
					{#each tests as test (test.name + (test.tunnelId ?? ''))}
						<DiagnosticsTestItem {test} compact />
					{/each}
				</div>
			{:else if showPlanned}
				<div class="planned">
					{#each plannedTests as label}
						<div class="planned-item">
							<span class="planned-icon">·</span>
							<span class="planned-name">{label}</span>
						</div>
					{/each}
				</div>
			{/if}
		</div>
	{/if}
</section>

<style>
	.group {
		font-size: 13px;
	}

	.group.highlight .head {
		background: var(--color-accent-tint);
	}

	.head {
		display: flex;
		align-items: center;
		gap: 10px;
		padding: 10px 14px;
		min-height: 40px;
	}

	.title-btn {
		display: flex;
		align-items: center;
		gap: 8px;
		flex: 1;
		min-width: 0;
		background: transparent;
		border: none;
		padding: 0;
		cursor: pointer;
		font: inherit;
		color: var(--color-text-primary);
		text-align: left;
	}

	.led {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		flex-shrink: 0;
	}
	.led-green {
		background: var(--color-success);
		box-shadow: 0 0 6px var(--color-success);
	}
	.led-yellow {
		background: var(--color-warning);
		box-shadow: 0 0 6px var(--color-warning);
	}
	.led-red {
		background: var(--color-error);
		box-shadow: 0 0 6px var(--color-error);
	}
	.led-running {
		background: var(--color-warning);
		animation: pulse 1.4s ease-in-out infinite;
	}
	.led-gray {
		background: var(--color-text-muted);
		opacity: 0.5;
	}

	@keyframes pulse {
		0%, 100% { opacity: 0.4; }
		50% { opacity: 1; }
	}

	.kind-badge {
		font-size: 10px;
		font-weight: 700;
		font-family: var(--font-mono);
		letter-spacing: 0.04em;
		padding: 1px 6px;
		border-radius: 4px;
		border: 1px solid transparent;
		flex-shrink: 0;
		line-height: 1.6;
	}

	.name {
		font-weight: 600;
		font-size: 13px;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.subtitle {
		font-size: 11px;
		color: var(--color-text-muted);
	}

	.summary {
		font-family: var(--font-mono);
		font-size: 11px;
		color: var(--color-text-muted);
		flex-shrink: 0;
	}

	.chev {
		background: transparent;
		border: none;
		padding: 4px 6px;
		cursor: pointer;
		color: var(--color-text-muted);
		font-size: 16px;
		line-height: 1;
		flex-shrink: 0;
	}
	.chev span {
		display: inline-block;
		transition: transform var(--t-fast) ease;
	}
	.chev .rotated {
		transform: rotate(90deg);
	}

	.body {
		padding: 0 14px 12px 32px;
		display: flex;
		flex-direction: column;
		gap: 4px;
	}

	.tests {
		display: flex;
		flex-direction: column;
		gap: 0;
	}

	.planned {
		display: flex;
		flex-direction: column;
		gap: 0;
		padding: 2px 0;
	}

	.planned-item {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 4px 6px;
		font-size: 12px;
		color: var(--color-text-muted);
	}

	.planned-icon {
		width: 14px;
		height: 14px;
		display: flex;
		align-items: center;
		justify-content: center;
		font-size: 16px;
		flex-shrink: 0;
		opacity: 0.4;
	}

	.planned-name {
		font-style: italic;
	}
</style>
