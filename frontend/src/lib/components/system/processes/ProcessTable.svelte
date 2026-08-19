<script lang="ts">
	import type { SystemProcessItem } from '$lib/api/client';
	import { Card } from '$lib/components/ui';
	import { RefreshCw, Search, ArrowUp, ArrowDown } from 'lucide-svelte';
	import ProcessRow from './ProcessRow.svelte';
	import type { SortField } from './shared';

	interface Props {
		processes: SystemProcessItem[];
		loading: boolean;
		initialLoaded: boolean;
		sortField: SortField;
		sortAsc: boolean;
		onsort: (field: SortField) => void;
		onkill: (proc: SystemProcessItem) => void;
	}

	let { processes, loading, initialLoaded, sortField, sortAsc, onsort, onkill }: Props = $props();
</script>

<Card padding="sm">
	<div class="table-container">
		{#if !initialLoaded && loading}
			<div class="empty-state">
				<RefreshCw size={24} class="spin" />
				<p>Сбор списка процессов роутера…</p>
			</div>
		{:else if processes.length === 0}
			<div class="empty-state">
				<Search size={24} class="muted" />
				<p>Процессы не найдены по текущему запросу</p>
			</div>
		{:else}
			<table class="proc-table">
				<thead>
					<tr>
						<th class="th-sortable col-th-pid" onclick={() => onsort('pid')}>
							<div class="th-wrap">
								<span>PID</span>
								{#if sortField === 'pid'}
									{#if sortAsc}<ArrowUp size={11} />{:else}<ArrowDown size={11} />{/if}
								{/if}
							</div>
						</th>
						<th class="th-sortable col-th-user" onclick={() => onsort('user')}>
							<div class="th-wrap">
								<span>Польз.</span>
								{#if sortField === 'user'}
									{#if sortAsc}<ArrowUp size={11} />{:else}<ArrowDown size={11} />{/if}
								{/if}
							</div>
						</th>
						<th class="th-sortable col-th-state" onclick={() => onsort('state')}>
							<div class="th-wrap">
								<span>Сост.</span>
								{#if sortField === 'state'}
									{#if sortAsc}<ArrowUp size={11} />{:else}<ArrowDown size={11} />{/if}
								{/if}
							</div>
						</th>
						<th class="th-sortable col-th-threads" onclick={() => onsort('threads')}>
							<div class="th-wrap">
								<span>Потоки</span>
								{#if sortField === 'threads'}
									{#if sortAsc}<ArrowUp size={11} />{:else}<ArrowDown size={11} />{/if}
								{/if}
							</div>
						</th>
						<th class="th-sortable col-th-cpu" onclick={() => onsort('cpu')}>
							<div class="th-wrap">
								<span>CPU %</span>
								{#if sortField === 'cpu'}
									{#if sortAsc}<ArrowUp size={11} />{:else}<ArrowDown size={11} />{/if}
								{/if}
							</div>
						</th>
						<th class="th-sortable col-th-mem" onclick={() => onsort('mem')}>
							<div class="th-wrap">
								<span>Память</span>
								{#if sortField === 'mem'}
									{#if sortAsc}<ArrowUp size={11} />{:else}<ArrowDown size={11} />{/if}
								{/if}
							</div>
						</th>
						<th class="th-sortable col-th-cmd" onclick={() => onsort('name')}>
							<div class="th-wrap">
								<span>Команда / Процесс</span>
								{#if sortField === 'name'}
									{#if sortAsc}<ArrowUp size={11} />{:else}<ArrowDown size={11} />{/if}
								{/if}
							</div>
						</th>
						<th class="col-th-act">Стоп</th>
					</tr>
				</thead>
				<tbody>
					{#each processes as proc (proc.pid)}
						<ProcessRow {proc} {onkill} />
					{/each}
				</tbody>
			</table>
		{/if}
	</div>
</Card>

<style>
	/* 3. Table Responsive Styles (No horizontal scrollbar) */
	.table-container {
		max-height: 540px;
		overflow-y: auto;
		overflow-x: hidden;
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
	}

	.proc-table {
		width: 100%;
		border-collapse: collapse;
		font-size: 0.82rem;
		table-layout: fixed;
	}

	.proc-table th {
		padding: 0.45rem 0.5rem;
		border-bottom: 1px solid var(--color-border);
		text-align: left;
		vertical-align: middle;
	}

	.proc-table th {
		position: sticky;
		top: 0;
		background: var(--color-bg-tertiary);
		color: var(--color-text-primary);
		font-size: 0.72rem;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.02em;
		z-index: 1;
	}

	/* Fixed column widths */
	.col-th-pid { width: 56px; }
	.col-th-user { width: 68px; }
	.col-th-state { width: 48px; text-align: center; }
	.col-th-threads { width: 56px; text-align: center; }
	.col-th-cpu { width: 76px; }
	.col-th-mem { width: 110px; }
	.col-th-cmd { width: auto; overflow: hidden; }
	.col-th-act { width: 48px; text-align: center; }

	.th-sortable {
		cursor: pointer;
		user-select: none;
	}

	.th-sortable:hover {
		background: var(--color-bg-hover, rgba(255,255,255,0.05));
	}

	.th-wrap {
		display: flex;
		align-items: center;
		gap: 0.25rem;
	}

	.empty-state {
		padding: 3rem;
		text-align: center;
		color: var(--color-text-muted);
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.5rem;
	}

</style>
