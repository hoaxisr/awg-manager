<script lang="ts">
	import type { AccessPolicy } from '$lib/types';

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

<div class="policy-grid">
	{#each policies as policy}
		<div class="policy-card">
			{#if selectable}
				<div class="select-cell">
					<input
						type="checkbox"
						class="select-check"
						checked={selectedNames?.has(policy.name)}
						onchange={() => onselect?.(policy.name)}
					/>
				</div>
			{/if}
			<div class="policy-body">
				<div class="policy-meta">
					{policy.deviceCount} устройств
				</div>
				<div class="policy-title-row">
					<span class="policy-name">{policy.description || policy.name}</span>
					{#if policy.standalone}
						<span class="badge-standalone">standalone</span>
					{/if}
				</div>
				{#if policy.interfaces?.length}
					<div class="policy-ifaces">
						{#each [...policy.interfaces].sort((a, b) => a.order - b.order) as iface}
							<span class="badge-iface" title={iface.name}>{iface.label || iface.name}</span>
						{/each}
					</div>
				{/if}
			</div>
			<div class="policy-actions">
				<button class="action-btn" title="Изменить" onclick={() => onedit(policy.name)}>
					<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
						<path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/>
						<path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/>
					</svg>
				</button>
				<button class="action-btn danger" title="Удалить" onclick={() => ondelete(policy.name)}>
					<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
						<polyline points="3 6 5 6 21 6"/>
						<path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/>
					</svg>
				</button>
			</div>
		</div>
	{/each}
</div>

<style>
	.policy-grid {
		display: grid;
		grid-template-columns: repeat(2, 1fr);
		gap: 12px;
	}

	.policy-card {
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		border-radius: 8px;
		padding: 14px 16px;
		display: flex;
		align-items: flex-start;
		gap: 12px;
		transition: border-color 0.15s;
	}

	.policy-card:hover {
		border-color: var(--border-hover);
	}

	.policy-body {
		flex: 1;
		min-width: 0;
	}

	.policy-meta {
		font-size: 0.6875rem;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--text-muted);
		margin-bottom: 2px;
	}

	.policy-title-row {
		display: flex;
		align-items: center;
		gap: 8px;
		margin-bottom: 6px;
	}

	.policy-name {
		font-weight: 500;
		font-size: 0.9375rem;
		color: var(--text-primary);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.badge-standalone {
		font-size: 0.625rem;
		padding: 1px 6px;
		border-radius: 9999px;
		background: var(--accent);
		color: white;
		font-weight: 500;
		white-space: nowrap;
		flex-shrink: 0;
	}

	.policy-ifaces {
		display: flex;
		flex-wrap: wrap;
		gap: 4px;
	}

	.badge-iface {
		font-size: 0.6875rem;
		padding: 2px 8px;
		border-radius: 9999px;
		background: var(--bg-hover);
		color: var(--text-primary);
		border: 1px solid var(--border);
		white-space: nowrap;
	}

	.policy-actions {
		display: flex;
		gap: 4px;
		flex-shrink: 0;
		align-self: center;
	}

	.action-btn {
		display: flex;
		padding: 5px;
		background: none;
		border: 1px solid transparent;
		color: var(--border-hover);
		cursor: pointer;
		border-radius: 6px;
		transition: all 0.15s;
	}

	.action-btn:hover {
		color: var(--accent);
		background: var(--bg-hover);
	}

	.action-btn.danger:hover {
		color: var(--error);
		background: rgba(247, 118, 142, 0.1);
	}

	.select-cell {
		width: 2rem;
		padding: 0.5rem;
		display: flex;
		align-items: center;
		flex-shrink: 0;
	}

	.select-check {
		accent-color: var(--accent);
		width: 1rem;
		height: 1rem;
		cursor: pointer;
	}

	@media (max-width: 768px) {
		.policy-grid {
			grid-template-columns: 1fr;
		}
	}
</style>
