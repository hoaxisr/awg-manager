<script lang="ts">
	import { api } from '$lib/api/client';
	import { m } from '$lib/i18n';
	import { Button } from '$lib/components/ui';
	import { notifications } from '$lib/stores/notifications';
	import type { XrayRealityState, XrayRealityTransport, XrayRealityDestCheck } from '$lib/types/xray';
	import { CheckCircle2, AlertTriangle, ShieldCheck, RefreshCw } from 'lucide-svelte';

	/**
	 * Shared form for the direct VLESS + REALITY route (white-IP routers).
	 * Used by the "Маршрут VLESS" modal in the server card and by the setup wizard.
	 */
	interface Props {
		rs: XrayRealityState | null;
		port: number;
		transport: XrayRealityTransport;
		dest: string;
		serverName?: string;
		publicHost?: string;
		/** Show the optional "address for links" override. */
		showPublicHost?: boolean;
		/** Show the transport selector (TCP / XHTTP). */
		showTransport?: boolean;
		/** Error shown when publicHost points to a cloud-only KeenDNS domain. */
		cloudHostError?: string;
		/** Required prompt when router is behind CGNAT and manual external host must be provided. */
		publicHostRequiredHint?: string;
		/** Optional switch-to-domain action shown inside cloudHostError. */
		onswitchtovlessdomain?: () => void;
		/** Visual container surface context. */
		surface?: 'inset' | 'modal';
		/** Called after a WAN re-check so the parent can store fresh state. */
		onrefreshed?: (rs: XrayRealityState) => void;
	}

	let {
		rs,
		port = $bindable(2053),
		transport = $bindable('tcp'),
		dest = $bindable('www.microsoft.com:443'),
		serverName = $bindable(''),
		publicHost = $bindable(''),
		showPublicHost = true,
		showTransport = true,
		cloudHostError = '',
		publicHostRequiredHint = '',
		onswitchtovlessdomain,
		surface = 'inset',
		onrefreshed
	}: Props = $props();

	let destCheck = $state<XrayRealityDestCheck | null>(null);
	let destChecking = $state(false);
	let netRefreshing = $state(false);

	const net = $derived(rs?.network ?? null);

	function destHost(d: string): string {
		const v = (d || '').trim().replace(/^https?:\/\//, '').replace(/\/.*$/, '');
		const idx = v.lastIndexOf(':');
		return idx > 0 ? v.slice(0, idx) : v;
	}

	async function checkDest() {
		destChecking = true;
		destCheck = null;
		try {
			destCheck = await api.checkXrayRealityDest(dest.trim(), serverName.trim() || undefined);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.servers_xray_reality_check_failed());
		} finally {
			destChecking = false;
		}
	}

	async function refreshNet() {
		netRefreshing = true;
		try {
			const fresh = await api.getXrayReality(true);
			onrefreshed?.(fresh);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.servers_xray_reality_wan_check_failed());
		} finally {
			netRefreshing = false;
		}
	}
</script>

<div class="rf" class:rf-modal={surface === 'modal'}>
	<div class="rf-banner" class:ok={net?.white_ip} class:warn={!net?.white_ip}>
		{#if !net}
			<AlertTriangle size={15} class="rf-banner-icon" />
			<span class="rf-banner-text">{m.servers_xray_reality_ip_unchecked()}</span>
		{:else if net.white_ip}
			<CheckCircle2 size={15} class="rf-banner-icon" />
			<span class="rf-banner-text">
				{m.servers_xray_reality_white_ip()} <b class="mono">{net.external_ip || net.wan_address}</b>{net.keendns_domain ? m.servers_xray_reality_keendns_links({ domain: net.keendns_domain }) : ''}
			</span>
		{:else}
			<AlertTriangle size={15} class="rf-banner-icon" />
			<span class="rf-banner-text">{m.servers_xray_reality_no_white_ip({ reason: net.reason || m.servers_xray_reality_unreachable() })}</span>
		{/if}
		<button type="button" class="rf-link" onclick={refreshNet} disabled={netRefreshing}>
			<RefreshCw size={12} />{netRefreshing ? m.servers_xray_checking() : m.common_check()}
		</button>
	</div>

	{#if showTransport}
		<div class="rf-field">
			<span class="rf-label">{m.servers_xray_reality_transport()}</span>
			<div class="rf-seg">
				<button type="button" class="rf-seg-btn" class:active={transport === 'tcp'} onclick={() => (transport = 'tcp')}>
					<b>{m.servers_xray_reality_tcp_title()}</b>
					<small>{m.servers_xray_reality_tcp_desc()}</small>
				</button>
				<button type="button" class="rf-seg-btn" class:active={transport === 'xhttp'} onclick={() => (transport = 'xhttp')}>
					<b>XHTTP (stream-one)</b>
					<small>{m.servers_xray_reality_xhttp_desc()}</small>
				</button>
			</div>
		</div>
	{/if}

	<div class="rf-row">
		<label class="rf-field">
			<span class="rf-label">{m.servers_xray_reality_port()}</span>
			<input class="rf-input mono" type="number" min="1024" max="65535" bind:value={port} />
			<span class="rf-hint">
				{m.servers_xray_reality_port_hint({ free: (rs?.port_candidates ?? [2053, 2083, 2087, 2096]).slice(0, 6).join(', ') })}
			</span>
		</label>
		{#if showPublicHost}
			<label class="rf-field">
				<span class="rf-label">{m.servers_xray_reality_public_host()}</span>
				<input
					class="rf-input mono"
					class:rf-input-error={Boolean(cloudHostError || publicHostRequiredHint)}
					placeholder={net?.public_host || m.servers_xray_auto()}
					bind:value={publicHost}
				/>
				{#if cloudHostError}
					<div class="rf-error-box">
						<AlertTriangle size={14} class="rf-error-icon" />
						<div class="rf-error-body">
							<span>{cloudHostError}</span>
							{#if onswitchtovlessdomain}
								<div>
									<Button variant="primary" size="sm" onclick={onswitchtovlessdomain}>
										{m.servers_ext_wiz_s2_switch_vless_domain()}
									</Button>
								</div>
							{/if}
						</div>
					</div>
				{:else if publicHostRequiredHint}
					<div class="rf-error-box">
						<AlertTriangle size={14} class="rf-error-icon" />
						<div class="rf-error-body">
							<span>{publicHostRequiredHint}</span>
						</div>
					</div>
				{/if}
				<span class="rf-hint">{m.servers_xray_reality_public_host_hint()}</span>
			</label>
		{/if}
	</div>

	<div class="rf-field">
		<span class="rf-label">{m.servers_xray_reality_dest()}</span>
		<div class="rf-dest-row">
			<input class="rf-input mono" bind:value={dest} oninput={() => (destCheck = null)} />
			<Button variant="secondary" size="sm" onclick={checkDest} disabled={destChecking || !dest.trim()}>
				<ShieldCheck size={14} class="rf-btn-icon" />
				{destChecking ? m.servers_xray_checking() : m.common_check()}
			</Button>
		</div>
		<div class="rf-chips">
			{#each rs?.dest_presets ?? [] as p (p)}
				<button
					type="button"
					class="rf-chip"
					class:active={dest === p}
					onclick={() => {
						dest = p;
						serverName = '';
						destCheck = null;
					}}
				>
					{destHost(p)}
				</button>
			{/each}
		</div>
		<span class="rf-hint">{m.servers_xray_reality_dest_hint()}</span>
		{#if destCheck}
			<div class="rf-banner" class:ok={destCheck.ok} class:warn={!destCheck.ok}>
				{#if destCheck.ok}
					<CheckCircle2 size={15} class="rf-banner-icon" />
					<span class="rf-banner-text">{m.servers_xray_reality_dest_ok({ dest: destCheck.dest, h2: destCheck.h2 ? '✓' : m.servers_xray_reality_h2_missing(), ms: destCheck.latency_ms })}</span>
				{:else}
					<AlertTriangle size={15} class="rf-banner-icon" />
					<span class="rf-banner-text">{m.servers_xray_reality_dest_bad({ dest: destCheck.dest, error: destCheck.error || m.servers_xray_reality_dest_bad_default() })}</span>
				{/if}
			</div>
		{/if}
	</div>

	<label class="rf-field">
		<span class="rf-label">SNI (serverName)</span>
		<input class="rf-input mono" placeholder={destHost(dest)} bind:value={serverName} />
		<span class="rf-hint">{m.servers_xray_reality_sni_hint()}</span>
	</label>
</div>

<style>
	.rf {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}
	.rf-field {
		display: flex;
		flex-direction: column;
		gap: 5px;
		min-width: 0;
	}
	.rf-row {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 12px;
	}
	@media (max-width: 640px) {
		.rf-row,
		.rf-seg {
			grid-template-columns: 1fr;
		}
	}
	.rf-label {
		font-size: 12px;
		font-weight: 500;
		color: var(--color-text-primary);
	}
	.rf-hint {
		font-size: 11px;
		color: var(--color-text-muted);
		line-height: 1.4;
	}
	.rf-input {
		width: 100%;
		box-sizing: border-box;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		color: var(--color-text-primary);
		font-size: 13px;
		padding: 7px 10px;
		transition: border-color var(--t-fast) ease;
	}
	.rf-input:focus {
		outline: none;
		border-color: var(--color-accent);
	}
	.rf-input.rf-input-error {
		border-color: var(--color-error-border);
	}
	.rf-input.rf-input-error:focus {
		border-color: var(--color-error);
	}
	.rf-error-box {
		display: flex;
		align-items: flex-start;
		gap: 8px;
		margin-top: 2px;
		padding: 8px 10px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-error-border);
		background: var(--color-error-tint);
		font-size: 12px;
		line-height: 1.4;
		color: var(--color-text-primary);
	}
	:global(.rf-error-icon) {
		color: var(--color-error);
		flex-shrink: 0;
		margin-top: 2px;
	}
	.rf-error-body {
		display: flex;
		flex-direction: column;
		gap: 6px;
		min-width: 0;
		flex: 1;
	}
	.mono {
		font-family: var(--font-mono);
	}
	.rf-seg {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 8px;
	}
	.rf-seg-btn {
		display: flex;
		flex-direction: column;
		gap: 3px;
		text-align: left;
		padding: 9px 11px;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-secondary);
		color: var(--color-text-primary);
		cursor: pointer;
		font-size: 12px;
		transition: border-color var(--t-fast) ease, background var(--t-fast) ease;
	}
	.rf-seg-btn:hover {
		border-color: var(--color-accent);
	}
	.rf-seg-btn b {
		font-weight: 600;
		color: var(--color-text-primary);
	}
	.rf-seg-btn small {
		font-size: 11px;
		color: var(--color-text-muted);
		line-height: 1.35;
	}
	.rf-seg-btn.active {
		border-color: var(--color-accent);
		background: var(--color-accent-tint);
	}
	.rf-dest-row {
		display: flex;
		gap: 8px;
		align-items: center;
	}
	.rf-dest-row :global(.rf-btn-icon) {
		flex-shrink: 0;
	}
	.rf-chips {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
	}
	.rf-chip {
		font-size: 11.5px;
		font-family: var(--font-mono);
		padding: 4px 10px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
		color: var(--color-text-secondary);
		cursor: pointer;
		transition: all var(--t-fast) ease;
	}
	.rf.rf-modal .rf-chip {
		background: var(--color-bg-primary);
	}
	.rf-chip:hover {
		border-color: var(--color-accent);
		color: var(--color-text-primary);
	}
	.rf-chip.active,
	.rf.rf-modal .rf-chip.active {
		border-color: var(--color-accent);
		background: var(--color-accent-tint);
		color: var(--color-accent);
	}
	.rf-banner {
		display: flex;
		align-items: flex-start;
		gap: 8px;
		flex-wrap: nowrap;
		font-size: 12px;
		line-height: 1.4;
		padding: 8px 10px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		color: var(--color-text-primary);
	}
	.rf-banner :global(.rf-banner-icon) {
		flex-shrink: 0;
		margin-top: 2px;
	}
	.rf-banner-text {
		flex: 1;
		min-width: 0;
	}
	@media (max-width: 480px) {
		.rf-banner {
			flex-wrap: wrap;
		}
	}
	.rf-banner.ok {
		border-color: var(--color-success-border);
		background: var(--color-success-tint);
	}
	.rf-banner.warn {
		border-color: var(--color-warning-border);
		background: var(--color-warning-tint);
	}
	.rf-banner.ok :global(.rf-banner-icon) {
		color: var(--color-success);
		flex-shrink: 0;
	}
	.rf-banner.warn :global(.rf-banner-icon) {
		color: var(--color-warning);
		flex-shrink: 0;
	}
	.rf-link {
		display: inline-flex;
		align-items: center;
		gap: 4px;
		margin-left: auto;
		flex-shrink: 0;
		white-space: nowrap;
		background: none;
		border: none;
		padding: 0;
		color: var(--color-accent);
		font-size: 12px;
		font-weight: 500;
		cursor: pointer;
	}
	.rf-link:hover:not(:disabled) {
		text-decoration: underline;
	}
	.rf-link:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}
</style>
