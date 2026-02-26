<script lang="ts">
	import { Modal } from '$lib/components/ui';
	import { LoadingSpinner } from '$lib/components/layout';
	import { api } from '$lib/api/client';
	import type { Settings, SystemInfo, KmodVersionsInfo } from '$lib/types';

	interface Props {
		settings: Settings;
		systemInfo: SystemInfo | null;
		saving: boolean;
		onModeChange: (mode: 'auto' | 'kernel' | 'userspace') => void;
		onRestart: (mode: 'auto' | 'kernel' | 'userspace') => Promise<void>;
		onRefreshSystemInfo?: () => void;
	}

	let { settings, systemInfo, saving, onModeChange, onRestart, onRefreshSystemInfo }: Props = $props();

	let showWarning = $state(false);
	let pendingMode: 'auto' | 'kernel' | 'userspace' | null = $state(null);
	let modalStep: 'confirm' | 'saved' | 'restarting' = $state('confirm');
	let savedMode: 'auto' | 'kernel' | 'userspace' | null = $state(null);
	let restarting = $state(false);
	let downloading = $state(false);
	// Show pending selection while confirmation is open, otherwise actual setting
	let displayMode = $derived(pendingMode ?? settings.backendMode);

	// Kmod version selector state
	let kmodVersions: KmodVersionsInfo | null = $state(null);
	let kmodVersionsLoading = $state(false);
	let selectedKmodVersion = $state('');
	let showKmodSwapModal = $state(false);
	let kmodSwapping = $state(false);

	let showKmodSection = $derived(
		systemInfo?.isAarch64 && systemInfo?.activeBackend === 'kernel'
	);

	$effect(() => {
		if (showKmodSection && !kmodVersions && !kmodVersionsLoading) {
			loadKmodVersions();
		}
	});

	async function loadKmodVersions() {
		kmodVersionsLoading = true;
		try {
			kmodVersions = await api.getKmodVersions();
			selectedKmodVersion = kmodVersions.current || kmodVersions.recommended;
		} catch {
			/* ignore */
		} finally {
			kmodVersionsLoading = false;
		}
	}

	function handleKmodSwap() {
		if (selectedKmodVersion && selectedKmodVersion !== kmodVersions?.current) {
			showKmodSwapModal = true;
		}
	}

	async function confirmKmodSwap() {
		kmodSwapping = true;
		try {
			await api.swapKmod(selectedKmodVersion);
			await new Promise(r => setTimeout(r, 3000));
			window.location.reload();
		} catch {
			kmodSwapping = false;
			showKmodSwapModal = false;
		}
	}

	function cancelKmodSwap() {
		showKmodSwapModal = false;
	}

	function handleModeSelect(mode: 'auto' | 'kernel' | 'userspace') {
		if (mode !== settings.backendMode) {
			pendingMode = mode;
			modalStep = 'confirm';
			showWarning = true;
		}
	}

	function confirmChange() {
		if (pendingMode) {
			onModeChange(pendingMode);
			savedMode = pendingMode;
			pendingMode = null;
			modalStep = 'saved';
		}
	}

	async function handleRestart() {
		if (!savedMode) return;
		modalStep = 'restarting';
		restarting = true;
		try {
			await onRestart(savedMode);
		} finally {
			restarting = false;
			resetModal();
		}
	}

	function cancelChange() {
		resetModal();
	}

	function closeSaved() {
		resetModal();
	}

	function resetModal() {
		showWarning = false;
		pendingMode = null;
		savedMode = null;
		modalStep = 'confirm';
		restarting = false;
	}

	async function handleDownloadRetry() {
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

	let modalTitle = $derived.by(() => {
		if (modalStep === 'restarting') return 'Перезапуск';
		if (modalStep === 'saved') return 'Режим изменён';
		return 'Подтверждение';
	});

	let targetMode = $derived(pendingMode ?? savedMode);
	let isTargetKernel = $derived(targetMode === 'kernel');

	const modeLabels = {
		auto: 'Авто (рекомендуется)',
		kernel: 'Модуль ядра',
		userspace: 'Userspace'
	} as const;

	const modeDescriptions = {
		auto: 'Модуль ядра если загружен, иначе userspace',
		kernel: 'amneziawg.ko — максимальная производительность',
		userspace: 'amneziawg-go — работает без модуля ядра'
	} as const;

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

	let activeBackendText = $derived(
		systemInfo?.activeBackend === 'kernel' ? 'Модуль ядра' : 'Userspace'
	);
</script>

<div>
	<div class="header">
		<span class="font-medium">Режим работы</span>
		<div class="header-status">
			{#if isDownloading}
				<span class="status-badge status-downloading">
					<LoadingSpinner size="sm" />
					<span>Скачивание...</span>
				</span>
			{:else}
				<span class="status-badge {kernelStatusClass}">{kernelStatusText}</span>
			{/if}
			<span class="status-separator">·</span>
			<span class="active-backend">{activeBackendText}</span>
		</div>
	</div>

	<div class="mode-list">
		{#each (['auto', 'kernel', 'userspace'] as const) as mode}
			{@const isDisabled = saving || (mode === 'kernel' && !systemInfo?.kernelModuleLoaded)}
			<label class="mode-item" class:disabled={isDisabled} class:selected={displayMode === mode}>
				<input
					type="radio"
					name="backendMode"
					value={mode}
					checked={displayMode === mode}
					disabled={isDisabled}
					onchange={() => handleModeSelect(mode)}
				/>
				<span class="mode-label">{modeLabels[mode]}</span>
				<span class="mode-desc">{modeDescriptions[mode]}</span>
			</label>
		{/each}
	</div>

	{#if dlStatus === 'download_failed'}
		<div class="notice notice-error">
			<div class="notice-content">
				<span>Не удалось скачать модуль ядра{systemInfo?.kernelModuleModel ? ` для ${systemInfo.kernelModuleModel}` : ''}.</span>
				{#if systemInfo?.kernelModuleDownloadError}
					<span class="error-detail">{systemInfo.kernelModuleDownloadError}</span>
				{/if}
			</div>
			<button class="btn btn-sm btn-retry" onclick={handleDownloadRetry} disabled={downloading}>
				{downloading ? 'Скачивание...' : 'Повторить'}
			</button>
		</div>
	{:else if dlStatus === 'unsupported'}
		<div class="notice">
			Модель {systemInfo?.kernelModuleModel || 'роутера'} не поддерживается — модуль ядра недоступен.
		</div>
	{:else if dlStatus === 'downloaded' && !systemInfo?.kernelModuleLoaded}
		<div class="notice">
			Модуль ядра скачан, но не загружен. Будет загружен при следующем запуске.
		</div>
	{:else if !systemInfo?.kernelModuleLoaded && systemInfo?.kernelModuleExists}
		<div class="notice">
			Модуль ядра установлен, но не загружен. Будет загружен при следующем запуске.
		</div>
	{:else if !systemInfo?.kernelModuleExists && dlStatus === 'not_needed' && !systemInfo?.kernelModuleLoaded}
		<div class="notice">
			Модуль ядра не найден. Будет скачан автоматически при запуске.
		</div>
	{/if}
</div>

{#if showKmodSection}
	<div class="kmod-version-section">
		<div class="header">
			<span class="font-medium">Версия модуля ядра</span>
			{#if systemInfo?.kernelModuleVersion}
				<span class="kmod-current-version">{systemInfo.kernelModuleVersion}</span>
			{/if}
		</div>

		{#if kmodVersionsLoading}
			<div class="kmod-loading">
				<LoadingSpinner size="sm" />
			</div>
		{:else if kmodVersions}
			<div class="kmod-controls">
				<select
					class="kmod-select"
					bind:value={selectedKmodVersion}
					disabled={kmodSwapping}
				>
					{#each kmodVersions.versions as ver}
						<option value={ver}>
							{ver}{ver === kmodVersions.recommended ? ' (рекомендуется)' : ''}{ver === kmodVersions.current ? ' — текущая' : ''}
						</option>
					{/each}
				</select>
				<button
					class="btn btn-sm btn-apply"
					onclick={handleKmodSwap}
					disabled={kmodSwapping || selectedKmodVersion === kmodVersions.current}
				>
					Применить
				</button>
			</div>
		{/if}
	</div>
{/if}

<Modal open={showWarning} title={modalTitle} onclose={modalStep === 'restarting' ? () => {} : cancelChange}>
	{#if modalStep === 'confirm'}
		<div class="modal-description">
			<p>Переключить режим на <strong>{pendingMode ? modeLabels[pendingMode] : ''}</strong>?</p>
			{#if isTargetKernel}
				<p>При использовании модуля ядра статистика туннеля не будет отображаться в веб-интерфейсе роутера. Статистику можно посмотреть в AWG Manager.</p>
			{/if}
			<p class="warning-text">Интерфейсы туннелей будут пересозданы. Привязки к политикам маршрутизации и маршрутам в интерфейсе роутера будут сброшены.</p>
			<p>Конфигурация туннелей (ключи, endpoint, параметры) сохранится.</p>
		</div>
	{:else if modalStep === 'saved'}
		<div class="modal-description">
			<p>Режим изменён на <strong>{savedMode ? modeLabels[savedMode] : ''}</strong>.</p>
			<p>Для применения необходим перезапуск awg-manager.</p>
			<p class="warning-text">Интерфейсы туннелей будут удалены и созданы заново.</p>
		</div>
	{:else if modalStep === 'restarting'}
		<div class="restart-status">
			<LoadingSpinner size="md" />
			<p>Перезапуск awg-manager...</p>
		</div>
	{/if}

	{#snippet actions()}
		{#if modalStep === 'confirm'}
			<button class="btn btn-secondary" onclick={cancelChange}>Отмена</button>
			<button class="btn btn-primary" onclick={confirmChange} disabled={saving}>
				{saving ? 'Сохранение...' : 'Подтвердить'}
			</button>
		{:else if modalStep === 'saved'}
			<button class="btn btn-secondary" onclick={closeSaved}>Позже</button>
			<button class="btn btn-warning" onclick={handleRestart}>
				Перезапустить
			</button>
		{/if}
	{/snippet}
</Modal>

<Modal open={showKmodSwapModal} title={kmodSwapping ? 'Перезапуск' : 'Смена версии модуля ядра'} onclose={kmodSwapping ? () => {} : cancelKmodSwap}>
	{#if kmodSwapping}
		<div class="restart-status">
			<LoadingSpinner size="md" />
			<p>Перезапуск awg-manager...</p>
		</div>
	{:else}
		<div class="modal-description">
			<p>Сменить версию модуля ядра на <strong>{selectedKmodVersion}</strong>?</p>
			<p>Для смены версии все туннели будут остановлены и удалены. После перезапуска туннели будут пересозданы автоматически.</p>
			{#if kmodVersions && selectedKmodVersion < kmodVersions.recommended}
				<p class="warning-text">Понижение версии может привести к нестабильности на некоторых моделях роутеров.</p>
			{/if}
		</div>
	{/if}

	{#snippet actions()}
		{#if !kmodSwapping}
			<button class="btn btn-secondary" onclick={cancelKmodSwap}>Отмена</button>
			<button class="btn btn-warning" onclick={confirmKmodSwap}>
				Применить и перезапустить
			</button>
		{/if}
	{/snippet}
</Modal>

<style>
	.header {
		display: flex;
		align-items: baseline;
		justify-content: space-between;
		margin-bottom: 1rem;
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

	.status-separator {
		color: var(--text-muted);
	}

	.active-backend {
		font-size: 0.8125rem;
		color: var(--text-secondary);
	}

	.mode-list {
		display: flex;
		flex-direction: column;
		gap: 0.125rem;
	}

	.mode-item {
		display: flex !important;
		align-items: baseline;
		gap: 0.5rem;
		padding: 0.5rem 0.25rem;
		margin-bottom: 0;
		border-radius: 6px;
		cursor: pointer;
		transition: background 0.15s;
	}

	.mode-item:hover:not(.disabled) {
		background: var(--bg-tertiary);
	}

	.mode-item.selected {
		background: var(--bg-tertiary);
	}

	.mode-item.disabled {
		opacity: 0.4;
		cursor: not-allowed;
	}

	.mode-item input[type="radio"] {
		width: auto;
		padding: 0;
		border: none;
		background: none;
		accent-color: var(--accent);
		flex-shrink: 0;
	}

	:global([data-theme="light"]) .mode-item input[type="radio"] {
		filter: invert(1);
	}

	.mode-label {
		font-weight: 500;
		color: var(--text-primary);
		white-space: nowrap;
	}

	.mode-desc {
		font-size: 0.8125rem;
		color: var(--text-muted);
	}

	.notice {
		margin-top: 0.75rem;
		padding: 0.625rem 0.75rem;
		background: var(--bg-tertiary);
		border-radius: 6px;
		font-size: 0.8125rem;
		color: var(--text-secondary);
	}

	.notice-error {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.75rem;
		background: var(--error-bg, rgba(239, 68, 68, 0.1));
		color: var(--error, #ef4444);
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

	.modal-description {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		color: var(--text-secondary);
	}

	.modal-description p {
		margin: 0;
	}

	.warning-text {
		color: var(--warning, #eab308);
		font-weight: 500;
	}

	.restart-status {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 1rem;
		padding: 1.5rem 0;
		color: var(--text-secondary);
	}

	.restart-status p {
		margin: 0;
	}

	.kmod-version-section {
		margin-top: 1.25rem;
		padding-top: 1rem;
		border-top: 1px solid var(--border);
	}

	.kmod-current-version {
		font-size: 0.8125rem;
		color: var(--text-muted);
	}

	.kmod-loading {
		display: flex;
		justify-content: center;
		padding: 0.5rem 0;
	}

	.kmod-controls {
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}

	.kmod-select {
		flex: 1;
		padding: 0.375rem 0.5rem;
		border: 1px solid var(--border);
		border-radius: 6px;
		background: var(--bg-secondary);
		color: var(--text-primary);
		font-size: 0.8125rem;
	}

	.btn-apply {
		background: var(--accent, #6366f1);
		color: white;
		border: none;
		cursor: pointer;
		flex-shrink: 0;
	}

	.btn-apply:hover:not(:disabled) {
		opacity: 0.9;
	}

	.btn-apply:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}
</style>
