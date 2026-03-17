<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { PageContainer, LoadingSpinner, EmptyState } from '$lib/components/layout';
	import { formatDate } from '$lib/utils/format';
	import type { LogsResponse } from '$lib/types';

	let logsResponse: LogsResponse | null = $state(null);
	let loading = $state(true);
	let loadError = $state(false);
	let clearing = $state(false);
	let filterCategory = $state('');
	let filterLevel = $state('');
	let pollInterval: number | null = $state(null);

	onMount(async () => {
		await loadData();
		pollInterval = window.setInterval(loadData, 10000);
	});

	onDestroy(() => {
		if (pollInterval) {
			clearInterval(pollInterval);
		}
	});

	async function loadData() {
		try {
			logsResponse = await api.getLogs(filterCategory || undefined, filterLevel || undefined);
			loadError = false;
		} catch (e) {
			if (!logsResponse) {
				loadError = true;
			}
		} finally {
			loading = false;
		}
	}

	async function clearLogs() {
		clearing = true;
		try {
			await api.clearLogs();
			notifications.success('Логи очищены');
			await loadData();
		} catch (e) {
			notifications.error('Не удалось очистить логи');
		} finally {
			clearing = false;
		}
	}

	async function copyToClipboard() {
		if (!logsResponse?.logs.length) return;

		const text = logsResponse.logs.map(log => {
			const time = formatDate(log.timestamp);
			const error = log.error ? ` | Error: ${log.error}` : '';
			return `[${time}] [${log.level.toUpperCase()}] [${log.category}] ${log.action} ${log.target}: ${log.message}${error}`;
		}).join('\n');

		try {
			// Clipboard API requires HTTPS; use fallback for HTTP (router access)
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
		} catch (e) {
			notifications.error('Не удалось скопировать');
		}
	}

	function getLevelClass(level: string): string {
		switch (level) {
			case 'error': return 'level-error';
			case 'warn': return 'level-warn';
			default: return 'level-info';
		}
	}

	function getLevelLabel(level: string): string {
		switch (level) {
			case 'error': return 'ERROR';
			case 'warn': return 'WARN';
			default: return 'INFO';
		}
	}

	function getCategoryLabel(category: string): string {
		switch (category) {
			case 'tunnel': return 'Туннель';
			case 'peer': return 'Пир';
			case 'settings': return 'Настройки';
			case 'system': return 'Система';
			case 'dns-route': return 'DNS-маршруты';
			default: return category;
		}
	}

	let filteredLogs = $derived((logsResponse as LogsResponse | null)?.logs ?? []);
</script>

<svelte:head>
	<title>Логи - AWG Manager</title>
</svelte:head>

<PageContainer>
	{#if logsResponse?.enabled}
		<div class="actions-bar">
			<button
				class="btn btn-secondary"
				onclick={copyToClipboard}
				disabled={!filteredLogs.length}
				title="Копировать в буфер обмена"
			>
				<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="16" height="16">
					<rect x="9" y="9" width="13" height="13" rx="2" ry="2"/>
					<path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>
				</svg>
				Копировать
			</button>
			<button
				class="btn btn-danger"
				onclick={clearLogs}
				disabled={clearing || !filteredLogs.length}
			>
				{clearing ? 'Очистка...' : 'Очистить'}
			</button>
		</div>
	{/if}

	{#if loading}
		<div class="flex justify-center py-12">
			<LoadingSpinner size="lg" message="Загрузка журнала..." />
		</div>
	{:else if loadError}
		<div class="card">
			<EmptyState
				title="Ошибка загрузки"
				description="Не удалось загрузить данные журнала. Попробуйте обновить страницу."
			>
				{#snippet icon()}
					<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="48" height="48">
						<circle cx="12" cy="12" r="10"/>
						<line x1="12" y1="8" x2="12" y2="12"/>
						<circle cx="12" cy="16" r="1" fill="currentColor"/>
					</svg>
				{/snippet}
			</EmptyState>
		</div>
	{:else if !logsResponse?.enabled}
		<div class="card">
			<EmptyState
				title="Логирование отключено"
				description="Включите логирование в настройках для записи событий."
			>
				{#snippet icon()}
					<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="48" height="48">
						<path d="M10.29 3.86L1.82 18a2 2 0 001.71 3h16.94a2 2 0 001.71-3L13.71 3.86a2 2 0 00-3.42 0z"/>
						<line x1="12" y1="9" x2="12" y2="13"/>
						<circle cx="12" cy="17" r="1" fill="currentColor"/>
					</svg>
				{/snippet}
				{#snippet action()}
					<a href="/settings" class="btn btn-primary">Открыть настройки</a>
				{/snippet}
			</EmptyState>
		</div>
	{:else}
		<div class="card mt-2">
			<div class="flex gap-4 mb-6 flex-wrap">
				<div class="flex flex-col gap-1">
					<label for="filterCategory" class="text-xs uppercase text-surface-400">Категория</label>
					<select id="filterCategory" class="filter-select" bind:value={filterCategory} onchange={loadData}>
						<option value="">Все</option>
						<option value="tunnel">Туннель</option>
						<option value="peer">Пир</option>
						<option value="settings">Настройки</option>
						<option value="system">Система</option>
						<option value="dns-route">DNS-маршруты</option>
					</select>
				</div>
				<div class="flex flex-col gap-1">
					<label for="filterLevel" class="text-xs uppercase text-surface-400">Уровень</label>
					<select id="filterLevel" class="filter-select" bind:value={filterLevel} onchange={loadData}>
						<option value="">Все</option>
						<option value="info">INFO</option>
						<option value="warn">WARN</option>
						<option value="error">ERROR</option>
					</select>
				</div>
			</div>

			{#if filteredLogs.length === 0}
				<p class="text-surface-400">Нет записей в журнале</p>
			{:else}
				<div class="overflow-x-auto">
					<table>
						<thead>
							<tr>
								<th>Время</th>
								<th>Уровень</th>
								<th>Категория</th>
								<th>Действие</th>
								<th>Цель</th>
								<th>Сообщение</th>
							</tr>
						</thead>
						<tbody>
							{#each filteredLogs.slice(0, 100) as log}
								<tr class={getLevelClass(log.level)}>
									<td class="font-mono text-[13px] whitespace-nowrap">{formatDate(log.timestamp)}</td>
									<td>
										<span class="level-badge {getLevelClass(log.level)} px-1.5 py-0.5 rounded text-[11px] font-semibold">
											{getLevelLabel(log.level)}
										</span>
									</td>
									<td>{getCategoryLabel(log.category)}</td>
									<td>{log.action}</td>
									<td class="font-mono text-[13px]">{log.target || '-'}</td>
									<td class="max-w-[300px]">
										{log.message}
										{#if log.error}
											<span class="block text-xs text-error-500 truncate max-w-[200px] cursor-help" title={log.error}>({log.error})</span>
										{/if}
									</td>
								</tr>
							{/each}
						</tbody>
					</table>
				</div>
				{#if filteredLogs.length > 100}
					<p class="text-surface-400 mt-2">Показаны последние 100 записей из {filteredLogs.length}</p>
				{/if}
			{/if}
		</div>
	{/if}
</PageContainer>

<style>
	.actions-bar {
		display: flex;
		justify-content: flex-end;
		gap: 0.5rem;
		margin-bottom: 1rem;
	}

	/* Filter select — uses CSS variables not expressible in Tailwind */
	.filter-select {
		padding: 0.375rem 0.75rem;
		background: var(--bg-tertiary);
		border: 1px solid var(--border);
		border-radius: 6px;
		color: var(--text-primary);
		font-size: 0.875rem;
		min-width: 120px;
	}

	.overflow-x-auto {
		cursor: default;
	}

	/* Table element selectors */
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

	/* Level badge colors */
	.level-info {
		background: var(--bg-tertiary);
		color: var(--text-secondary);
	}

	.level-warn {
		background: var(--warning-bg, rgba(234, 179, 8, 0.1));
		color: var(--warning, #eab308);
	}

	.level-error {
		background: var(--danger-bg, rgba(239, 68, 68, 0.1));
		color: var(--danger, #ef4444);
	}

	/* Row highlighting for error/warn levels */
	tr.level-error {
		background: var(--danger-bg, rgba(239, 68, 68, 0.05));
	}

	tr.level-warn {
		background: var(--warning-bg, rgba(234, 179, 8, 0.05));
	}
</style>
