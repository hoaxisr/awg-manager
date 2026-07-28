<script lang="ts">
	import type { DnsRoute } from '$lib/types';
	import HrNeoRuleCard from './HrNeoRuleCard.svelte';
	import RoutingRuleAddMenu from '$lib/components/routing/RoutingRuleAddMenu.svelte';
	import { pluralize, RULE_WORDS } from '$lib/utils/pluralize';

	interface Props {
		target: string;
		targetKind: 'policy' | 'interface';
		rules: DnsRoute[];
		brokenRuleIds?: Set<string>;
		oncatalog: () => void;
		onmanual: () => void;
		oneditrule: (rule: DnsRoute) => void;
		ondeleterule: (rule: DnsRoute) => void;
		oniconrule?: (rule: DnsRoute) => void;
		ontogglerule?: (rule: DnsRoute, enabled: boolean) => void;
		toggleLoadingId?: string | null;
		displayRule?: (rule: DnsRoute) => DnsRoute;
	}

	let {
		target,
		targetKind,
		rules,
		brokenRuleIds = new Set(),
		oncatalog,
		onmanual,
		oneditrule,
		ondeleterule,
		oniconrule,
		ontogglerule,
		toggleLoadingId = null,
		displayRule,
	}: Props = $props();

	let sortedRules = $derived([...rules].sort((a, b) => a.name.localeCompare(b.name)));
</script>

<div class="hr-list">
	<header class="list-header">
		<div class="list-title">
			<h2>{target}</h2>
			<span class="kind-badge kind-{targetKind}">{targetKind}</span>
			<span class="count">{pluralize(rules.length, RULE_WORDS)}</span>
		</div>
		<RoutingRuleAddMenu label="Добавить правило" oncatalog={oncatalog} onmanual={onmanual} />
	</header>

	{#if sortedRules.length === 0}
		<div class="empty">Пусто. Добавьте первое правило для этого target.</div>
	{:else}
		<div class="route-grid">
			{#each sortedRules as rule (rule.id)}
				{@const cardRule = displayRule ? displayRule(rule) : rule}
				<HrNeoRuleCard
					rule={cardRule}
					broken={brokenRuleIds.has(rule.id)}
					toggleLoading={toggleLoadingId === rule.id}
					ontoggle={ontogglerule ? (enabled) => ontogglerule(rule, enabled) : undefined}
					onedit={() => oneditrule(rule)}
					ondelete={() => ondeleterule(rule)}
					onicon={oniconrule ? () => oniconrule(rule) : undefined}
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
		background: color-mix(in srgb, var(--color-accent) 15%, transparent);
		color: var(--accent);
	}

	.kind-interface {
		background: color-mix(in srgb, var(--color-info) 15%, transparent);
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

	.route-grid {
		/* Up to 3 columns; min width fits «константинопольские» in the title area. */
		--hr-rule-col-min: 20rem;
		display: grid;
		gap: 12px;
		grid-template-columns: repeat(
			auto-fill,
			minmax(max(var(--hr-rule-col-min), calc((100% - 2 * 12px) / 3)), 1fr)
		);
	}

	@media (max-width: 639px) {
		.route-grid {
			grid-template-columns: minmax(0, 1fr);
		}
	}

	@media (max-width: 640px) {
		.list-header {
			flex-wrap: wrap;
		}
		.list-title {
			flex: 1 1 100%;
		}
		.list-header :global(.dropdown-wrapper) {
			flex: 1 1 100%;
		}
		.list-header :global(.dropdown-wrapper .btn) {
			width: 100%;
		}
	}
</style>
