<script lang="ts">
	import type { PingLogEntry } from '$lib/types';
	import { formatDate } from '$lib/utils/format';
	import { notifications } from '$lib/stores/notifications';

	interface Props {
		logs: PingLogEntry[];
		tunnels: Array<{ tunnelId: string; tunnelName: string }>;
		filterTunnelId: string;
		clearing?: boolean;
		onFilterChange: (tunnelId: string) => void;
		onClear?: () => void;
	}

	let { logs, tunnels, filterTunnelId, clearing = false, onFilterChange, onClear }: Props = $props();

	let filteredLogs = $derived(filterTunnelId
		? logs.filter(l => l.tunnelId === filterTunnelId)
		: logs);

	let displayLimit = $state(50);

	// Reset limit when filter changes
	$effect(() => {
		filterTunnelId;
		displayLimit = 50;
	});

	let visibleLogs = $derived(filteredLogs.slice(0, displayLimit));

	async function copyToClipboard() {
		if (!filteredLogs.length) return;

		const text = filteredLogs.map(log => {
			const time = formatDate(log.timestamp);
			const result =
				log.stateChange === 'initial' ? 'INIT' :
				log.stateChange === 'dead' ? 'DEAD' :
				log.stateChange === 'alive' ? 'RECOVERED' :
				log.stateChange === 'forced_restart' ? (log.success ? 'RESTART OK' : 'RESTART FAIL') :
				log.stateChange === 'grace' ? 'FAIL (grace)' :
				log.success ? 'OK' : 'FAIL';
			const latency = log.latency >= 0 ? `${log.latency}ms` : '-';
			const error = log.error ? ` | ${log.error}` : '';
			return `[${time}] ${log.tunnelName} ${result} ${latency} ${log.failCount}/${log.threshold}${error}`;
		}).join('\n');

		try {
			if (navigator.clipboard && window.isSecureContext) {
				await navigator.clipboard.writeText(text);
			} else {
				const textarea = document.createElement('textarea');
				textarea.value = text;
				textarea.style.position = 'fixed';
				textarea.style.opacity = '0';
				document.body.appendChild(textarea);
				textarea.select();
				document.execCommand('copy');
				document.body.removeChild(textarea);
			}
			notifications.success('Скопировано в буфер обмена');
		} catch {
			notifications.error('Не удалось скопировать');
		}
	}
</script>

<div class="card mt-4">
	<div class="logs-header">
		<h3>Журнал проверок</h3>
		<div class="logs-actions">
			<button
				class="btn btn-secondary btn-sm"
				onclick={copyToClipboard}
				disabled={!filteredLogs.length}
				title="Копировать в буфер обмена"
			>
				<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="14" height="14">
					<rect x="9" y="9" width="13" height="13" rx="2" ry="2"/>
					<path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>
				</svg>
				Копировать
			</button>
			{#if onClear}
				<button
					class="btn btn-danger btn-sm"
					onclick={onClear}
					disabled={clearing || !filteredLogs.length}
				>
					{clearing ? 'Очистка...' : 'Очистить'}
				</button>
			{/if}
			<select value={filterTunnelId} onchange={(e) => onFilterChange(e.currentTarget.value)}>
				<option value="">Все туннели</option>
				{#each tunnels as tunnel}
					<option value={tunnel.tunnelId}>{tunnel.tunnelName}</option>
				{/each}
			</select>
		</div>
	</div>

	{#if filteredLogs.length === 0}
		<p class="text-muted">Нет записей в журнале</p>
	{:else}
		<div class="logs-table">
			<table>
				<thead>
					<tr>
						<th>Время</th>
						<th>Туннель</th>
						<th>Результат</th>
						<th>Задержка</th>
						<th>Счётчик</th>
					</tr>
				</thead>
				<tbody>
					{#each visibleLogs as log}
						<tr class:log-error={!log.success && log.stateChange !== 'initial'} class:log-state-change={log.stateChange && log.stateChange !== 'grace'}>
							<td class="time-cell">{formatDate(log.timestamp)}</td>
							<td>{log.tunnelName}</td>
							<td>
								{#if log.stateChange === 'initial'}
									<span class="state-initial">INIT</span>
								{:else if log.stateChange === 'dead'}
									<span class="state-dead">DEAD</span>
								{:else if log.stateChange === 'alive'}
									<span class="state-alive">RECOVERED</span>
								{:else if log.stateChange === 'forced_restart'}
									<span class={log.success ? 'state-alive' : 'state-restart'}>{log.success ? 'RESTART OK' : 'RESTART FAIL'}</span>
								{:else if log.stateChange === 'grace'}
									<span class="result-fail" title="Grace period — счётчик сброшен">FAIL (grace)</span>
								{:else if log.success}
									<span class="result-ok">OK</span>
								{:else}
									<span class="result-fail" title={log.error}>FAIL</span>
								{/if}
							</td>
							<td>
								{#if log.latency >= 0}
									{log.latency} ms
								{:else}
									-
								{/if}
							</td>
							<td>{log.failCount}/{log.threshold}</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
		{#if filteredLogs.length > displayLimit}
			<div class="show-more">
				<span class="text-muted">Показаны {displayLimit} из {filteredLogs.length}</span>
				<button
					class="btn btn-secondary btn-sm"
					onclick={() => displayLimit += 50}
				>
					Ещё 50
				</button>
				<button
					class="btn btn-secondary btn-sm"
					onclick={() => displayLimit = filteredLogs.length}
				>
					Все
				</button>
			</div>
		{/if}
	{/if}
</div>

<style>
	.logs-header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		margin-bottom: 1rem;
		flex-wrap: wrap;
		gap: 0.5rem;
	}

	.logs-header h3 {
		margin-bottom: 0;
	}

	.logs-actions {
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}

	.logs-actions select {
		padding: 0.375rem 0.75rem;
		background: var(--bg-tertiary);
		border: 1px solid var(--border);
		border-radius: 6px;
		color: var(--text-primary);
		font-size: 0.875rem;
	}

	.logs-table {
		overflow-x: auto;
		cursor: default;
	}

	table {
		width: 100%;
		border-collapse: collapse;
		font-size: 0.875rem;
	}

	th, td {
		padding: 0.5rem 0.75rem;
		text-align: left;
		border-bottom: 1px solid var(--border);
	}

	th {
		font-weight: 500;
		color: var(--text-muted);
		font-size: 0.75rem;
		text-transform: uppercase;
	}

	.time-cell {
		font-family: monospace;
		font-size: 0.8125rem;
		white-space: nowrap;
	}

	.log-error {
		background: rgba(247, 118, 142, 0.05);
	}

	.log-state-change {
		font-weight: 500;
	}

	.result-ok {
		color: var(--success);
	}

	.result-fail {
		color: var(--error);
		cursor: help;
	}

	.state-dead {
		color: var(--error);
		font-weight: 600;
	}

	.state-alive {
		color: var(--success);
		font-weight: 600;
	}

	.state-restart {
		color: var(--warning);
		font-weight: 600;
	}

	.state-initial {
		color: var(--text-muted);
		font-weight: 600;
	}

	.show-more {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		margin-top: 0.75rem;
	}

	.text-muted {
		color: var(--text-muted);
	}
</style>
