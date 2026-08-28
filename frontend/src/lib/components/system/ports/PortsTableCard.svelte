<script lang="ts">
	import type { SystemPortBinding } from '$lib/api/client';
	import { Button, Card, SegmentedControl } from '$lib/components/ui';
	import { RefreshCw, Power } from 'lucide-svelte';
	import PortProcessBadges from './PortProcessBadges.svelte';
	import type { GroupedProcessPort, ProtoFilter } from './types';

	interface Props {
		bindings: SystemPortBinding[];
		groups: GroupedProcessPort[];
		filterProto: ProtoFilter;
		search: string;
		loading: boolean;
		busy: boolean;
		onrefresh: () => void;
		onkill: (group: GroupedProcessPort) => void;
	}

	let {
		bindings,
		groups,
		filterProto = $bindable(),
		search = $bindable(),
		loading,
		busy,
		onrefresh,
		onkill,
	}: Props = $props();

	const tcpCount = $derived(bindings.filter((b) => b.proto.startsWith('tcp')).length);
	const udpCount = $derived(bindings.filter((b) => b.proto.startsWith('udp')).length);
</script>

<Card padding="sm">
	<div class="table-header">
		<div class="left-controls">
			<h3>Открытые порты системы ({groups.length} процессов, {bindings.length} сокетов)</h3>
			<SegmentedControl
				value={filterProto}
				options={[
					{ value: 'all', label: `Все (${bindings.length})` },
					{ value: 'tcp', label: `TCP (${tcpCount})` },
					{ value: 'udp', label: `UDP (${udpCount})` },
				]}
				ariaLabel="Фильтр по протоколу"
				onchange={(v) => (filterProto = v as ProtoFilter)}
			/>
		</div>
		<div class="right-controls">
			<input
				type="text"
				placeholder="Поиск по порту, процессу, IP…"
				bind:value={search}
				class="table-search-input"
			/>
			<Button variant="ghost" onclick={onrefresh} disabled={loading}>
				{#snippet iconBefore()}<RefreshCw size={14} />{/snippet}
				Обновить
			</Button>
		</div>
	</div>

	{#if loading && bindings.length === 0}
		<p class="muted">Сканирование портов…</p>
	{:else if groups.length === 0}
		<p class="muted">Порты не найдены</p>
	{:else}
		<div class="table-wrap">
			<table class="ports-table">
				<thead>
					<tr>
						<th style="width: 90px;">Протокол</th>
						<th style="width: 80px;">Порт</th>
						<th style="width: 220px;">Адреса привязки</th>
						<th>Процесс / Служба</th>
						<th style="width: 85px;">PID</th>
						<th style="width: 130px; text-align: right;">Действие</th>
					</tr>
				</thead>
				<tbody>
					{#each groups as g (g.key)}
						<tr class:self-row={g.isSelf}>
							<td>
								<div class="proto-list">
									{#each g.protocols as proto}
										<span class="proto-badge {proto.startsWith('udp') ? 'udp' : 'tcp'}">
											{proto.toUpperCase()}
										</span>
									{/each}
								</div>
							</td>
							<td>
								<span class="port-num">{g.port}</span>
							</td>
							<td>
								<div class="addr-list">
									{#each g.addresses as addr}
										<code class="addr-item">{addr.ip}</code>
									{/each}
								</div>
							</td>
							<td>
								<div class="proc-cell">
									<div class="proc-title">
										<strong>{g.processName || '—'}</strong>
										<PortProcessBadges
											service={g.service}
											isSelf={g.isSelf}
											isCritical={g.isCritical}
											selfLabel="текущий"
											selfTitle="Текущий веб-сервер"
											criticalTitle="Системный процесс"
										/>
									</div>
									{#if g.cmdline}
										<div class="proc-cmd" title={g.cmdline}>{g.cmdline}</div>
									{:else if g.exe}
										<div class="proc-cmd" title={g.exe}>{g.exe}</div>
									{/if}
								</div>
							</td>
							<td>
								{#if g.pid}
									<code>{g.pid}</code>
								{:else}
									<span class="muted">—</span>
								{/if}
							</td>
							<td class="act-cell">
								{#if g.pid}
									<Button
										size="sm"
										variant={g.isCritical || g.isSelf ? 'secondary' : 'outline-danger'}
										disabled={busy}
										onclick={() => onkill(g)}
									>
										{#snippet iconBefore()}<Power size={13} />{/snippet}
										Освободить
									</Button>
								{:else}
									<span class="muted">—</span>
								{/if}
							</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	{/if}
</Card>

<style>
	.proto-list {
		display: flex;
		flex-direction: column;
		gap: 0.2rem;
	}
	.proto-badge {
		font-size: 0.7rem;
		font-weight: 700;
		padding: 0.15rem 0.4rem;
		border-radius: 4px;
		letter-spacing: 0.03em;
		display: inline-block;
		text-align: center;
	}
	.proto-badge.tcp {
		background: var(--color-accent-tint, rgba(59, 130, 246, 0.2));
		color: var(--color-accent, #60a5fa);
		border: 1px solid var(--color-accent-border, rgba(59, 130, 246, 0.4));
	}
	.proto-badge.udp {
		background: rgba(168, 85, 247, 0.18);
		color: #c084fc;
		border: 1px solid rgba(168, 85, 247, 0.35);
	}

	.port-num {
		font-weight: 700;
		font-size: 0.95rem;
		font-family: var(--font-mono, monospace);
		color: var(--color-text-primary);
	}

	.addr-list {
		display: flex;
		flex-direction: column;
		gap: 0.15rem;
	}
	.addr-item {
		font-size: 0.8rem;
		color: var(--color-text-secondary);
	}

	.table-header {
		display: flex;
		flex-wrap: wrap;
		gap: 0.75rem;
		justify-content: space-between;
		align-items: center;
		margin-bottom: 0.75rem;
	}
	.left-controls, .right-controls {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
		align-items: center;
	}
	.table-header h3 {
		margin: 0;
		font-size: 0.95rem;
		font-weight: 600;
		color: var(--color-text-primary);
	}
	.table-search-input {
		padding: 0.35rem 0.55rem;
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
		color: var(--color-text-primary);
		font-size: 0.82rem;
		min-width: 180px;
	}
	.table-search-input:focus {
		border-color: var(--color-accent);
		outline: none;
	}

	.table-wrap {
		max-height: 480px;
		overflow-y: auto;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm, 6px);
		background: var(--color-bg-secondary);
	}
	.ports-table {
		width: 100%;
		border-collapse: collapse;
		font-size: 0.85rem;
	}
	.ports-table th, .ports-table td {
		padding: 0.5rem 0.65rem;
		border-bottom: 1px solid var(--color-border);
		text-align: left;
		vertical-align: middle;
	}
	.ports-table th {
		position: sticky;
		top: 0;
		background: var(--color-bg-tertiary);
		color: var(--color-text-primary);
		font-size: 0.75rem;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		z-index: 1;
	}
	.proc-cell {
		display: flex;
		flex-direction: column;
		gap: 0.15rem;
		max-width: 320px;
	}
	.proc-title {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.35rem;
		color: var(--color-text-primary);
	}
	.proc-cmd {
		font-size: 0.75rem;
		color: var(--color-text-muted);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.act-cell {
		text-align: right;
	}

	.muted {
		color: var(--color-text-muted);
	}

	@media (max-width: 768px) {
		.table-header {
			flex-direction: column;
			align-items: stretch;
		}
	}
</style>
