<script lang="ts">
	import { Card } from '$lib/components/ui';
	import type { SystemServiceItem } from '$lib/api/client';
	import { stripAnsi } from '$lib/utils/ansi';
	import { RefreshCw, Play, Square, RotateCw, FileCode, Copy, Trash2, Search } from 'lucide-svelte';

	type ServiceAction = 'start' | 'stop' | 'restart';

	interface Props {
		/** Отфильтрованные службы для показа. */
		items: SystemServiceItem[];
		/** Общее число загруженных служб — отличает первую загрузку от пустого фильтра. */
		totalCount: number;
		loading: boolean;
		acting: string | null;
		searchQuery: string;
		onAction: (item: SystemServiceItem, action: ServiceAction) => void;
		onToggleEnable: (item: SystemServiceItem, enable: boolean) => void;
		onEdit: (item: SystemServiceItem) => void;
		onClone: (item: SystemServiceItem) => void;
		onDelete: (item: SystemServiceItem) => void;
	}

	let {
		items,
		totalCount,
		loading,
		acting,
		searchQuery,
		onAction,
		onToggleEnable,
		onEdit,
		onClone,
		onDelete,
	}: Props = $props();

	function statusHint(item: SystemServiceItem): string {
		return stripAnsi(item.statusText || '');
	}
</script>

<Card padding="sm">
	{#if loading && totalCount === 0}
		<div class="empty-state">
			<RefreshCw size={24} class="spin" />
			<p>Загрузка списка служб…</p>
		</div>
	{:else if items.length === 0}
		<div class="empty-state">
			<Search size={24} class="muted" />
			<p>Службы не найдены по запросу «{searchQuery}»</p>
		</div>
	{:else}
		<div class="table-wrap">
			<table class="svc-table">
				<thead>
					<tr>
						<th style="width: 26%;">Служба</th>
						<th style="width: 20%;">Автозапуск</th>
						<th style="width: 22%;">Статус</th>
						<th style="text-align: right; width: 32%;">Действия</th>
					</tr>
				</thead>
				<tbody>
					{#each items as item (item.script)}
						<tr class:is-managed={item.managed} class:is-acting={acting === item.script}>
							<!-- Name & script -->
							<td class="col-name">
								<div class="name-cell-wrap">
									<div class="name-line">
										<span class="svc-name">{item.name}</span>
										{#if item.managed}
											<span class="badge-managed" title={item.managedHint}>Система</span>
										{/if}
									</div>
									<code class="svc-path" title={item.script}>{item.script}</code>
								</div>
							</td>

							<!-- Autostart toggle -->
							<td class="col-autostart">
								<div class="autostart-cell-wrap">
									<button
										type="button"
										class="btn-autostart"
										class:is-enabled={item.enabled}
										disabled={acting === item.script || (item.name === 'awg-manager' && item.enabled)}
										title={
											item.name === 'awg-manager' && item.enabled
												? 'Автозапуск AWG Manager нельзя отключить'
												: item.enabled
													? 'Автозапуск включен (Sxx). Нажмите для выключения (Kxx)'
													: 'Автозапуск выключен (Kxx). Нажмите для включения (Sxx)'
										}
										onclick={() => onToggleEnable(item, !item.enabled)}
									>
										<span class="autostart-indicator"></span>
										<span class="autostart-label">{item.enabled ? 'ВКЛ (S)' : 'ВЫКЛ (K)'}</span>
									</button>
								</div>
							</td>

							<!-- Status -->
							<td class="col-status">
								<div class="status-cell-wrap">
									<span class="status-pill" class:running={item.running}>
										<span class="dot"></span>
										<span>{item.running ? 'Запущен' : 'Остановлен'}</span>
									</span>
									{#if statusHint(item)}
										<span class="status-hint-text" title={statusHint(item)}>{statusHint(item)}</span>
									{/if}
								</div>
							</td>

							<!-- Actions -->
							<td class="col-actions">
								<div class="action-buttons-group">
									<!-- Start -->
									<button
										type="button"
										class="btn-act btn-start"
										disabled={acting === item.script || item.running}
										title="Запустить службу"
										onclick={() => onAction(item, 'start')}
									>
										<Play size={12} />
										<span>Старт</span>
									</button>

									<!-- Stop -->
									<button
										type="button"
										class="btn-act btn-stop"
										disabled={acting === item.script || !item.running}
										title="Остановить службу"
										onclick={() => onAction(item, 'stop')}
									>
										<Square size={12} />
										<span>Стоп</span>
									</button>

									<!-- Restart -->
									<button
										type="button"
										class="btn-act btn-restart"
										disabled={acting === item.script}
										title="Перезапустить службу"
										onclick={() => onAction(item, 'restart')}
									>
										<RotateCw size={12} class={acting === item.script ? 'spin' : ''} />
										<span>Рестарт</span>
									</button>

									<!-- Edit Code -->
									<button
										type="button"
										class="btn-act btn-edit"
										title="Просмотреть / Редактировать скрипт"
										onclick={() => onEdit(item)}
									>
										<FileCode size={12} />
										<span>Скрипт</span>
									</button>

									<!-- Clone -->
									<button
										type="button"
										class="btn-act btn-clone"
										title="Клонировать эту службу"
										onclick={() => onClone(item)}
									>
										<Copy size={12} />
									</button>

									<!-- Delete -->
									{#if !item.managed && item.name !== 'awg-manager'}
										<button
											type="button"
											class="btn-act btn-delete"
											title="Удалить службу с роутера"
											onclick={() => onDelete(item)}
										>
											<Trash2 size={12} />
										</button>
									{/if}
								</div>
							</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	{/if}
</Card>

<style>
	/* Table */
	.table-wrap {
		overflow-x: auto;
		border-radius: var(--radius-sm, 6px);
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
	}

	.svc-table {
		width: 100%;
		border-collapse: collapse;
		font-size: 0.82rem;
	}

	.svc-table th, .svc-table td {
		padding: 0.55rem 0.75rem;
		border-bottom: 1px solid var(--color-border);
		text-align: left;
		vertical-align: middle;
	}

	.svc-table th {
		background: var(--color-bg-tertiary);
		color: var(--color-text-primary);
		font-size: 0.72rem;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.03em;
	}

	.svc-table tr:hover {
		background: var(--color-bg-hover, rgba(255, 255, 255, 0.03));
	}

	.svc-table tr.is-managed {
		background: var(--color-accent-tint, rgba(96, 165, 250, 0.04));
	}

	.name-cell-wrap {
		display: flex;
		flex-direction: column;
		gap: 0.15rem;
	}

	.name-line {
		display: flex;
		align-items: center;
		gap: 0.4rem;
	}

	.svc-name {
		font-weight: 700;
		color: var(--color-text-primary);
		font-size: 0.88rem;
	}

	.badge-managed {
		font-size: 0.65rem;
		font-weight: 700;
		padding: 0.05rem 0.3rem;
		border-radius: 3px;
		background: rgba(96, 165, 250, 0.18);
		color: #60a5fa;
	}

	.svc-path {
		font-size: 0.73rem;
		color: var(--color-text-muted);
		font-family: var(--font-mono, monospace);
	}

	.status-cell-wrap {
		display: flex;
		flex-direction: column;
		gap: 0.15rem;
	}

	.status-pill {
		display: inline-flex;
		align-items: center;
		gap: 0.35rem;
		font-size: 0.75rem;
		font-weight: 600;
		padding: 0.12rem 0.5rem;
		border-radius: 999px;
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		color: var(--color-text-muted);
		width: fit-content;
	}
	.status-pill .dot {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		background: #94a3b8;
	}
	.status-pill.running {
		background: rgba(16, 185, 129, 0.12);
		border-color: rgba(16, 185, 129, 0.35);
		color: #059669;
	}
	:global(.dark) .status-pill.running {
		background: rgba(16, 185, 129, 0.15);
		border-color: rgba(16, 185, 129, 0.35);
		color: #34d399;
	}
	.status-pill.running .dot {
		background: #10b981;
		box-shadow: 0 0 6px rgba(16, 185, 129, 0.6);
	}

	.status-hint-text {
		font-size: 0.72rem;
		color: var(--color-text-muted);
		max-width: 240px;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	/* Autostart button toggle */
	.autostart-cell-wrap {
		display: flex;
		align-items: center;
	}

	.btn-autostart {
		display: inline-flex;
		align-items: center;
		gap: 0.35rem;
		padding: 0.15rem 0.5rem;
		font-size: 0.75rem;
		font-weight: 600;
		border-radius: 999px;
		border: 1px solid var(--color-border);
		background: var(--color-bg-tertiary);
		color: var(--color-text-muted);
		cursor: pointer;
		transition: all 0.15s ease;
		user-select: none;
	}
	.btn-autostart:hover:not(:disabled) {
		background: var(--color-bg-hover, rgba(255, 255, 255, 0.08));
		color: var(--color-text-primary);
	}
	.btn-autostart:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}

	.autostart-indicator {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		background: #94a3b8;
		transition: background 0.15s ease, box-shadow 0.15s ease;
	}

	.btn-autostart.is-enabled {
		background: rgba(59, 130, 246, 0.12);
		border-color: rgba(59, 130, 246, 0.35);
		color: #2563eb;
	}
	:global(.dark) .btn-autostart.is-enabled {
		background: rgba(59, 130, 246, 0.15);
		border-color: rgba(59, 130, 246, 0.35);
		color: #60a5fa;
	}
	.btn-autostart.is-enabled .autostart-indicator {
		background: #3b82f6;
		box-shadow: 0 0 6px rgba(59, 130, 246, 0.6);
	}

	.action-buttons-group {
		display: flex;
		align-items: center;
		justify-content: flex-end;
		gap: 0.3rem;
		flex-wrap: wrap;
	}

	.btn-act {
		display: inline-flex;
		align-items: center;
		gap: 0.25rem;
		padding: 0.25rem 0.45rem;
		font-size: 0.75rem;
		font-weight: 600;
		border-radius: var(--radius-sm, 5px);
		border: 1px solid var(--color-border);
		background: var(--color-bg-tertiary);
		color: var(--color-text-secondary);
		cursor: pointer;
		transition: all 0.15s ease;
	}
	.btn-act:hover:not(:disabled) {
		background: var(--color-bg-hover, rgba(255,255,255,0.08));
		color: var(--color-text-primary);
	}
	.btn-act:disabled {
		opacity: 0.4;
		cursor: not-allowed;
	}

	.btn-start:hover:not(:disabled) {
		background: rgba(16, 185, 129, 0.15);
		color: #10b981;
		border-color: #10b981;
	}
	.btn-stop:hover:not(:disabled) {
		background: rgba(245, 158, 11, 0.15);
		color: #f59e0b;
		border-color: #f59e0b;
	}
	.btn-delete:hover:not(:disabled) {
		background: rgba(239, 68, 68, 0.15);
		color: #ef4444;
		border-color: #ef4444;
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
