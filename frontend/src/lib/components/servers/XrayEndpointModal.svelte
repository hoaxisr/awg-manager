<script lang="ts">
	import { api } from '$lib/api/client';
	import { m } from '$lib/i18n';
	import { notifications } from '$lib/stores/notifications';
	import { Button, Modal } from '$lib/components/ui';
	import type { XrayConfig, XrayEndpointConfig, XHTTPTuning, XHTTPPreset } from '$lib/types';
	import type { XrayRealityState, XrayRealityTransport } from '$lib/types/xray';
	import type { KeenDNSDiscovery } from '$lib/types/serverWizard';
	import XrayEndpointRealityForm from './XrayEndpointRealityForm.svelte';
	import XrayEndpointTransport from './XrayEndpointTransport.svelte';
	import XrayEndpointFields from './XrayEndpointFields.svelte';
	import {
		createDefaultTuning,
		getEndpointScheme,
		realityDestHost,
		type EndpointScheme,
	} from './xrayEndpointModel';
	import { AlertTriangle } from 'lucide-svelte';

	// Окно добавления/настройки маршрута VLESS (CDN, KeenDNS или прямой Reality).
	// Открывается методами openAdd/openEdit/openReality; сохраняет само и
	// сообщает владельцу через onsaved.
	interface Props {
		open: boolean;
		config: XrayConfig | null;
		endpoints: XrayEndpointConfig[];
		tuning: XHTTPTuning | null;
		keendnsDiscovery: KeenDNSDiscovery | null;
		realityState: XrayRealityState | null;
		/** Сохранено; при правке эндпоинта — новый конфиг. Владелец перечитывает данные. */
		onsaved: (c?: XrayConfig) => Promise<void> | void;
		/** Ключи Reality перевыпущены — ссылки у владельца устарели. */
		onkeyschanged: () => void;
	}

	let {
		open = $bindable(false),
		config,
		endpoints,
		tuning,
		keendnsDiscovery,
		realityState = $bindable(null),
		onsaved,
		onkeyschanged,
	}: Props = $props();

	let editingEndpointId = $state<string | null>(null);
	let editEndpoint = $state<XrayEndpointConfig>({
		id: '',
		name: '',
		enabled: true,
		connection_way: 'direct',
		origin_type: 'public_address',
		public_domain: '',
		public_port: 443,
		origin_host: '',
		origin_scheme: 'https',
		origin_port: 443,
		path: '/cdn-bridge/',
		transport: 'xhttp',
		mode: 'packet-up',
		uplink_method: 'POST',
	});
	let showEndpointAdvancedTuning = $state(false);
	let endpointSaving = $state(false);
	let endpointScheme = $state<EndpointScheme>('keendns_public');

	let realityFormPort = $state(2053);
	let realityFormTransport = $state<XrayRealityTransport>('tcp');
	let realityFormDest = $state('www.microsoft.com:443');
	let realityFormServerName = $state('');
	let realityFormPublicHost = $state('');

	// Имена маршрутов по умолчанию — данные на роутере (сохраняются в конфиг,
	// правятся пользователем), поэтому фиксированы и не зависят от языка UI:
	// смена схемы переименовывает маршрут, только пока имя — одно из стандартных,
	// а сравнение с локализованной строкой ломалось бы при смене языка.
	const nameCdn = () => 'Через CDN';
	const nameKeendnsPublic = () => 'KeenDNS (Белый IP)';
	const nameKeendnsCloud = () => 'KeenDNS (Серый IP / Облако)';
	const nameKeendnsLegacy = () => 'KeenDNS (Белый / Серый IP)';

	function syncRealityFormFromState() {
		const s = realityState?.settings;
		realityFormPort = s?.public_key ? s.port : realityState?.suggested_port || s?.port || 2053;
		realityFormTransport = (s?.transport as XrayRealityTransport) || 'tcp';
		realityFormDest = s?.dest || 'www.microsoft.com:443';
		realityFormServerName = (s?.server_names && s.server_names[0]) || '';
		realityFormPublicHost = s?.public_host || '';
	}

	export function openReality() {
		editingEndpointId = 'reality';
		endpointScheme = 'reality';
		showEndpointAdvancedTuning = false;
		syncRealityFormFromState();
		open = true;
		if (!realityState) {
			void api.getXrayReality(true).then((r) => {
				realityState = r;
				syncRealityFormFromState();
			}).catch(() => {});
		}
	}

	export function openAdd(defaultScheme: EndpointScheme = 'keendns_public') {
		editingEndpointId = null;
		endpointScheme = defaultScheme;
		syncRealityFormFromState();
		const keendnsPub = keendnsDiscovery?.publications?.find(
			(p) => p.compatible && !p.fqdn.toLowerCase().startsWith('tg.')
		)?.fqdn || (keendnsDiscovery?.domain ? `vless.${keendnsDiscovery.domain}` : '');
		const isCDN = defaultScheme === 'cdn';
		const isCloud = defaultScheme === 'keendns_cloud';
		editEndpoint = {
			id: `ep-${Date.now().toString(36)}`,
			name: isCDN ? nameCdn() : isCloud ? nameKeendnsCloud() : nameKeendnsPublic(),
			enabled: true,
			connection_way: isCDN ? 'external_https' : 'direct',
			origin_type: isCloud ? 'cloud_address' : 'public_address',
			public_domain: isCDN ? '' : keendnsPub,
			public_port: 443,
			origin_host: isCDN ? keendnsPub : '',
			origin_scheme: 'https',
			origin_port: 443,
			path: config?.path || '/cdn-bridge/',
			transport: 'xhttp',
			mode: 'packet-up',
			uplink_method: 'POST',
			xhttp_tuning: tuning ? JSON.parse(JSON.stringify(tuning)) : createDefaultTuning('live_low_latency'),
		};
		showEndpointAdvancedTuning = editEndpoint.xhttp_tuning?.preset === 'custom';
		open = true;
		if (defaultScheme === 'reality' || !realityState) {
			void api.getXrayReality(false).then((r) => {
				realityState = r;
				if (!editingEndpointId) syncRealityFormFromState();
			}).catch(() => {});
		}
	}

	export function openEdit(ep: XrayEndpointConfig) {
		editingEndpointId = ep.id;
		const cloned: XrayEndpointConfig = JSON.parse(JSON.stringify(ep));
		if (!cloned.public_port) cloned.public_port = config?.public_port || 443;
		if (!cloned.path) cloned.path = config?.path || '/cdn-bridge/';
		if (!cloned.transport) cloned.transport = config?.transport || 'xhttp';
		if (!cloned.mode) cloned.mode = config?.mode || 'packet-up';
		const epTuning = cloned.xhttp_tuning
			? cloned.xhttp_tuning
			: tuning
				? JSON.parse(JSON.stringify(tuning))
				: createDefaultTuning((cloned.uplink_method === 'GET' ? 'cdn_get' : 'live_low_latency') as XHTTPPreset);
		if (!epTuning.fp_chunk_bytes) {
			epTuning.fp_chunk_bytes = 131072;
		}
		if (!epTuning.fp_max_ways) {
			epTuning.fp_max_ways = 1;
		}
		cloned.xhttp_tuning = epTuning;
		editEndpoint = cloned;
		endpointScheme = getEndpointScheme(cloned);
		showEndpointAdvancedTuning = editEndpoint.xhttp_tuning?.preset === 'custom';
		open = true;
	}

	function selectEndpointScheme(scheme: EndpointScheme) {
		endpointScheme = scheme;
		if (scheme === 'reality') {
			syncRealityFormFromState();
			if (!realityState) {
				void api.getXrayReality(true).then((r) => {
					realityState = r;
					syncRealityFormFromState();
				}).catch(() => {});
			}
			return;
		}
		const keendnsPub = keendnsDiscovery?.publications?.find(
			(p) => p.compatible && !p.fqdn.toLowerCase().startsWith('tg.')
		)?.fqdn || '';
		if (scheme === 'cdn') {
			editEndpoint.connection_way = 'external_https';
			if (!editEndpoint.origin_host && keendnsPub) {
				editEndpoint.origin_host = keendnsPub;
			}
			if (
				!editEndpoint.name ||
				editEndpoint.name.startsWith('KeenDNS')
			) {
				editEndpoint.name = nameCdn();
			}
		} else if (scheme === 'keendns_cloud') {
			editEndpoint.connection_way = 'direct';
			editEndpoint.origin_type = 'cloud_address';
			if (!editEndpoint.public_domain && keendnsPub) {
				editEndpoint.public_domain = keendnsPub;
			}
			if (
				!editEndpoint.name ||
				editEndpoint.name === nameCdn() ||
				editEndpoint.name === nameKeendnsPublic() ||
				editEndpoint.name === nameKeendnsLegacy()
			) {
				editEndpoint.name = nameKeendnsCloud();
			}
		} else {
			editEndpoint.connection_way = 'direct';
			editEndpoint.origin_type = 'public_address';
			if (!editEndpoint.public_domain && keendnsPub) {
				editEndpoint.public_domain = keendnsPub;
			}
			if (
				!editEndpoint.name ||
				editEndpoint.name === nameCdn() ||
				editEndpoint.name === nameKeendnsCloud() ||
				editEndpoint.name === nameKeendnsLegacy()
			) {
				editEndpoint.name = nameKeendnsPublic();
			}
		}
	}

	async function saveEndpoint() {
		if (endpointScheme === 'reality') {
			const portNum = Number(realityFormPort);
			if (!Number.isInteger(portNum) || portNum < 1024 || portNum > 65535) {
				notifications.error(m.servers_xray_ep_port_range());
				return;
			}
			const destTrim = realityFormDest.trim();
			if (!destTrim) {
				notifications.error(m.servers_xray_ep_dest_required());
				return;
			}
			endpointSaving = true;
			try {
				const sn = realityFormServerName.trim();
				realityState = await api.updateXrayReality({
					enabled: true,
					port: portNum,
					transport: realityFormTransport,
					dest: destTrim,
					server_names: sn ? [sn] : [realityDestHost(destTrim)],
					public_host: realityFormPublicHost.trim(),
				});
				notifications.success(
					editingEndpointId === 'reality' ? m.servers_xray_reality_saved() : m.servers_xray_reality_added()
				);
				open = false;
				await onsaved();
			} catch (e) {
				notifications.error(e instanceof Error ? e.message : m.servers_xray_reality_save_failed());
			} finally {
				endpointSaving = false;
			}
			return;
		}
		if (!config) return;
		const dom = (editEndpoint.public_domain || '').trim();
		if (!dom) {
			notifications.error(m.servers_xray_ep_domain_required());
			return;
		}
		endpointSaving = true;
		try {
			const cleanEp: XrayEndpointConfig = {
				...editEndpoint,
				public_domain: dom,
				name: (editEndpoint.name || '').trim() || dom,
				public_port: editEndpoint.public_port || 443,
				path: (editEndpoint.path || '').trim() || '/cdn-bridge/',
				uplink_method: editEndpoint.xhttp_tuning?.uplink_method || editEndpoint.uplink_method || 'POST',
			};
			const currentList = [...endpoints];
			if (editingEndpointId) {
				const idx = currentList.findIndex((e) => e.id === editingEndpointId);
				if (idx >= 0) {
					currentList[idx] = cleanEp;
				} else {
					currentList.push(cleanEp);
				}
			} else {
				currentList.push(cleanEp);
			}
			const saved = await api.updateXrayServerConfig({
				...config,
				endpoints: currentList,
			});
			notifications.success(editingEndpointId ? m.servers_xray_route_updated() : m.servers_xray_route_added());
			open = false;
			await onsaved(saved);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.servers_xray_route_save_failed());
		} finally {
			endpointSaving = false;
		}
	}
</script>

<Modal
	bind:open
	title={editingEndpointId === 'reality'
		? m.servers_xray_ep_title_reality()
		: editingEndpointId
			? m.servers_xray_ep_title_edit({ name: editEndpoint.name || editEndpoint.public_domain })
			: m.servers_xray_ep_title_new()}
	onclose={() => (open = false)}
	size="lg"
>
	<div class="settings-modal-wrap">
		<p class="field-hint">
			{m.servers_xray_ep_intro()}
		</p>

		<!-- Scheme Selector -->
		<div class="preset-tabs preset-tabs-4">
			<button
				type="button"
				class="preset-tab"
				class:active={endpointScheme === 'cdn'}
				onclick={() => selectEndpointScheme('cdn')}
			>
				<div class="preset-name">{m.servers_xray_scheme_cdn()}</div>
				<div class="preset-desc">{m.servers_xray_scheme_cdn_desc()}</div>
			</button>
			<button
				type="button"
				class="preset-tab"
				class:active={endpointScheme === 'keendns_public'}
				onclick={() => selectEndpointScheme('keendns_public')}
			>
				<div class="preset-name">{m.servers_xray_scheme_keendns_public()}</div>
				<div class="preset-desc">{m.servers_xray_scheme_keendns_public_desc()}</div>
			</button>
			<button
				type="button"
				class="preset-tab"
				class:active={endpointScheme === 'keendns_cloud'}
				onclick={() => selectEndpointScheme('keendns_cloud')}
			>
				<div class="preset-name">{m.servers_xray_scheme_keendns_cloud()}</div>
				<div class="preset-desc">{m.servers_xray_scheme_keendns_cloud_desc()}</div>
			</button>
			<button
				type="button"
				class="preset-tab"
				class:active={endpointScheme === 'reality'}
				onclick={() => selectEndpointScheme('reality')}
			>
				<div class="preset-top-row">
					<span class="preset-name">{m.servers_xray_reality_white_ip_badge()}</span>
					{#if realityState?.network}
						<span class="badge {realityState.network.white_ip ? 'badge-success' : 'badge-warning'}">
							{realityState.network.white_ip ? m.servers_xray_white_ip() : m.servers_xray_needs_white_ip()}
						</span>
					{/if}
				</div>
				<div class="preset-desc">{m.servers_xray_scheme_reality_desc({ port: realityFormPort || 2053 })}</div>
			</button>
		</div>

		{#if editingEndpointId && editingEndpointId !== 'reality' && endpointScheme === 'reality'}
			<div class="alert-banner info">
				<AlertTriangle size={16} class="banner-icon banner-icon-info" />
				<div class="banner-content">
					<div class="banner-desc">
						{m.servers_xray_ep_reality_parallel({ name: editEndpoint.name || editEndpoint.public_domain })}
					</div>
				</div>
			</div>
		{/if}

		{#if endpointScheme === 'reality'}
			<XrayEndpointRealityForm
				bind:realityState
				bind:port={realityFormPort}
				bind:transport={realityFormTransport}
				bind:dest={realityFormDest}
				bind:serverName={realityFormServerName}
				bind:publicHost={realityFormPublicHost}
				{onkeyschanged}
			/>
		{:else}
		<div class="settings-form">
			<XrayEndpointFields bind:endpoint={editEndpoint} {keendnsDiscovery} />

			<!-- Unified Per-endpoint Transport Profile (XHTTP Presets + FiniteProto v2) -->
			<XrayEndpointTransport bind:endpoint={editEndpoint} bind:showAdvanced={showEndpointAdvancedTuning} />
		</div>
		{/if}
	</div>

	{#snippet actions()}
		<Button variant="secondary" onclick={() => (open = false)}>{m.common_cancel()}</Button>
		<Button variant="primary" onclick={saveEndpoint} disabled={endpointSaving}>
			{endpointSaving ? m.common_saving() : endpointScheme === 'reality' ? m.servers_xray_save_reality() : m.servers_xray_save_route()}
		</Button>
	{/snippet}
</Modal>

<style>
	.badge {
		font-size: 11px;
		padding: 2px 8px;
		border-radius: var(--radius-sm);
		font-weight: 500;
		border: 1px solid transparent;
	}
	.badge-success {
		background: var(--color-success-tint);
		border-color: var(--color-success-border);
		color: var(--color-success);
	}
	.badge-warning {
		background: var(--color-warning-tint);
		border-color: var(--color-warning-border);
		color: var(--color-warning);
	}

	/* Settings & Egress Modals */
	.settings-modal-wrap {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}

	.settings-form {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}

	.field-hint {
		font-size: 11.5px;
		color: var(--color-text-muted);
		margin: 0;
	}

	.alert-banner {
		display: flex;
		align-items: flex-start;
		gap: 10px;
		padding: 10px 14px;
		border-radius: var(--radius-sm);
		font-size: 12.5px;
	}
	.alert-banner.info {
		background: var(--color-accent-tint);
		border: 1px solid var(--color-accent-border);
		color: var(--color-text-primary);
	}
	.banner-content {
		display: flex;
		flex-direction: column;
		gap: 4px;
		flex: 1;
	}
	.banner-desc {
		color: var(--color-text-muted);
		font-size: 12px;
		line-height: 1.4;
	}

	.preset-tabs {
		display: grid;
		grid-template-columns: repeat(3, 1fr);
		gap: 8px;
	}

	.preset-tabs-4 {
		grid-template-columns: repeat(2, 1fr);
	}

	.preset-tab {
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 8px 10px;
		text-align: left;
		cursor: pointer;
		transition: all var(--t-fast) ease;
		display: flex;
		flex-direction: column;
		gap: 3px;
	}
	.preset-tab:hover {
		border-color: var(--color-accent);
	}
	.preset-tab.active {
		border-color: var(--color-accent);
		background: var(--color-accent-tint, rgba(99, 102, 241, 0.08));
	}

	.preset-name {
		font-size: 12px;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.preset-top-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 8px;
		flex-wrap: wrap;
	}

	.preset-desc {
		font-size: 11px;
		color: var(--color-text-muted);
		line-height: 1.35;
	}

	@media (max-width: 900px) {
		.preset-tabs-4 {
			grid-template-columns: repeat(2, 1fr);
		}
	}

	@media (max-width: 640px) {
		.preset-tabs,
		.preset-tabs-4 {
			grid-template-columns: 1fr;
		}
	}
</style>
