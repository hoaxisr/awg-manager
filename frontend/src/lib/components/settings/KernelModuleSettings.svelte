<script lang="ts">
	import { LoadingSpinner } from '$lib/components/layout';
	import { Modal } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import type { SystemInfo } from '$lib/types';

	interface Props {
		systemInfo: SystemInfo | null;
		onRefreshSystemInfo?: () => void;
	}

	let { systemInfo, onRefreshSystemInfo }: Props = $props();

	let downloading = $state(false);
	let showAltInstall = $state(false);

	async function handleDownload() {
		downloading = true;
		try {
			await api.downloadKmod();
			onRefreshSystemInfo?.();
		} catch {
			onRefreshSystemInfo?.();
		} finally {
			downloading = false;
		}
	}

	let dlStatus = $derived(systemInfo?.kernelModuleDownloadStatus ?? 'not_needed');
	let isDownloading = $derived(dlStatus === 'downloading' || downloading);

	let kernelStatusText = $derived.by(() => {
		if (systemInfo?.kernelModuleLoaded) return 'Загружен';
		if (isDownloading) return 'Скачивание...';
		if (dlStatus === 'downloaded' && systemInfo?.kernelModuleExists) return 'Скачан, не загружен';
		if (dlStatus === 'download_failed') return 'Ошибка загрузки';
		if (dlStatus === 'unsupported') return 'Не поддерживается';
		if (systemInfo?.kernelModuleExists) return 'Не загружен';
		return 'Отсутствует';
	});

	let kernelStatusClass = $derived.by(() => {
		if (systemInfo?.kernelModuleLoaded) return 'status-loaded';
		if (isDownloading) return 'status-downloading';
		if (dlStatus === 'downloaded' && systemInfo?.kernelModuleExists) return 'status-exists';
		if (dlStatus === 'download_failed') return 'status-error';
		if (dlStatus === 'unsupported') return 'status-missing';
		if (systemInfo?.kernelModuleExists) return 'status-exists';
		return 'status-missing';
	});

	let showDownloadButton = $derived(
		dlStatus === 'download_failed' ||
		(!systemInfo?.kernelModuleLoaded && !systemInfo?.kernelModuleExists && dlStatus === 'not_needed')
	);
</script>

<div>
	<div class="header">
		<span class="font-medium">Модуль ядра</span>
		<div class="header-status">
			{#if isDownloading}
				<span class="status-badge status-downloading">
					<LoadingSpinner size="sm" />
					<span>Скачивание...</span>
				</span>
			{:else}
				<span class="status-badge {kernelStatusClass}">{kernelStatusText}</span>
			{/if}
		</div>
	</div>

	{#if systemInfo?.kernelModuleModel}
		<div class="info-row">
			<span class="info-label">Модель:</span>
			<span class="info-value">{systemInfo.kernelModuleModel}</span>
		</div>
	{/if}

	{#if dlStatus === 'download_failed'}
		<div class="notice notice-error">
			<div class="notice-content">
				<span>Не удалось скачать модуль ядра{systemInfo?.kernelModuleModel ? ` для ${systemInfo.kernelModuleModel}` : ''}.</span>
				{#if systemInfo?.kernelModuleDownloadError}
					<span class="error-detail">{systemInfo.kernelModuleDownloadError}</span>
				{/if}
			</div>
			<button class="btn btn-sm btn-retry" onclick={handleDownload} disabled={downloading}>
				{downloading ? 'Скачивание...' : 'Повторить'}
			</button>
		</div>
	{:else if dlStatus === 'unsupported'}
		<div class="notice">
			Модель {systemInfo?.kernelModuleModel || 'роутера'} не поддерживается — модуль ядра недоступен.
		</div>
	{:else if !systemInfo?.kernelModuleLoaded && systemInfo?.kernelModuleExists}
		<div class="notice">
			Модуль ядра установлен, но не загружен. Будет загружен при следующем запуске.
		</div>
	{:else if showDownloadButton}
		<div class="notice">
			<span>Модуль ядра не найден.</span>
			<button class="btn btn-sm btn-primary" onclick={handleDownload} disabled={downloading}>
				{downloading ? 'Скачивание...' : 'Скачать'}
			</button>
		</div>
	{:else if systemInfo?.kernelModuleLoaded}
		<div class="notice notice-ok">
			Модуль ядра загружен и работает.
		</div>
	{/if}

	<button class="btn-link" onclick={() => showAltInstall = true}>
		Другие способы установки AWG
	</button>
</div>

<Modal bind:open={showAltInstall} title="Другие способы установки AWG" size="sm" onclose={() => showAltInstall = false}>
	<p class="alt-install-text">
		Вы можете использовать AmneziaWG по
		<a href="https://gitlab.com/ShidlaSGC/keenetic-entware-awg-go/-/blob/main/README.md" target="_blank" rel="noopener">данной инструкции</a>.
	</p>
</Modal>

<style>
	.header {
		display: flex;
		align-items: baseline;
		justify-content: space-between;
		margin-bottom: 0.75rem;
		flex-wrap: wrap;
		gap: 0.5rem;
	}

	.header-status {
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}

	.status-badge {
		padding: 0.125rem 0.5rem;
		border-radius: 4px;
		font-size: 0.75rem;
		font-weight: 500;
	}

	.status-loaded {
		background: var(--success-bg, rgba(34, 197, 94, 0.1));
		color: var(--success, #22c55e);
	}

	.status-exists {
		background: var(--warning-bg, rgba(234, 179, 8, 0.1));
		color: var(--warning, #eab308);
	}

	.status-missing {
		background: var(--bg-tertiary);
		color: var(--text-muted);
	}

	.status-error {
		background: var(--error-bg, rgba(239, 68, 68, 0.1));
		color: var(--error, #ef4444);
	}

	.status-downloading {
		display: inline-flex;
		align-items: center;
		gap: 0.375rem;
		background: var(--accent-bg, rgba(99, 102, 241, 0.1));
		color: var(--accent, #6366f1);
	}

	.info-row {
		display: flex;
		gap: 0.5rem;
		font-size: 0.8125rem;
		margin-bottom: 0.5rem;
	}

	.info-label {
		color: var(--text-muted);
	}

	.info-value {
		color: var(--text-secondary);
	}

	.notice {
		margin-top: 0.5rem;
		padding: 0.625rem 0.75rem;
		background: var(--bg-tertiary);
		border-radius: 6px;
		font-size: 0.8125rem;
		color: var(--text-secondary);
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.75rem;
	}

	.notice-error {
		background: var(--error-bg, rgba(239, 68, 68, 0.1));
		color: var(--error, #ef4444);
	}

	.notice-ok {
		background: var(--success-bg, rgba(34, 197, 94, 0.1));
		color: var(--success, #22c55e);
	}

	.notice-content {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
	}

	.error-detail {
		font-size: 0.75rem;
		opacity: 0.8;
	}

	.btn-sm {
		padding: 0.25rem 0.75rem;
		font-size: 0.8125rem;
		border-radius: 4px;
		white-space: nowrap;
	}

	.btn-retry {
		background: var(--error, #ef4444);
		color: white;
		border: none;
		cursor: pointer;
		flex-shrink: 0;
	}

	.btn-retry:hover:not(:disabled) {
		opacity: 0.9;
	}

	.btn-retry:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}

	.btn-link {
		background: none;
		border: none;
		color: var(--accent, #6366f1);
		font-size: 0.8125rem;
		cursor: pointer;
		padding: 0;
		margin-top: 0.5rem;
	}

	.btn-link:hover {
		text-decoration: underline;
	}

	.alt-install-text {
		font-size: 0.875rem;
		color: var(--text-secondary);
		line-height: 1.5;
	}

	.alt-install-text a {
		color: var(--accent, #6366f1);
		text-decoration: underline;
	}
</style>
