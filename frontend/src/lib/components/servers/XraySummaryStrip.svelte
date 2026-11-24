<script lang="ts">
	import { m } from '$lib/i18n';
	import type { VerificationReport } from '$lib/types/serverWizard';
	import { Compass, ShieldCheck } from 'lucide-svelte';

	// Двухстрочная сводка карточки VLESS: выход в интернет и последняя проверка.
	interface Props {
		shortEgressSummary: string;
		allowLan: boolean;
		hasLegacySocksEgress: boolean;
		currentEgressUnavailable: boolean;
		checkSummaryText: string;
		checkLoading: boolean;
		checkStatus?: VerificationReport['overall_status'];
		onegress: () => void;
		onwizard: () => void;
		oncheck: () => void;
		ondetails: () => void;
	}

	let {
		shortEgressSummary,
		allowLan,
		hasLegacySocksEgress,
		currentEgressUnavailable,
		checkSummaryText,
		checkLoading,
		checkStatus,
		onegress,
		onwizard,
		oncheck,
		ondetails,
	}: Props = $props();
</script>

<section class="summary-strip" aria-label={m.servers_xray_summary_aria()}>
	<div class="summary-line">
		<div class="summary-left">
			<Compass size={15} class="summary-icon" />
			<span class="summary-text" class:warn-text={hasLegacySocksEgress || currentEgressUnavailable}>
				{shortEgressSummary}{allowLan ? m.servers_xray_lan_open_suffix() : ''}
			</span>
			{#if currentEgressUnavailable}
				<span class="inline-warn-badge">{m.servers_xray_tunnel_unavailable()}</span>
			{:else if hasLegacySocksEgress}
				<span class="inline-warn-badge">{m.servers_xray_choice_required()}</span>
			{/if}
		</div>
		<div class="summary-actions">
			<button type="button" class="link-btn" onclick={onegress}>
				{m.common_edit()}
			</button>
			<span class="dot-sep">·</span>
			<button type="button" class="link-btn" onclick={onwizard}>
				{m.servers_xray_setup_wizard()}
			</button>
		</div>
	</div>

	<div class="summary-line">
		<div class="summary-left">
			<ShieldCheck size={15} class="summary-icon" />
			<span
				class="summary-text muted"
				class:warn-text={checkStatus === 'warning' || checkStatus === 'blocked'}
			>
				{checkSummaryText}
			</span>
		</div>
		<div class="summary-actions">
			<button
				type="button"
				class="link-btn"
				onclick={oncheck}
				disabled={checkLoading}
			>
				{checkLoading ? m.servers_xray_checking_dots() : m.common_check()}
			</button>
			<span class="dot-sep">·</span>
			<button
				type="button"
				class="link-btn"
				onclick={ondetails}
			>
				{m.servers_xray_details()}
			</button>
		</div>
	</div>
</section>

<style>
	/* 2-line Summary Strip */
	.summary-strip {
		display: flex;
		flex-direction: column;
		gap: 6px;
		padding: 9px 12px;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
	}

	.summary-line {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 10px;
		flex-wrap: wrap;
		font-size: 12.5px;
	}

	.summary-left {
		display: flex;
		align-items: center;
		gap: 8px;
		min-width: 0;
		flex-wrap: wrap;
	}

	.summary-text {
		color: var(--color-text-primary);
		font-weight: 500;
	}
	.summary-text.muted {
		color: var(--color-text-secondary);
		font-weight: 400;
	}
	.summary-text.warn-text {
		color: var(--color-warning);
	}

	.inline-warn-badge {
		font-size: 11px;
		padding: 1px 6px;
		border-radius: 4px;
		background: var(--color-warning-tint);
		border: 1px solid var(--color-warning-border);
		color: var(--color-warning);
		font-weight: 500;
	}

	.summary-actions {
		display: flex;
		align-items: center;
		gap: 6px;
		flex-shrink: 0;
	}

	.link-btn {
		background: none;
		border: none;
		padding: 0;
		color: var(--color-accent);
		font-size: 12px;
		font-weight: 500;
		cursor: pointer;
	}
	.link-btn:hover:not(:disabled) {
		text-decoration: underline;
	}
	.link-btn:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}

	.dot-sep {
		color: var(--color-text-muted);
		opacity: 0.6;
	}
</style>
