<script lang="ts">
	// Вкладка «Основные параметры»: адрес MTProxy, SNI, выходные каналы,
	// домены веб-подключения и зона сброса.
	import { m } from '$lib/i18n';
	import type { TgWebProxyConfig, TelegramUpstream, WebProxyEndpoint, TgNetworkPreset } from '$lib/types';
	import type { KeenDNSDiscovery } from '$lib/types/serverWizard';
	import { Button, IconButton } from '$lib/components/ui';
	import TelegramUpstreamsEditor from '../TelegramUpstreamsEditor.svelte';
	import { directWanState, isCloudKeenDNSHost } from '../external-access/externalAccessModel';
	import {
		WEBPROXY_PRESETS,
		getPresetMeta,
		inferEndpointPreset,
		newEndpoint,
		type EgressChoice,
	} from './tgProxyModel';
	import { AlertTriangle, Plus, Trash2 } from 'lucide-svelte';

	interface Props {
		editConfig: Partial<TgWebProxyConfig>;
		editUpstreams: TelegramUpstream[];
		egressOptions: EgressChoice[];
		keendnsInfo?: KeenDNSDiscovery | null;
		onrequestreset: () => void;
	}

	let {
		editConfig = $bindable({}),
		editUpstreams = $bindable([]),
		egressOptions,
		keendnsInfo = null,
		onrequestreset,
	}: Props = $props();

	let keenDNSBaseDomain = $derived(
		keendnsInfo?.enabled && keendnsInfo.domain ? keendnsInfo.domain.trim() : ''
	);
	let keenDNSBaseIsDirect = $derived(
		Boolean(keendnsInfo?.enabled && keendnsInfo.mode === 'direct')
	);
	let detectedWhiteIP = $derived(
		keendnsInfo?.wan_ip && !keendnsInfo.wan_ip_private ? keendnsInfo.wan_ip.trim() : ''
	);
	let isCloudDirectHost = $derived(
		editConfig.scenario !== 'cdn_http' &&
			isCloudKeenDNSHost(editConfig.direct_host, keenDNSBaseDomain, keenDNSBaseIsDirect)
	);

	let wanState = $derived(
		directWanState(editConfig.scenario, editConfig.direct_host, editConfig.direct_port)
	);

	function applyEndpointPreset(ep: WebProxyEndpoint, presetId: TgNetworkPreset, idx: number) {
		const meta = getPresetMeta(presetId);
		ep.preset = meta.id;
		ep.is_cdn = meta.is_cdn;
		ep.carrier_mode = meta.carrier_mode;
		if (idx === 0) {
			editConfig.network_preset = meta.id;
			editConfig.carrier_mode = meta.carrier_mode;
		}
	}

	function addEndpoint() {
		if (!editConfig.endpoints) editConfig.endpoints = [];
		editConfig.endpoints.push(newEndpoint(editConfig, editConfig.endpoints.length));
	}

	function removeEndpoint(idx: number) {
		if (editConfig.endpoints && editConfig.endpoints.length > 1) {
			editConfig.endpoints.splice(idx, 1);
		}
	}
</script>

<div class="settings-form">
	<div class="form-field">
		<label class="field-label" for="tg-settings-direct-host">{m.servers_tg_direct_host_label()}</label>
		<input
			id="tg-settings-direct-host"
			type="text"
			bind:value={editConfig.direct_host}
			class="text-input"
			class:input-error={isCloudDirectHost}
			placeholder="router.example.com"
		/>
		{#if keenDNSBaseDomain || detectedWhiteIP}
			<div class="pills-row">
				{#if keenDNSBaseDomain}
					<button
						type="button"
						class="pill-btn"
						class:pill-warn={!keenDNSBaseIsDirect}
						class:active={(editConfig.direct_host || '').trim().toLowerCase() === keenDNSBaseDomain.toLowerCase()}
						onclick={() => (editConfig.direct_host = keenDNSBaseDomain)}
					>
						{m.servers_ext_wiz_s3_tg_3rd_domain_pill({ domain: keenDNSBaseDomain })}{keenDNSBaseIsDirect ? '' : ` ${m.servers_ext_wiz_s3_tg_cloud_suffix()}`}
					</button>
				{/if}
				{#if detectedWhiteIP}
					<button
						type="button"
						class="pill-btn"
						class:active={(editConfig.direct_host || '').trim() === detectedWhiteIP}
						onclick={() => (editConfig.direct_host = detectedWhiteIP)}
					>
						{m.servers_ext_wiz_s3_tg_white_ip_pill({ ip: detectedWhiteIP })}
					</button>
				{/if}
			</div>
		{/if}
		{#if isCloudDirectHost}
			<div class="direct-cloud-warn">
				<AlertTriangle size={14} class="warn-icon-inline" />
				<span>{m.servers_tg_direct_host_cloud_warn({ domain: (editConfig.direct_host || '').trim() })}</span>
			</div>
		{/if}
		<span class="field-hint">{m.servers_tg_direct_host_hint()}</span>
	</div>

	<div class="form-row">
		<label class="form-field">
			<span class="field-label">{m.servers_tg_tls_domain_label()}</span>
			<input type="text" bind:value={editConfig.tls_domain} class="text-input" placeholder="www.google.com" />
		</label>
		<label class="form-field">
			<span class="field-label">{m.servers_tg_direct_port_label()}</span>
			<input
				type="number"
				min="1024"
				max="65535"
				bind:value={editConfig.direct_port}
				class="text-input"
				class:input-error={wanState === 'bad_port'}
				placeholder="8443"
			/>
		</label>
	</div>
	{#if wanState === 'bad_port'}
		<span class="field-hint port-error" role="alert">{m.servers_ext_wiz_s3_direct_port_invalid()} {m.servers_tg_direct_wan_bad_port()}</span>
	{:else if wanState === 'opens'}
		<span class="field-hint">{m.servers_tg_direct_wan_opens({ port: editConfig.direct_port || 8443 })}</span>
	{:else if wanState === 'no_host'}
		<span class="field-hint">{m.servers_tg_direct_wan_no_host({ port: editConfig.direct_port || 8443 })}</span>
	{/if}

	<TelegramUpstreamsEditor bind:upstreams={editUpstreams} {egressOptions} />

	<div class="settings-section-divider"></div>
	<div class="section-title-row">
		<div>
			<h4 class="section-heading">{m.servers_tg_domains_title()}</h4>
			<p class="section-desc">
				{m.servers_tg_domains_desc()}
			</p>
		</div>
		<Button variant="secondary" size="sm" onclick={addEndpoint}>
			<Plus size={14} />
			{m.servers_tg_domain_add()}
		</Button>
	</div>

	<div class="endpoints-list">
		{#if editConfig.endpoints && editConfig.endpoints.length > 0}
			{#each editConfig.endpoints as ep, idx (ep.id || idx)}
				{@const epPresetMeta = getPresetMeta(ep.preset || inferEndpointPreset(ep, editConfig))}
				<div class="endpoint-card">
					<div class="endpoint-card-header">
						<div class="endpoint-title-group">
							<span class="endpoint-card-title">
								{ep.hostname
									? m.servers_tg_domain_title_host({ n: idx + 1, host: ep.hostname })
									: m.servers_tg_domain_title({ n: idx + 1 })}
							</span>
							<span class="badge {epPresetMeta.badgeClass} text-[10px]">{epPresetMeta.badge()}</span>
						</div>
						<div class="endpoint-card-actions">
							<label class="cdn-checkbox-label">
								<input type="checkbox" bind:checked={ep.enabled} class="checkbox-input" />
								<span>{m.servers_tg_domain_enabled()}</span>
							</label>
							{#if (editConfig.endpoints?.length || 0) > 1}
								<IconButton
									variant="danger"
									onclick={() => removeEndpoint(idx)}
									title={m.servers_tg_domain_remove()}
									ariaLabel={m.servers_tg_domain_remove()}
								>
									<Trash2 size={14} />
								</IconButton>
							{/if}
						</div>
					</div>

					<div class="ep-preset-bar">
						<span class="ep-preset-label">{m.servers_tg_domain_scheme()}</span>
						<div class="ep-preset-pills">
							{#each WEBPROXY_PRESETS as p (p.id)}
								<button
									type="button"
									class="ep-preset-pill"
									class:active={(ep.preset || epPresetMeta.id) === p.id}
									onclick={() => applyEndpointPreset(ep, p.id, idx)}
								>
									{p.label()}
								</button>
							{/each}
						</div>
						<span class="ep-preset-specs font-mono">{epPresetMeta.subtitle()} — {epPresetMeta.specs()}</span>
					</div>

					<div class="form-row">
						<label class="form-field flex-1">
							<span class="field-label">{m.servers_tg_domain_label()}</span>
							<input type="text" bind:value={ep.hostname} class="text-input font-mono" placeholder="tg.example.com" />
						</label>
						<label class="form-field flex-1">
							<span class="field-label">{m.servers_tg_domain_desc_label()}</span>
							<input type="text" bind:value={ep.description} class="text-input" placeholder={m.servers_tg_domain_primary()} />
						</label>
					</div>
				</div>
			{/each}
		{/if}
	</div>

	<div class="reset-zone">
		<div class="reset-zone-info">
			<div class="reset-zone-title">{m.server_reset_zone_title()}</div>
			<p class="reset-zone-desc">{m.server_reset_tg_zone_desc()}</p>
		</div>
		<Button variant="danger" size="sm" onclick={onrequestreset}>
			<Trash2 size={14} />
			{m.server_reset_button()}
		</Button>
	</div>
</div>

<style>
	.settings-form {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}

	.form-row {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 12px;
	}

	.form-field {
		display: flex;
		flex-direction: column;
		gap: 4px;
		min-width: 0;
		margin-bottom: 0;
	}

	.field-label {
		font-size: 12px;
		font-weight: 500;
		color: var(--color-text-primary);
		margin-bottom: 0;
		line-height: 1.35;
	}

	.field-hint {
		font-size: 11.5px;
		color: var(--color-text-muted);
		margin: 0;
	}

	.text-input {
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 6px 10px;
		font-size: 13px;
		color: var(--color-text-primary);
	}

	.field-hint.port-error {
		color: var(--color-error);
	}

	.text-input.input-error {
		border-color: var(--color-warning-border);
	}

	.pills-row {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
		margin-top: 2px;
	}

	.pill-btn {
		display: inline-flex;
		align-items: center;
		max-width: 100%;
		padding: 3px 9px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		color: var(--color-text-secondary);
		font-size: 11px;
		line-height: 1.35;
		text-align: left;
		overflow-wrap: anywhere;
		cursor: pointer;
		transition: border-color 0.15s, background 0.15s, color 0.15s;
	}

	.pill-btn:hover {
		border-color: var(--color-accent);
		color: var(--color-text-primary);
	}

	.pill-btn.active {
		border-color: var(--color-accent-border);
		background: var(--color-accent-tint);
		color: var(--color-accent);
		font-weight: 600;
	}

	.pill-btn.pill-warn {
		border-color: var(--color-warning-border);
		color: var(--color-warning);
	}

	.pill-btn.pill-warn:hover,
	.pill-btn.pill-warn.active {
		border-color: var(--color-warning);
		background: var(--color-warning-tint);
		color: var(--color-warning);
	}

	.pill-btn.pill-warn.active {
		font-weight: 600;
	}

	.direct-cloud-warn {
		display: flex;
		align-items: flex-start;
		gap: 6px;
		padding: 7px 9px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-warning-border);
		background: var(--color-warning-tint);
		font-size: 11.5px;
		line-height: 1.4;
		color: var(--color-text-primary);
	}

	:global(.warn-icon-inline) {
		color: var(--color-warning);
		flex-shrink: 0;
		margin-top: 2px;
	}

	.badge {
		font-size: 11px;
		padding: 2px 8px;
		border-radius: var(--radius-sm);
		font-weight: 500;
	}
	.badge-success {
		background: var(--color-success-tint);
		border: 1px solid var(--color-success-border);
		color: var(--color-success);
	}
	.badge-neutral {
		background: var(--color-bg-tertiary);
		color: var(--color-text-muted);
	}
	.badge-info {
		background: var(--color-info-tint);
		border: 1px solid var(--color-info-border);
		color: var(--color-info);
	}
	.badge-purple {
		background: var(--color-purple-tint);
		border: 1px solid rgba(187, 154, 247, 0.25);
		color: var(--color-purple);
	}

	.settings-section-divider {
		height: 1px;
		background: var(--color-border);
		margin: 6px 0;
	}

	.section-title-row {
		display: flex;
		justify-content: space-between;
		align-items: flex-start;
		gap: 12px;
	}

	.section-heading {
		font-size: 14px;
		font-weight: 600;
		color: var(--color-text-primary);
		margin: 0 0 2px 0;
	}

	.section-desc {
		font-size: 11px;
		color: var(--color-text-muted);
		margin: 0;
	}

	.endpoints-list {
		display: flex;
		flex-direction: column;
		gap: 10px;
	}

	.endpoint-card {
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 12px;
		display: flex;
		flex-direction: column;
		gap: 8px;
	}

	.endpoint-card-header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		padding-bottom: 6px;
		border-bottom: 1px dashed var(--color-border);
	}

	.endpoint-title-group {
		display: flex;
		align-items: center;
		gap: 8px;
		flex-wrap: wrap;
	}

	.endpoint-card-title {
		font-size: 12px;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.ep-preset-bar {
		display: flex;
		flex-direction: column;
		gap: 6px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 8px 10px;
	}

	.ep-preset-label {
		font-size: 11px;
		font-weight: 500;
		color: var(--color-text-muted);
	}

	.ep-preset-pills {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
	}

	.ep-preset-pill {
		font-size: 11px;
		font-weight: 500;
		padding: 4px 10px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		color: var(--color-text-secondary);
		cursor: pointer;
	}
	.ep-preset-pill.active {
		background: var(--color-accent-tint);
		border-color: var(--color-accent-border);
		color: var(--color-accent);
		font-weight: 600;
	}

	.ep-preset-specs {
		font-size: 11px;
		color: var(--color-text-muted);
	}

	.endpoint-card-actions {
		display: flex;
		align-items: center;
		gap: 12px;
	}

	.cdn-checkbox-label {
		display: flex;
		align-items: center;
		gap: 5px;
		font-size: 12px;
		color: var(--color-text-secondary);
		cursor: pointer;
	}

	.checkbox-input {
		accent-color: var(--color-accent);
		cursor: pointer;
	}

	.flex-1 {
		flex: 1;
	}
	.font-mono {
		font-family: var(--font-mono);
	}

	.reset-zone {
		margin-top: 10px;
		padding: 12px 14px;
		border: 1px solid color-mix(in srgb, var(--color-error) 35%, var(--color-border));
		border-radius: var(--radius-sm);
		background: color-mix(in srgb, var(--color-error) 6%, var(--color-bg-secondary));
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 12px;
		flex-wrap: wrap;
	}

	.reset-zone-info {
		display: flex;
		flex-direction: column;
		gap: 3px;
		flex: 1 1 260px;
	}

	.reset-zone-title {
		font-size: 13px;
		font-weight: 600;
		color: var(--color-error);
	}

	.reset-zone-desc {
		margin: 0;
		font-size: 12px;
		color: var(--color-text-secondary);
		line-height: 1.4;
	}

	@media (max-width: 640px) {
		.form-row {
			grid-template-columns: 1fr;
		}
	}
</style>
