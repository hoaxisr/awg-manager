<script lang="ts">
	import { untrack } from 'svelte';
	import { api } from '$lib/api/client';
	import { m } from '$lib/i18n';
	import { notifications } from '$lib/stores/notifications';
	import { fircForeign } from '$lib/stores/firc';
	import { Button, Modal } from '$lib/components/ui';
	import type { XrayConfig } from '$lib/types';
	import type { EgressOption } from '$lib/types/serverWizard';
	import { EXTERNAL_ACCESS_LABELS, cleanTunnelDisplayName, unavailableSuffix } from './external-access/connectionLabels';
	import { AlertTriangle } from 'lucide-svelte';

	// Окно «Куда направлять интернет»: форма выхода VLESS, заполняется из конфига при открытии.
	interface Props {
		open: boolean;
		config: XrayConfig | null;
		tunnelInterfaces: EgressOption[];
		hasLegacySocksEgress: boolean;
		currentEgressUnavailable: boolean;
		/** Новый конфиг после сохранения; владелец перечитывает данные. */
		onsaved: (c: XrayConfig) => Promise<void> | void;
	}

	let {
		open = $bindable(false),
		config,
		tunnelInterfaces,
		hasLegacySocksEgress,
		currentEgressUnavailable,
		onsaved,
	}: Props = $props();

	let egressMode = $state<'direct' | 'router' | 'interface' | 'socks'>('direct');
	let egressInterface = $state('');
	let egressSocksPort = $state(1099);
	let egressAllowLan = $state(false);
	let customEgressIface = $state(false);
	let egressSaving = $state(false);
	// Намеренно не $state: защёлка «форма уже заполнена для этого открытия»
	// читается и пишется только внутри эффекта ниже; реактивная заставила бы
	// эффект перезапускаться от собственной записи.
	let syncedForOpen = false;

	$effect(() => {
		if (open) {
			if (!syncedForOpen) {
				syncedForOpen = true;
				untrack(() => syncEgressFromConfig(config));
			}
		} else {
			syncedForOpen = false;
		}
	});

	function syncEgressFromConfig(c: XrayConfig | null) {
		if (!c) return;
		egressAllowLan = Boolean(c.allow_lan);
		const mode =
			c.outbound_mode ||
			(c.outbound_socks_port > 0 ? 'socks' : c.outbound_interface ? 'interface' : 'direct');
		if (mode === 'router') {
			egressMode = 'router';
			egressInterface = '';
		} else if (mode === 'socks') {
			egressMode = 'socks';
			egressSocksPort = c.outbound_socks_port > 0 ? c.outbound_socks_port : 1099;
			egressInterface = '';
		} else if (mode === 'interface' || mode === 'outbound') {
			egressMode = 'interface';
			egressInterface = c.outbound_interface || '';
			if (egressInterface && tunnelInterfaces.length > 0) {
				customEgressIface = !tunnelInterfaces.some((t) => t.iface === egressInterface);
			}
		} else {
			egressMode = 'direct';
			egressInterface = '';
		}
	}

	function selectEgressMode(mode: 'direct' | 'router' | 'interface' | 'socks') {
		egressMode = mode;
		if (mode === 'interface' && !egressInterface && tunnelInterfaces.length > 0) {
			egressInterface = tunnelInterfaces[0].iface || '';
			customEgressIface = false;
		}
		if (mode === 'socks' && (!egressSocksPort || egressSocksPort <= 0)) {
			egressSocksPort = 1099;
		}
	}

	async function saveEgress() {
		if (!config) return;
		if (egressMode === 'interface' && !egressInterface.trim()) {
			notifications.error(m.servers_xray_egress_pick_tunnel());
			return;
		}
		egressSaving = true;
		try {
			const payload: Partial<XrayConfig> = {
				...config,
				outbound_mode: egressMode,
				outbound_interface: egressMode === 'interface' ? egressInterface.trim() : '',
				outbound_socks_port: egressMode === 'socks' ? (egressSocksPort || 1099) : 0,
				allow_lan: egressAllowLan,
			};
			const saved = await api.updateXrayServerConfig(payload);
			notifications.success(m.servers_xray_egress_saved());
			open = false;
			await onsaved(saved);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.servers_xray_egress_save_failed());
		} finally {
			egressSaving = false;
		}
	}
</script>

<Modal bind:open title={m.servers_xray_egress_title()} onclose={() => (open = false)} size="md">
	<div class="egress-modal-body">
		<p class="field-hint" style="margin-bottom: 4px;">
			{m.servers_xray_egress_intro()}
		</p>

		{#if hasLegacySocksEgress}
			<div class="alert-banner warning" style="margin-bottom: 4px;">
				<AlertTriangle size={16} class="banner-icon banner-icon-warn" />
				<div class="banner-content">
					<div class="banner-title">{EXTERNAL_ACCESS_LABELS.egressLegacyWarning}</div>
					<div class="banner-desc">
						{m.servers_xray_egress_legacy_desc()}
					</div>
				</div>
			</div>
		{/if}

		<div class="preset-tabs">
			<button
				type="button"
				class="preset-tab"
				class:active={egressMode === 'direct'}
				onclick={() => selectEgressMode('direct')}
			>
				<div class="preset-name">{EXTERNAL_ACCESS_LABELS.egressDirect}</div>
				<div class="preset-desc">{m.servers_xray_egress_direct_desc()}</div>
			</button>
			<button
				type="button"
				class="preset-tab"
				class:active={egressMode === 'router'}
				disabled={$fircForeign !== null}
				onclick={() => selectEgressMode('router')}
			>
				<div class="preset-name">{EXTERNAL_ACCESS_LABELS.egressFirc}</div>
				<div class="preset-desc">{$fircForeign ? m.firc_foreign_egress() : m.servers_xray_egress_router_desc()}</div>
			</button>
			<button
				type="button"
				class="preset-tab"
				class:active={egressMode === 'interface'}
				onclick={() => selectEgressMode('interface')}
			>
				<div class="preset-name">{EXTERNAL_ACCESS_LABELS.egressTunnel}</div>
				<div class="preset-desc">{m.servers_xray_egress_tunnel_desc()}</div>
			</button>
		</div>

		{#if egressMode === 'interface'}
			<div class="egress-detail-box">
				<label class="form-field">
					<span class="field-label">{m.servers_xray_egress_pick_tunnel_label()}</span>
					<select
						class="select-input"
						value={customEgressIface ? '__custom__' : egressInterface}
						onchange={(e) => {
							const val = (e.currentTarget as HTMLSelectElement).value;
							if (val === '__custom__') {
								customEgressIface = true;
							} else {
								customEgressIface = false;
								egressInterface = val;
							}
						}}
					>
						<option value="" disabled>{m.servers_xray_egress_pick_tunnel_placeholder()}</option>
						{#each tunnelInterfaces as opt (opt.id)}
							<option value={opt.iface}>
								{cleanTunnelDisplayName(opt.name)}{opt.available ? '' : unavailableSuffix()}
							</option>
						{/each}
						<option value="__custom__">{m.servers_xray_egress_custom_iface()}</option>
					</select>
				</label>

				{#if currentEgressUnavailable && egressInterface === config?.outbound_interface}
					<div class="field-warn">
						{m.servers_xray_egress_current_unavailable()}
					</div>
				{/if}

				{#if customEgressIface || tunnelInterfaces.length === 0}
					<label class="form-field" style="margin-top: 8px;">
						<span class="field-label">{m.servers_xray_egress_iface_label()}</span>
						<input
							type="text"
							bind:value={egressInterface}
							placeholder={m.servers_xray_egress_iface_placeholder()}
							class="text-input font-mono"
						/>
					</label>
				{/if}
			</div>
		{/if}

		<label class="lan-access-toggle">
			<input type="checkbox" class="checkbox-input" bind:checked={egressAllowLan} />
			<div class="lan-access-text">
				<span class="lan-access-title">{m.servers_xray_lan_title()}</span>
				<span class="lan-access-desc">{m.servers_xray_lan_desc()}</span>
			</div>
		</label>
	</div>

	{#snippet actions()}
		<Button variant="secondary" onclick={() => (open = false)}>{m.common_cancel()}</Button>
		<Button variant="primary" onclick={saveEgress} disabled={egressSaving || (egressMode === 'router' && $fircForeign !== null)}>
			{egressSaving ? m.common_saving() : m.common_save()}
		</Button>
	{/snippet}
</Modal>

<style>
	.egress-modal-body {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}

	.form-field {
		display: flex;
		flex-direction: column;
		justify-content: space-between;
		gap: 4px;
	}

	.field-label {
		font-size: 12px;
		font-weight: 500;
		color: var(--color-text-primary);
	}

	.text-input,
	.select-input {
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 7px 10px;
		font-size: 13px;
		color: var(--color-text-primary);
		outline: none;
	}
	.text-input:focus,
	.select-input:focus {
		border-color: var(--color-accent);
	}

	.field-hint {
		font-size: 11.5px;
		color: var(--color-text-muted);
		margin: 0;
	}

	.field-warn {
		font-size: 12px;
		color: var(--color-warning);
		margin-top: 6px;
	}

	.alert-banner {
		display: flex;
		align-items: flex-start;
		gap: 10px;
		padding: 10px 14px;
		border-radius: var(--radius-sm);
		font-size: 12.5px;
	}
	.alert-banner.warning {
		background: var(--color-warning-tint);
		border: 1px solid var(--color-warning-border);
		color: var(--color-text-primary);
	}
	.banner-content {
		display: flex;
		flex-direction: column;
		gap: 4px;
		flex: 1;
	}
	.banner-title {
		font-weight: 600;
	}
	.banner-desc {
		color: var(--color-text-muted);
		font-size: 12px;
		line-height: 1.4;
	}

	.font-mono {
		font-family: var(--font-mono);
	}

	.preset-tabs {
		display: grid;
		grid-template-columns: repeat(3, 1fr);
		gap: 8px;
	}

	.preset-tab {
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 8px 10px;
		text-align: left;
		cursor: pointer;
		transition: all var(--t-fast) ease;
		display: flex;
		flex-direction: column;
		gap: 3px;
	}
	.preset-tab:disabled {
		cursor: default;
		opacity: 0.6;
	}
	.preset-tab:hover:not(:disabled) {
		border-color: var(--color-accent);
	}
	.preset-tab.active {
		border-color: var(--color-accent);
		background: var(--color-accent-tint, rgba(99, 102, 241, 0.08));
	}

	.preset-name {
		font-size: 12px;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.preset-desc {
		font-size: 11px;
		color: var(--color-text-muted);
		line-height: 1.35;
	}

	.egress-detail-box {
		padding-top: 8px;
		border-top: 1px solid var(--color-border);
	}

	.lan-access-toggle {
		display: flex;
		align-items: flex-start;
		gap: 10px;
		margin-top: 10px;
		padding: 10px 12px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		cursor: pointer;
	}

	.lan-access-text {
		display: flex;
		flex-direction: column;
		gap: 2px;
	}

	.lan-access-title {
		font-size: 12.5px;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.lan-access-desc {
		font-size: 11.5px;
		color: var(--color-text-muted);
		line-height: 1.35;
	}

	.checkbox-input {
		width: 16px;
		height: 16px;
		accent-color: var(--color-accent);
		cursor: pointer;
	}

	@media (max-width: 640px) {
		.preset-tabs {
			grid-template-columns: 1fr;
		}
	}
</style>
