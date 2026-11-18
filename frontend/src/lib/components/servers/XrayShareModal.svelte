<script lang="ts">
	import { untrack } from 'svelte';
	import { Modal, Button } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import type { XrayShareLinks, XrayClient, XrayEndpointConfig } from '$lib/types';
	import { renderCrispQrDataUrl } from '$lib/utils/qrSvg';
	import { Copy, Download, Send, Layers } from 'lucide-svelte';

	interface Props {
		open: boolean;
		clientId: string;
		clientRemark: string;
		publicDomain?: string;
		endpoints?: XrayEndpointConfig[];
		initialEndpointId?: string;
		clients?: XrayClient[];
		onclose: () => void;
	}

	let {
		open = $bindable(false),
		clientId = $bindable(''),
		clientRemark = $bindable(''),
		publicDomain = '',
		endpoints = [],
		initialEndpointId = '',
		clients = [],
		onclose
	}: Props = $props();

	interface RouteOption {
		id: string;
		domain: string;
		description: string;
		mode?: string;
	}

	let links = $state<XrayShareLinks | null>(null);
	let loading = $state(false);
	let copyAllLoading = $state(false);
	let activeTab = $state<'vless' | 'finiteproto' | 'happ' | 'singbox' | 'mihomo'>('vless');
	let isPrimaryFinite = $derived(Boolean(links?.vless_url?.includes('mode=finiteproto')));
	let qrDataUrl = $state('');
	let qrGenerating = $state(false);
	let qrZoomOpen = $state(false);
	let availableDomains = $state<RouteOption[]>([]);
	let selectedDomain = $state('');
	let selectedEndpointId = $state('');
	let initializedForOpen = false;

	$effect(() => {
		if (open) {
			if (!initializedForOpen && clientId) {
				initializedForOpen = true;
				untrack(() => {
					void initModal();
				});
			}
		} else {
			initializedForOpen = false;
			links = null;
			qrDataUrl = '';
			qrZoomOpen = false;
			availableDomains = [];
			selectedDomain = '';
			selectedEndpointId = '';
		}
	});

	function cleanDomainDescription(raw?: string, domain?: string): string {
		const d = (raw || '').trim();
		if (d) {
			return d;
		}
		const dom = (domain || '').toLowerCase();
		if (dom.includes('edge.') || dom.includes('cdn.') || dom.includes('selcdn') || dom.includes('twcdn')) {
			return 'Через CDN';
		}
		if (dom.includes('netcraze.club') || dom.includes('crazedns') || dom.includes('keenetic.')) {
			return 'KeenDNS';
		}
		return 'Прямой адрес';
	}

	async function initModal() {
		loading = true;
		try {
			const domainList: RouteOption[] = [];
			const seen = new Set<string>();

			// 0. Configured Endpoints from XrayConfig
			if (endpoints && endpoints.length > 0) {
				for (const ep of endpoints) {
					if (!ep.enabled || !ep.public_domain) continue;
					const dom = ep.public_domain.split(',')[0].trim();
					if (!dom) continue;
					const key = `${ep.id}::${dom.toLowerCase()}`;
					if (!seen.has(key)) {
						seen.add(key);
						seen.add(dom.toLowerCase());
						domainList.push({
							id: ep.id,
							domain: dom,
							description: ep.name || cleanDomainDescription('', dom),
							mode: ep.mode || ep.transport || 'packet-up'
						});
					}
				}
			}

			// 1. Domains from Dispatcher (fallback if not covered by endpoints)
			const dispCfg = await api.getDispatcherConfig().catch(() => null);
			if (domainList.length === 0 && dispCfg?.routes) {
				for (const r of dispCfg.routes) {
					const domLower = (r.domain || '').toLowerCase();
					if (r.enabled && r.route_xray && r.domain && !seen.has(domLower)) {
						seen.add(domLower);
						domainList.push({
							id: '',
							domain: r.domain,
							description: cleanDomainDescription(r.description, r.domain)
						});
					}
				}
			}

			// 2. Domains from server PublicDomain (fallback)
			if (domainList.length === 0 && publicDomain) {
				const parts = publicDomain.split(',').map((d) => d.trim()).filter(Boolean);
				for (const p of parts) {
					const domLower = p.toLowerCase();
					if (!seen.has(domLower)) {
						seen.add(domLower);
						domainList.push({ id: '', domain: p, description: cleanDomainDescription('', p) });
					}
				}
			}

			availableDomains = domainList;

			if (availableDomains.length > 0) {
				const byInitial = initialEndpointId
					? availableDomains.find((d) => d.id === initialEndpointId || d.domain === initialEndpointId)
					: undefined;
				const preferred =
					byInitial ||
					availableDomains.find(
						(d) =>
							d.domain.includes('edge.') ||
							d.domain.includes('cdn.') ||
							d.domain.includes('selcdn') ||
							(dispCfg?.public_hostname && d.domain === dispCfg.public_hostname)
					) ||
					availableDomains[0];
				selectedDomain = preferred.domain;
				selectedEndpointId = preferred.id;
			}
			await loadLinks(selectedDomain || undefined, selectedEndpointId || undefined);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка загрузки данных подключения');
		} finally {
			loading = false;
		}
	}

	async function onClientChange(newClientId: string) {
		const found = clients.find((c) => c.id === newClientId);
		if (!found) return;
		clientId = found.id;
		clientRemark = found.remark;
		await loadLinks(selectedDomain || undefined, selectedEndpointId || undefined);
	}

	async function onRouteChange(val: string) {
		const found = availableDomains.find((d) => (d.id ? d.id === val : d.domain === val));
		if (found) {
			selectedDomain = found.domain;
			selectedEndpointId = found.id;
		} else {
			selectedDomain = val;
			selectedEndpointId = '';
		}
		await loadLinks(selectedDomain || undefined, selectedEndpointId || undefined);
	}

	async function loadLinks(domainOverride?: string, endpointId?: string) {
		try {
			links = await api.getXrayClientLinks(clientId, domainOverride, endpointId);
			if (links) {
				if (links.vless_url?.includes('mode=finiteproto') && activeTab === 'finiteproto') {
					activeTab = 'vless';
				}
				const content =
					activeTab === 'happ'
						? links.happ_json
						: activeTab === 'finiteproto' && links.finiteproto_url
							? links.finiteproto_url
							: links.vless_url;
				generateQR(content);
			}
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка получения ссылок');
		}
	}

	async function copyAllRoutes() {
		if (!clientId || availableDomains.length === 0) return;
		copyAllLoading = true;
		try {
			const results = await Promise.all(
				availableDomains.map((opt) =>
					api.getXrayClientLinks(clientId, opt.domain, opt.id || undefined)
				)
			);
			const urls = results.map((r) => r.vless_url).filter(Boolean);
			if (urls.length > 0 && (await copyToClipboard(urls.join('\n')))) {
				notifications.success(`Скопированы все маршруты VLESS (${urls.length} шт.)`);
			}
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Не удалось скопировать все маршруты');
		} finally {
			copyAllLoading = false;
		}
	}

	async function generateQR(content: string) {
		if (!qrDataUrl) {
			qrGenerating = true;
		}
		try {
			qrDataUrl = await renderCrispQrDataUrl(content);
		} catch {
			qrDataUrl = '';
		} finally {
			qrGenerating = false;
		}
	}

	async function copyCurrent() {
		if (!links) return;
		let text = '';
		let label = '';
		switch (activeTab) {
			case 'vless':
				text = links.vless_url;
				label = 'VLESS ссылка';
				break;
			case 'finiteproto':
				text = links.finiteproto_url || links.vless_url;
				label = 'VLESS FiniteProto ссылка';
				break;
			case 'happ':
				text = links.happ_json;
				label = 'Happ JSON';
				break;
			case 'singbox':
				text = links.singbox_json;
				label = 'Sing-box outbound JSON';
				break;
			case 'mihomo':
				text = links.mihomo_yaml;
				label = 'Clash YAML';
				break;
		}
		if (text && (await copyToClipboard(text))) {
			notifications.success(`${label} скопирована`);
		}
	}

	function shareToTelegram() {
		if (!links?.vless_url) return;
		const label = selectedDomain ? `${clientRemark || 'Клиент'} · ${selectedDomain}` : clientRemark || 'Клиент';
		const tgUrl = `https://t.me/share/url?url=${encodeURIComponent(links.vless_url)}&text=${encodeURIComponent(`VLESS (${label})`)}`;
		window.open(tgUrl, '_blank');
	}

	function downloadConfig() {
		if (!links) return;
		let content = '';
		let filename = '';
		let type = 'application/json';

		switch (activeTab) {
			case 'vless':
				content = links.vless_url;
				filename = `vless-${clientRemark || 'client'}.txt`;
				type = 'text/plain';
				break;
			case 'finiteproto':
				content = links.finiteproto_url || links.vless_url;
				filename = `vless-finiteproto-${clientRemark || 'client'}.txt`;
				type = 'text/plain';
				break;
			case 'happ':
				content = links.happ_json;
				filename = `happ-${clientRemark || 'client'}.json`;
				break;
			case 'singbox':
				content = links.singbox_json;
				filename = `singbox-outbound-${clientRemark || 'client'}.json`;
				break;
			case 'mihomo':
				content = links.mihomo_yaml;
				filename = `clash-proxy-${clientRemark || 'client'}.yaml`;
				type = 'text/yaml';
				break;
		}

		const blob = new Blob([content], { type });
		const url = URL.createObjectURL(blob);
		const a = document.createElement('a');
		a.href = url;
		a.download = filename;
		a.click();
		URL.revokeObjectURL(url);
		notifications.success(`Файл ${filename} скачан`);
	}
</script>

<Modal bind:open title={`Подключение: ${clientRemark || 'Клиент'}`} {onclose} size="lg">
	{#if loading}
		<div class="loading-box">Загрузка конфигурации...</div>
	{:else if links}
		<div class="share-container">
			<!-- QR Code Section -->
			<div class="qr-col">
				{#if qrGenerating}
					<div class="qr-placeholder">Генерация QR...</div>
				{:else if qrDataUrl}
					<button
						type="button"
						class="qr-wrapper qr-zoom-btn"
						onclick={() => (qrZoomOpen = true)}
						title="Нажмите, чтобы увеличить QR-код"
					>
						<img src={qrDataUrl} alt="QR-код VLESS" class="qr-img" />
					</button>
					<div class="qr-hint">
						{isPrimaryFinite || activeTab === 'finiteproto'
							? 'Ссылка и QR-код транспорта FiniteProto v2 (нажмите на QR для увеличения)'
							: 'Отсканируйте в Happ / v2rayNG / Streisand (нажмите на QR для увеличения)'}
					</div>
				{:else}
					<div class="qr-placeholder">QR-код недоступен</div>
				{/if}
			</div>

			<!-- Format Tabs and Preview Section -->
			<div class="data-col">
				{#if clients.length > 1}
					<div class="endpoint-picker">
						<span class="picker-label">Устройство:</span>
						<select
							class="picker-select"
							value={clientId}
							onchange={(e) => void onClientChange(e.currentTarget.value)}
						>
							{#each clients as c (c.id)}
								<option value={c.id}>
									{c.remark || 'Устройство'}{c.enabled ? '' : ' (выключен)'}
								</option>
							{/each}
						</select>
					</div>
				{/if}
				{#if availableDomains.length > 1}
					<div class="endpoint-picker">
						<span class="picker-label">Конфигурация / маршрут VLESS:</span>
						<div class="route-pills">
							{#each availableDomains as opt (opt.id || opt.domain)}
								<button
									type="button"
									class="route-pill"
									class:active={(selectedEndpointId || selectedDomain) === (opt.id || opt.domain)}
									onclick={() => void onRouteChange(opt.id || opt.domain)}
								>
									<span class="route-pill-title">{opt.description || 'Маршрут VLESS'}</span>
									<span class="route-pill-sub font-mono">
										{opt.domain} · {opt.mode === 'finiteproto' ? 'FiniteProto v2' : 'XHTTP'}
									</span>
								</button>
							{/each}
						</div>
					</div>
				{:else if availableDomains.length === 1}
					<div class="endpoint-picker">
						<span class="picker-label">Конфигурация VLESS:</span>
						<div class="readonly-pill font-mono">
							{availableDomains[0].description ? `${availableDomains[0].description} (${availableDomains[0].domain})` : availableDomains[0].domain}
							· {availableDomains[0].mode === 'finiteproto' || isPrimaryFinite ? 'FiniteProto v2' : 'XHTTP'}
						</div>
					</div>
				{/if}

				<div class="tabs">
					<button
						type="button"
						class="tab"
						class:active={activeTab === 'vless'}
						onclick={() => { activeTab = 'vless'; if (links) generateQR(links.vless_url); }}
					>
						{isPrimaryFinite ? 'VLESS URL (FiniteProto v2)' : 'VLESS URL (XHTTP)'}
					</button>
					{#if links.finiteproto_url && !isPrimaryFinite}
						<button
							type="button"
							class="tab"
							class:active={activeTab === 'finiteproto'}
							onclick={() => { activeTab = 'finiteproto'; if (links?.finiteproto_url) generateQR(links.finiteproto_url); }}
						>
							FiniteProto v2
						</button>
					{/if}
					<button
						type="button"
						class="tab"
						class:active={activeTab === 'happ'}
						onclick={() => { activeTab = 'happ'; if (links) generateQR(links.happ_json); }}
					>
						Happ JSON
					</button>
					<button
						type="button"
						class="tab"
						class:active={activeTab === 'singbox'}
						onclick={() => { activeTab = 'singbox'; }}
					>
						Sing-box
					</button>
					<button
						type="button"
						class="tab"
						class:active={activeTab === 'mihomo'}
						onclick={() => { activeTab = 'mihomo'; }}
					>
						Clash YAML
					</button>
				</div>

				<div class="content-preview">
					{#if activeTab === 'vless'}
						<div class="code-box url-box">{links.vless_url}</div>
					{:else if activeTab === 'finiteproto'}
						<div class="code-box url-box">{links.finiteproto_url || links.vless_url}</div>
					{:else if activeTab === 'happ'}
						<pre class="code-box">{links.happ_json}</pre>
					{:else if activeTab === 'singbox'}
						<pre class="code-box">{links.singbox_json}</pre>
					{:else if activeTab === 'mihomo'}
						<pre class="code-box">{links.mihomo_yaml}</pre>
					{/if}
				</div>

				<div class="actions-row">
					<Button variant="primary" size="sm" onclick={copyCurrent}>
						<Copy size={14} class="mr-1" />
						Копировать
					</Button>
					{#if availableDomains.length > 1 && activeTab === 'vless'}
						<Button variant="secondary" size="sm" onclick={copyAllRoutes} disabled={copyAllLoading}>
							<Layers size={14} class="mr-1" />
							{copyAllLoading ? 'Копирование...' : `Скопировать все маршруты (${availableDomains.length})`}
						</Button>
					{/if}
					<Button variant="secondary" size="sm" onclick={downloadConfig}>
						<Download size={14} class="mr-1" />
						Скачать
					</Button>
					<span class="tg-share-wrap">
						<Button variant="secondary" size="sm" onclick={shareToTelegram}>
							<Send size={14} class="mr-1" />
							В Telegram
						</Button>
					</span>
				</div>
			</div>
		</div>
	{:else}
		<div class="error-box">Не удалось получить данные для подключения</div>
	{/if}

	{#snippet actions()}
		<Button variant="secondary" onclick={onclose}>Закрыть</Button>
	{/snippet}
</Modal>

<Modal
	bind:open={qrZoomOpen}
	title="QR-код VLESS для сканирования"
	onclose={() => (qrZoomOpen = false)}
	size="sm"
>
	{#if qrDataUrl}
		<div class="qr-zoom-body">
			<div class="qr-zoom-frame">
				<img src={qrDataUrl} alt="QR-код VLESS крупным планом" class="qr-zoom-img" />
			</div>
		</div>
	{/if}
	{#snippet actions()}
		<Button variant="secondary" onclick={() => (qrZoomOpen = false)}>Закрыть</Button>
	{/snippet}
</Modal>

<style>
	.loading-box, .error-box {
		padding: 2rem;
		text-align: center;
		color: var(--color-text-muted);
		font-size: 14px;
	}

	.share-container {
		display: flex;
		gap: 1.5rem;
		align-items: flex-start;
		min-height: 280px;
	}

	.qr-col {
		display: flex;
		flex-direction: column;
		align-items: center;
		width: 264px;
		flex-shrink: 0;
	}

	.qr-wrapper {
		background: #ffffff;
		padding: 10px;
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		box-shadow: 0 4px 12px rgba(0, 0, 0, 0.15);
		display: flex;
		align-items: center;
		justify-content: center;
	}

	.qr-zoom-btn {
		cursor: zoom-in;
		transition: transform var(--t-fast) ease, box-shadow var(--t-fast) ease;
	}

	.qr-zoom-btn:hover {
		transform: scale(1.02);
		box-shadow: 0 6px 16px rgba(0, 0, 0, 0.2);
	}

	.qr-img {
		width: 240px;
		height: 240px;
		image-rendering: crisp-edges;
		display: block;
	}

	.qr-zoom-body {
		display: flex;
		justify-content: center;
	}

	.qr-zoom-frame {
		background: #ffffff;
		padding: 16px;
		border-radius: var(--radius);
		border: 1px solid var(--color-border);
	}

	.qr-zoom-img {
		width: min(360px, 78vw);
		height: auto;
		aspect-ratio: 1 / 1;
		image-rendering: crisp-edges;
		display: block;
	}

	.qr-placeholder {
		width: 240px;
		height: 240px;
		background: var(--color-bg-secondary);
		border: 1px dashed var(--color-border);
		border-radius: var(--radius);
		display: flex;
		align-items: center;
		justify-content: center;
		font-size: 12px;
		color: var(--color-text-muted);
	}

	.qr-hint {
		margin-top: 10px;
		font-size: 11px;
		color: var(--color-text-muted);
		text-align: center;
		line-height: 1.4;
	}

	.data-col {
		flex: 1;
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
	}

	.tabs {
		display: flex;
		gap: 4px;
		border-bottom: 1px solid var(--color-border);
		padding-bottom: 4px;
		flex-wrap: wrap;
	}

	.tab {
		background: transparent;
		border: none;
		border-radius: var(--radius-sm);
		padding: 6px 12px;
		font-size: 12px;
		font-weight: 500;
		color: var(--color-text-muted);
		cursor: pointer;
		transition: all var(--t-fast) ease;
	}

	.tab:hover {
		color: var(--color-text-primary);
		background: var(--color-bg-hover);
	}

	.tab.active {
		color: var(--color-accent);
		background: var(--color-accent-tint);
	}

	.content-preview {
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 10px;
		min-height: 160px;
		max-height: 220px;
		overflow-y: auto;
	}

	.url-box {
		word-break: break-all;
		white-space: pre-wrap;
		user-select: all;
		line-height: 1.55;
		font-size: 11.5px;
	}

	.code-box {
		margin: 0;
		font-family: var(--font-mono);
		font-size: 11px;
		line-height: 1.45;
		color: var(--color-text-primary);
		white-space: pre-wrap;
		word-break: break-word;
	}

	.actions-row {
		display: flex;
		gap: 8px;
		flex-wrap: wrap;
	}

	.endpoint-picker {
		display: flex;
		flex-direction: column;
		gap: 4px;
		margin-bottom: 4px;
	}

	.picker-label {
		font-size: 11px;
		font-weight: 500;
		color: var(--color-text-muted);
		text-transform: uppercase;
		letter-spacing: 0.04em;
	}

	.picker-select {
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 6px 10px;
		font-size: 12px;
		color: var(--color-text-primary);
		outline: none;
		cursor: pointer;
	}
	.picker-select:focus {
		border-color: var(--color-accent);
	}

	.route-pills {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(160px, 1fr));
		gap: 6px;
	}

	.route-pill {
		display: flex;
		flex-direction: column;
		gap: 2px;
		padding: 7px 10px;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		text-align: left;
		cursor: pointer;
		transition: all var(--t-fast) ease;
	}
	.route-pill:hover {
		border-color: var(--color-accent);
		background: var(--color-bg-hover);
	}
	.route-pill.active {
		border-color: var(--color-accent);
		background: var(--color-accent-tint);
	}
	.route-pill-title {
		font-size: 12px;
		font-weight: 600;
		color: var(--color-text-primary);
	}
	.route-pill-sub {
		font-size: 11px;
		color: var(--color-text-muted);
		word-break: break-all;
	}

	.readonly-pill {
		padding: 6px 10px;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		font-size: 12px;
		color: var(--color-text-secondary);
	}

	.tg-share-wrap :global(button) {
		color: #229ED9 !important;
		border-color: rgba(34, 158, 217, 0.4) !important;
	}
	.tg-share-wrap :global(button:hover) {
		background: rgba(34, 158, 217, 0.1) !important;
		border-color: #229ED9 !important;
	}

	.font-mono {
		font-family: var(--font-mono);
	}

	@media (max-width: 640px) {
		.share-container {
			flex-direction: column;
			align-items: stretch;
		}
		.qr-col {
			width: 100%;
		}
	}
</style>
