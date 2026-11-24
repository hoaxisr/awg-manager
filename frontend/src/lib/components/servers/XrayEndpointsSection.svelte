<script lang="ts">
	import { api } from '$lib/api/client';
	import { m } from '$lib/i18n';
	import { notifications } from '$lib/stores/notifications';
	import { Button, IconButton, Toggle } from '$lib/components/ui';
	import type { XrayConfig, XrayEndpointConfig, XHTTPTuning } from '$lib/types';
	import type { XrayRealityState } from '$lib/types/xray';
	import { getVlessPresetLabel } from './external-access/connectionLabels';
	import { getEndpointBadge, realityDestHost } from './xrayEndpointModel';
	import { Plus, Settings, Trash2, Globe } from 'lucide-svelte';

	// Список маршрутов VLESS (эндпоинты + прямой Reality) с действиями строк:
	// включение и удаление. Настройка маршрута — окно у владельца (onedit/oneditreality).
	interface Props {
		config: XrayConfig | null;
		endpoints: XrayEndpointConfig[];
		tuning: XHTTPTuning | null;
		realityState: XrayRealityState | null;
		hasRealityRoute: boolean;
		onadd: () => void;
		onedit: (ep: XrayEndpointConfig) => void;
		oneditreality: () => void;
		/** Список маршрутов сохранён: новый конфиг, владелец перечитывает данные. */
		onchanged: (c: XrayConfig) => Promise<void> | void;
		/** Маршрут Reality включён/выключен/удалён. */
		onrealitychanged: () => void;
	}

	let {
		config,
		endpoints,
		tuning,
		realityState = $bindable(null),
		hasRealityRoute,
		onadd,
		onedit,
		oneditreality,
		onchanged,
		onrealitychanged,
	}: Props = $props();

	let realityToggling = $state(false);

	let totalRoutesCount = $derived(endpoints.length + (hasRealityRoute ? 1 : 0));
	let realityPublicHost = $derived(
		realityState?.settings?.public_host || realityState?.network?.public_host || ''
	);
	let effectiveUplinkMethod = $derived(tuning?.uplink_method || config?.uplink_method || 'POST');

	async function handleToggleRealityRoute(nextEnabled: boolean) {
		if (!realityState) return;
		if (!nextEnabled && endpoints.filter((e) => e.enabled).length === 0) {
			notifications.error(m.servers_xray_route_keep_one());
			return;
		}
		realityToggling = true;
		try {
			realityState = await api.updateXrayReality({ enabled: nextEnabled });
			notifications.success(
				nextEnabled ? m.servers_xray_reality_route_on() : m.servers_xray_reality_route_off()
			);
			onrealitychanged();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.servers_xray_reality_toggle_failed());
			realityState = await api.getXrayReality(false).catch(() => realityState);
		} finally {
			realityToggling = false;
		}
	}

	async function handleDeleteRealityRoute() {
		if (endpoints.length === 0) {
			notifications.error(m.servers_xray_route_cannot_delete_last());
			return;
		}
		if (!confirm(m.servers_xray_reality_delete_confirm())) return;
		try {
			realityState = await api.updateXrayReality({ reset: true });
			notifications.success(m.servers_xray_reality_deleted());
			onrealitychanged();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.servers_xray_reality_delete_failed());
		}
	}

	async function handleToggleEndpoint(ep: XrayEndpointConfig) {
		if (!config) return;
		const nextEnabled = !ep.enabled;
		if (!nextEnabled && endpoints.filter((e) => e.enabled).length <= 1 && !realityState?.settings?.enabled) {
			notifications.error(m.servers_xray_route_keep_one());
			return;
		}
		try {
			const nextList = endpoints.map((item) =>
				item.id === ep.id ? { ...item, enabled: nextEnabled } : item
			);
			const saved = await api.updateXrayServerConfig({
				...config,
				endpoints: nextList,
			});
			notifications.success(
				nextEnabled ? m.servers_xray_route_on({ name: ep.name }) : m.servers_xray_route_off({ name: ep.name })
			);
			await onchanged(saved);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.servers_xray_route_toggle_failed());
		}
	}

	async function handleDeleteEndpoint(ep: XrayEndpointConfig) {
		if (!config) return;
		if (endpoints.length <= 1 && !hasRealityRoute) {
			notifications.error(m.servers_xray_route_cannot_delete_last());
			return;
		}
		const name = ep.name || ep.public_domain;
		if (!confirm(m.servers_xray_route_delete_confirm({ name }))) return;
		try {
			const nextList = endpoints.filter((item) => item.id !== ep.id);
			const saved = await api.updateXrayServerConfig({
				...config,
				endpoints: nextList,
			});
			notifications.success(m.servers_xray_route_deleted({ name }));
			await onchanged(saved);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.servers_xray_route_delete_failed());
		}
	}
</script>

<section class="clients-section">
	<div class="section-header">
		<div class="section-title">
			<Globe size={14} class="summary-icon" />
			<span>{m.servers_xray_routes_title()}</span>
			<span class="counter">({totalRoutesCount})</span>
		</div>
		<Button variant="secondary" size="sm" onclick={() => onadd()}>
			<Plus size={14} class="mr-1" />
			{m.servers_xray_add_route()}
		</Button>
	</div>

	{#if endpoints.length === 0 && !hasRealityRoute}
		<div class="empty-clients">
			<span>{m.servers_xray_routes_empty()}</span>
			<Button variant="primary" size="sm" onclick={() => onadd()}>
				<Plus size={14} class="mr-1" />
				{m.servers_xray_add_route()}
			</Button>
		</div>
	{:else}
		<div class="clients-list">
			{#each endpoints as ep (ep.id)}
				{@const badge = getEndpointBadge(ep)}
				{@const isFinite = ep.mode === 'finiteproto' || ep.transport === 'finiteproto'}
				{@const epPreset = getVlessPresetLabel(ep.xhttp_tuning?.preset || tuning?.preset)}
				{@const epMethod = ep.xhttp_tuning?.uplink_method || ep.uplink_method || effectiveUplinkMethod}
				<div class="client-item" class:client-disabled={!ep.enabled}>
					<div class="endpoint-info">
						<Toggle
							checked={ep.enabled}
							onchange={() => void handleToggleEndpoint(ep)}
						/>
						<div class="endpoint-meta">
							<div class="endpoint-top-line">
								<span class="client-name" class:muted={!ep.enabled}>{ep.name || ep.public_domain}</span>
								<span class="badge {badge.cls}">{badge.label}</span>
								{#if isFinite}
									<span class="tuning-badge font-mono">
										{m.servers_xray_fp_badge({ kb: Math.round((ep.xhttp_tuning?.fp_chunk_bytes || 131072) / 1024), ways: ep.xhttp_tuning?.fp_max_ways || 1 })}
									</span>
								{:else}
									<span class="tuning-badge font-mono">XHTTP ({ep.mode || 'packet-up'}) · {epPreset} · {epMethod}</span>
								{/if}
							</div>
							<div class="endpoint-sub-line font-mono">
								<span>{ep.public_domain}:{ep.public_port || config?.public_port || 443}{ep.path || config?.path || '/cdn-bridge/'}</span>
								{#if ep.connection_way === 'external_https' && ep.origin_host}
									<span class="endpoint-origin-arrow">&rarr; {ep.origin_host}</span>
								{/if}
							</div>
						</div>
					</div>

					<div class="client-actions">
						<Button variant="secondary" size="sm" onclick={() => onedit(ep)}>
							<Settings size={14} class="mr-1" />
							{m.servers_xray_configure()}
						</Button>
						{#if endpoints.length > 1 || hasRealityRoute}
							<IconButton
								variant="danger"
								ariaLabel={m.servers_xray_delete_route()}
								title={m.servers_xray_delete_route()}
								onclick={() => void handleDeleteEndpoint(ep)}
							>
								<Trash2 size={14} />
							</IconButton>
						{/if}
					</div>
				</div>
			{/each}

			{#if hasRealityRoute && realityState}
				{@const rSet = realityState.settings}
				{@const rEnabled = Boolean(rSet.enabled)}
				{@const rWhiteIP = realityState.network?.white_ip}
				{@const rSni = (rSet.server_names && rSet.server_names[0]) || realityDestHost(rSet.dest)}
				<div class="client-item" class:client-disabled={!rEnabled}>
					<div class="endpoint-info">
						<Toggle
							checked={rEnabled}
							disabled={realityToggling}
							onchange={() => void handleToggleRealityRoute(!rEnabled)}
						/>
						<div class="endpoint-meta">
							<div class="endpoint-top-line">
								<span class="client-name" class:muted={!rEnabled}>{m.servers_xray_reality_row_name()}</span>
								<span class="badge {rWhiteIP === false ? 'badge-warning' : 'badge-success'}">
									{m.servers_xray_reality_white_ip_badge()}
								</span>
								{#if rEnabled && !config?.enabled}
									<span class="badge badge-warning">{m.servers_xray_server_off()}</span>
								{:else if rEnabled && realityState.last_error && !realityState.runtime.running}
									<span class="badge badge-warning">{m.servers_xray_start_error()}</span>
								{:else if rEnabled && realityState.runtime.running && !realityState.wan_port_open}
									<span class="badge badge-warning">{m.servers_xray_port_not_open({ port: rSet.port })}</span>
								{/if}
								<span class="tuning-badge font-mono">
									VLESS + REALITY · {rSet.transport === 'xhttp' ? 'XHTTP (stream-one)' : 'TCP (raw)'}
									{#if realityState.runtime.running}
										{m.servers_xray_active_conns({ n: realityState.runtime.active_conns })}
									{/if}
								</span>
							</div>
							<div class="endpoint-sub-line font-mono">
								<span>{realityPublicHost || m.servers_xray_address_unknown()}:{rSet.port}</span>
								<span class="endpoint-origin-arrow">{m.servers_xray_masquerade_as({ sni: rSni })}</span>
							</div>
							{#if rEnabled && realityState.last_error}
								<div class="inline-warn-msg">{realityState.last_error}</div>
							{/if}
						</div>
					</div>

					<div class="client-actions">
						<Button variant="secondary" size="sm" onclick={() => oneditreality()}>
							<Settings size={14} class="mr-1" />
							{m.servers_xray_configure()}
						</Button>
						{#if endpoints.length > 0}
							<IconButton
								variant="danger"
								ariaLabel={m.servers_xray_delete_reality_route()}
								title={m.servers_xray_delete_reality_route()}
								onclick={() => void handleDeleteRealityRoute()}
							>
								<Trash2 size={14} />
							</IconButton>
						{/if}
					</div>
				</div>
			{/if}
		</div>
	{/if}
</section>

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
	.badge-info {
		background: var(--color-accent-tint);
		border-color: var(--color-accent-border);
		color: var(--color-accent);
	}
	.badge-purple {
		background: var(--color-accent-tint);
		border-color: var(--color-accent-border);
		color: var(--color-accent);
	}

	.endpoint-info {
		display: flex;
		align-items: center;
		gap: 10px;
		min-width: 200px;
		flex: 1;
	}

	.endpoint-meta {
		display: flex;
		flex-direction: column;
		gap: 3px;
		min-width: 0;
	}

	.endpoint-top-line {
		display: flex;
		align-items: center;
		gap: 8px;
		flex-wrap: wrap;
	}

	.endpoint-sub-line {
		font-size: 11.5px;
		color: var(--color-text-muted);
		display: flex;
		align-items: center;
		gap: 6px;
		flex-wrap: wrap;
		word-break: break-all;
	}

	.endpoint-origin-arrow {
		color: var(--color-text-secondary);
		opacity: 0.85;
	}

	/* Clients Section */
	.clients-section {
		display: flex;
		flex-direction: column;
		gap: 8px;
	}

	.section-header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 8px;
	}

	.section-title {
		font-size: 13px;
		font-weight: 600;
		color: var(--color-text-primary);
		display: flex;
		align-items: center;
		gap: 6px;
	}

	.counter {
		color: var(--color-text-muted);
		font-size: 12px;
	}

	.empty-clients {
		padding: 1.25rem;
		text-align: center;
		background: var(--color-bg-primary);
		border: 1px dashed var(--color-border);
		border-radius: var(--radius-sm);
		color: var(--color-text-muted);
		font-size: 12.5px;
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 10px;
	}

	.clients-list {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}

	.client-item {
		display: flex;
		justify-content: space-between;
		align-items: center;
		padding: 8px 12px;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		gap: 10px;
		flex-wrap: wrap;
	}

	.client-item.client-disabled {
		background: var(--color-bg-secondary);
	}

	.client-name {
		font-size: 13.5px;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.client-name.muted {
		color: var(--color-text-muted);
	}

	.client-actions {
		display: flex;
		align-items: center;
		gap: 6px;
	}

	.font-mono {
		font-family: var(--font-mono);
	}

	.tuning-badge {
		font-size: 10px;
		padding: 2px 6px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		color: var(--color-accent);
	}

	.inline-warn-msg {
		margin-top: 4px;
		font-size: 11.5px;
		color: var(--color-warning);
	}
</style>
