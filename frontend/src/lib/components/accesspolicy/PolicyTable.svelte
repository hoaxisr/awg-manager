<script lang="ts">
	import type { AccessPolicy } from '$lib/types';
	import { pluralize, DEVICE_WORDS } from '$lib/utils/pluralize';
	import { Badge } from '$lib/components/ui';
	import { SquarePen, Trash2 } from 'lucide-svelte';
	import RoutingTargetBadges from '$lib/components/routing/RoutingTargetBadges.svelte';
	import PolicyIcon from './PolicyIcon.svelte';
	import { isHydraRouteAccessPolicy } from '$lib/utils/accessPolicy';

	interface Props {
		policies: AccessPolicy[];
		onedit: (name: string) => void;
		ondelete: (name: string) => void;
		selectable?: boolean;
		selectedNames?: Set<string>;
		onselect?: (name: string) => void;
	}

	let { policies, onedit, ondelete, selectable, selectedNames, onselect }: Props = $props();
</script>

<div class="route-grid">
	{#each policies as policy}
		{@const isHrPolicy = isHydraRouteAccessPolicy(policy)}
		{@const policyLabel = policy.description || policy.name}
		<div class="policy-card" class:policy-card-hr={isHrPolicy}>
			<div class="card-main">
				{#if selectable}
					<input
						type="checkbox"
						class="select-check"
						checked={selectedNames?.has(policy.name)}
						disabled={isHrPolicy}
						onchange={() => onselect?.(policy.name)}
					/>
				{/if}
				<PolicyIcon
					label={policy.description}
					policyName={policy.name}
					isHydraRoute={isHrPolicy}
				/>
				<div class="card-info">
					<div class="card-title">
						<h3 title={policyLabel}>{policyLabel}</h3>
						<div class="routing-badges">
							{#if isHrPolicy}
								<Badge variant="warning" uppercase size="xs" pill>HydraRoute</Badge>
							{/if}
							{#if policy.standalone}
								<Badge variant="accent" uppercase size="xs" pill>standalone</Badge>
							{/if}
						</div>
					</div>
					<span class="card-stat">{pluralize(policy.deviceCount, DEVICE_WORDS)}</span>
					{#if policy.interfaces?.length}
						{@const sortedIfaces = [...policy.interfaces].sort((a, b) => a.order - b.order)}
						<div class="card-route">
							<RoutingTargetBadges
								labels={sortedIfaces.map((iface) => iface.label || iface.name)}
								titles={sortedIfaces.map((iface) => iface.name)}
								overflowNoun="интерфейсов"
							/>
						</div>
					{/if}
				</div>
			</div>
			<div class="card-actions">
				<div class="action-row">
					<button
						type="button"
						class="route-action-btn"
						title={isHrPolicy
							? `Открыть HydraRoute-политику «${policy.description || policy.name}»`
							: `Изменить политику «${policy.description || policy.name}»`}
						onclick={() => onedit(policy.name)}
					>
						<SquarePen size={15} />
					</button>
					{#if !isHrPolicy}
						<button
							type="button"
							class="route-action-btn danger"
							title={`Удалить политику «${policy.description || policy.name}»`}
							onclick={() => ondelete(policy.name)}
						>
							<Trash2 size={15} />
						</button>
					{/if}
				</div>
			</div>
		</div>
	{/each}
</div>

<style>
	.policy-card {
		display: flex;
		justify-content: space-between;
		min-width: 0;
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		border-radius: 8px;
		padding: 14px;
		gap: 10px;
		transition: border-color 0.15s;
	}

	.policy-card:hover {
		border-color: var(--border-hover);
	}

	.policy-card .card-main {
		display: flex;
		flex: 1;
		gap: 10px;
		min-width: 0;
	}

	.policy-card .card-info {
		display: flex;
		flex-direction: column;
		flex: 1;
		gap: 1px;
		min-width: 0;
	}

	.policy-card .card-title {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 6px;
		min-width: 0;
	}

	.policy-card .card-title h3 {
		flex: 0 1 auto;
		font-size: 0.875rem;
		font-weight: 600;
		color: var(--text-primary);
		margin: 0;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		min-width: 0;
	}

	.policy-card .card-stat {
		font-size: 0.6875rem;
		color: var(--text-muted);
	}

	.policy-card-hr {
		border-color: rgba(245, 158, 11, 0.35);
	}

	.policy-card .card-actions {
		display: flex;
		flex-direction: column;
		align-items: flex-end;
		flex-shrink: 0;
		margin-left: 8px;
		align-self: stretch;
	}

	.policy-card .action-row {
		display: flex;
		gap: 4px;
		align-items: center;
		margin-top: auto;
	}

	.select-check {
		accent-color: var(--accent);
		width: 1rem;
		height: 1rem;
		cursor: pointer;
		flex-shrink: 0;
		margin-top: 10px;
	}
</style>
