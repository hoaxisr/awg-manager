<script lang="ts">
	import type { AccessPolicy } from '$lib/types';

	interface Props {
		policies: AccessPolicy[];
		onedit: (name: string) => void;
		ondelete: (name: string) => void;
	}

	let { policies, onedit, ondelete }: Props = $props();
</script>

<div class="table-wrapper">
	<table class="policy-table">
		<thead>
			<tr>
				<th class="col-name-h">Имя</th>
				<th class="col-ifaces-h">Интерфейсы</th>
				<th class="col-devices-h">Устройства</th>
				<th class="col-actions-h"></th>
			</tr>
		</thead>
		<tbody>
			{#each policies as policy}
				<tr>
					<td>
						<div class="name-cell">
							<span class="policy-desc">{policy.description || policy.name}</span>
							{#if policy.standalone}
								<span class="badge-standalone">standalone</span>
							{/if}
						</div>
					</td>
					<td>
						<div class="ifaces-cell">
							{#each [...(policy.interfaces ?? [])].sort((a, b) => a.order - b.order) as iface}
								<span class="badge-iface" title={iface.name}>{iface.label || iface.name}</span>
							{/each}
						</div>
					</td>
					<td>
						<span class="text-muted">{policy.deviceCount}</span>
					</td>
					<td>
						<div class="actions-cell">
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
					</td>
				</tr>
			{/each}
		</tbody>
	</table>
</div>

<style>
	.table-wrapper {
		border: 1px solid var(--border);
		border-radius: 8px;
		overflow: hidden;
	}

	.policy-table {
		width: 100%;
		border-collapse: collapse;
		font-size: 0.875rem;
		table-layout: fixed;
	}

	.policy-table thead tr {
		background: var(--bg-tertiary, var(--bg-primary));
	}

	.policy-table th {
		padding: 8px 12px;
		text-align: left;
		font-size: 0.6875rem;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--text-muted);
		border-bottom: 1px solid var(--border);
	}

	.col-name-h { width: 30%; }
	.col-ifaces-h { width: auto; }
	.col-devices-h { width: 100px; }
	.col-actions-h { width: 70px; }

	.policy-table td {
		padding: 10px 12px;
		border-bottom: 1px solid var(--border);
		vertical-align: middle;
	}

	.policy-table tbody tr:last-child td {
		border-bottom: none;
	}

	.policy-table tbody tr:hover {
		background: var(--bg-hover);
	}

	.name-cell {
		display: flex;
		align-items: center;
		gap: 8px;
	}

	.policy-desc {
		font-weight: 500;
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

	.ifaces-cell {
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

	.actions-cell {
		display: flex;
		gap: 4px;
		justify-content: flex-end;
	}

	.text-muted {
		color: var(--text-muted);
		font-size: 0.8125rem;
	}

	.action-btn {
		display: flex;
		padding: 4px;
		background: none;
		border: none;
		color: var(--border-hover);
		cursor: pointer;
		border-radius: 4px;
		transition: color 0.15s;
	}

	.action-btn:hover {
		color: var(--accent);
	}

	.action-btn.danger:hover {
		color: var(--error);
	}
</style>
