<script lang="ts">
	import type { DnsRoute } from '$lib/types';
	import { Button } from '$lib/components/ui';
	import HrNeoRuleCard from './HrNeoRuleCard.svelte';

	interface Props {
		target: string;
		targetKind: 'policy' | 'interface';
		rules: DnsRoute[];
		brokenRuleIds?: Set<string>;
		onaddrule: () => void;
		oneditrule: (rule: DnsRoute) => void;
		ondeleterule: (rule: DnsRoute) => void;
	}

	let {
		target,
		targetKind,
		rules,
		brokenRuleIds = new Set(),
		onaddrule,
		oneditrule,
		ondeleterule,
	}: Props = $props();

	let sortedRules = $derived([...rules].sort((a, b) => a.name.localeCompare(b.name)));
</script>

<div class="hr-list">
	<header class="list-header">
		<div class="list-title">
			<h2>{target}</h2>
			<span class="kind-badge kind-{targetKind}">{targetKind}</span>
			<span class="count">{rules.length} правил</span>
		</div>
		<Button variant="primary" size="sm" onclick={onaddrule}>+ Добавить правило</Button>
	</header>

	{#if sortedRules.length === 0}
		<div class="empty">Пусто. Добавьте первое правило для этого target.</div>
	{:else}
		<div class="route-grid">
			{#each sortedRules as rule (rule.id)}
				<HrNeoRuleCard
					{rule}
					broken={brokenRuleIds.has(rule.id)}
					onedit={() => oneditrule(rule)}
					ondelete={() => ondeleterule(rule)}
				/>
			{/each}
		</div>
	{/if}
</div>

<style>
	.hr-list {
		display: flex;
		flex-direction: column;
		gap: 14px;
	}

	.list-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		padding-bottom: 12px;
		border-bottom: 1px solid var(--border);
		gap: 12px;
	}

	.list-title {
		display: flex;
		align-items: center;
		gap: 10px;
		min-width: 0;
	}

	.list-title h2 {
		margin: 0;
		font-size: 1.0625rem;
		color: var(--text-primary);
		font-weight: 600;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.kind-badge {
		background: var(--bg-tertiary);
		color: var(--text-muted);
		font-size: 0.6875rem;
		padding: 2px 8px;
		border-radius: 10px;
		text-transform: uppercase;
		letter-spacing: 0.05em;
	}

	.kind-policy {
		background: rgba(122, 162, 247, 0.15);
		color: var(--accent);
	}

	.kind-interface {
		background: rgba(125, 207, 255, 0.15);
		color: var(--info);
	}

	.count {
		color: var(--text-muted);
		font-size: 0.8125rem;
	}

	.empty {
		padding: 32px;
		text-align: center;
		color: var(--text-muted);
		font-style: italic;
		background: var(--bg-secondary);
		border: 1px dashed var(--border);
		border-radius: 8px;
	}

	@media (max-width: 640px) {
		.list-header {
			flex-wrap: wrap;
		}
		.list-title {
			flex: 1 1 100%;
		}
		.list-header :global(.btn) {
			flex: 1 1 100%;
		}
	}
</style>
