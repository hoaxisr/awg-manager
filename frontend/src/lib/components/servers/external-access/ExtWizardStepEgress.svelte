<script lang="ts">
	// Шаг 4 мастера внешнего доступа: куда направлять интернет-трафик.
	import { m } from '$lib/i18n';
	import type { EgressOption } from '$lib/types/serverWizard';
	import type { ExternalAccessWizardDraft, isDraftEgressValid } from './externalAccessModel';
	import { EXTERNAL_ACCESS_LABELS, cleanTunnelDisplayName } from './connectionLabels';
	import { AlertTriangle, CheckCircle2, Compass } from 'lucide-svelte';

	interface Props {
		draft: ExternalAccessWizardDraft;
		wantXray: boolean;
		wantTg: boolean;
		egressValidation: ReturnType<typeof isDraftEgressValid>;
		currentXrayEgressOption: EgressOption | undefined;
		currentTgEgressOption: EgressOption | undefined;
		xrayRouterOption: EgressOption | undefined;
		xrayEgressCategory: 'direct' | 'router' | 'tunnel';
		tgEgressCategory: 'direct' | 'tunnel';
		xrayTunnelOptions: EgressOption[];
		tgTunnelOptions: EgressOption[];
		onselectxraycategory: (cat: 'direct' | 'router' | 'tunnel') => void;
		onselecttgcategory: (cat: 'direct' | 'tunnel') => void;
		onselectxrayegress: (id: string) => void;
		onselecttgegress: (id: string) => void;
	}

	let {
		draft,
		wantXray,
		wantTg,
		egressValidation,
		currentXrayEgressOption,
		currentTgEgressOption,
		xrayRouterOption,
		xrayEgressCategory,
		tgEgressCategory,
		xrayTunnelOptions,
		tgTunnelOptions,
		onselectxraycategory,
		onselecttgcategory,
		onselectxrayegress,
		onselecttgegress,
	}: Props = $props();

	function formatTunnelTechBadge(opt: EgressOption): string {
		const shortId = (opt.id || '').replace(/^system:/i, '').trim();
		const iface = (opt.iface || '').trim();
		if (iface && shortId && iface.toLowerCase() !== shortId.toLowerCase()) {
			return `${shortId} · ${iface}`;
		}
		return iface || shortId;
	}
</script>

<div class="step-section">
	<h3 class="step-heading">{m.servers_ext_wiz_s4_heading()}</h3>
	<p class="step-desc">
		{m.servers_ext_wiz_s4_desc()}
	</p>

	{#if !egressValidation.valid && egressValidation.reason}
		<div class="legacy-egress-banner">
			<AlertTriangle size={16} />
			<span>{egressValidation.reason}</span>
		</div>
	{/if}

	{#if wantXray}
		<div class="egress-service-box">
			<div class="egress-group-title">
				{draft.selectedKind === 'both' ? m.servers_ext_wiz_s4_vless_both() : m.servers_ext_wiz_s4_vless_route()}
			</div>

			{#if currentXrayEgressOption && !currentXrayEgressOption.available && currentXrayEgressOption.unavailable_reason !== egressValidation.reason}
				<div class="legacy-egress-banner">
					<AlertTriangle size={15} />
					<span>
						{currentXrayEgressOption.unavailable_reason || m.servers_ext_wiz_s4_current_unavailable({ name: currentXrayEgressOption.name })}
					</span>
				</div>
			{/if}

			<div class="cards-grid">
				<button
					type="button"
					class="choice-card compact"
					class:selected={xrayEgressCategory === 'direct'}
					onclick={() => onselectxraycategory('direct')}
				>
					<div class="choice-title">{m.servers_ext_wiz_s4_direct()}</div>
					<div class="choice-text">
						{m.servers_ext_wiz_s4_direct_text()}
					</div>
				</button>

				<button
					type="button"
					class="choice-card compact"
					class:selected={xrayEgressCategory === 'router'}
					disabled={xrayRouterOption && !xrayRouterOption.available}
					onclick={() => onselectxraycategory('router')}
				>
					<div class="choice-title">{EXTERNAL_ACCESS_LABELS.egressFirc}</div>
					<div class="choice-text">
						{(!xrayRouterOption?.available && xrayRouterOption?.unavailable_reason) || m.servers_ext_wiz_s4_router_text()}
					</div>
				</button>

				<button
					type="button"
					class="choice-card compact"
					class:selected={xrayEgressCategory === 'tunnel'}
					onclick={() => onselectxraycategory('tunnel')}
				>
					<div class="choice-title">{EXTERNAL_ACCESS_LABELS.egressTunnel}</div>
					<div class="choice-text">
						{m.servers_ext_wiz_s4_tunnel_text()}
					</div>
				</button>
			</div>

			{#if xrayEgressCategory === 'tunnel'}
				<div class="tunnel-picker-box">
					<span class="field-label">{m.servers_ext_wiz_s4_pick_vless()}</span>
					{#if xrayTunnelOptions.length === 0}
						<div class="empty-tunnels-note">
							{m.servers_ext_wiz_s4_no_tunnels_vless()}
						</div>
					{:else}
						<div class="egress-list">
							{#each xrayTunnelOptions as opt (opt.id)}
								{@const techBadge = formatTunnelTechBadge(opt)}
								<button
									type="button"
									class="egress-card"
									class:selected={draft.upstreamDevice === opt.id || draft.upstreamDevice === opt.iface}
									class:unavailable={!opt.available}
									disabled={!opt.available && !opt.current && draft.upstreamDevice !== opt.id && draft.upstreamDevice !== opt.iface}
									onclick={() => onselectxrayegress(opt.id)}
								>
									<div class="egress-card-left">
										<Compass size={17} class={opt.available ? 'icon-info-soft' : 'icon-warning-soft'} />
										<div class="egress-card-info">
											<div class="egress-name">
												<span>{cleanTunnelDisplayName(opt.name)}</span>
												{#if techBadge}
													<span class="tunnel-iface-pill mono">{techBadge}</span>
												{/if}
												{#if opt.current}
													<span class="current-pill">{m.servers_ext_wiz_s4_current()}</span>
												{/if}
											</div>
											{#if !opt.available}
												<div class="egress-warn">
													{opt.unavailable_reason || opt.degraded_msg || m.servers_ext_wiz_s4_opt_unavailable()}
												</div>
											{/if}
										</div>
									</div>
									<div class="egress-status">
										{#if opt.available}
											<CheckCircle2 size={16} class="icon-success-soft" />
										{:else}
											<AlertTriangle size={16} class="icon-warning-soft" />
										{/if}
									</div>
								</button>
							{/each}
						</div>
					{/if}
				</div>
			{/if}
		</div>
	{/if}

	{#if wantTg}
		<div class="egress-service-box">
			<div class="egress-group-title">
				{draft.selectedKind === 'both' ? m.servers_ext_wiz_s4_tg_both() : m.servers_ext_wiz_s4_tg_route()}
			</div>

			{#if currentTgEgressOption && !currentTgEgressOption.available}
				<div class="legacy-egress-banner">
					<AlertTriangle size={15} />
					<span>
						{currentTgEgressOption.unavailable_reason || m.servers_ext_wiz_s4_current_unavailable({ name: currentTgEgressOption.name })}
					</span>
				</div>
			{/if}

			<div class="cards-grid">
				<button
					type="button"
					class="choice-card compact"
					class:selected={tgEgressCategory === 'direct'}
					onclick={() => onselecttgcategory('direct')}
				>
					<div class="choice-title">{m.servers_ext_wiz_s4_direct()}</div>
					<div class="choice-text">
						{m.servers_ext_wiz_s4_direct_text()}
					</div>
				</button>

				<button
					type="button"
					class="choice-card compact"
					class:selected={tgEgressCategory === 'tunnel'}
					onclick={() => onselecttgcategory('tunnel')}
				>
					<div class="choice-title">{EXTERNAL_ACCESS_LABELS.egressTunnel}</div>
					<div class="choice-text">
						{m.servers_ext_wiz_s4_tg_tunnel_text()}
					</div>
				</button>
			</div>

			{#if tgEgressCategory === 'tunnel'}
				<div class="tunnel-picker-box">
					<span class="field-label">{m.servers_ext_wiz_s4_pick_tg()}</span>
					{#if tgTunnelOptions.length === 0}
						<div class="empty-tunnels-note">
							{m.servers_ext_wiz_s4_no_tunnels_tg()}
						</div>
					{:else}
						<div class="egress-list">
							{#each tgTunnelOptions as opt (opt.id)}
								{@const techBadge = formatTunnelTechBadge(opt)}
								<button
									type="button"
									class="egress-card"
									class:selected={draft.tgUpstreamDevice === opt.id || draft.tgUpstreamDevice === opt.iface}
									class:unavailable={!opt.available}
									disabled={!opt.available && !opt.current && draft.tgUpstreamDevice !== opt.id && draft.tgUpstreamDevice !== opt.iface}
									onclick={() => onselecttgegress(opt.id)}
								>
									<div class="egress-card-left">
										<Compass size={17} class={opt.available ? 'icon-success-soft' : 'icon-warning-soft'} />
										<div class="egress-card-info">
											<div class="egress-name">
												<span>{cleanTunnelDisplayName(opt.name)}</span>
												{#if techBadge}
													<span class="tunnel-iface-pill mono">{techBadge}</span>
												{/if}
												{#if opt.current}
													<span class="current-pill">{m.servers_ext_wiz_s4_current()}</span>
												{/if}
											</div>
											{#if !opt.available}
												<div class="egress-warn">
													{opt.unavailable_reason || opt.degraded_msg || m.servers_ext_wiz_s4_opt_unavailable()}
												</div>
											{/if}
										</div>
									</div>
									<div class="egress-status">
										{#if opt.available}
											<CheckCircle2 size={16} class="icon-success-soft" />
										{:else}
											<AlertTriangle size={16} class="icon-warning-soft" />
										{/if}
									</div>
								</button>
							{/each}
						</div>
					{/if}
				</div>
			{/if}
		</div>
	{/if}
</div>

<style>
	.egress-service-box {
		display: flex;
		flex-direction: column;
		gap: 10px;
		padding: 12px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-primary);
	}

	.egress-group-title {
		font-size: 13.5px;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.tunnel-picker-box {
		display: flex;
		flex-direction: column;
		gap: 8px;
		padding-top: 8px;
		border-top: 1px solid var(--color-border);
	}

	.empty-tunnels-note {
		font-size: 12px;
		color: var(--color-text-muted);
		padding: 8px 10px;
		background: var(--color-bg-secondary);
		border-radius: var(--radius-sm);
	}

	.legacy-egress-banner {
		display: flex;
		align-items: flex-start;
		gap: 8px;
		padding: 9px 12px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-warning-border);
		background: var(--color-warning-tint);
		color: var(--color-warning);
		font-size: 12px;
	}

	/* Причина бывает в три строки: иконка не сжимается и стоит у первой. */
	.legacy-egress-banner :global(svg) {
		flex-shrink: 0;
		margin-top: 1px;
	}

	.egress-list {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}

	.egress-card {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 10px;
		padding: 10px 12px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
		text-align: left;
		cursor: pointer;
		transition: border-color 0.15s, background 0.15s;
		min-width: 0;
		max-width: 100%;
	}

	.egress-card:hover:not(:disabled) {
		border-color: var(--color-accent);
	}

	.egress-card.selected {
		border-color: var(--color-accent-border);
		background: var(--color-accent-tint);
	}

	.egress-card:disabled,
	.egress-card.unavailable {
		opacity: 0.6;
	}

	.egress-card:disabled {
		cursor: not-allowed;
	}

	.tunnel-iface-pill {
		font-size: 11px;
		font-weight: 500;
		padding: 1px 7px;
		border-radius: 999px;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		color: var(--color-text-secondary);
	}

	.egress-card-left {
		display: flex;
		align-items: center;
		gap: 10px;
		min-width: 0;
		flex: 1;
	}

	.egress-card-left > :global(svg) {
		flex-shrink: 0;
	}

	.egress-status {
		flex-shrink: 0;
		display: flex;
		align-items: center;
	}

	.egress-card-info {
		min-width: 0;
		flex: 1;
	}

	.egress-name {
		font-size: 13px;
		font-weight: 600;
		color: var(--color-text-primary);
		display: flex;
		align-items: center;
		gap: 6px;
		flex-wrap: wrap;
		min-width: 0;
		overflow-wrap: anywhere;
		word-break: break-word;
	}

	.egress-warn {
		font-size: 11.5px;
		color: var(--color-warning);
		margin-top: 2px;
		min-width: 0;
		overflow-wrap: anywhere;
		word-break: break-word;
	}
</style>
