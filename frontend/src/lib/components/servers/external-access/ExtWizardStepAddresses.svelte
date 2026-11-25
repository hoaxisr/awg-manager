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
		effectiveDirectHost?: string;
		isDirectHostCloudKeenDNS?: boolean;
		isDirectPortInvalid?: boolean;
		isRealityPublicHostCloudKeenDNS?: boolean;
		realityPublicHostRequired?: boolean;
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
		onswitchtowebbridge?: () => void;
		onswitchtowebonly?: () => void;
		onswitchtovlessdomain?: () => void;
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
		effectiveDirectHost = '',
		isDirectHostCloudKeenDNS = false,
		isDirectPortInvalid = false,
		isRealityPublicHostCloudKeenDNS = false,
		realityPublicHostRequired = false,
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
		onswitchtowebbridge,
		onswitchtowebonly,
		onswitchtovlessdomain,
	}: Props = $props();

	// Запасного адреса нет — прямой надо указать.
	let mustSetDirectHost = $derived(
		(draft.originType === 'cloud_address' && draft.keendnsMode !== 'manual') || !effectiveDirectHost
	);

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
				cloudHostError={isRealityPublicHostCloudKeenDNS
					? m.servers_ext_wiz_s3_reality_cloud_host_error({
							domain: realityPublicHost.trim(),
						})
					: ''}
				publicHostRequiredHint={realityPublicHostRequired
					? m.servers_ext_wiz_s3_reality_public_host_required()
					: ''}
				{onswitchtovlessdomain}
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
				{#if keenDNSBaseDomain || detectedMtprotoWhiteIP}
					<div class="pills-row">
						{#if keenDNSBaseDomain}
							<button
								type="button"
								class="pill-btn"
								class:pill-warn={!keenDNSBaseIsDirect}
								class:active={draft.directHost.trim().toLowerCase() === keenDNSBaseDomain.toLowerCase()}
								onclick={() => (draft.directHost = keenDNSBaseDomain)}
							>
								{m.servers_ext_wiz_s3_tg_3rd_domain_pill({ domain: keenDNSBaseDomain })}{keenDNSBaseIsDirect ? '' : ` ${m.servers_ext_wiz_s3_tg_cloud_suffix()}`}
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
			<div class="field-block">
				<label class="field-label" for="wiz-direct-host-only">
					{m.servers_ext_wiz_s3_tg_direct_host_label()}
				</label>
				<input
					id="wiz-direct-host-only"
					type="text"
					class="text-input mono"
					class:input-error={isDirectHostCloudKeenDNS}
					bind:value={draft.directHost}
					placeholder={detectedMtprotoDirectDomain || detectedMtprotoWhiteIP || m.servers_ext_wiz_s3_tg_direct_host_ph()}
				/>
				{#if isDirectHostCloudKeenDNS}
					<div class="cloud-host-error-box">
						<AlertTriangle size={14} class="icon-error-soft" />
						<div class="dual-cgnat-warn-body">
							<span>{m.servers_ext_wiz_s3_cloud_host_error({ domain: effectiveDirectHost || draft.directHost.trim() })}</span>
							{#if onswitchtowebbridge}
								<div>
									<Button variant="primary" size="sm" onclick={onswitchtowebbridge}>
										{m.servers_ext_wiz_s2_tg_switch_web()}
									</Button>
								</div>
							{/if}
						</div>
					</div>
				{:else if keenDNSBaseDomain && !keenDNSBaseIsDirect}
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

			<div class="form-grid-2">
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
						class:input-error={isDirectPortInvalid}
						bind:value={draft.directPort}
					/>
					{#if isDirectPortInvalid}
						<span class="field-error">{m.servers_ext_wiz_s3_direct_port_invalid()}</span>
					{/if}
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
				{#if keenDNSBaseDomain || detectedMtprotoWhiteIP}
					<div class="pills-row">
						{#if keenDNSBaseDomain}
							<button
								type="button"
								class="pill-btn"
								class:pill-warn={!keenDNSBaseIsDirect}
								class:active={draft.directHost.trim().toLowerCase() === keenDNSBaseDomain.toLowerCase()}
								onclick={() => (draft.directHost = keenDNSBaseDomain)}
							>
								{m.servers_ext_wiz_s3_tg_3rd_domain_pill({ domain: keenDNSBaseDomain })}{keenDNSBaseIsDirect ? '' : ` ${m.servers_ext_wiz_s3_tg_cloud_suffix()}`}
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
			<div class="field-block">
				<label class="field-label" for="wiz-direct-host">
					{m.servers_ext_wiz_s3_direct_host()}
				</label>
				<input
					id="wiz-direct-host"
					type="text"
					class="text-input mono"
					class:input-error={isDirectHostCloudKeenDNS}
					bind:value={draft.directHost}
					placeholder={detectedMtprotoDirectDomain ||
						detectedMtprotoWhiteIP ||
						(mustSetDirectHost ? m.servers_ext_wiz_s3_tg_direct_host_ph() : effectiveDirectHost)}
				/>
				{#if isDirectHostCloudKeenDNS}
					<div class="cloud-host-error-box">
						<AlertTriangle size={14} class="icon-error-soft" />
						<div class="dual-cgnat-warn-body">
							<span>{m.servers_ext_wiz_s3_cloud_host_error({ domain: effectiveDirectHost || draft.directHost.trim() })}</span>
							{#if onswitchtowebonly}
								<div>
									<Button variant="secondary" size="sm" onclick={onswitchtowebonly}>
										{m.servers_ext_wiz_s3_disable_mtproto_btn()}
									</Button>
								</div>
							{/if}
						</div>
					</div>
				{:else if keenDNSBaseDomain && !keenDNSBaseIsDirect}
					<div class="scheme-warn-note" style="margin-top: 2px;">
						<AlertTriangle size={14} />
						<span>
							{m.servers_ext_wiz_s3_tg_cloud_warn_before()} <strong class="mono">{keenDNSBaseDomain}</strong> {m.servers_ext_wiz_s3_tg_cloud_warn_after()}
						</span>
					</div>
				{/if}
				<span class="field-hint">
					{mustSetDirectHost ? m.servers_ext_wiz_s3_tg_direct_only_hint() : m.servers_ext_wiz_s3_direct_host_hint()}
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
	{#if !showAdvancedAddresses && wantTgDirect && isDirectPortInvalid}
		<span class="field-error" role="alert">
			{m.servers_ext_wiz_s3_direct_port()} {m.servers_ext_wiz_s3_direct_port_invalid()}
		</span>
	{/if}

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
							min="1024"
							max="65535"
							class="text-input mono"
							class:input-error={isDirectPortInvalid}
							bind:value={draft.directPort}
						/>
						{#if isDirectPortInvalid}
							<span class="field-error">{m.servers_ext_wiz_s3_direct_port_invalid()}</span>
						{/if}
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
				cloudHostError={isRealityPublicHostCloudKeenDNS
					? m.servers_ext_wiz_s3_reality_cloud_host_error({
							domain: realityPublicHost.trim(),
						})
					: ''}
				publicHostRequiredHint={realityPublicHostRequired
					? m.servers_ext_wiz_s3_reality_public_host_required()
					: ''}
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
		gap: 5px;
		max-width: 100%;
		padding: 5px 10px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
		color: var(--color-text-secondary);
		font-size: 12px;
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

	.pill-btn.pill-warn:hover {
		border-color: var(--color-warning);
		background: var(--color-warning-tint);
		color: var(--color-warning);
	}

	.pill-btn.pill-warn.active {
		border-color: var(--color-warning);
		background: var(--color-warning-tint);
		color: var(--color-warning);
		font-weight: 600;
	}

	:global(.ext-wizard .text-input.input-error) {
		border-color: var(--color-error-border);
	}

	:global(.ext-wizard .text-input.input-error:focus) {
		border-color: var(--color-error);
	}

	.field-error {
		font-size: 11.5px;
		color: var(--color-error);
		line-height: 1.4;
	}

	.cloud-host-error-box {
		display: flex;
		align-items: flex-start;
		gap: 8px;
		margin-top: 4px;
		padding: 9px 11px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-error-border);
		background: var(--color-error-tint);
		font-size: 12px;
		line-height: 1.45;
		color: var(--color-text-primary);
	}

	:global(.icon-error-soft) {
		color: var(--color-error);
		flex-shrink: 0;
		margin-top: 2px;
	}

	.scheme-warn-note {
		display: flex;
		align-items: flex-start;
		gap: 8px;
		font-size: 12px;
		line-height: 1.4;
		color: var(--color-warning);
	}

	.scheme-warn-note > :global(svg) {
		flex-shrink: 0;
		margin-top: 2px;
	}

	.scheme-warn-note > span {
		min-width: 0;
		flex: 1;
		overflow-wrap: anywhere;
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
		gap: 8px;
		min-width: 0;
		flex: 1;
	}

	/* Длинная подпись на 360px шире блока. */
	.dual-cgnat-warn-body :global(button) {
		white-space: normal;
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
