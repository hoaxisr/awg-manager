<script lang="ts">
	// Шаг 3 мастера внешнего доступа: адрес подключения, KeenDNS, порты и Reality.
	import { m } from '$lib/i18n';
	import { Button } from '$lib/components/ui';
	import type { KeenDNSDiscovery, KeenDNSPublication } from '$lib/types/serverWizard';
	import type { XrayRealityState, XrayRealityTransport } from '$lib/types/xray';
	import type { ExternalAccessWizardDraft, validateKeenDNSLabelClient } from './externalAccessModel';
	import KeenDnsAddressBox from './KeenDnsAddressBox.svelte';
	import RealityFields from '../reality/RealityFields.svelte';
	import { AlertTriangle, ChevronDown, ChevronUp } from 'lucide-svelte';

	type KeenDNSMode = 'manual' | 'existing' | 'create';

	interface Props {
		draft: ExternalAccessWizardDraft;
		isRealityOnly: boolean;
		isTgDirectOnly: boolean;
		usesReality: boolean;
		wantXray: boolean;
		wantTgWeb: boolean;
		wantTgDirect: boolean;
		grayIPDetected?: boolean;
		isDirectHostCloudKeenDNS?: boolean;
		detectedMtprotoWhiteIP: string;
		keenDNSBaseDomain: string;
		keenDNSBaseIsDirect: boolean;
		detectedMtprotoDirectDomain: string;
		keendnsInfo: KeenDNSDiscovery | null;
		keendnsAvailable: boolean;
		keendnsValidation: ReturnType<typeof validateKeenDNSLabelClient>;
		keendnsLabelConflict: string;
		previewKeenDNSFqdn: string;
		compatibleKeenDNSPublications: KeenDNSPublication[];
		realityState: XrayRealityState | null;
		realityPort: number;
		realityTransport: XrayRealityTransport;
		realityDest: string;
		realityServerName: string;
		realityPublicHost: string;
		showAdvancedAddresses: boolean;
		onsetkeendnsmode: (mode: KeenDNSMode) => void;
		onapplypublication: (label: string, fqdn: string) => void;
		onsynckeendns: () => void;
		onswitchtowebonly?: () => void;
	}

	let {
		draft = $bindable(),
		isRealityOnly,
		isTgDirectOnly,
		usesReality,
		wantXray,
		wantTgWeb,
		wantTgDirect,
		grayIPDetected = false,
		isDirectHostCloudKeenDNS = false,
		detectedMtprotoWhiteIP,
		keenDNSBaseDomain,
		keenDNSBaseIsDirect,
		detectedMtprotoDirectDomain,
		keendnsInfo,
		keendnsAvailable,
		keendnsValidation,
		keendnsLabelConflict,
		previewKeenDNSFqdn,
		compatibleKeenDNSPublications,
		realityState = $bindable(),
		realityPort = $bindable(),
		realityTransport = $bindable(),
		realityDest = $bindable(),
		realityServerName = $bindable(),
		realityPublicHost = $bindable(),
		showAdvancedAddresses = $bindable(),
		onsetkeendnsmode,
		onapplypublication,
		onsynckeendns,
		onswitchtowebonly,
	}: Props = $props();

	let hideCloudFallback = $derived(draft.originType === 'cloud_address' && draft.keendnsMode !== 'manual');

	const TLS_SNI_PRESETS = [
		{ domain: 'www.google.com', label: 'Google (www.google.com)' },
		{ domain: 'www.microsoft.com', label: 'Microsoft (www.microsoft.com)' },
		{ domain: 'www.apple.com', label: 'Apple (www.apple.com)' },
		{ domain: 'dl.google.com', label: 'Google Downloads (dl.google.com)' },
	];
</script>

<div class="step-section">
	<h3 class="step-heading">{m.servers_ext_wiz_s3_heading()}</h3>

	{#if isRealityOnly}
		<p class="step-desc">
			{m.servers_ext_wiz_s3_reality_desc()}
		</p>
		<div class="origin-box">
			<RealityFields
				rs={realityState}
				bind:port={realityPort}
				bind:transport={realityTransport}
				bind:dest={realityDest}
				bind:serverName={realityServerName}
				bind:publicHost={realityPublicHost}
				onrefreshed={(fresh) => (realityState = fresh)}
			/>
		</div>
	{:else if isTgDirectOnly}
		<p class="step-desc">
			{m.servers_ext_wiz_s3_tg_only_desc()}
		</p>
		<div class="origin-box">
			<div class="origin-header-row">
				<span class="origin-title">{m.servers_ext_wiz_s3_tg_direct_title()}</span>
				{#if (keenDNSBaseDomain && keenDNSBaseIsDirect) || detectedMtprotoWhiteIP}
					<div class="pills-row">
						{#if keenDNSBaseDomain && keenDNSBaseIsDirect}
							<button
								type="button"
								class="pill-btn"
								class:active={draft.directHost.trim().toLowerCase() === keenDNSBaseDomain.toLowerCase()}
								onclick={() => (draft.directHost = keenDNSBaseDomain)}
							>
								{m.servers_ext_wiz_s3_tg_3rd_domain_pill({ domain: keenDNSBaseDomain })}
							</button>
						{/if}
						{#if detectedMtprotoWhiteIP}
							<button
								type="button"
								class="pill-btn"
								class:active={draft.directHost.trim() === detectedMtprotoWhiteIP}
								onclick={() => (draft.directHost = detectedMtprotoWhiteIP)}
							>
								{m.servers_ext_wiz_s3_tg_white_ip_pill({ ip: detectedMtprotoWhiteIP })}
							</button>
						{/if}
					</div>
				{/if}
			</div>
			<div class="form-grid-2">
				<div class="field-block">
					<label class="field-label" for="wiz-direct-host-only">
						{m.servers_ext_wiz_s3_tg_direct_host_label()}
					</label>
					<input
						id="wiz-direct-host-only"
						type="text"
						class="text-input mono"
						bind:value={draft.directHost}
						placeholder={detectedMtprotoDirectDomain || detectedMtprotoWhiteIP || m.servers_ext_wiz_s3_tg_direct_host_ph()}
					/>
					{#if keenDNSBaseDomain && !keenDNSBaseIsDirect}
						<div class="scheme-warn-note" style="margin-top: 2px;">
							<AlertTriangle size={14} />
							<span>
								{m.servers_ext_wiz_s3_tg_cloud_warn_before()} <strong class="mono">{keenDNSBaseDomain}</strong> {m.servers_ext_wiz_s3_tg_cloud_warn_after()}
							</span>
						</div>
					{/if}
					<span class="field-hint">
						{m.servers_ext_wiz_s3_tg_direct_only_hint()}
					</span>
				</div>

				<div class="field-block">
					<label class="field-label" for="wiz-tls-preset-only">
						{m.servers_ext_wiz_s3_tls_domain()}
					</label>
					<select id="wiz-tls-preset-only" class="select-input" bind:value={draft.tlsPreset}>
						{#each TLS_SNI_PRESETS as p (p.domain)}
							<option value={p.domain}>{p.label}</option>
						{/each}
						<option value="__custom__">{m.servers_ext_wiz_s3_tls_custom()}</option>
					</select>
					{#if draft.tlsPreset === '__custom__'}
						<input
							type="text"
							class="text-input mono"
							style="margin-top: 6px;"
							bind:value={draft.customTlsDomain}
							placeholder="example.com"
						/>
					{/if}
				</div>
			</div>

			<div class="form-grid-2" style="margin-top: 4px;">
				<div class="field-block">
					<label class="field-label" for="wiz-direct-port-only">
						{m.servers_ext_wiz_s3_tg_only_port_label()}
					</label>
					<input
						id="wiz-direct-port-only"
						type="number"
						min="1024"
						max="65535"
						class="text-input mono"
						bind:value={draft.directPort}
					/>
					<span class="field-hint">
						{m.servers_ext_wiz_s3_tg_only_port_hint()}
					</span>
				</div>
			</div>
		</div>
	{:else}
	{#if draft.connectionWay === 'direct'}
		<!-- Схема: Напрямую к адресу роутера -->
		<p class="step-desc">
			{m.servers_ext_wiz_s3_direct_desc()}
		</p>

		<KeenDnsAddressBox
			variant="direct"
			mode={draft.keendnsMode}
			bind:label={draft.keendnsLabel}
			{keendnsInfo}
			{keendnsAvailable}
			validation={keendnsValidation}
			labelConflict={keendnsLabelConflict}
			previewFqdn={previewKeenDNSFqdn}
			publications={compatibleKeenDNSPublications}
			onsetmode={onsetkeendnsmode}
			{onapplypublication}
			onsync={onsynckeendns}
		>
			{#snippet manual()}
				<div class="field-block">
					<label class="field-label" for="wiz-direct-pub-domain">
						{m.servers_ext_wiz_s3_phone_addr()}
					</label>
					<input
						id="wiz-direct-pub-domain"
						type="text"
						class="text-input mono"
						bind:value={draft.publicDomain}
						placeholder="connect.example.keenetic.pro"
					/>
					<span class="field-hint">
						{m.servers_ext_wiz_s3_phone_addr_hint()}
					</span>
				</div>
			{/snippet}
		</KeenDnsAddressBox>
	{:else}
		<!-- Схема: Через внешний сервис с вашим доменом -->
		<p class="step-desc">
			{m.servers_ext_wiz_s3_ext_desc()}
		</p>

		{#if wantXray || wantTgWeb}
			<div class="field-block">
				<label class="field-label" for="wiz-pub-domain">
					{m.servers_ext_wiz_s3_ext_phone_addr()}
				</label>
				<input
					id="wiz-pub-domain"
					type="text"
					class="text-input mono"
					bind:value={draft.publicDomain}
					placeholder="access.example.com"
				/>
				<span class="field-hint">
					{m.servers_ext_wiz_s3_ext_phone_addr_hint()}
				</span>
			</div>
		{/if}

		{#if draft.selectedKind === 'both' && wantTgWeb}
			<label class="checkbox-row">
				<input type="checkbox" bind:checked={draft.useSeparateTgDomain} />
				<span>{m.servers_ext_wiz_s3_separate_tg()}</span>
			</label>

			{#if draft.useSeparateTgDomain}
				<div class="field-block">
					<label class="field-label" for="wiz-tg-domain">
						{m.servers_ext_wiz_s3_tg_domain()}
					</label>
					<input
						id="wiz-tg-domain"
						type="text"
						class="text-input mono"
						bind:value={draft.tgPublicDomain}
						placeholder="tg.example.com"
					/>
				</div>
			{/if}
		{/if}

		<KeenDnsAddressBox
			variant="ext"
			mode={draft.keendnsMode}
			bind:label={draft.keendnsLabel}
			{keendnsInfo}
			{keendnsAvailable}
			validation={keendnsValidation}
			labelConflict={keendnsLabelConflict}
			previewFqdn={previewKeenDNSFqdn}
			publications={compatibleKeenDNSPublications}
			onsetmode={onsetkeendnsmode}
			{onapplypublication}
			onsync={onsynckeendns}
		>
			{#snippet manual()}
				<div class="form-grid-3">
					<div class="field-block">
						<label class="field-label" for="wiz-origin-host">{m.servers_ext_wiz_s3_router_addr()}</label>
						<input
							id="wiz-origin-host"
							type="text"
							class="text-input mono"
							bind:value={draft.originHost}
							placeholder={m.servers_ext_wiz_s3_router_addr_ph()}
						/>
					</div>
					<div class="field-block">
						<label class="field-label" for="wiz-origin-scheme">{m.servers_ext_wiz_s3_protocol()}</label>
						<select
							id="wiz-origin-scheme"
							class="select-input mono"
							bind:value={draft.originScheme}
						>
							<option value="http">HTTP</option>
							<option value="https">HTTPS</option>
						</select>
					</div>
					<div class="field-block">
						<label class="field-label" for="wiz-origin-port">{m.servers_wizard_result_param_port()}</label>
						<input
							id="wiz-origin-port"
							type="number"
							class="text-input mono"
							bind:value={draft.originPort}
						/>
					</div>
				</div>
			{/snippet}
		</KeenDnsAddressBox>
	{/if}

	{#if wantTgDirect}
		<div class="origin-box">
			{#if (draft.originType === 'cloud_address' || grayIPDetected) && onswitchtowebonly}
				<div class="dual-cgnat-warn-box">
					<AlertTriangle size={15} class="warn-icon" />
					<div class="dual-cgnat-warn-body">
						<span>{m.servers_ext_wiz_s3_dual_cgnat_warn()}</span>
						<div>
							<Button variant="secondary" size="sm" onclick={onswitchtowebonly}>
								{m.servers_ext_wiz_s3_disable_mtproto_btn()}
							</Button>
						</div>
					</div>
				</div>
			{/if}
			<div class="origin-header-row">
				<span class="origin-title">{m.servers_ext_wiz_s3_tg_direct_title()}</span>
				{#if (keenDNSBaseDomain && keenDNSBaseIsDirect) || detectedMtprotoWhiteIP}
					<div class="pills-row">
						{#if keenDNSBaseDomain && keenDNSBaseIsDirect}
							<button
								type="button"
								class="pill-btn"
								class:active={draft.directHost.trim().toLowerCase() === keenDNSBaseDomain.toLowerCase()}
								onclick={() => (draft.directHost = keenDNSBaseDomain)}
							>
								{m.servers_ext_wiz_s3_tg_3rd_domain_pill({ domain: keenDNSBaseDomain })}
							</button>
						{/if}
						{#if detectedMtprotoWhiteIP}
							<button
								type="button"
								class="pill-btn"
								class:active={draft.directHost.trim() === detectedMtprotoWhiteIP}
								onclick={() => (draft.directHost = detectedMtprotoWhiteIP)}
							>
								{m.servers_ext_wiz_s3_tg_white_ip_pill({ ip: detectedMtprotoWhiteIP })}
							</button>
						{/if}
					</div>
				{/if}
			</div>
			<div class="form-grid-2">
				<div class="field-block">
					<label class="field-label" for="wiz-direct-host">
						{m.servers_ext_wiz_s3_direct_host()}
					</label>
					<input
						id="wiz-direct-host"
						type="text"
						class="text-input mono"
						bind:value={draft.directHost}
						placeholder={hideCloudFallback
							? detectedMtprotoDirectDomain || detectedMtprotoWhiteIP || m.servers_ext_wiz_s3_tg_direct_host_ph()
							: detectedMtprotoDirectDomain || detectedMtprotoWhiteIP || draft.originHost || draft.publicDomain || 'router.example.com'}
					/>
					{#if (keenDNSBaseDomain && !keenDNSBaseIsDirect) || isDirectHostCloudKeenDNS}
						<div class="scheme-warn-note" style="margin-top: 2px;">
							<AlertTriangle size={14} />
							<span>
								{m.servers_ext_wiz_s3_tg_cloud_warn_before()} <strong class="mono">{keenDNSBaseDomain || draft.directHost.trim()}</strong> {m.servers_ext_wiz_s3_tg_cloud_warn_after()}
							</span>
						</div>
					{/if}
					<span class="field-hint">
						{hideCloudFallback ? m.servers_ext_wiz_s3_tg_direct_only_hint() : m.servers_ext_wiz_s3_direct_host_hint()}
					</span>
				</div>

				<div class="field-block">
					<label class="field-label" for="wiz-tls-preset">
						{m.servers_ext_wiz_s3_tls_domain()}
					</label>
					<select id="wiz-tls-preset" class="select-input" bind:value={draft.tlsPreset}>
						{#each TLS_SNI_PRESETS as p (p.domain)}
							<option value={p.domain}>{p.label}</option>
						{/each}
						<option value="__custom__">{m.servers_ext_wiz_s3_tls_custom()}</option>
					</select>
					{#if draft.tlsPreset === '__custom__'}
						<input
							type="text"
							class="text-input mono"
							style="margin-top: 6px;"
							bind:value={draft.customTlsDomain}
							placeholder="example.com"
						/>
					{/if}
				</div>
			</div>
		</div>
	{/if}

	<button
		type="button"
		class="advanced-toggle"
		onclick={() => (showAdvancedAddresses = !showAdvancedAddresses)}
	>
		{#if showAdvancedAddresses}
			<ChevronUp size={15} />
		{:else}
			<ChevronDown size={15} />
		{/if}
		<span>{m.servers_ext_wiz_s3_advanced()}</span>
	</button>

	{#if showAdvancedAddresses}
		<div class="advanced-box">
			{#if wantXray}
				<div class="form-grid-2">
					<div class="field-block">
						<label class="field-label" for="wiz-xray-path">{m.servers_ext_wiz_s3_vless_path()}</label>
						<input
							id="wiz-xray-path"
							type="text"
							class="text-input mono"
							bind:value={draft.path}
						/>
					</div>
					<div class="field-block">
						<label class="field-label" for="wiz-pub-port">{m.servers_ext_wiz_s3_public_port()}</label>
						<input
							id="wiz-pub-port"
							type="number"
							class="text-input mono"
							bind:value={draft.publicPort}
						/>
					</div>
				</div>
			{/if}
			{#if wantTgDirect}
				<div class="form-grid-2">
					<div class="field-block">
						<label class="field-label" for="wiz-direct-port">{m.servers_ext_wiz_s3_direct_port()}</label>
						<input
							id="wiz-direct-port"
							type="number"
							class="text-input mono"
							bind:value={draft.directPort}
						/>
					</div>
				</div>
			{/if}
		</div>
	{/if}

	{#if usesReality}
		<div class="origin-box">
			<div class="origin-title">{m.servers_ext_wiz_s3_reality_parallel()}</div>
			<RealityFields
				rs={realityState}
				bind:port={realityPort}
				bind:transport={realityTransport}
				bind:dest={realityDest}
				bind:serverName={realityServerName}
				bind:publicHost={realityPublicHost}
				onrefreshed={(fresh) => (realityState = fresh)}
			/>
		</div>
	{/if}
	{/if}
</div>

<style>
	.origin-header-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 10px;
		flex-wrap: wrap;
	}

	.pills-row {
		display: flex;
		gap: 6px;
		flex-wrap: wrap;
	}

	.pill-btn {
		display: inline-flex;
		align-items: center;
		padding: 5px 10px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
		color: var(--color-text-secondary);
		font-size: 12px;
		cursor: pointer;
	}

	.pill-btn.active {
		border-color: var(--color-accent-border);
		background: var(--color-accent-tint);
		color: var(--color-accent);
		font-weight: 600;
	}

	.scheme-warn-note {
		display: flex;
		align-items: center;
		gap: 8px;
		font-size: 12px;
		color: var(--color-warning);
	}

	.dual-cgnat-warn-box {
		display: flex;
		align-items: flex-start;
		gap: 8px;
		padding: 9px 11px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-warning-border);
		background: var(--color-warning-tint);
		font-size: 12px;
		line-height: 1.45;
		color: var(--color-text-primary);
	}

	.dual-cgnat-warn-body {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}

	.advanced-box {
		padding: 12px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		display: flex;
		flex-direction: column;
		gap: 10px;
	}
</style>
