<script lang="ts">
	// Шаг 5 мастера внешнего доступа: сводка перед применением, ход применения
	// и результат с данными подключения.
	import { m, uiText, type UiText } from '$lib/i18n';
	import type {
		EgressOption,
		JobStatusResponse,
		PreflightResponse,
		RevealCredentials,
		ServerPlanRecord,
	} from '$lib/types/serverWizard';
	import type { XrayRealityState, XrayRealityTransport } from '$lib/types/xray';
	import { formatDomainList, type ExternalAccessWizardDraft } from './externalAccessModel';
	import { EXTERNAL_ACCESS_LABELS, cleanTunnelDisplayName } from './connectionLabels';
	import PreflightList from '../wizard/PreflightList.svelte';
	import PlanSummary from '../wizard/PlanSummary.svelte';
	import ApplyProgress from '../wizard/ApplyProgress.svelte';
	import ConnectionResult from '../wizard/ConnectionResult.svelte';
	import ConnectionChecks from '../wizard/ConnectionChecks.svelte';
	import RealityLinkResult from '../reality/RealityLinkResult.svelte';
	import { AlertTriangle, CheckCircle2, ChevronDown, ChevronUp, Globe, Info } from 'lucide-svelte';

	interface Props {
		draft: ExternalAccessWizardDraft;
		applyPhase: 'review' | 'applying' | 'done';
		isRealityOnly: boolean;
		isTgDirectOnly: boolean;
		grayIPDetected?: boolean;
		usesReality: boolean;
		wantXray: boolean;
		wantTg: boolean;
		wantTgDirect: boolean;
		effectiveDirectHost?: string;
		effectiveTlsDomain: string;
		keendnsAvailable: boolean;
		previewKeenDNSFqdn: string;
		realityState: XrayRealityState | null;
		realityInitiallyEnabled: boolean;
		realityPort: number;
		realityTransport: XrayRealityTransport;
		realityDest: string;
		realityServerName: string;
		realityPublicHost: string;
		realityResultLink: string;
		realityResultError: UiText | null;
		preflight: PreflightResponse | null;
		planRecord: ServerPlanRecord | null;
		jobStatus: JobStatusResponse | null;
		cancelling: boolean;
		creds: RevealCredentials | null;
		revealLoading: boolean;
		egressOptions: EgressOption[];
		showTechnicalPlan: boolean;
		oncanceljob: () => void;
		onreveal: () => void;
	}

	let {
		draft = $bindable(),
		applyPhase,
		isRealityOnly,
		isTgDirectOnly,
		grayIPDetected = false,
		usesReality,
		wantXray,
		wantTg,
		wantTgDirect,
		effectiveDirectHost = '',
		effectiveTlsDomain,
		keendnsAvailable,
		previewKeenDNSFqdn,
		realityState,
		realityInitiallyEnabled,
		realityPort,
		realityTransport,
		realityDest,
		realityServerName,
		realityPublicHost,
		realityResultLink,
		realityResultError,
		preflight,
		planRecord,
		jobStatus,
		cancelling,
		creds,
		revealLoading,
		egressOptions,
		showTechnicalPlan = $bindable(),
		oncanceljob,
		onreveal,
	}: Props = $props();

	function formatEgressSummaryText(id: string): string {
		if (!id || id === 'direct') return EXTERNAL_ACCESS_LABELS.egressDirect;
		if (id === 'firc:router') return EXTERNAL_ACCESS_LABELS.egressFirc;
		const found = egressOptions.find((o) => o.id === id || o.iface === id);
		if (found) return m.servers_ext_wiz_egress_via_tunnel({ name: cleanTunnelDisplayName(found.name) });
		return m.servers_ext_wiz_egress_via_iface({ name: id });
	}
</script>

<div class="step-section">
	{#if applyPhase === 'review'}
		<h3 class="step-heading">{m.servers_ext_wiz_s5_heading()}</h3>
		<p class="step-desc">
			{m.servers_ext_wiz_s5_desc()}
		</p>

		{#if !isRealityOnly && !isTgDirectOnly && draft.keendnsMode === 'create' && keendnsAvailable}
			<div class="keendns-publish-alert">
				<Globe size={18} class="icon-info-soft" />
				<div class="publish-alert-body">
					<div class="publish-alert-title">
						{m.servers_ext_wiz_s5_publish_title()}
					</div>
					<div class="publish-alert-desc">
						{m.servers_ext_wiz_s5_publish_p1()} <strong class="mono">https://{previewKeenDNSFqdn}</strong> {m.servers_ext_wiz_s5_publish_p2()} (<code>127.0.0.1:{draft.dispatcherPort}</code>) {m.servers_ext_wiz_s5_publish_p3()} (<code>system.configuration.save</code>){m.servers_ext_wiz_s5_publish_p4()}
					</div>
				</div>
			</div>
		{/if}

		<!-- Human-readable summary -->
		<div class="human-summary-card">
			{#if isRealityOnly}
				<div class="summary-row-item">
					<span class="sum-label">{m.servers_ext_wiz_s5_services()}</span>
					<span class="sum-val">
						{m.servers_ext_wiz_s5_svc_reality()}
						{#if draft.tgInitiallyEnabled}
							· Telegram {draft.disableOtherService ? m.servers_ext_wiz_s5_will_disable() : m.servers_ext_wiz_s5_stays_on()}
						{/if}
					</span>
				</div>
				<div class="summary-row-item">
					<span class="sum-label">{m.servers_ext_wiz_s5_entry()}</span>
					<span class="sum-val">
						{#if grayIPDetected}
							{m.servers_ext_wiz_s5_entry_reality_cgnat({ transport: realityTransport === 'xhttp' ? 'XHTTP (stream-one)' : 'TCP (raw)', port: realityPort })}
						{:else}
							{m.servers_ext_wiz_s5_entry_reality({ transport: realityTransport === 'xhttp' ? 'XHTTP (stream-one)' : 'TCP (raw)', port: realityPort })}
						{/if}
					</span>
				</div>
				<div class="summary-row-item">
					<span class="sum-label">{m.servers_ext_wiz_s3_phone_addr()}</span>
					<span class="sum-val mono">
						{realityPublicHost.trim() || realityState?.network?.public_host || m.servers_ext_wiz_s5_auto()}
					</span>
				</div>
				<div class="summary-row-item">
					<span class="sum-label">{m.servers_ext_wiz_s5_camouflage()}</span>
					<span class="sum-val mono">{realityServerName.trim() || realityDest.trim()}</span>
				</div>
				<div class="summary-row-item">
					<span class="sum-label">{m.servers_ext_wiz_s5_inet_vless()}</span>
					<span class="sum-val">{formatEgressSummaryText(draft.upstreamDevice)}</span>
				</div>
				<div class="summary-row-item">
					<span class="sum-label">{m.servers_ext_wiz_s5_firewall()}</span>
					<span class="sum-val">{m.servers_ext_wiz_s5_firewall_text({ port: realityPort })}</span>
				</div>
			{:else if isTgDirectOnly}
				<div class="summary-row-item">
					<span class="sum-label">{m.servers_ext_wiz_s5_services()}</span>
					<span class="sum-val">
						{m.servers_ext_wiz_s5_svc_tg_mtproto()}
						{#if draft.vlessInitiallyEnabled}
							· VLESS {draft.disableOtherService ? m.servers_ext_wiz_s5_will_disable() : m.servers_ext_wiz_s5_stays_on()}
						{/if}
					</span>
				</div>
				<div class="summary-row-item">
					<span class="sum-label">{m.servers_ext_wiz_s5_entry()}</span>
					<span class="sum-val">
						{grayIPDetected
							? m.servers_ext_wiz_s5_entry_tg_direct_cgnat({ port: draft.directPort || 8443 })
							: m.servers_ext_wiz_s5_entry_tg_direct({ port: draft.directPort || 8443 })}
					</span>
				</div>
				<div class="summary-row-item">
					<span class="sum-label">{m.servers_ext_wiz_s5_tg_direct_addr()}</span>
					<span class="sum-val mono">{draft.directHost.trim() || '—'}</span>
				</div>
				<div class="summary-row-item">
					<span class="sum-label">{m.servers_ext_wiz_s5_tls_domain()}</span>
					<span class="sum-val mono">{effectiveTlsDomain}</span>
				</div>
				<div class="summary-row-item">
					<span class="sum-label">{m.servers_ext_wiz_s5_inet_tg()}</span>
					<span class="sum-val">{formatEgressSummaryText(draft.tgUpstreamDevice)}</span>
				</div>
				<div class="summary-row-item">
					<span class="sum-label">{m.servers_ext_wiz_s5_firewall()}</span>
					<span class="sum-val">{m.servers_ext_wiz_s5_tg_firewall_text({ port: draft.directPort || 8443 })}</span>
				</div>
				<div class="summary-row-item">
					<span class="sum-label">{m.servers_ext_wiz_s5_restart()}</span>
					<span class="sum-val">
						{planRecord?.plan?.restart_required
							? m.servers_ext_wiz_s5_restart_yes()
							: m.servers_ext_wiz_s5_restart_no()}
					</span>
				</div>
			{:else}
			<div class="summary-row-item">
				<span class="sum-label">{m.servers_ext_wiz_s5_services()}</span>
				<span class="sum-val">
					{#if draft.selectedKind === 'both'}
						{m.servers_ext_wiz_s5_svc_both()}
					{:else if draft.selectedKind === 'xray'}
						{m.servers_ext_wiz_s1_vless_title()}
						{#if draft.tgInitiallyEnabled}
							· Telegram {draft.disableOtherService ? m.servers_ext_wiz_s5_will_disable() : m.servers_ext_wiz_s5_stays_on()}
						{/if}
					{:else}
						{m.servers_ext_lbl_telegram_service()}
						{#if draft.vlessInitiallyEnabled}
							· VLESS {draft.disableOtherService ? m.servers_ext_wiz_s5_will_disable() : m.servers_ext_wiz_s5_stays_on()}
						{/if}
					{/if}
				</span>
			</div>

			<div class="summary-row-item">
				<span class="sum-label">{m.servers_ext_wiz_s3_phone_addr()}</span>
				<span class="sum-val mono">
					{formatDomainList(draft.publicDomain) || (draft.keendnsMode === 'create' ? previewKeenDNSFqdn : formatDomainList(draft.directHost) || '—')}
				</span>
			</div>

			<div class="summary-row-item">
				<span class="sum-label">{m.servers_ext_wiz_s3_router_addr()}</span>
				<span class="sum-val mono">
					{#if draft.keendnsMode === 'create' && keendnsAvailable}
						{previewKeenDNSFqdn} ({m.servers_ext_wiz_s5_new_keendns()})
					{:else if draft.keendnsMode === 'existing'}
						{formatDomainList(draft.originHost || draft.publicDomain)} ({m.servers_ext_wiz_s5_existing_keendns()})
					{:else if draft.connectionWay === 'direct'}
						{formatDomainList(draft.publicDomain) || m.servers_ext_wiz_s5_direct_conn()}
					{:else}
						{draft.originHost ? `${draft.originScheme}://${formatDomainList(draft.originHost)}:${draft.originPort}` : m.servers_ext_sum_not_set()}
					{/if}
				</span>
			</div>

			{#if wantXray}
				<div class="summary-row-item">
					<span class="sum-label">{m.servers_ext_wiz_s5_inet_vless()}</span>
					<span class="sum-val">{formatEgressSummaryText(draft.upstreamDevice)}</span>
				</div>
				{#if usesReality}
					<div class="summary-row-item">
						<span class="sum-label">{m.servers_ext_wiz_s5_reality_entry()}</span>
						<span class="sum-val">
							{m.servers_ext_wiz_s5_reality_parallel({ transport: realityTransport === 'xhttp' ? 'XHTTP' : 'TCP (raw)', port: realityPort, dest: realityDest })}
						</span>
					</div>
				{:else if realityInitiallyEnabled}
					<div class="summary-row-item">
						<span class="sum-label">{m.servers_ext_wiz_s5_reality_entry()}</span>
						<span class="sum-val">{m.servers_ext_wiz_s5_will_be_off()}</span>
					</div>
				{/if}
			{/if}

			{#if wantTg}
				{#if wantTgDirect}
					<div class="summary-row-item">
						<span class="sum-label">{m.servers_ext_wiz_s5_tg_direct_entry()}</span>
						<span class="sum-val mono">
							{effectiveDirectHost || formatDomainList(draft.directHost) || '—'}:{draft.directPort || 8443} {m.servers_ext_wiz_s5_port_auto_open()}
						</span>
					</div>
				{/if}
				<div class="summary-row-item">
					<span class="sum-label">{m.servers_ext_wiz_s5_inet_tg()}</span>
					<span class="sum-val">{formatEgressSummaryText(draft.tgUpstreamDevice)}</span>
				</div>
			{/if}

			<div class="summary-row-item">
				<span class="sum-label">{m.servers_ext_wiz_s5_restart()}</span>
				<span class="sum-val">
					{planRecord?.plan?.restart_required
						? m.servers_ext_wiz_s5_restart_yes()
						: m.servers_ext_wiz_s5_restart_no()}
				</span>
			</div>
			{/if}
		</div>

		<!-- First device name input ONLY when VLESS has 0 existing clients -->
		{#if wantXray}
			{#if draft.existingClientCount === 0}
				<div class="first-device-box">
					<label class="field-label" for="wiz-first-device">
						{m.servers_ext_wiz_s5_first_device()}
					</label>
					<input
						id="wiz-first-device"
						type="text"
						class="text-input"
						bind:value={draft.clientRemark}
						placeholder={m.servers_ext_wiz_my_phone()}
					/>
					<span class="field-hint">
						{m.servers_ext_wiz_s5_first_device_hint()}
					</span>
				</div>
			{:else}
				<div class="preserve-note">
					{m.servers_ext_wiz_s5_devices_exist({ count: draft.existingClientCount })}
				</div>
			{/if}
		{/if}

		{#if preflight && (preflight.has_warnings || !preflight.can_proceed)}
			<PreflightList checks={preflight.checks} />
		{/if}

		{#if planRecord?.plan}
			<button
				type="button"
				class="advanced-toggle"
				onclick={() => (showTechnicalPlan = !showTechnicalPlan)}
			>
				{#if showTechnicalPlan}
					<ChevronUp size={14} />
				{:else}
					<ChevronDown size={14} />
				{/if}
				<span>{m.servers_ext_wiz_s5_show_plan({ count: planRecord.plan?.items?.length || 0 })}</span>
			</button>
			{#if showTechnicalPlan}
				<PlanSummary plan={planRecord.plan} />
			{/if}
		{/if}
	{:else if applyPhase === 'applying' && jobStatus}
		<ApplyProgress
			job={jobStatus}
			oncancel={oncanceljob}
			{cancelling}
		/>
	{:else if applyPhase === 'done'}
		<!-- Post-apply status summary distinguishing saved vs cert pending vs external unverified -->
		<div class="post-apply-banners">
			<div class="status-pill-card ok">
				<CheckCircle2 size={16} />
				<div>
					<div class="pill-card-title">{m.servers_ext_wiz_s5_saved()}</div>
					<div class="pill-card-desc">{m.servers_ext_wiz_s5_saved_desc()}</div>
				</div>
			</div>

			{#if creds?.keendns_cert_status === 'ready'}
				<div class="status-pill-card ok">
					<CheckCircle2 size={16} />
					<div>
						<div class="pill-card-title">{m.servers_ext_wiz_s5_cert_ok()}</div>
						<div class="pill-card-desc">
							{m.servers_ext_wiz_s5_cert_ok_before()} <code class="mono">{creds.keendns_fqdn}</code> {m.servers_ext_wiz_s5_cert_ok_after()}
						</div>
					</div>
				</div>
			{:else if creds?.keendns_cert_status === 'pending'}
				<div class="status-pill-card warn">
					<AlertTriangle size={16} />
					<div>
						<div class="pill-card-title">{m.servers_ext_wiz_s5_cert_pending()}</div>
						<div class="pill-card-desc">
							{m.servers_ext_wiz_s5_cert_pending_before()} <code class="mono">{creds.keendns_fqdn}</code> {m.servers_ext_wiz_s5_cert_pending_after()}
						</div>
					</div>
				</div>
			{:else if creds?.keendns_cert_status === 'invalid'}
				<div class="status-pill-card warn">
					<AlertTriangle size={16} />
					<div>
						<div class="pill-card-title">{m.servers_ext_wiz_s5_cert_invalid()}</div>
						<div class="pill-card-desc">
							{m.servers_ext_wiz_s5_cert_invalid_before()} <code class="mono">{creds.keendns_fqdn}</code> {m.servers_ext_wiz_s5_cert_invalid_after()}
						</div>
					</div>
				</div>
			{:else if creds?.keendns_fqdn}
				<div class="status-pill-card info">
					<Info size={16} />
					<div>
						<div class="pill-card-title">{m.servers_ext_wiz_s5_cert_unchecked()}</div>
						<div class="pill-card-desc">
							{m.servers_ext_wiz_s5_cert_unchecked_before()} <code class="mono">{creds.keendns_fqdn}</code> {m.servers_ext_wiz_s5_cert_unchecked_after()}
						</div>
					</div>
				</div>
			{/if}

		{#if !isRealityOnly}
			<div class="status-pill-card info">
				<Info size={16} />
				<div>
					<div class="pill-card-title">{m.servers_ext_wiz_s5_external_unchecked()}</div>
					<div class="pill-card-desc">
						{m.servers_ext_wiz_s5_external_unchecked_desc()}
					</div>
				</div>
			</div>
		{/if}
		</div>

		{#if !isRealityOnly}
			<ConnectionResult
				kind={draft.selectedKind}
				{creds}
				loading={revealLoading}
				{onreveal}
			/>
		{/if}
		{#if usesReality}
			<div style={!isRealityOnly ? 'margin-top: 14px;' : ''}>
				<RealityLinkResult link={realityResultLink} error={realityResultError ? uiText(realityResultError) : null} />
			</div>
		{/if}
		{#if !isRealityOnly}
			<div style="margin-top: 14px;">
				<ConnectionChecks kind={draft.selectedKind} autoRun={true} />
			</div>
		{/if}
	{/if}
</div>

<style>
	.preserve-note {
		font-size: 12px;
		color: var(--color-text-secondary);
		padding: 9px 11px;
		border-radius: var(--radius-sm);
		background: var(--color-accent-tint);
		border: 1px solid var(--color-accent-border);
	}

	/* Step 5 Review & Post-Apply */
	.keendns-publish-alert {
		display: flex;
		align-items: flex-start;
		gap: 10px;
		padding: 11px 13px;
		border-radius: var(--radius-sm);
		background: var(--color-accent-tint);
		border: 1px solid var(--color-accent-border);
	}

	.keendns-publish-alert > :global(svg),
	.status-pill-card > :global(svg) {
		flex-shrink: 0;
		margin-top: 2px;
	}

	.publish-alert-body {
		display: flex;
		flex-direction: column;
		gap: 3px;
		min-width: 0;
		flex: 1;
	}

	.publish-alert-title {
		font-size: 13px;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.publish-alert-desc {
		font-size: 12px;
		color: var(--color-text-secondary);
		line-height: 1.4;
		overflow-wrap: anywhere;
		word-break: break-word;
	}

	.human-summary-card {
		display: flex;
		flex-direction: column;
		gap: 8px;
		padding: 12px 14px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		min-width: 0;
	}

	.summary-row-item {
		display: grid;
		grid-template-columns: minmax(140px, 38%) minmax(0, 1fr);
		align-items: baseline;
		gap: 12px;
		padding-bottom: 6px;
		border-bottom: 1px solid var(--color-border);
		font-size: 12.5px;
		min-width: 0;
	}

	.summary-row-item:last-child {
		padding-bottom: 0;
		border-bottom: none;
	}

	.sum-label {
		color: var(--color-text-muted);
	}

	.sum-val {
		color: var(--color-text-primary);
		font-weight: 600;
		text-align: right;
		line-height: 1.4;
		min-width: 0;
		max-width: 100%;
		overflow-wrap: anywhere;
		word-break: break-word;
	}

	.first-device-box {
		display: flex;
		flex-direction: column;
		gap: 6px;
		padding: 11px 13px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
	}

	.post-apply-banners {
		display: flex;
		flex-direction: column;
		gap: 8px;
	}

	.status-pill-card {
		display: flex;
		align-items: flex-start;
		gap: 10px;
		padding: 10px 12px;
		border-radius: var(--radius-sm);
		font-size: 12px;
	}

	.status-pill-card > div {
		min-width: 0;
		flex: 1;
	}

	.status-pill-card.ok {
		background: var(--color-success-tint);
		border: 1px solid var(--color-success-border);
		color: var(--color-success);
	}

	.status-pill-card.warn {
		background: var(--color-warning-tint);
		border: 1px solid var(--color-warning-border);
		color: var(--color-warning);
	}

	.status-pill-card.info {
		background: var(--color-accent-tint);
		border: 1px solid var(--color-accent-border);
		color: var(--color-accent);
	}

	.pill-card-title {
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.pill-card-desc {
		color: var(--color-text-secondary);
		margin-top: 2px;
		line-height: 1.4;
		overflow-wrap: anywhere;
		word-break: break-word;
	}

	@media (max-width: 640px) {
		.summary-row-item {
			grid-template-columns: 1fr;
			gap: 2px;
		}
		.sum-val {
			text-align: left;
		}
	}
</style>
