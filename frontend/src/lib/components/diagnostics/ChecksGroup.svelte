<script lang="ts" module>
	export type GroupLed = 'gray' | 'green' | 'yellow' | 'red' | 'running';
</script>

<script lang="ts">
	import type { Snippet } from 'svelte';
	import type { DiagTestEvent } from '$lib/types';
	import { DiagnosticsTestItem } from './';

	interface Props {
		name: string;
		subtitle?: string;
		led: GroupLed;
		summary: string;
		tests?: DiagTestEvent[];
		expanded: boolean;
		onToggle: () => void;
		actions?: Snippet;
		body?: Snippet;
		highlight?: boolean;
	}

	let {
		name,
		subtitle = '',
		led,
		summary,
		tests = [],
		expanded,
		onToggle,
		actions,
		body,
		highlight = false,
	}: Props = $props();
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
			<span class="name">{name}</span>
			{#if subtitle}<span class="subtitle">{subtitle}</span>{/if}
		</button>
		<span class="summary">{summary}</span>
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

	{#if expanded && (body || tests.length > 0)}
		<div class="body">
			{#if body}
				{@render body()}
			{:else}
				<div class="tests">
					{#each tests as test (test.name + (test.tunnelId ?? ''))}
						<DiagnosticsTestItem {test} compact />
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
</style>
