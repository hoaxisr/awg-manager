<script lang="ts">
	// Короткая сводка карточки «Прокси Telegram»: выход в интернет, состояние
	// настройки/процесса/доступности и итог последней проверки.
	import { m, formatLocale } from '$lib/i18n';
	import type { TgWebProxyConfig, TgWebProxyStatus } from '$lib/types';
	import type { VerificationReport } from '$lib/types/serverWizard';
	import { Compass, ShieldCheck } from 'lucide-svelte';

	interface Props {
		config: TgWebProxyConfig | null;
		status: TgWebProxyStatus | null;
		shortEgressSummary: string;
		currentEgressUnavailable: boolean;
		checkLoading: boolean;
		lastCheckError: string;
		lastCheckReport: VerificationReport | null;
		onchangeegress: () => void;
		onopenwizard: () => void;
		oncheck: () => void;
		ondetails: () => void;
	}

	let {
		config,
		status,
		shortEgressSummary,
		currentEgressUnavailable,
		checkLoading,
		lastCheckError,
		lastCheckReport,
		onchangeegress,
		onopenwizard,
		oncheck,
		ondetails,
	}: Props = $props();

	let configEnabledText = $derived(
		config ? (config.enabled ? m.servers_tg_config_enabled() : m.servers_tg_config_disabled()) : '...'
	);
	let processStateText = $derived.by(() => {
		if (status?.running) return m.servers_tg_process_running();
		if (status?.direct_online || status?.webproxy_online) return m.servers_tg_process_partial();
		return m.servers_tg_process_stopped();
	});
	let reachabilityStateText = $derived.by(() => {
		if (checkLoading) return m.servers_tg_reach_checking();
		if (lastCheckError) return m.servers_tg_reach_error();
		if (!lastCheckReport) return m.servers_tg_reach_unchecked();
		if (lastCheckReport.overall_status === 'ok') return m.servers_tg_reach_ok();
		if (lastCheckReport.overall_status === 'warning') return m.servers_tg_reach_warning();
		return m.servers_tg_reach_failed();
	});

	function formatCheckTime(iso?: string): string {
		if (!iso) return '';
		try {
			return new Date(iso).toLocaleTimeString(formatLocale(), {
				hour: '2-digit',
				minute: '2-digit',
			});
		} catch {
			return '';
		}
	}

	let checkSummaryText = $derived.by(() => {
		if (checkLoading) return m.servers_tg_check_running();
		if (lastCheckError) return m.servers_tg_check_error({ error: lastCheckError });
		if (!lastCheckReport) return m.servers_tg_check_never();
		const time = formatCheckTime(lastCheckReport.checked_at);
		if (lastCheckReport.overall_status === 'ok') return m.servers_tg_check_ok({ time });
		if (lastCheckReport.overall_status === 'warning') return m.servers_tg_check_warning({ time });
		return m.servers_tg_check_failed({ time });
	});
</script>

<section class="summary-strip" aria-label={m.servers_tg_summary_aria()}>
	<div class="summary-line">
		<div class="summary-left">
			<Compass size={15} class="summary-icon" />
			<span class="summary-text" class:warn-text={currentEgressUnavailable}>
				{shortEgressSummary}
			</span>
			{#if currentEgressUnavailable}
				<span class="inline-warn-badge">{m.servers_tg_tunnel_unavailable()}</span>
			{/if}
		</div>
		<div class="summary-actions">
			<button type="button" class="link-btn" onclick={onchangeegress}>
				{m.common_edit()}
			</button>
			<span class="dot-sep">·</span>
			<button type="button" class="link-btn" onclick={onopenwizard}>
				{m.servers_tg_wizard_link()}
			</button>
		</div>
	</div>

	<div class="summary-line">
		<div class="summary-left">
			<span class="summary-text muted">
				{m.servers_tg_state_config()} <strong>{configEnabledText}</strong> · {m.servers_tg_state_process()} <strong>{processStateText}</strong> · {m.servers_tg_state_reach()} <strong>{reachabilityStateText}</strong>
			</span>
		</div>
	</div>

	<div class="summary-line">
		<div class="summary-left">
			<ShieldCheck size={15} class="summary-icon" />
			<span
				class="summary-text muted"
				class:warn-text={lastCheckReport?.overall_status === 'warning' || lastCheckReport?.overall_status === 'blocked'}
			>
				{checkSummaryText}
			</span>
		</div>
		<div class="summary-actions">
			<button type="button" class="link-btn" onclick={oncheck} disabled={checkLoading}>
				{checkLoading ? m.servers_tg_checking() : m.common_check()}
			</button>
			<span class="dot-sep">·</span>
			<button type="button" class="link-btn" onclick={ondetails}>
				{m.servers_tg_details()}
			</button>
		</div>
	</div>
</section>

<style>
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

	:global(.summary-icon) {
		color: var(--color-accent);
		flex-shrink: 0;
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

	.dot-sep {
		color: var(--color-text-muted);
		opacity: 0.6;
	}
</style>
