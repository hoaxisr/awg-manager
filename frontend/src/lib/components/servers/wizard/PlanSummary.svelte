<script lang="ts">
	import type { ChangePlan } from '$lib/types/serverWizard';
	import { m } from '$lib/i18n';
	import { ShieldCheck, RefreshCw, Layers } from 'lucide-svelte';

	interface Props {
		plan: ChangePlan;
	}

	let { plan }: Props = $props();

	const ACTION_LABELS: Record<string, () => string> = {
		create: m.servers_wizard_action_create,
		enable: m.servers_wizard_action_enable,
		modify: m.servers_wizard_action_modify,
		keep: m.servers_wizard_action_keep,
		publish: m.servers_wizard_action_publish,
		disable: m.servers_wizard_action_disable,
	};

	const TARGET_LABELS: Record<string, () => string> = {
		'awgm-xhttpd': m.servers_wizard_target_xhttpd,
		'cdn-dispatcher': m.servers_wizard_target_dispatcher,
		client: m.servers_wizard_target_client,
		direct_host: m.servers_wizard_target_direct_host,
		keendns_proxy: m.servers_wizard_target_keendns,
		origin_source: m.servers_wizard_target_origin,
		outbound_routing: m.servers_wizard_target_outbound,
		public_domain: m.servers_wizard_target_public_domain,
		telemt: m.servers_wizard_target_telemt,
		tgwebproxy: m.servers_wizard_target_tgwebproxy,
		'tproxy-server': m.servers_wizard_target_tproxy,
		upstream_egress: m.servers_wizard_target_upstream,
		wan_firewall: m.servers_wizard_target_wan_firewall,
	};

	// Неизвестное серверу-новее значение показываем как есть.
	const label = (labels: Record<string, () => string>, key: string) => labels[key]?.() ?? key;

	function formatFingerprint(fp: string): string {
		if (!fp || fp.length < 12) return fp;
		return fp.slice(0, 6) + '...' + fp.slice(-6);
	}
</script>

<div class="plan-wrapper">
	<!-- Summary Card -->
	<div class="plan-card">
		<div class="summary-top">
			<div class="summary-title-group">
				<Layers size={18} class="summary-icon" />
				<span class="summary-title">{plan.summary}</span>
			</div>
			{#if plan.restart_required}
				<span class="badge-restart">
					<RefreshCw size={12} class="mr-1" />
					{m.servers_wizard_plan_restart()}
				</span>
			{/if}
		</div>

		{#if plan.state_fingerprint}
			<div class="fingerprint-row">
				<span class="fingerprint-label">
					<ShieldCheck size={14} class="shield-icon" />
					{m.servers_wizard_plan_fingerprint()}
				</span>
				<span class="fingerprint-val" title={plan.state_fingerprint}>
					{formatFingerprint(plan.state_fingerprint)}
				</span>
			</div>
		{/if}
	</div>

	<!-- Change Items -->
	<div>
		<div class="section-title">
			{m.servers_wizard_plan_actions({ count: plan.items?.length || 0 })}
		</div>

		<div class="items-card">
			{#each plan.items as item}
				<div class="item-row">
					<div class="item-main">
						<span class="action-badge action-{item.action}">
							{label(ACTION_LABELS, item.action)}
						</span>
						<div class="item-desc-group">
							<div class="item-desc">{item.description}</div>
							<div class="item-target">
								{m.servers_wizard_plan_target()} <span class="target-val">{label(TARGET_LABELS, item.target)}</span>
							</div>
						</div>
					</div>

					{#if item.new_value}
						<div class="item-val-wrapper">
							<span class="item-val">
								{item.new_value}
							</span>
						</div>
					{/if}
				</div>
			{/each}
		</div>
	</div>
</div>

<style>
	.plan-wrapper {
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}

	.plan-card {
		padding: 1rem;
		border-radius: var(--radius);
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
	}

	.summary-top {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem;
	}

	.summary-title-group {
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}

	:global(.summary-icon) {
		color: var(--color-accent);
		flex-shrink: 0;
	}

	.summary-title {
		font-size: 0.8125rem;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.badge-restart {
		display: inline-flex;
		align-items: center;
		font-size: 0.6875rem;
		font-weight: 600;
		padding: 2px 8px;
		border-radius: var(--radius-pill);
		background: var(--color-warning-tint);
		border: 1px solid var(--color-warning-border);
		color: var(--color-warning);
		flex-shrink: 0;
	}

	.fingerprint-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		padding-top: 0.5rem;
		border-top: 1px solid var(--color-border);
		font-size: 0.75rem;
	}

	.fingerprint-label {
		display: flex;
		align-items: center;
		gap: 0.375rem;
		color: var(--color-text-muted);
	}

	:global(.shield-icon) {
		color: var(--color-success);
	}

	.fingerprint-val {
		font-family: var(--font-mono);
		font-size: 0.6875rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		padding: 2px 6px;
		border-radius: var(--radius-sm);
		color: var(--color-text-secondary);
	}

	.section-title {
		font-size: 0.75rem;
		font-weight: 600;
		color: var(--color-text-muted);
		text-transform: uppercase;
		letter-spacing: 0.05em;
		margin-bottom: 0.5rem;
	}

	.items-card {
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		background: var(--color-bg-primary);
		overflow: hidden;
	}

	.item-row {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		gap: 0.75rem;
		padding: 0.75rem 1rem;
		border-bottom: 1px solid var(--color-border);
		flex-wrap: wrap;
	}

	.item-row:last-child {
		border-bottom: none;
	}

	.item-main {
		display: flex;
		align-items: flex-start;
		gap: 0.625rem;
		/* Основа, а не 0: иначе строка не переносится и широкое значение сжимает описание до буквы. */
		flex: 1 1 16rem;
		min-width: 0;
	}

	.action-badge {
		font-size: 0.625rem;
		font-weight: 700;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		padding: 2px 6px;
		border-radius: var(--radius-sm);
		flex-shrink: 0;
	}

	.action-create {
		background: var(--color-accent-tint);
		border: 1px solid var(--color-accent-border);
		color: var(--color-accent);
	}

	.action-enable {
		background: var(--color-success-tint);
		border: 1px solid var(--color-success-border);
		color: var(--color-success);
	}

	.action-modify {
		background: var(--color-info-tint);
		border: 1px solid var(--color-info-border);
		color: var(--color-info);
	}

	.action-publish {
		background: var(--color-accent-tint);
		border: 1px solid var(--color-accent-border);
		color: var(--color-accent);
	}

	.action-keep {
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		color: var(--color-text-secondary);
	}

	.action-disable {
		background: var(--color-warning-tint);
		border: 1px solid var(--color-warning-border);
		color: var(--color-warning);
	}

	.item-desc-group {
		display: flex;
		flex-direction: column;
		gap: 0.125rem;
		min-width: 0;
		flex: 1;
	}

	.item-desc {
		font-size: 0.75rem;
		font-weight: 600;
		color: var(--color-text-primary);
		overflow-wrap: anywhere;
		word-break: break-word;
	}

	.item-target {
		font-family: var(--font-mono);
		font-size: 0.6875rem;
		color: var(--color-text-muted);
		overflow-wrap: anywhere;
		word-break: break-word;
	}

	.target-val {
		color: var(--color-text-secondary);
	}

	.item-val-wrapper {
		min-width: 0;
		max-width: 100%;
		text-align: right;
	}

	.item-val {
		display: inline-block;
		max-width: 100%;
		font-family: var(--font-mono);
		font-size: 0.6875rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		padding: 2px 6px;
		border-radius: var(--radius-sm);
		color: var(--color-accent);
		overflow-wrap: anywhere;
		word-break: break-word;
	}
</style>
