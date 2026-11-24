<script lang="ts">
	// Шаг 1 мастера внешнего доступа: что подключаем (VLESS / Telegram / оба).
	import { m } from '$lib/i18n';
	import type { WizardKind } from '$lib/types/serverWizard';
	import type { ExternalAccessWizardDraft } from './externalAccessModel';
	import { Shield, Send, Layers, AlertTriangle } from 'lucide-svelte';

	interface Props {
		draft: ExternalAccessWizardDraft;
		wantTg: boolean;
		otherServiceCurrentlyEnabled: boolean;
		otherServiceLabel: string;
		onselectkind: (kind: WizardKind) => void;
		onselectscenario: (scenario: string) => void;
	}

	let {
		draft = $bindable(),
		wantTg,
		otherServiceCurrentlyEnabled,
		otherServiceLabel,
		onselectkind,
		onselectscenario,
	}: Props = $props();
</script>

<div class="step-section">
	<h3 class="step-heading">{m.servers_ext_wiz_s1_heading()}</h3>
	<p class="step-desc">
		{m.servers_ext_wiz_s1_desc()}
	</p>

	<div class="cards-grid">
		<button
			type="button"
			class="choice-card"
			class:selected={draft.selectedKind === 'xray'}
			onclick={() => onselectkind('xray')}
		>
			<div class="choice-top">
				<Shield size={20} class="icon-info-soft" />
				{#if draft.vlessInitiallyEnabled && !draft.tgInitiallyEnabled}
					<span class="current-pill">{m.servers_ext_wiz_enabled_now()}</span>
				{/if}
			</div>
			<div class="choice-title">{m.servers_ext_wiz_s1_vless_title()}</div>
			<div class="choice-text">
				{m.servers_ext_wiz_s1_vless_text()}
			</div>
		</button>

		<button
			type="button"
			class="choice-card"
			class:selected={draft.selectedKind === 'tgwebproxy'}
			onclick={() => onselectkind('tgwebproxy')}
		>
			<div class="choice-top">
				<Send size={20} class="icon-success-soft" />
				{#if draft.tgInitiallyEnabled && !draft.vlessInitiallyEnabled}
					<span class="current-pill">{m.servers_ext_wiz_enabled_now()}</span>
				{/if}
			</div>
			<div class="choice-title">{m.servers_ext_wiz_s1_tg_title()}</div>
			<div class="choice-text">
				{m.servers_ext_wiz_s1_tg_text()}
			</div>
		</button>

		<button
			type="button"
			class="choice-card"
			class:selected={draft.selectedKind === 'both'}
			onclick={() => onselectkind('both')}
		>
			<div class="choice-top">
				<Layers size={20} class="icon-accent-soft" />
				{#if draft.vlessInitiallyEnabled && draft.tgInitiallyEnabled}
					<span class="current-pill">{m.servers_ext_wiz_enabled_now()}</span>
				{/if}
			</div>
			<div class="choice-title">{m.servers_ext_wiz_s1_both_title()}</div>
			<div class="choice-text">
				{m.servers_ext_wiz_s1_both_text()}
			</div>
		</button>
	</div>

	{#if otherServiceCurrentlyEnabled}
		<div class="other-service-warning">
			<AlertTriangle size={16} class="warn-icon" />
			<div class="other-service-body">
				<div class="other-service-title">
					{m.servers_ext_wiz_s1_other_on({ service: otherServiceLabel })}
				</div>
				<label class="checkbox-row">
					<input type="checkbox" bind:checked={draft.disableOtherService} />
					<span>
						{m.servers_ext_wiz_s1_other_disable({ service: otherServiceLabel })}
					</span>
				</label>
			</div>
		</div>
	{/if}

	{#if wantTg}
		<h3 class="step-heading" style="margin-top: 6px;">
			{m.servers_ext_wiz_s1_tg_way()}
		</h3>
		<div class="cards-grid">
			<button
				type="button"
				class="choice-card compact"
				class:selected={draft.tgScenario === 'direct_fake_tls'}
				onclick={() => onselectscenario('direct_fake_tls')}
			>
				<div class="choice-top">
					<span class="choice-title">{m.servers_ext_wiz_s1_tg_direct_title()}</span>
					<span class="tech-sub-badge">MTProto / Fake-TLS</span>
				</div>
				<div class="choice-text">
					{m.servers_ext_wiz_s1_tg_direct_text()}
				</div>
			</button>

			<button
				type="button"
				class="choice-card compact"
				class:selected={draft.tgScenario === 'cdn_http'}
				onclick={() => onselectscenario('cdn_http')}
			>
				<div class="choice-top">
					<span class="choice-title">{m.servers_ext_wiz_s1_tg_domain_title()}</span>
					<span class="tech-sub-badge">{m.servers_ext_wiz_s1_tg_domain_badge()}</span>
				</div>
				<div class="choice-text">
					{m.servers_ext_wiz_s1_tg_domain_text()}
				</div>
			</button>

			<button
				type="button"
				class="choice-card compact"
				class:selected={draft.tgScenario === 'dual'}
				onclick={() => onselectscenario('dual')}
			>
				<div class="choice-top">
					<span class="choice-title">{m.servers_ext_wiz_s1_tg_dual_title()}</span>
					<span class="tech-sub-badge">{m.servers_ext_wiz_s1_tg_dual_badge()}</span>
				</div>
				<div class="choice-text">
					{m.servers_ext_wiz_s1_tg_dual_text()}
				</div>
			</button>
		</div>
	{/if}
</div>

<style>
	.other-service-warning {
		display: flex;
		align-items: flex-start;
		gap: 10px;
		padding: 11px 13px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-warning-border);
		background: var(--color-warning-tint);
	}

	.other-service-body {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}

	.other-service-title {
		font-size: 12.5px;
		font-weight: 600;
		color: var(--color-text-primary);
	}
</style>
