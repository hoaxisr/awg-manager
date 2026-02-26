<script lang="ts">
	import { onDestroy } from 'svelte';
	import { api } from '$lib/api/client';
	import type { DiagnosticsStatus } from '$lib/types';
	import { PageContainer, PageHeader } from '$lib/components/layout';
	import { notifications } from '$lib/stores/notifications';

	let status = $state<DiagnosticsStatus>({ status: 'idle', progress: '' });
	let pollTimer = $state<number | null>(null);
	let starting = $state(false);

	function stopPolling() {
		if (pollTimer) {
			clearInterval(pollTimer);
			pollTimer = null;
		}
	}

	async function pollStatus() {
		try {
			status = await api.getDiagnosticsStatus();
			if (status.status === 'done' || status.status === 'error' || status.status === 'idle') {
				stopPolling();
			}
		} catch {
			stopPolling();
		}
	}

	async function startDiagnostics() {
		starting = true;
		try {
			await api.runDiagnostics();
			status = { status: 'running', progress: 'Запуск...' };
			pollTimer = setInterval(pollStatus, 2000) as unknown as number;
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка запуска');
		} finally {
			starting = false;
		}
	}

	async function downloadReport() {
		try {
			await api.downloadDiagnosticsReport();
		} catch {
			notifications.error('Ошибка скачивания отчёта');
		}
	}

	onDestroy(stopPolling);
</script>

<svelte:head>
	<title>Диагностика - AWG Manager</title>
</svelte:head>

<PageContainer>
	<PageHeader title="Диагностика" />

	<div class="settings-stack">
		<div class="card">
			<div class="card-body">
				<h3 class="card-title">Диагностический отчёт</h3>
				<p class="card-description">
					Собирает информацию о системе, туннелях, маршрутах и запускает тесты.
					Результат сохраняется в JSON-файл для отправки разработчику.
				</p>

				{#if status.status === 'idle'}
					<div class="warning-box">
						Диагностика временно перезапустит туннели. Соединение будет прервано на ~15 сек.
					</div>
					<button
						class="btn btn-primary"
						onclick={startDiagnostics}
						disabled={starting}
					>
						{starting ? 'Запуск...' : 'Запустить диагностику'}
					</button>

				{:else if status.status === 'running'}
					<div class="progress-section">
						<div class="progress-bar">
							<div class="progress-bar-fill"></div>
						</div>
						<p class="progress-text">{status.progress}</p>
					</div>

				{:else if status.status === 'done'}
					<div class="result-section">
						<p class="result-text">Диагностика завершена</p>
						<div class="result-actions">
							<button class="btn btn-primary" onclick={downloadReport}>
								Скачать отчёт
							</button>
							<button class="btn btn-secondary" onclick={startDiagnostics}>
								Запустить снова
							</button>
						</div>
					</div>

				{:else if status.status === 'error'}
					<div class="error-box">
						<p>Ошибка: {status.error}</p>
					</div>
					<button class="btn btn-primary" onclick={startDiagnostics}>
						Попробовать снова
					</button>
				{/if}
			</div>
		</div>
	</div>
</PageContainer>

<style>
	.card-description {
		color: var(--text-secondary);
		font-size: 14px;
		margin-bottom: 16px;
		line-height: 1.5;
	}

	.warning-box {
		background: rgba(234, 179, 8, 0.1);
		border: 1px solid rgba(234, 179, 8, 0.3);
		border-radius: 8px;
		padding: 12px 16px;
		margin-bottom: 16px;
		color: var(--text-secondary);
		font-size: 13px;
	}

	.error-box {
		background: rgba(239, 68, 68, 0.1);
		border: 1px solid rgba(239, 68, 68, 0.3);
		border-radius: 8px;
		padding: 12px 16px;
		margin-bottom: 16px;
		color: var(--text-secondary);
	}

	.progress-section {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}

	.progress-bar {
		height: 4px;
		background: var(--bg-tertiary);
		border-radius: 2px;
		overflow: hidden;
	}

	.progress-bar-fill {
		height: 100%;
		background: var(--accent);
		border-radius: 2px;
		animation: indeterminate 1.5s ease-in-out infinite;
	}

	@keyframes indeterminate {
		0% { width: 0%; margin-left: 0; }
		50% { width: 60%; margin-left: 20%; }
		100% { width: 0%; margin-left: 100%; }
	}

	.progress-text {
		color: var(--text-secondary);
		font-size: 14px;
	}

	.result-section {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}

	.result-text {
		color: var(--accent);
		font-weight: 500;
	}

	.result-actions {
		display: flex;
		gap: 8px;
	}
</style>
