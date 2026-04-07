<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/stores';
	import { tunnels } from '$lib/stores/tunnels';
	import { notifications } from '$lib/stores/notifications';
	import { PageContainer } from '$lib/components/layout';
	import { decodeVpnLink, isVpnLink } from '$lib/utils/vpnlink';
	import { api } from '$lib/api/client';
	import type { SystemInfo } from '$lib/types';

	const initialTab = (['file', 'paste', 'link'] as const).includes(
		$page.url.searchParams.get('tab') as 'file' | 'paste' | 'link'
	) ? $page.url.searchParams.get('tab') as 'file' | 'paste' | 'link' : 'file';

	let loading = $state(false);
	let importContent = $state('');
	let importName = $state('');
	let fileInput = $state<HTMLInputElement>();
	let dragOver = $state(false);
	let activeTab = $state<'file' | 'paste' | 'link'>(initialTab);
	let linkInput = $state('');
	let linkPreview = $state('');
	let linkError = $state('');
	let systemInfo = $state<SystemInfo | null>(null);
	let selectedBackend = $state<'nativewg' | 'kernel'>('nativewg');

	$effect(() => {
		api.getSystemInfo().then(info => {
			systemInfo = info;
			if (info.backendAvailability && !info.backendAvailability.nativewg && info.backendAvailability.kernel) {
				selectedBackend = 'kernel';
			}
		}).catch(() => {});
	});

	function handleFileSelect(event: Event) {
		const input = event.target as HTMLInputElement;
		if (input.files && input.files[0]) {
			readFile(input.files[0]);
		}
	}

	function handleDrop(event: DragEvent) {
		event.preventDefault();
		dragOver = false;
		if (event.dataTransfer?.files && event.dataTransfer.files[0]) {
			readFile(event.dataTransfer.files[0]);
		}
	}

	function handleDragOver(event: DragEvent) {
		event.preventDefault();
		dragOver = true;
	}

	function handleDragLeave() {
		dragOver = false;
	}

	function readFile(file: File) {
		if (!importName) {
			const baseName = file.name.replace(/\.conf$/i, '');
			importName = baseName;
		}

		const reader = new FileReader();
		reader.onload = (e) => {
			const content = e.target?.result as string;
			if (content) {
				importContent = content;
				notifications.success(`Файл "${file.name}" загружен`);
			}
		};
		reader.onerror = () => {
			notifications.error('Не удалось прочитать файл');
		};
		reader.readAsText(file);
	}

	function handleLinkInput() {
		linkError = '';
		linkPreview = '';

		if (!linkInput.trim()) {
			importContent = '';
			return;
		}

		try {
			const result = decodeVpnLink(linkInput);
			linkPreview = result.config;
			importContent = result.config;
			if (result.name && !importName) {
				importName = result.name;
			}
		} catch (e) {
			linkError = e instanceof Error ? e.message : 'Ошибка декодирования';
			importContent = '';
		}
	}

	async function handleImport() {
		let content = importContent.trim();
		if (!content) {
			notifications.error('Вставьте содержимое конфигурации, загрузите файл или вставьте vpn:// ссылку');
			return;
		}

		// Auto-detect vpn:// in paste tab
		if (isVpnLink(content)) {
			try {
				const result = decodeVpnLink(content);
				content = result.config;
				if (result.name && !importName) {
					importName = result.name;
				}
			} catch (e) {
				notifications.error(e instanceof Error ? e.message : 'Ошибка декодирования vpn:// ссылки');
				return;
			}
		}

		loading = true;
		try {
			const tunnel = await tunnels.importConfig(content, importName, selectedBackend);
			if (tunnel.warnings?.length) {
				tunnel.warnings.forEach(w => notifications.warning(w));
			}
			notifications.success('Туннель успешно импортирован');
			goto(`/tunnels/${tunnel.id}`);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка импорта');
		} finally {
			loading = false;
		}
	}
</script>

<svelte:head>
	<title>Новый туннель - AWG Manager</title>
</svelte:head>

<PageContainer>
<div class="page-header">
	<a href="/" class="back-link">
		<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="20" height="20">
			<line x1="19" y1="12" x2="5" y2="12"/>
			<polyline points="12 19 5 12 12 5"/>
		</svg>
		Назад
	</a>
	<h1 class="page-title">Новый туннель</h1>
</div>

<div class="import-container">
	<label class="field-label" for="import-name">Название туннеля</label>
	<div class="top-row">
		<input type="text" id="import-name" class="name-input" bind:value={importName} placeholder="Мой VPN">
		<button class="btn btn-primary btn-import" onclick={handleImport} disabled={loading || !importContent.trim()}>
			{#if loading}
				<span class="spinner"></span>
			{/if}
			Импортировать
		</button>
	</div>

	<div class="backend-selector">
		<span class="field-label">Режим работы</span>
		<div class="backend-options">
			<button
				type="button"
				class="backend-option"
				class:selected={selectedBackend === 'nativewg'}
				class:disabled={systemInfo !== null && !systemInfo.backendAvailability?.nativewg}
				disabled={systemInfo !== null && !systemInfo.backendAvailability?.nativewg}
				title={systemInfo !== null && !systemInfo.backendAvailability?.nativewg ? 'Прошивка не поддерживает нативный WireGuard' : ''}
				onclick={() => selectedBackend = 'nativewg'}
			>
				<span class="backend-name">NativeWG</span>
				<span class="backend-desc">DNS/IP маршрутизация, failover, виден в UI роутера</span>
			</button>
			<button
				type="button"
				class="backend-option"
				class:selected={selectedBackend === 'kernel'}
				class:disabled={systemInfo !== null && !systemInfo.backendAvailability?.kernel}
				disabled={systemInfo !== null && !systemInfo.backendAvailability?.kernel}
				title={systemInfo !== null && !systemInfo.backendAvailability?.kernel ? 'Модуль ядра не загружен' : ''}
				onclick={() => selectedBackend = 'kernel'}
			>
				<span class="backend-name">Kernel</span>
				<span class="backend-desc">Без интеграции в роутер, для сторонних проектов</span>
			</button>
		</div>
	</div>

	<div class="tabs">
		<button
			class="tab"
			class:tab-active={activeTab === 'file'}
			onclick={() => activeTab = 'file'}
		>
			<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="16" height="16">
				<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/>
				<polyline points="17 8 12 3 7 8"/>
				<line x1="12" y1="3" x2="12" y2="15"/>
			</svg>
			Загрузить файл
		</button>
		<button
			class="tab"
			class:tab-active={activeTab === 'paste'}
			onclick={() => activeTab = 'paste'}
		>
			<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="16" height="16">
				<path d="M16 4h2a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h2"/>
				<rect x="8" y="2" width="8" height="4" rx="1" ry="1"/>
			</svg>
			Вставить текст
		</button>
		<button
			class="tab"
			class:tab-active={activeTab === 'link'}
			onclick={() => activeTab = 'link'}
		>
			<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="16" height="16">
				<path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71"/>
				<path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71"/>
			</svg>
			Ссылка
		</button>
	</div>

	<div class="tab-content">
		{#if activeTab === 'file'}
			<div
				class="file-drop-zone"
				class:drag-over={dragOver}
				class:has-content={!!importContent.trim()}
				ondrop={handleDrop}
				ondragover={handleDragOver}
				ondragleave={handleDragLeave}
				role="button"
				tabindex="0"
				onclick={() => fileInput?.click()}
				onkeydown={(e) => e.key === 'Enter' && fileInput?.click()}
			>
				<input
					type="file"
					accept=".conf"
					bind:this={fileInput}
					onchange={handleFileSelect}
					style="display: none"
				>
				{#if importContent.trim()}
					<div class="drop-content">
						<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" width="48" height="48">
							<polyline points="20 6 9 17 4 12"/>
						</svg>
						<div class="drop-text">
							<p class="drop-title">Файл загружен</p>
							<p class="drop-hint">Нажмите чтобы заменить</p>
						</div>
					</div>
				{:else}
					<div class="drop-content">
						<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" width="48" height="48">
							<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/>
							<polyline points="17 8 12 3 7 8"/>
							<line x1="12" y1="3" x2="12" y2="15"/>
						</svg>
						<div class="drop-text">
							<p class="drop-title">Перетащите .conf файл сюда</p>
							<p class="drop-hint">или нажмите для выбора</p>
						</div>
					</div>
				{/if}
			</div>
		{:else if activeTab === 'paste'}
			<textarea
				class="config-textarea"
				bind:value={importContent}
				placeholder="[Interface]
PrivateKey = ...
Address = 10.0.0.2/32

[Peer]
PublicKey = ...
Endpoint = vpn.example.com:51820
AllowedIPs = 0.0.0.0/0"
			></textarea>
		{:else if activeTab === 'link'}
			<textarea
				class="config-textarea link-input"
				bind:value={linkInput}
				oninput={handleLinkInput}
				placeholder="Вставьте vpn:// ссылку из AmneziaVPN"
			></textarea>
			{#if linkError}
				<p class="link-error">{linkError}</p>
			{/if}
			{#if linkPreview}
				<textarea
					class="config-textarea link-preview"
					readonly
					value={linkPreview}
				></textarea>
			{/if}
		{/if}
	</div>

	<p class="form-hint">
		Поддерживаются WireGuard и AmneziaWG конфигурации с параметрами Jc, Jmin, Jmax, S1-S4, H1-H4, I1-I5, а также vpn:// ссылки AmneziaVPN.
	</p>
</div>
</PageContainer>

<style>
	.back-link {
		display: flex;
		align-items: center;
		gap: 4px;
		color: var(--text-secondary);
		font-size: 13px;
		padding: 6px 10px;
		border-radius: 6px;
		transition: all 0.15s;
	}

	.back-link:hover {
		background: var(--bg-tertiary);
		color: var(--text-primary);
	}

	.import-container {
		max-width: 700px;
		margin: 0 auto;
		padding: 0 1rem;
	}

	.field-label {
		display: block;
		font-size: 13px;
		font-weight: 500;
		color: var(--text-secondary);
		margin-bottom: 6px;
	}

	.top-row {
		display: flex;
		align-items: stretch;
		gap: 12px;
		margin-bottom: 1.5rem;
	}

	.name-input {
		flex: 1;
		min-width: 0;
		height: 42px;
		padding: 0 12px;
		font-size: 14px;
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		border-radius: 6px;
		color: var(--text-primary);
		transition: border-color 0.15s;
	}

	.name-input:focus {
		outline: none;
		border-color: var(--accent);
	}

	.btn-import {
		padding: 0 24px !important;
		font-size: 14px;
		white-space: nowrap;
		flex-shrink: 0;
	}

	.tabs {
		display: flex;
		border-bottom: 1px solid var(--border);
		gap: 0;
	}

	.tab {
		display: flex;
		align-items: center;
		gap: 6px;
		padding: 10px 16px;
		font-size: 13px;
		font-weight: 500;
		color: var(--text-muted);
		background: none;
		border: none;
		border-bottom: 2px solid transparent;
		cursor: pointer;
		transition: all 0.15s;
		margin-bottom: -1px;
	}

	.tab:hover {
		color: var(--text-secondary);
	}

	.tab-active {
		color: var(--accent);
		border-bottom-color: var(--accent);
	}

	.tab-content {
		margin-top: 0;
	}

	.config-textarea {
		width: 100%;
		min-height: 300px;
		padding: 14px;
		font-family: monospace;
		font-size: 13px;
		line-height: 1.5;
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		border-top: none;
		border-radius: 0 0 8px 8px;
		color: var(--text-primary);
		resize: vertical;
		transition: border-color 0.15s;
		white-space: pre;
		overflow-x: auto;
	}

	.config-textarea:focus {
		outline: none;
		border-color: var(--accent);
	}

	.config-textarea::placeholder {
		color: var(--text-muted);
	}

	.file-drop-zone {
		min-height: 220px;
		border: 2px dashed var(--border);
		border-top: 2px dashed var(--border);
		border-radius: 0 0 8px 8px;
		display: flex;
		align-items: center;
		justify-content: center;
		cursor: pointer;
		transition: all 0.15s ease;
		background: transparent;
	}

	.file-drop-zone:hover {
		border-color: var(--accent);
		background: var(--bg-tertiary);
	}

	.file-drop-zone.drag-over {
		border-color: var(--accent);
		background: rgba(122, 162, 247, 0.1);
	}

	.file-drop-zone.has-content {
		border-color: var(--success);
		border-style: solid;
	}

	.file-drop-zone.has-content svg {
		color: var(--success);
	}

	.drop-content {
		display: flex;
		align-items: center;
		gap: 16px;
	}

	.drop-content svg {
		color: var(--text-muted);
		flex-shrink: 0;
	}

	.drop-title {
		font-size: 17px;
		font-weight: 500;
		color: var(--text-primary);
		margin-bottom: 4px;
	}

	.drop-hint {
		font-size: 14px;
		color: var(--text-muted);
	}

	.link-input {
		min-height: 100px;
		border-radius: 0 0 8px 8px;
	}

	.link-error {
		font-size: 13px;
		color: var(--error);
		margin: 8px 0 0;
		padding: 0 2px;
	}

	.link-preview {
		min-height: 200px;
		margin-top: 12px;
		border: 1px solid var(--border);
		border-radius: 8px;
		opacity: 0.85;
		cursor: default;
	}

	.form-hint {
		font-size: 12px;
		color: var(--text-muted);
		margin-top: 1rem;
	}

	.spinner {
		width: 16px;
		height: 16px;
		border: 2px solid currentColor;
		border-top-color: transparent;
		border-radius: 50%;
		animation: spin 0.8s linear infinite;
		margin-right: 8px;
	}

	@keyframes spin {
		to { transform: rotate(360deg); }
	}

	.backend-selector {
		margin-bottom: 1.5rem;
	}

	.backend-options {
		display: flex;
		gap: 12px;
		margin-top: 6px;
	}

	.backend-option {
		flex: 1;
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		gap: 4px;
		padding: 14px;
		border: 1px solid var(--border);
		border-radius: 8px;
		cursor: pointer;
		transition: all 0.15s;
		background: var(--bg-secondary);
		text-align: center;
	}

	.backend-option:hover:not(.disabled) {
		border-color: var(--accent);
	}

	.backend-option.selected {
		border-color: var(--accent);
		background: rgba(122, 162, 247, 0.08);
	}

	.backend-option.disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}

	.backend-name {
		font-size: 14px;
		font-weight: 500;
		color: var(--text-primary);
	}

	.backend-desc {
		font-size: 12px;
		color: var(--text-muted);
	}

	@media (max-width: 500px) {
		.top-row {
			flex-direction: column;
			align-items: stretch;
		}

		.name-input {
			max-width: none;
		}

		.btn-import {
			align-self: flex-start;
		}

		.backend-options {
			flex-direction: column;
		}
	}
</style>
