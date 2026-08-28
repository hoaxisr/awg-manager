<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/stores';
	import { tunnels } from '$lib/stores/tunnels';
	import { notifications } from '$lib/stores/notifications';
	import { PageContainer } from '$lib/components/layout';
	import { BackLink, Button } from '$lib/components/ui';
	import TunnelConfigImportPanel, {
		type TunnelImportTab
	} from '$lib/components/tunnels/TunnelConfigImportPanel.svelte';
	import { decodeVpnLink, isVpnLink, vpnLinkUnsupportedPortalReason } from '$lib/utils/vpnlink';
	import { nativewgUnavailableHint } from '$lib/utils/backendAvailability';
	import { api } from '$lib/api/client';
	import type { SystemInfo } from '$lib/types';

	function normalizeTunnelImportTab(raw: string | null): TunnelImportTab {
		if (raw === 'file' || raw === 'paste' || raw === 'vpn') return raw;
		if (raw === 'link' || raw === 'premium') return 'vpn';
		return 'file';
	}

	const initialTab = normalizeTunnelImportTab($page.url.searchParams.get('tab'));

	let loading = $state(false);
	let importContent = $state('');
	let importName = $state('');
	let activeTab = $state<TunnelImportTab>(initialTab);
	let vpnPasteInput = $state('');
	let linkPreview = $state('');
	let systemInfo = $state<SystemInfo | null>(null);
	let selectedBackend = $state<'nativewg' | 'kernel'>('nativewg');

	let nativewgHint = $derived(
		systemInfo !== null && !systemInfo.backendAvailability?.nativewg
			? nativewgUnavailableHint(systemInfo.nativewgReason)
			: ''
	);

	// Sync activeTab → URL (?tab=). Mirrors what canonical Tabs urlParam
	// does — kept inline because this page uses a custom div-based tab UI.
	$effect(() => {
		const t = activeTab;
		const sp = new URLSearchParams($page.url.search);
		if (t === 'file') {
			sp.delete('tab');
		} else {
			sp.set('tab', t === 'vpn' ? 'vpn' : t);
		}
		const nextSearch = sp.toString();
		if (nextSearch === $page.url.searchParams.toString()) return;
		const target = $page.url.pathname + (nextSearch ? `?${nextSearch}` : '') + $page.url.hash;
		void goto(target, { replaceState: true, keepFocus: true, noScroll: true });
	});

	$effect(() => {
		api.getSystemInfo().then(info => {
			systemInfo = info;
			if (info.backendAvailability && !info.backendAvailability.nativewg && info.backendAvailability.kernel) {
				selectedBackend = 'kernel';
			}
		}).catch(() => {});
	});

	function handleFileLoaded(file: File) {
		if (!importName) {
			importName = file.name.replace(/\.conf$/i, '');
		}
	}

	/** Импорт из сырого текста (.conf или vpn:// с клиентским конфигом). Обновляет importContent после успешного декода vpn:// */
	async function executeImport(rawContent: string) {
		let content = rawContent.trim();
		if (!content) {
			notifications.error('Вставьте содержимое конфигурации, загрузите файл или вставьте vpn:// ссылку');
			return;
		}

		if (isVpnLink(content)) {
			const unsupported = vpnLinkUnsupportedPortalReason(content);
			if (unsupported) {
				notifications.error(unsupported);
				return;
			}
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

		importContent = content;

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

	async function handleImport() {
		await executeImport(importContent);
	}

	async function handlePremiumCountryConfig(config: string, meta: { suggestedName?: string }) {
		if (!importName && meta.suggestedName) {
			importName = meta.suggestedName;
		}
		await executeImport(config);
	}

</script>

<svelte:head>
	<title>Новый туннель - AWG Manager</title>
</svelte:head>

<PageContainer>
<div class="page-header">
	<BackLink href="/" />
	<h2 class="page-title">Новый туннель</h2>
</div>

<div class="import-container">
	<label class="field-label" for="import-name">Название туннеля</label>
	<div class="top-row">
		<input type="text" id="import-name" class="name-input" bind:value={importName} placeholder="Мой VPN">
		<div class="btn-import-wrap">
			<Button variant="primary" size="md" onclick={handleImport} disabled={!importContent.trim()} loading={loading}>
				Импортировать
			</Button>
		</div>
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
				title={nativewgHint}
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
				<span class="backend-desc">Через OpkgTun и модуль ядра, с поддержкой до AWG 3.1</span>
			</button>
		</div>
		{#if nativewgHint}
			<p class="backend-hint">{nativewgHint}</p>
		{/if}
	</div>

	<TunnelConfigImportPanel
		variant="page"
		bind:importContent
		bind:activeTab
		bind:vpnPasteInput
		bind:linkPreview
		onfileloaded={(file) => handleFileLoaded(file)}
		onregularconfig={(meta) => {
			if (meta.suggestedName && !importName) importName = meta.suggestedName;
		}}
		oncountryconfig={handlePremiumCountryConfig}
	/>

	<p class="form-hint">
		Поддерживаются WireGuard и AmneziaWG конфигурации с параметрами Jc, Jmin, Jmax, S1-S4, H1-H4, I1-I5; вкладка vpn:// распознаёт клиентский конфиг в ссылке или ключ Premium (запрос списка стран через прокси cp.amnezia.org).
	</p>
</div>
</PageContainer>

<style>
	.import-container {
		max-width: 700px;
		margin: 0 auto;
		padding: 0 1rem;
	}

	.field-label {
		display: block;
		font-size: 13px;
		font-weight: 500;
		color: var(--color-text-secondary);
		margin-bottom: 6px;
	}

	.top-row {
		display: flex;
		align-items: center;
		gap: 12px;
		margin-bottom: 1.5rem;
	}

	.name-input {
		flex: 1;
		min-width: 0;
		box-sizing: border-box;
		height: 42px;
		padding: 0 12px;
		font-size: 14px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: 6px;
		color: var(--color-text-primary);
		transition: border-color 0.15s;
	}

	.name-input:focus {
		outline: none;
		border-color: var(--color-accent);
	}

	.btn-import-wrap {
		display: flex;
		align-items: center;
		flex-shrink: 0;
	}

	.btn-import-wrap :global(.btn.size-md) {
		box-sizing: border-box;
		height: 42px;
		min-height: 42px;
		max-height: 42px;
		padding-block: 0;
		padding-inline: 24px;
		font-size: 14px;
	}

	.form-hint {
		font-size: 12px;
		color: var(--color-text-muted);
		margin-top: 1rem;
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
		border: 1px solid var(--color-border);
		border-radius: 8px;
		cursor: pointer;
		transition: all 0.15s;
		background: var(--color-bg-secondary);
		text-align: center;
	}

	.backend-option:hover:not(.disabled) {
		border-color: var(--color-accent);
	}

	.backend-option.selected {
		border-color: var(--color-accent);
		background: rgba(122, 162, 247, 0.08);
	}

	.backend-option.disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}

	.backend-name {
		font-size: 14px;
		font-weight: 500;
		color: var(--color-text-primary);
	}

	.backend-desc {
		font-size: 12px;
		color: var(--color-text-muted);
	}

	.backend-hint {
		margin: 8px 0 0;
		font-size: 12px;
		line-height: 1.4;
		color: var(--color-text-muted);
	}

	@media (max-width: 640px) {
		.top-row {
			flex-direction: column;
			align-items: stretch;
		}

		.name-input {
			width: 100%;
			max-width: none;
		}

		.btn-import-wrap {
			width: 100%;
		}

		.btn-import-wrap :global(.btn.size-md) {
			width: 100%;
		}

		.backend-options {
			flex-direction: column;
		}
	}
</style>
