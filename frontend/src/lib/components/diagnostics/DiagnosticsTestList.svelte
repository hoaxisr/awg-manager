<script lang="ts">
	import type { DiagTestEvent } from '$lib/types';
	import DiagnosticsTestItem from './DiagnosticsTestItem.svelte';
	import { slide } from 'svelte/transition';

	interface Props {
		tests: DiagTestEvent[];
		currentPhase?: string;
	}

	interface TunnelGroup {
		tunnelId: string;
		tunnelName: string;
		tests: DiagTestEvent[];
	}

	let { tests, currentPhase = '' }: Props = $props();
	let showDetailed = $state(false);
	let expandGeneration = $state(0);
	let expandAll = $state(false);

	let basicTests = $derived(tests.filter(t => t.level === 'basic'));
	let detailedTests = $derived(tests.filter(t => t.level === 'detailed'));

	function groupByTunnel(items: DiagTestEvent[]): { global: DiagTestEvent[]; groups: TunnelGroup[] } {
		const global: DiagTestEvent[] = [];
		const map = new Map<string, TunnelGroup>();

		for (const t of items) {
			if (!t.tunnelId) {
				global.push(t);
				continue;
			}
			let group = map.get(t.tunnelId);
			if (!group) {
				group = { tunnelId: t.tunnelId, tunnelName: t.tunnelName ?? t.tunnelId, tests: [] };
				map.set(t.tunnelId, group);
			}
			group.tests.push(t);
		}

		return { global, groups: [...map.values()] };
	}

	let basicGrouped = $derived(groupByTunnel(basicTests));
	let detailedGrouped = $derived(groupByTunnel(detailedTests));

	function toggleExpandAll() {
		expandAll = !expandAll;
		expandGeneration++;
	}
</script>

{#if currentPhase}
	<p class="phase-label">{currentPhase}</p>
{/if}

{#if tests.length > 0}
	<button class="expand-toggle" onclick={toggleExpandAll}>
		{expandAll ? 'Свернуть все' : 'Развернуть все'}
	</button>
{/if}

<div class="test-list">
	{#each basicGrouped.global as test (test.name)}
		<DiagnosticsTestItem {test} {expandGeneration} expandDirection={expandAll} />
	{/each}

	{#each basicGrouped.groups as group (group.tunnelId)}
		<div class="tunnel-group-header">
			<span class="tunnel-group-name">{group.tunnelName}</span>
			<span class="tunnel-group-id">{group.tunnelId}</span>
		</div>
		{#each group.tests as test (test.name + test.tunnelId)}
			<DiagnosticsTestItem {test} {expandGeneration} expandDirection={expandAll} />
		{/each}
	{/each}
</div>

{#if detailedTests.length > 0}
	<button
		class="details-toggle"
		onclick={() => showDetailed = !showDetailed}
	>
		{showDetailed ? 'Скрыть детали' : `Подробные тесты (${detailedTests.length})`}
	</button>

	{#if showDetailed}
		<div class="test-list" transition:slide={{ duration: 200 }}>
			{#each detailedGrouped.global as test (test.name)}
				<DiagnosticsTestItem {test} {expandGeneration} expandDirection={expandAll} />
			{/each}

			{#each detailedGrouped.groups as group (group.tunnelId)}
				<div class="tunnel-group-header">
					<span class="tunnel-group-name">{group.tunnelName}</span>
					<span class="tunnel-group-id">{group.tunnelId}</span>
				</div>
				{#each group.tests as test (test.name + test.tunnelId)}
					<DiagnosticsTestItem {test} {expandGeneration} expandDirection={expandAll} />
				{/each}
			{/each}
		</div>
	{/if}
{/if}

<style>
	.test-list {
		display: flex;
		flex-direction: column;
	}

	.phase-label {
		color: var(--text-secondary);
		font-size: 13px;
		margin-bottom: 4px;
		padding-left: 4px;
	}

	.expand-toggle {
		display: inline-flex;
		align-items: center;
		padding: 4px 10px;
		margin-bottom: 8px;
		border: 1px solid var(--border-primary);
		border-radius: 6px;
		background: none;
		color: var(--text-secondary);
		font-size: 12px;
		cursor: pointer;
	}

	.expand-toggle:hover {
		background: var(--bg-secondary);
		color: var(--text-primary);
	}

	.tunnel-group-header {
		display: flex;
		align-items: baseline;
		gap: 8px;
		padding: 10px 12px 4px;
		margin-top: 4px;
		border-top: 1px solid var(--border-primary);
	}

	.tunnel-group-header:first-child {
		border-top: none;
		margin-top: 0;
	}

	.tunnel-group-name {
		font-size: 13px;
		font-weight: 600;
		color: var(--text-primary);
	}

	.tunnel-group-id {
		font-size: 12px;
		font-family: monospace;
		color: var(--text-tertiary);
	}

	.details-toggle {
		display: block;
		width: 100%;
		padding: 8px 12px;
		margin-top: 4px;
		border: 1px dashed var(--border-primary);
		border-radius: 6px;
		background: none;
		color: var(--text-secondary);
		font-size: 13px;
		cursor: pointer;
		text-align: center;
	}

	.details-toggle:hover {
		background: var(--bg-secondary);
	}
</style>
