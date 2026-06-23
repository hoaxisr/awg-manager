<script lang="ts">
	import AmneziaConfEditor from './AmneziaConfEditor.svelte';
	import VpnLinkPasteImport from './VpnLinkPasteImport.svelte';
	import { notifications } from '$lib/stores/notifications';
	import {
		getVpnPastePresentation,
		PREMIUM_VPN_KEY_STORAGE
	} from '$lib/utils/amneziaPremiumVpnPaste';
	import { Upload, Clipboard, Crown, Link, Check } from 'lucide-svelte';

	export type TunnelImportTab = 'file' | 'paste' | 'vpn';

	interface CountryConfigMeta {
		suggestedName?: string;
		countryCode: string;
		countryLabel: string;
	}

	interface Props {
		variant?: 'page' | 'modal';
		importContent?: string;
		activeTab?: TunnelImportTab;
		vpnPasteInput?: string;
		linkPreview?: string;
		storageKey?: string;
		loadStoredKeyOnMount?: boolean;
		pastePlaceholder?: string;
		oncountryconfig?: (config: string, meta: CountryConfigMeta) => void | Promise<void>;
		onregularconfig?: (meta: { suggestedName?: string }) => void;
		/** Вызывается после успешного чтения файла (например, подсказка имени). */
		onfileloaded?: (file: File, content: string) => void;
	}

	let {
		variant = 'page',
		importContent = $bindable(''),
		activeTab = $bindable<TunnelImportTab>('file'),
		vpnPasteInput = $bindable(''),
		linkPreview = $bindable(''),
		storageKey = PREMIUM_VPN_KEY_STORAGE,
		loadStoredKeyOnMount = true,
		pastePlaceholder = '[Interface]\nPrivateKey = ...\nAddress = 10.0.0.2/32\n\n[Peer]\nPublicKey = ...\nEndpoint = vpn.example.com:51820\nAllowedIPs = 0.0.0.0/0',
		oncountryconfig,
		onregularconfig,
		onfileloaded
	}: Props = $props();

	let fileInput = $state<HTMLInputElement>();
	let dragOver = $state(false);
	let vpnPasteImport = $state<VpnLinkPasteImport>();

	let vpnPastePresentation = $derived(getVpnPastePresentation(vpnPasteInput));

	function handleFileSelect(event: Event) {
		const input = event.target as HTMLInputElement;
		if (input.files?.[0]) {
			readFile(input.files[0]);
		}
	}

	function handleDrop(event: DragEvent) {
		event.preventDefault();
		dragOver = false;
		if (event.dataTransfer?.files?.[0]) {
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
		const reader = new FileReader();
		reader.onload = (e) => {
			const content = e.target?.result as string;
			if (content && content.trim()) {
				importContent = content;
				onfileloaded?.(file, content);
				notifications.success(`Файл "${file.name}" загружен`);
			} else {
				notifications.error(`Файл "${file.name}" пуст`);
			}
		};
		reader.onerror = () => {
			notifications.error('Не удалось прочитать файл');
		};
		reader.readAsText(file);
	}

	export function activateVpnTab() {
		activeTab = 'vpn';
		void vpnPasteImport?.analyzeNow();
	}
</script>

<div class="import-panel" class:import-panel--modal={variant === 'modal'}>
	<div class="tabs">
		<button
			type="button"
			class="tab"
			class:tab-active={activeTab === 'file'}
			onclick={() => (activeTab = 'file')}
		>
			<Upload size={16} />
			{variant === 'page' ? 'Загрузить файл' : 'Файл'}
		</button>
		<button
			type="button"
			class="tab"
			class:tab-active={activeTab === 'paste'}
			onclick={() => (activeTab = 'paste')}
		>
			<Clipboard size={16} />
			Вставить текст
		</button>
		<button type="button" class="tab" class:tab-active={activeTab === 'vpn'} onclick={activateVpnTab}>
			{#if vpnPastePresentation.kind === 'premium'}
				<Crown size={16} aria-hidden="true" />
			{:else}
				<Link size={16} aria-hidden="true" />
			{/if}
			{vpnPastePresentation.label}
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
					accept=".conf,text/plain,application/octet-stream"
					bind:this={fileInput}
					onchange={handleFileSelect}
					style="display: none"
				>
				{#if importContent.trim()}
					<div class="drop-content">
						<Check size={variant === 'page' ? 48 : 36} strokeWidth={1.5} style="flex-shrink:0; color:var(--import-success)" />
						<div class="drop-text">
							<p class="drop-title">Файл загружен</p>
							<p class="drop-hint">Нажмите чтобы заменить</p>
						</div>
					</div>
				{:else}
					<div class="drop-content">
						<Upload size={variant === 'page' ? 48 : 36} strokeWidth={1.5} style="flex-shrink:0; color:var(--import-text-muted)" />
						<div class="drop-text">
							<p class="drop-title">Перетащите .conf файл сюда</p>
							<p class="drop-hint">или нажмите для выбора</p>
						</div>
					</div>
				{/if}
			</div>
		{:else if activeTab === 'paste'}
			<AmneziaConfEditor
				bind:value={importContent}
				variant={variant === 'modal' ? 'modal' : 'page'}
				placeholder={pastePlaceholder}
			/>
		{:else if activeTab === 'vpn'}
			<VpnLinkPasteImport
				bind:this={vpnPasteImport}
				bind:value={vpnPasteInput}
				bind:configContent={importContent}
				bind:linkPreview
				{storageKey}
				{variant}
				{loadStoredKeyOnMount}
				{oncountryconfig}
				{onregularconfig}
			/>
		{/if}
	</div>
</div>

<style>
	.import-panel--modal {
		--import-border: var(--border);
		--import-text-muted: var(--text-muted);
		--import-text-secondary: var(--text-secondary);
		--import-accent: var(--accent);
		--import-bg-tertiary: var(--bg-tertiary);
		--import-success: var(--success);
	}

	.import-panel:not(.import-panel--modal) {
		--import-border: var(--color-border);
		--import-text-muted: var(--color-text-muted);
		--import-text-secondary: var(--color-text-secondary);
		--import-accent: var(--color-accent);
		--import-bg-tertiary: var(--color-bg-tertiary);
		--import-success: var(--color-success);
	}

	.tabs {
		display: flex;
		border-bottom: 1px solid var(--import-border);
		gap: 0;
	}

	.tab {
		display: flex;
		align-items: center;
		gap: 6px;
		padding: var(--import-tab-pad, 10px 16px);
		font-size: var(--import-tab-font, 13px);
		font-weight: 500;
		color: var(--import-text-muted);
		background: none;
		border: none;
		border-bottom: 2px solid transparent;
		cursor: pointer;
		transition: all 0.15s;
		margin-bottom: -1px;
	}

	.import-panel--modal .tab {
		--import-tab-pad: 8px 14px;
		--import-tab-font: 0.8125rem;
	}

	.tab:hover {
		color: var(--import-text-secondary);
	}

	.tab-active {
		color: var(--import-accent);
		border-bottom-color: var(--import-accent);
	}

	.tab-content {
		margin-top: 16px;
		padding: 0;
	}

	.file-drop-zone {
		min-height: var(--import-drop-min-h, 220px);
		border: 2px dashed var(--import-border);
		border-radius: 8px;
		display: flex;
		align-items: center;
		justify-content: center;
		cursor: pointer;
		transition: all 0.15s ease;
		background: transparent;
	}

	.import-panel--modal .file-drop-zone {
		--import-drop-min-h: 140px;
	}

	.file-drop-zone:hover {
		border-color: var(--import-accent);
		background: var(--import-bg-tertiary);
	}

	.file-drop-zone.drag-over {
		border-color: var(--import-accent);
		background: rgba(122, 162, 247, 0.1);
	}

	.file-drop-zone.has-content {
		border-color: var(--import-success);
		border-style: solid;
	}

	.drop-content {
		display: flex;
		align-items: center;
		gap: var(--import-drop-gap, 16px);
	}

	.import-panel--modal .drop-content {
		--import-drop-gap: 12px;
	}

	.drop-title {
		font-size: var(--import-drop-title, 17px);
		font-weight: 500;
		color: var(--color-text-primary, var(--text-primary));
		margin-bottom: 4px;
	}

	.import-panel--modal .drop-title {
		--import-drop-title: 0.875rem;
		margin-bottom: 2px;
	}

	.drop-hint {
		font-size: var(--import-drop-hint, 14px);
		color: var(--import-text-muted);
	}

	.import-panel--modal .drop-hint {
		--import-drop-hint: 0.75rem;
	}

	@media (max-width: 640px) {
		.import-panel:not(.import-panel--modal) .tabs {
			flex-direction: column;
			align-items: stretch;
			gap: 6px;
			border-bottom: none;
			margin-bottom: 4px;
		}

		.import-panel:not(.import-panel--modal) .tab {
			width: 100%;
			justify-content: flex-start;
			margin-bottom: 0;
			border: 1px solid var(--import-border);
			border-radius: var(--radius-sm);
		}

		.import-panel:not(.import-panel--modal) .tab-active {
			background: var(--color-accent-tint);
			border-color: var(--import-accent);
			border-bottom-color: var(--import-accent);
		}
	}
</style>
