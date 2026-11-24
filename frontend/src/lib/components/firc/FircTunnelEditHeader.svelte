<script lang="ts">
	import { m } from '$lib/i18n';
	import type { FircTunnel, FircTunnelState } from '$lib/types/firc';
	import type { FircTunnelDiagnostic } from '$lib/utils/fircDiagnostics';
	import { fircEditTunnelSubTab } from '$lib/stores/firc';
	import { Button, Badge, StatusDot } from '$lib/components/ui';
	import { RefreshCw, RotateCw, Link2, Server, Shield, Network } from 'lucide-svelte';

	interface Props {
		draft: FircTunnel;
		liveState: FircTunnelState | undefined;
		diagnostic: FircTunnelDiagnostic | null;
		busyAction: '' | 'probe' | 'refresh' | 'restart';
		nodeCount: number;
		onrefresh: () => void;
		onrestart: () => void;
	}

	let {
		draft = $bindable(),
		liveState,
		diagnostic,
		busyAction,
		nodeCount,
		onrefresh,
		onrestart,
	}: Props = $props();
</script>

	<div class="top-meta-bar">
		<div class="top-meta-left">
			<StatusDot
				variant={!draft.enable
					? 'muted'
					: liveState?.status === 'up'
						? 'success'
						: diagnostic?.tone === 'error'
							? 'error'
							: 'warning'}
				size="md"
			/>
			<Badge variant="accent" size="sm" mono>{draft.device}</Badge>
			<input
				class="inp name-inp"
				type="text"
				placeholder={m.firc_tunnel_edit_name_placeholder()}
				bind:value={draft.description}
			/>
		</div>
		<div class="top-meta-right">
			<Button
				variant="secondary"
				size="sm"
				loading={busyAction === 'refresh'}
				onclick={onrefresh}
			>
				{#snippet iconBefore()}<RefreshCw size={13} />{/snippet}
				{m.firc_tunnel_edit_refresh()}
			</Button>
			<Button
				variant="secondary"
				size="sm"
				loading={busyAction === 'restart'}
				onclick={onrestart}
			>
				{#snippet iconBefore()}<RotateCw size={13} />{/snippet}
				{m.servers_card_restart()}
			</Button>
		</div>
	</div>

	{#if diagnostic?.reason}
		<div
			class="modal-diag-banner"
			class:is-error={diagnostic.tone === 'error'}
			class:is-warning={diagnostic.tone === 'warning'}
			class:is-info={diagnostic.tone === 'info'}
			role="alert"
			data-testid="firc-modal-diagnostic"
		>
			{diagnostic.reason}
		</div>
	{/if}

	<!-- Вкладки внутри модалки — свои чипы: canonical Tabs рассчитан на страницу (URL, память вкладки). -->
	<div class="tunnel-subtabs">
		<button
			type="button"
			class="subtab-btn"
			class:active={$fircEditTunnelSubTab === 'nodes'}
			onclick={() => fircEditTunnelSubTab.set('nodes')}
		>
			<Server size={13} />
			<span>{m.firc_tunnel_edit_tab_nodes({ count: nodeCount })}</span>
		</button>
		<button
			type="button"
			class="subtab-btn"
			class:active={$fircEditTunnelSubTab === 'sources'}
			onclick={() => fircEditTunnelSubTab.set('sources')}
		>
			<Link2 size={13} />
			<span>{m.firc_tunnel_edit_tab_sources({ count: draft.sources.length })}</span>
		</button>
		<button
			type="button"
			class="subtab-btn"
			class:active={$fircEditTunnelSubTab === 'failover'}
			onclick={() => fircEditTunnelSubTab.set('failover')}
		>
			<Shield size={13} />
			<span>{m.firc_tunnel_edit_tab_failover()}</span>
		</button>
		<button
			type="button"
			class="subtab-btn"
			class:active={$fircEditTunnelSubTab === 'inbounds'}
			onclick={() => fircEditTunnelSubTab.set('inbounds')}
		>
			<Network size={13} />
			<span>{m.firc_tunnel_edit_tab_inbounds({ count: draft.inbounds?.length ?? 0 })}</span>
		</button>
	</div>

<style>
	.top-meta-bar {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 10px;
		flex-wrap: wrap;
	}
	.top-meta-left {
		display: flex;
		align-items: center;
		gap: 8px;
		flex: 1 1 240px;
	}
	.name-inp {
		max-width: 260px;
		font-weight: 600;
	}
	.top-meta-right {
		display: flex;
		align-items: center;
		gap: 6px;
	}
	.tunnel-subtabs {
		display: flex;
		flex-wrap: wrap;
		gap: 4px;
		border-bottom: 1px solid var(--border);
		padding-bottom: 8px;
	}
	.subtab-btn {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		padding: 6px 12px;
		border-radius: 6px;
		border: 1px solid transparent;
		background: transparent;
		color: var(--text-secondary);
		font-size: 12px;
		font-weight: 600;
		cursor: pointer;
	}
	.subtab-btn:hover {
		color: var(--text-primary);
		background: var(--bg-tertiary);
	}
	.subtab-btn.active {
		color: var(--accent);
		background: color-mix(in srgb, var(--accent) 12%, var(--bg-tertiary));
		border-color: color-mix(in srgb, var(--accent) 35%, var(--border));
	}
	.modal-diag-banner {
		padding: 8px 12px;
		border-radius: 8px;
		border: 1px solid var(--border);
		background: var(--bg-tertiary);
		color: var(--text-secondary);
		font-size: 12px;
		line-height: 1.4;
		overflow-wrap: anywhere;
	}
	.modal-diag-banner.is-error {
		background: var(--color-error-tint, rgba(247, 118, 142, 0.14));
		border-color: var(--color-error-border, rgba(247, 118, 142, 0.4));
		color: var(--color-error, #f7768e);
	}
	.modal-diag-banner.is-warning {
		background: var(--color-warning-tint, rgba(224, 175, 104, 0.14));
		border-color: var(--color-warning-border, rgba(224, 175, 104, 0.4));
		color: var(--color-warning, #e0af68);
	}
	.modal-diag-banner.is-info {
		background: var(--color-info-tint, rgba(125, 207, 255, 0.14));
		border-color: var(--color-info-border, rgba(125, 207, 255, 0.4));
		color: var(--color-info, #7dcfff);
	}
	.inp {
		padding: 5px 8px;
		border-radius: 6px;
		border: 1px solid var(--border);
		background: var(--bg-primary);
		color: var(--text-primary);
		font-size: 12px;
		width: 100%;
		box-sizing: border-box;
	}
</style>
