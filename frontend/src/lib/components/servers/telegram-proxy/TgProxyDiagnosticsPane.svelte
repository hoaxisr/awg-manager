<script lang="ts">
	// Вкладка «Диагностика и проверка»: состояние портов и выхода плюс проверки.
	import { m } from '$lib/i18n';
	import type { TgWebProxyConfig, TgWebProxyStatus } from '$lib/types';
	import ConnectionChecks from '../wizard/ConnectionChecks.svelte';

	interface Props {
		config: TgWebProxyConfig | null;
		status: TgWebProxyStatus | null;
		egressSummaryLabel: string;
		cardFingerprint: string;
	}

	let { config, status, egressSummaryLabel, cardFingerprint }: Props = $props();

	let directPort = $derived(config?.direct_port || 8443);
	let listenPort = $derived(config?.listen_port || 8085);
</script>

<div class="diagnostics-pane">
	<section class="meta-strip">
		<div class="strip-item">
			<span class="strip-label">MTProto (:{directPort})</span>
			<span class="strip-val">
				<span class="health-dot" class:online={status?.direct_online}></span>
				<span class="health-text">{status?.direct_online ? m.servers_tg_diag_online({ port: directPort }) : m.servers_tg_diag_off()}</span>
			</span>
		</div>
		<div class="strip-divider"></div>
		<div class="strip-item">
			<span class="strip-label">{m.servers_tg_diag_bridge({ port: listenPort })}</span>
			<span class="strip-val">
				<span class="health-dot" class:online={status?.webproxy_online}></span>
				<span class="health-text">{status?.webproxy_online ? m.servers_tg_diag_online({ port: listenPort }) : m.servers_tg_diag_off()}</span>
			</span>
		</div>
		<div class="strip-divider"></div>
		<div class="strip-item">
			<span class="strip-label">{m.servers_tg_diag_core()}</span>
			<span class="strip-val">
				<span class="health-dot" class:online={status?.raw_online}></span>
				<span class="health-text">{status?.raw_online ? m.servers_tg_diag_online({ port: 2398 }) : m.servers_tg_diag_core_off()}</span>
			</span>
		</div>
		<div class="strip-divider"></div>
		<div class="strip-item">
			<span class="strip-label">{m.servers_tg_diag_egress({ label: egressSummaryLabel })}</span>
			<span class="strip-val">
				{#if status?.upstream_status === 'ok'}
					<span class="health-dot online"></span>
					<span class="health-text font-mono">{m.servers_tg_diag_upstream_ok()}</span>
				{:else if status?.upstream_status === 'degraded'}
					<span class="health-dot degraded"></span>
					<span class="health-text font-mono">{m.servers_tg_diag_upstream_degraded()}</span>
				{:else}
					<span class="health-dot stopped"></span>
					<span class="health-text font-mono">{m.servers_tg_diag_upstream_down()}</span>
				{/if}
			</span>
		</div>
	</section>

	<ConnectionChecks kind="tgwebproxy" currentFingerprint={cardFingerprint} autoRun={true} />
</div>

<style>
	.diagnostics-pane {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}

	.meta-strip {
		display: flex;
		align-items: center;
		gap: 1rem;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 0.65rem 1rem;
		flex-wrap: wrap;
	}

	.strip-item {
		display: flex;
		flex-direction: column;
		gap: 2px;
	}

	.strip-label {
		font-size: 10px;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--color-text-muted);
	}

	.strip-val {
		font-size: 12px;
		font-weight: 500;
		color: var(--color-text-primary);
		display: flex;
		align-items: center;
		gap: 6px;
	}

	.strip-divider {
		width: 1px;
		height: 24px;
		background: var(--color-border);
	}

	.health-dot {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		background: var(--color-text-muted);
	}
	.health-dot.online {
		background: var(--color-success);
		box-shadow: 0 0 3px var(--color-success-border);
	}
	.health-dot.degraded {
		background: var(--color-warning);
		box-shadow: 0 0 3px var(--color-warning-border);
	}
	.health-dot.stopped {
		background: var(--color-error);
	}

	.health-text {
		font-size: 12px;
	}

	.font-mono {
		font-family: var(--font-mono);
	}

	@media (max-width: 640px) {
		.meta-strip {
			flex-direction: column;
			align-items: flex-start;
		}
		.strip-divider {
			display: none;
		}
	}
</style>
