<script lang="ts">
	// Шаг 2 мастера внешнего доступа: как устройства подключаются к роутеру.
	import { m, uiText } from '$lib/i18n';
	import { Button } from '$lib/components/ui';
	import type { KeenDNSDiscovery } from '$lib/types/serverWizard';
	import type { XrayRealityState } from '$lib/types/xray';
	import {
		draftHasSavedWebScheme,
		restoreSavedConnectionScheme,
		type ExternalAccessWizardDraft,
	} from './externalAccessModel';
	import { AlertTriangle, CheckCircle2, HelpCircle, RefreshCw, Zap } from 'lucide-svelte';

	type WizardRealityMode = 'off' | 'also' | 'only';

	interface Props {
		draft: ExternalAccessWizardDraft;
		realityMode: WizardRealityMode;
		isRealityOnly: boolean;
		isTgDirectOnly: boolean;
		wantXray: boolean;
		wantTgDirect?: boolean;
		grayIPDetected?: boolean;
		realityPort: number;
		realityState: XrayRealityState | null;
		realityWhiteIP: boolean;
		realityNetChecking: boolean;
		checkingFacts: boolean;
		showRouterFacts: boolean;
		keendnsInfo: KeenDNSDiscovery | null;
		canReturnToSavedScheme?: boolean;
		onrestoresavedscheme?: () => void;
		oncheckreality: () => void;
		oncheckfacts: () => void;
		onswitchtowebbridge: () => void;
		onswitchtowebonly?: () => void;
		onswitchtovlessdomain?: () => void;
	}

	let {
		draft = $bindable(),
		realityMode = $bindable(),
		isRealityOnly,
		isTgDirectOnly,
		wantXray,
		wantTgDirect = false,
		grayIPDetected = false,
		realityPort,
		realityState,
		realityWhiteIP,
		realityNetChecking,
		checkingFacts,
		showRouterFacts,
		keendnsInfo,
		canReturnToSavedScheme = true,
		onrestoresavedscheme,
		oncheckreality,
		oncheckfacts,
		onswitchtowebbridge,
		onswitchtowebonly,
		onswitchtovlessdomain,
	}: Props = $props();
</script>

<div class="step-section">
	<h3 class="step-heading">{m.servers_ext_wiz_s2_heading()}</h3>

	{#if isTgDirectOnly}
		<div class="origin-box">
			<div class="choice-top">
				<span class="choice-title">{m.servers_ext_wiz_s2_tg_direct_title()}</span>
				<span class="tech-sub-badge badge-ok">
					{m.servers_ext_wiz_s2_tg_direct_port_badge({ port: draft.directPort || 8443 })}
				</span>
			</div>
			<div class="choice-text">
				{m.servers_ext_wiz_s2_tg_direct_desc()}
			</div>
		</div>

		<div class="origin-box">
			<div class="question-header-row">
				<span class="field-label">{m.servers_ext_wiz_s2_q_tg_white_ip()}</span>
				<Button
					variant="secondary"
					size="sm"
					onclick={() => {
						oncheckreality();
						oncheckfacts();
					}}
					disabled={checkingFacts || realityNetChecking}
				>
					<RefreshCw size={13} class={checkingFacts || realityNetChecking ? 'spin' : ''} />
					{checkingFacts || realityNetChecking ? m.servers_wizard_checks_running() : m.common_check()}
				</Button>
			</div>

			<div
				class="reality-wan-box"
				class:ok={realityState?.network ? realityWhiteIP : Boolean(keendnsInfo?.wan_ip && !keendnsInfo.wan_ip_private)}
				class:warn={realityState?.network ? !realityWhiteIP : Boolean(keendnsInfo?.wan_ip_private || grayIPDetected)}
			>
				{#if realityState?.network}
					{#if realityWhiteIP}
						<CheckCircle2 size={15} class="icon-success-soft" />
						<span>
							{m.servers_ext_wiz_s2_tg_ip_confirmed()} <strong class="mono">{realityState.network.external_ip || realityState.network.wan_address}</strong>
							{realityState.network.keendns_domain ? ` · ${m.servers_ext_wiz_s2_tg_direct_domain_also({ domain: realityState.network.keendns_domain })}` : ''}
						</span>
					{:else}
						<AlertTriangle size={15} class="icon-warning-soft" />
						<div class="warn-banner-body">
							<span>
								{m.servers_ext_wiz_s2_tg_ip_not_found({ reason: realityState.network.reason || m.servers_ext_wiz_s2_tg_ip_cgnat_reason() })}
							</span>
							<div>
								<Button variant="primary" size="sm" onclick={onswitchtowebbridge}>
									{m.servers_ext_wiz_s2_tg_switch_web()}
								</Button>
							</div>
							<label class="checkbox-row">
								<input type="checkbox" bind:checked={draft.confirmDirectBehindNAT} />
								<span>{m.servers_ext_wiz_s2_confirm_behind_nat()}</span>
							</label>
						</div>
					{/if}
				{:else if keendnsInfo?.wan_ip}
					{#if !keendnsInfo.wan_ip_private}
						<CheckCircle2 size={15} class="icon-success-soft" />
						<span>
							{m.servers_ext_wiz_s2_tg_wan_public()} <strong class="mono">{keendnsInfo.wan_ip}</strong>
						</span>
					{:else}
						<AlertTriangle size={15} class="icon-warning-soft" />
						<div class="warn-banner-body">
							<span>
								{m.servers_ext_wiz_s2_tg_wan_cgnat_before()}<strong class="mono">{keendnsInfo.wan_ip}</strong>{m.servers_ext_wiz_s2_tg_wan_cgnat_after()}
							</span>
							<div>
								<Button variant="primary" size="sm" onclick={onswitchtowebbridge}>
									{m.servers_ext_wiz_s2_tg_switch_web()}
								</Button>
							</div>
							<label class="checkbox-row">
								<input type="checkbox" bind:checked={draft.confirmDirectBehindNAT} />
								<span>{m.servers_ext_wiz_s2_confirm_behind_nat()}</span>
							</label>
						</div>
					{/if}
				{:else if grayIPDetected}
					<AlertTriangle size={15} class="icon-warning-soft" />
					<div class="warn-banner-body">
						<span>
							{m.servers_ext_wiz_s2_tg_ip_not_found({ reason: m.servers_ext_wiz_s2_tg_ip_cgnat_reason() })}
						</span>
						<div>
							<Button variant="primary" size="sm" onclick={onswitchtowebbridge}>
								{m.servers_ext_wiz_s2_tg_switch_web()}
							</Button>
						</div>
						<label class="checkbox-row">
							<input type="checkbox" bind:checked={draft.confirmDirectBehindNAT} />
							<span>{m.servers_ext_wiz_s2_confirm_behind_nat()}</span>
						</label>
					</div>
				{:else}
					<AlertTriangle size={15} class="icon-warning-soft" />
					<span>{m.servers_ext_wiz_s2_ip_unchecked()}</span>
				{/if}
			</div>
		</div>
	{:else if draft.isEditMode && draft.keepExistingConnectionScheme}
		<div class="saved-scheme-card">
			<div class="saved-scheme-top">
				<div class="saved-scheme-badge">
					<CheckCircle2 size={15} />
					<span>{m.servers_ext_wiz_s2_keep()}</span>
				</div>
				<Button
					variant="secondary"
					size="sm"
					onclick={() => {
						draft.keepExistingConnectionScheme = false;
						draft.schemeCustomized = true;
					}}
				>
					{m.servers_ext_wiz_s2_change_scheme()}
				</Button>
			</div>
			<div class="saved-scheme-summary">
				{uiText(draft.connectionSchemeSummary) || m.servers_ext_wiz_s2_scheme_default()}
			</div>
			{#if uiText(draft.connectionSchemeWarning)}
				<div class="scheme-warn-note">
					<AlertTriangle size={14} />
					<span>{m.servers_ext_wiz_s2_scheme_warn({ warning: uiText(draft.connectionSchemeWarning) })}</span>
				</div>
			{:else}
				<div class="saved-scheme-hint">
					{m.servers_ext_wiz_s2_keep_hint()}
				</div>
			{/if}
		</div>
	{:else}
		{#if draft.isEditMode && canReturnToSavedScheme}
			<div class="return-saved-row">
				<button
					type="button"
					class="link-btn"
					onclick={() => {
						if (onrestoresavedscheme) {
							onrestoresavedscheme();
						} else {
							restoreSavedConnectionScheme(draft);
						}
					}}
				>
					&larr; {m.servers_ext_wiz_s2_return_saved()}
				</button>
			</div>
		{/if}

		<div class="field-block">
			<span class="field-label">{m.servers_ext_wiz_s2_q_way()}</span>
			<div class="cards-grid cols-2">
				<button
					type="button"
					class="choice-card"
					class:selected={!isRealityOnly && draft.connectionWay === 'direct'}
					onclick={() => {
						if (realityMode === 'only') {
							realityMode = 'off';
							draft.realityOnlyDisabled = true;
							if (draft.vlessInitiallyEnabled) {
								draft.schemeCustomized = true;
							}
							draft.confirmDirectBehindNAT = false;
						}
						if (draftHasSavedWebScheme(draft)) {
							draft.schemeCustomized = true;
						}
						draft.connectionWay = 'direct';
					}}
				>
					<div class="choice-title">{m.servers_ext_model_scheme_router_domain()}</div>
					<div class="choice-text">
						{m.servers_ext_wiz_s2_router_domain_text()}
					</div>
				</button>

				<button
					type="button"
					class="choice-card"
					class:selected={!isRealityOnly && draft.connectionWay === 'external_https'}
					onclick={() => {
						if (realityMode === 'only') {
							realityMode = 'off';
							draft.realityOnlyDisabled = true;
							if (draft.vlessInitiallyEnabled) {
								draft.schemeCustomized = true;
							}
							draft.confirmDirectBehindNAT = false;
						}
						if (draftHasSavedWebScheme(draft)) {
							draft.schemeCustomized = true;
						}
						draft.connectionWay = 'external_https';
						draft.publicPort = 443;
					}}
				>
					<div class="choice-title">{m.servers_ext_model_scheme_own_domain()}</div>
					<div class="choice-text">
						{m.servers_ext_wiz_s2_own_domain_text()}
					</div>
				</button>

				{#if draft.selectedKind === 'xray'}
					<button
						type="button"
						class="choice-card choice-card-full"
						class:selected={isRealityOnly}
						onclick={() => {
							realityMode = 'only';
							draft.realityOnlyDisabled = false;
							if (draft.vlessHasSavedWebScheme) {
								draft.schemeCustomized = true;
							}
							draft.confirmDirectBehindNAT = false;
						}}
					>
						<div class="choice-top">
							<span class="choice-title">{m.servers_ext_wiz_s2_reality_title()}</span>
							{#if realityWhiteIP}
								<span class="current-pill">{m.servers_ext_wiz_recommended()}</span>
							{/if}
						</div>
						<div class="choice-text">
							{m.servers_ext_wiz_s2_reality_text({ port: realityPort })}
						</div>
					</button>
				{/if}
			</div>
		</div>

		{#if isRealityOnly}
			<div class="origin-box">
				<div class="question-header-row">
					<span class="field-label">{m.servers_ext_wiz_s2_q_reality_ip()}</span>
					<Button
						variant="secondary"
						size="sm"
						onclick={() => oncheckreality()}
						disabled={realityNetChecking}
					>
						<RefreshCw size={13} class={realityNetChecking ? 'spin' : ''} />
						{realityNetChecking ? m.servers_wizard_checks_running() : m.common_check()}
					</Button>
				</div>
				<div class="reality-wan-box" class:ok={realityWhiteIP} class:warn={!realityWhiteIP}>
					{#if realityState?.network && realityWhiteIP}
						<CheckCircle2 size={15} class="icon-success-soft" />
						<span>
							{m.servers_ext_wiz_s2_ip_confirmed()} <strong class="mono">{realityState.network.external_ip || realityState.network.wan_address}</strong>
							{realityState.network.keendns_domain ? ` · ${m.servers_ext_wiz_s2_links_on_keendns({ domain: realityState.network.keendns_domain })}` : ''}
						</span>
					{:else if (realityState?.network && !realityWhiteIP) || grayIPDetected}
						<AlertTriangle size={15} class="icon-warning-soft" />
						<div class="warn-banner-body">
							<span>
								{realityState?.network
									? m.servers_ext_wiz_s2_ip_not_found({ reason: realityState.network.reason || m.servers_ext_wiz_s2_ip_unreachable() })
									: m.servers_ext_wiz_s2_reality_cgnat_warn()}
							</span>
							{#if onswitchtovlessdomain}
								<div>
									<Button variant="primary" size="sm" onclick={onswitchtovlessdomain}>
										{m.servers_ext_wiz_s2_switch_vless_domain()}
									</Button>
								</div>
							{/if}
							<label class="checkbox-row">
								<input type="checkbox" bind:checked={draft.confirmDirectBehindNAT} />
								<span>{m.servers_ext_wiz_s2_reality_confirm_behind_nat()}</span>
							</label>
						</div>
					{:else}
						<AlertTriangle size={15} class="icon-warning-soft" />
						<span>{m.servers_ext_wiz_s2_ip_unchecked()}</span>
					{/if}
				</div>
			</div>
		{:else}
		<div class="field-block" style="margin-top: 4px;">
			<div class="question-header-row">
				<span class="field-label">{m.servers_ext_wiz_s2_q_ip_type()}</span>
				<Button
					variant="secondary"
					size="sm"
					onclick={oncheckfacts}
					disabled={checkingFacts}
				>
					<HelpCircle size={13} />
					{checkingFacts ? m.servers_wizard_checks_running() : m.servers_ext_wiz_s2_dont_know()}
				</Button>
			</div>

			<div class="cards-grid cols-2">
				<button
					type="button"
					class="choice-card compact"
					class:selected={draft.originType === 'public_address'}
					onclick={() => {
						draft.originType = 'public_address';
						draft.confirmDirectBehindNAT = false;
					}}
				>
					<div class="choice-title">
						{m.servers_ext_wiz_s2_public_ip()}
					</div>
					<div class="choice-text">
						{m.servers_ext_wiz_s2_public_ip_text()}
					</div>
				</button>

				<button
					type="button"
					class="choice-card compact"
					class:selected={draft.originType === 'cloud_address'}
					onclick={() => {
						draft.originType = 'cloud_address';
						draft.confirmDirectBehindNAT = false;
					}}
				>
					<div class="choice-title">
						{m.servers_ext_wiz_s2_private_ip()}
					</div>
					<div class="choice-text">
						{m.servers_ext_wiz_s2_private_ip_text()}
					</div>
				</button>
			</div>

			{#if grayIPDetected && draft.originType === 'public_address'}
				{@const detectedPrivateIP = keendnsInfo?.wan_ip || realityState?.network?.wan_address || ''}
				<div class="reality-wan-box warn" style="margin-top: 6px;">
					<AlertTriangle size={15} class="icon-warning-soft" />
					<div class="warn-banner-body">
						<span>
							{detectedPrivateIP
								? m.servers_ext_wiz_s2_public_ip_cgnat_mismatch({ ip: detectedPrivateIP })
								: m.servers_ext_wiz_s2_public_ip_cgnat_mismatch_generic()}
						</span>
						<div>
							<Button
								variant="primary"
								size="sm"
								onclick={() => {
									draft.originType = 'cloud_address';
									draft.confirmDirectBehindNAT = false;
								}}
							>
								{m.servers_ext_wiz_s2_switch_private_ip()}
							</Button>
						</div>
						<label class="checkbox-row">
							<input type="checkbox" bind:checked={draft.confirmDirectBehindNAT} />
							<span>{m.servers_ext_wiz_s2_public_ip_confirm_behind_nat()}</span>
						</label>
					</div>
				</div>
			{/if}

			{#if showRouterFacts && keendnsInfo}
				<div class="router-facts-box">
					<div class="facts-title">{m.servers_ext_wiz_s2_facts_title()}</div>
					<ul class="facts-list">
						<li>
							<strong>{m.servers_ext_wiz_s2_facts_wan()}</strong>
							{#if keendnsInfo.wan_ip}
								<code>{keendnsInfo.wan_ip}</code>
								— {keendnsInfo.wan_ip_private
									? m.servers_ext_wiz_s2_facts_wan_private()
									: m.servers_ext_wiz_s2_facts_wan_public()}
							{:else}
								{m.servers_ext_wiz_s2_facts_undetected()}
							{/if}
						</li>
						<li>
							<strong>KeenDNS:</strong>
							{#if keendnsInfo.enabled && keendnsInfo.domain}
								{m.servers_ext_wiz_s2_facts_domain()} <code>{keendnsInfo.domain}</code>, {m.servers_ext_wiz_s2_facts_mode()}
								<strong>
									{keendnsInfo.mode === 'cloud'
										? m.servers_ext_wiz_s2_facts_mode_cloud()
										: keendnsInfo.mode === 'direct'
											? m.servers_ext_wiz_s2_facts_mode_direct()
											: keendnsInfo.mode}
								</strong>
								({keendnsInfo.cert_ok ? m.servers_ext_wiz_s2_facts_cert_ok() : m.servers_ext_wiz_s2_facts_cert_unchecked()})
							{:else if keendnsInfo.state === 'unsupported'}
								{m.servers_ext_wiz_s2_facts_unsupported()}
							{:else}
								{m.servers_ext_wiz_s2_facts_not_configured()}
							{/if}
						</li>
					</ul>
					<div class="facts-scope">
						{m.servers_ext_wiz_s2_facts_note()}
					</div>
				</div>
			{/if}
		</div>

		{#if wantTgDirect && (draft.originType === 'cloud_address' || grayIPDetected) && onswitchtowebonly}
			<div class="reality-wan-box warn">
				<AlertTriangle size={15} class="icon-warning-soft" />
				<div class="warn-banner-body">
					<span>{m.servers_ext_wiz_s2_dual_cgnat_warn()}</span>
					<div>
						<Button variant="secondary" size="sm" onclick={onswitchtowebonly}>
							{m.servers_ext_wiz_s2_switch_web_only()}
						</Button>
					</div>
				</div>
			</div>
		{/if}

		{#if wantXray}
			<div class="reality-opt-box" class:selected={realityMode === 'also'}>
				<label class="reality-opt-toggle">
					<input
						type="checkbox"
						checked={realityMode === 'also'}
						onchange={(e) => {
							realityMode = e.currentTarget.checked ? 'also' : 'off';
							draft.confirmDirectBehindNAT = false;
						}}
					/>
					<div class="reality-opt-body">
						<div class="reality-opt-head">
							<Zap size={14} class="icon-accent-soft" />
							<span class="choice-title">{m.servers_ext_wiz_s2_reality_also()}</span>
							{#if realityState?.network}
								<span class="tech-sub-badge" class:badge-ok={realityWhiteIP} class:badge-warn={!realityWhiteIP}>
									{realityWhiteIP ? m.servers_ext_wiz_s2_white_ip_found() : m.servers_ext_wiz_s2_white_ip_required()}
								</span>
							{/if}
						</div>
						<div class="choice-text">
							{draft.connectionWay === 'external_https' ? m.servers_ext_wiz_s2_reality_also_ext({ port: realityPort }) : m.servers_ext_wiz_s2_reality_also_direct({ port: realityPort })}
						</div>
					</div>
				</label>
				{#if realityMode === 'also' && grayIPDetected && !realityWhiteIP}
					<div class="reality-opt-warn">
						<div class="scheme-warn-note">
							<AlertTriangle size={14} />
							<span>{m.servers_ext_wiz_s2_reality_also_cgnat_note()}</span>
						</div>
						{#if draft.originType !== 'public_address'}
							<label class="checkbox-row">
								<input type="checkbox" bind:checked={draft.confirmDirectBehindNAT} />
								<span>{m.servers_ext_wiz_s2_reality_confirm_behind_nat()}</span>
							</label>
						{/if}
					</div>
				{/if}
			</div>
		{/if}
		{/if}
	{/if}
</div>

<style>
	.saved-scheme-card {
		padding: 14px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-accent-border);
		background: var(--color-accent-tint);
		display: flex;
		flex-direction: column;
		gap: 10px;
	}

	.saved-scheme-top {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 10px;
		flex-wrap: wrap;
	}

	.saved-scheme-badge {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		font-size: 13.5px;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.saved-scheme-summary {
		font-size: 13px;
		color: var(--color-text-primary);
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 9px 11px;
		min-width: 0;
		overflow-wrap: anywhere;
		word-break: break-word;
		line-height: 1.45;
	}

	.saved-scheme-hint {
		font-size: 12px;
		color: var(--color-text-muted);
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

	.return-saved-row {
		margin-bottom: 2px;
	}

	.link-btn {
		background: none;
		border: none;
		padding: 0;
		color: var(--color-accent);
		font-size: 12.5px;
		font-weight: 500;
		cursor: pointer;
	}

	.link-btn:hover {
		text-decoration: underline;
	}

	.question-header-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 8px;
		flex-wrap: wrap;
	}

	.router-facts-box {
		padding: 11px 13px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
		display: flex;
		flex-direction: column;
		gap: 8px;
		font-size: 12px;
		min-width: 0;
	}

	.facts-title {
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.facts-list {
		margin: 0;
		padding-left: 18px;
		display: flex;
		flex-direction: column;
		gap: 4px;
		color: var(--color-text-secondary);
	}

	.facts-list li {
		overflow-wrap: anywhere;
		word-break: break-word;
	}

	.facts-scope {
		font-size: 11.5px;
		color: var(--color-text-muted);
		border-top: 1px solid var(--color-border);
		padding-top: 6px;
	}

	.choice-card-full {
		grid-column: 1 / -1;
	}

	.reality-opt-box {
		display: flex;
		flex-direction: column;
		gap: 8px;
		padding: 11px 13px;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-primary);
		transition: border-color 0.15s, background 0.15s;
	}

	.reality-opt-box:hover {
		border-color: var(--color-accent);
	}

	.reality-opt-box.selected {
		border-color: var(--color-accent);
		background: var(--color-accent-tint);
	}

	.reality-opt-toggle {
		display: flex;
		align-items: flex-start;
		gap: 10px;
		cursor: pointer;
	}

	.reality-opt-toggle > input[type='checkbox'] {
		margin-top: 2px;
		width: 16px;
		height: 16px;
		accent-color: var(--color-accent);
		cursor: pointer;
		flex-shrink: 0;
	}

	.reality-opt-warn {
		display: flex;
		flex-direction: column;
		gap: 6px;
		padding-left: 26px;
	}

	.reality-opt-body {
		display: flex;
		flex-direction: column;
		gap: 4px;
		min-width: 0;
		flex: 1;
	}

	.reality-opt-head {
		display: flex;
		align-items: center;
		gap: 6px;
		flex-wrap: wrap;
	}

	.reality-wan-box {
		display: flex;
		align-items: flex-start;
		gap: 8px;
		font-size: 12px;
		line-height: 1.45;
		padding: 8px 10px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		color: var(--color-text-primary);
	}

	.reality-wan-box > :global(svg) {
		flex-shrink: 0;
		margin-top: 2px;
	}

	.reality-wan-box > span {
		min-width: 0;
		flex: 1;
		overflow-wrap: anywhere;
	}

	.reality-wan-box.ok {
		border-color: var(--color-success-border);
		background: var(--color-success-tint);
	}

	.reality-wan-box.warn {
		border-color: var(--color-warning-border);
		background: var(--color-warning-tint);
	}

	.warn-banner-body {
		display: flex;
		flex-direction: column;
		gap: 8px;
		min-width: 0;
		flex: 1;
	}
</style>
