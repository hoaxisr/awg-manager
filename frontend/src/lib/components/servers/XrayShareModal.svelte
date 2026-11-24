<script lang="ts">
	import { untrack } from 'svelte';
	import { Modal, Button } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { m } from '$lib/i18n';
	import { notifications } from '$lib/stores/notifications';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import type { XrayShareLinks, XrayClient, XrayEndpointConfig } from '$lib/types';
	import { renderCrispQrDataUrl } from '$lib/utils/qrSvg';
	import { Copy, Download, Send, Layers } from 'lucide-svelte';
	import XrayShareQr from './XrayShareQr.svelte';
	import XrayShareRoutePicker, { shownDomain, type RouteOption } from './XrayShareRoutePicker.svelte';

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

	let links = $state<XrayShareLinks | null>(null);
	let loading = $state(false);
	let copyAllLoading = $state(false);
	let activeTab = $state<'vless' | 'finiteproto' | 'happ' | 'singbox' | 'mihomo'>('vless');
	let isPrimaryFinite = $derived(Boolean(links?.vless_url?.includes('mode=finiteproto')));
	let isReality = $derived(Boolean(links?.vless_url?.includes('security=reality')));

	let qrDataUrl = $state('');
	let qrGenerating = $state(false);
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
			availableDomains = [];
			selectedDomain = '';
			selectedEndpointId = '';
		}
	});

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
							description: ep.name || '',
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
							description: r.description || ''
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
						domainList.push({ id: '', domain: p, description: '' });
					}
				}
			}

			availableDomains = domainList;

			// 3. Direct REALITY entry (white-IP preset), offered alongside CDN/KeenDNS routes.
			// Хост неизвестен — домен `:port`, подпись «белый IP» подставляется при показе
			// (бэкенд для endpoint_id=reality домен не читает).
			const reality = await api.getXrayReality().catch(() => null);
			if (reality?.settings?.enabled && reality.settings.public_key) {
				const host = reality.settings.public_host || reality.network?.public_host || '';
				availableDomains = [
					...availableDomains,
					{
						id: 'reality',
						domain: `${host}:${reality.settings.port}`,
						description: '',
						mode: reality.settings.transport === 'xhttp' ? 'reality-xhttp' : 'reality-tcp'
					}
				];
			}

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
			notifications.error(e instanceof Error ? e.message : m.servers_xray_share_load_failed());
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
			notifications.error(e instanceof Error ? e.message : m.servers_xray_share_links_failed());
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
				notifications.success(m.servers_xray_share_all_copied({ count: urls.length }));
			}
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.servers_xray_share_all_copy_failed());
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
		let label: () => string = () => '';
		switch (activeTab) {
			case 'vless':
				text = links.vless_url;
				label = () => m.servers_xray_share_vless_link();
				break;
			case 'finiteproto':
				text = links.finiteproto_url || links.vless_url;
				label = () => m.servers_xray_share_fp_link();
				break;
			case 'happ':
				text = links.happ_json;
				label = () => 'Happ JSON';
				break;
			case 'singbox':
				text = links.singbox_json;
				label = () => 'Sing-box outbound JSON';
				break;
			case 'mihomo':
				text = links.mihomo_yaml;
				label = () => 'Clash YAML';
				break;
		}
		if (text && (await copyToClipboard(text))) {
			notifications.success(m.servers_xray_share_copied({ label: label() }));
		}
	}

	function shareToTelegram() {
		if (!links?.vless_url) return;
		const name = clientRemark || m.servers_xray_client_fallback();
		const label = selectedDomain ? `${name} · ${shownDomain(selectedDomain)}` : name;
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
		notifications.success(m.servers_xray_share_file_downloaded({ filename }));
	}
</script>

<Modal bind:open title={m.servers_xray_share_title({ name: clientRemark || m.servers_xray_client_fallback() })} {onclose} size="lg">
	{#if loading}
		<div class="loading-box">{m.servers_xray_share_loading()}</div>
	{:else if links}
		<div class="share-container">
			<XrayShareQr {qrDataUrl} generating={qrGenerating} finite={isPrimaryFinite || activeTab === 'finiteproto'} />

			<!-- Format Tabs and Preview Section -->
			<div class="data-col">
				<XrayShareRoutePicker
					{clients}
					{clientId}
					routes={availableDomains}
					{selectedEndpointId}
					{selectedDomain}
					{isPrimaryFinite}
					onclientchange={(id) => void onClientChange(id)}
					onroutechange={(val) => void onRouteChange(val)}
				/>

				<div class="tabs">
					<button
						type="button"
						class="tab"
						class:active={activeTab === 'vless'}
						onclick={() => { activeTab = 'vless'; if (links) generateQR(links.vless_url); }}
					>
						{isPrimaryFinite ? 'VLESS URL (FiniteProto v2)' : isReality ? 'VLESS URL (Reality)' : 'VLESS URL (XHTTP)'}
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
						{isReality ? 'JSON (Xray / Happ)' : m.servers_xray_share_tab_xhttp_json()}
					</button>
					{#if links.singbox_json}
						<button
							type="button"
							class="tab"
							class:active={activeTab === 'singbox'}
							onclick={() => { activeTab = 'singbox'; }}
						>
							Sing-box
						</button>
					{/if}
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
						{m.common_copy()}
					</Button>
					{#if availableDomains.length > 1 && activeTab === 'vless'}
						<Button variant="secondary" size="sm" onclick={copyAllRoutes} disabled={copyAllLoading}>
							<Layers size={14} class="mr-1" />
							{copyAllLoading ? m.servers_xray_share_copying() : m.servers_xray_share_copy_all({ count: availableDomains.length })}
						</Button>
					{/if}
					<Button variant="secondary" size="sm" onclick={downloadConfig}>
						<Download size={14} class="mr-1" />
						{m.common_download()}
					</Button>
					<span class="tg-share-wrap">
						<Button variant="secondary" size="sm" onclick={shareToTelegram}>
							<Send size={14} class="mr-1" />
							{m.servers_xray_share_to_telegram()}
						</Button>
					</span>
				</div>
			</div>
		</div>
	{:else}
		<div class="error-box">{m.servers_xray_share_failed()}</div>
	{/if}

	{#snippet actions()}
		<Button variant="secondary" onclick={onclose}>{m.common_close()}</Button>
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

	.tg-share-wrap :global(button) {
		color: #229ED9 !important;
		border-color: rgba(34, 158, 217, 0.4) !important;
	}
	.tg-share-wrap :global(button:hover) {
		background: rgba(34, 158, 217, 0.1) !important;
		border-color: #229ED9 !important;
	}

	@media (max-width: 640px) {
		.share-container {
			flex-direction: column;
			align-items: stretch;
		}
	}
</style>
