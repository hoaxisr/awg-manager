<script lang="ts">
	import { untrack } from 'svelte';
	import { api } from '$lib/api/client';
	import { m } from '$lib/i18n';
	import { notifications } from '$lib/stores/notifications';
	import { Button, Modal } from '$lib/components/ui';
	import type { XrayConfig, XrayStatus, XrayEndpointConfig } from '$lib/types';
	import ConnectionChecks from './wizard/ConnectionChecks.svelte';
	import { formatDomainList } from './external-access/externalAccessModel';
	import { ExternalLink, Sparkles, Trash2 } from 'lucide-svelte';

	// Окно «Настройки и диагностика VLESS»: системные порты и проверки.
	// Форма заполняется из конфига при каждом открытии.
	interface Props {
		open: boolean;
		settingsTab: 'general' | 'diagnostics';
		config: XrayConfig | null;
		status: XrayStatus | null;
		endpoints: XrayEndpointConfig[];
		shortEgressSummary: string;
		cardFingerprint: string;
		onOpenDispatcher?: () => void;
		onopenwizard: () => void;
		onreset: () => void;
		/** Новый конфиг после сохранения; владелец перечитывает данные. */
		onsaved: (c: XrayConfig) => Promise<void> | void;
	}

	let {
		open = $bindable(false),
		settingsTab = $bindable('general'),
		config,
		status,
		endpoints,
		shortEgressSummary,
		cardFingerprint,
		onOpenDispatcher,
		onopenwizard,
		onreset,
		onsaved,
	}: Props = $props();

	let editConfig = $state<Partial<XrayConfig>>({});
	let saveConfigLoading = $state(false);
	// Намеренно не $state: защёлка «форма уже заполнена для этого открытия»
	// читается и пишется только внутри эффекта ниже; реактивная заставила бы
	// эффект перезапускаться от собственной записи.
	let syncedForOpen = false;

	$effect(() => {
		if (open) {
			if (!syncedForOpen) {
				syncedForOpen = true;
				untrack(() => {
					editConfig = config
						? {
								...config,
								outbound_mode:
									config.outbound_mode ||
									(config.outbound_socks_port > 0
										? 'socks'
										: config.outbound_interface
											? 'interface'
											: 'direct'),
							}
						: {};
				});
			}
		} else {
			syncedForOpen = false;
		}
	});

	async function saveSettings() {
		saveConfigLoading = true;
		try {
			if (editConfig.outbound_mode === 'direct' || editConfig.outbound_mode === 'router') {
				editConfig.outbound_interface = '';
				editConfig.outbound_socks_port = 0;
			} else if (editConfig.outbound_mode === 'socks') {
				editConfig.outbound_interface = '';
				if (!editConfig.outbound_socks_port || editConfig.outbound_socks_port <= 0) {
					editConfig.outbound_socks_port = 1099;
				}
			} else if (editConfig.outbound_mode === 'interface') {
				editConfig.outbound_socks_port = 0;
			}
			const saved = await api.updateXrayServerConfig(editConfig);
			notifications.success(m.servers_xray_settings_saved());
			open = false;
			await onsaved(saved);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.servers_xray_settings_save_failed());
		} finally {
			saveConfigLoading = false;
		}
	}
</script>

<Modal bind:open title={m.servers_xray_settings_title()} onclose={() => (open = false)} size="lg">
	<div class="settings-modal-wrap">
		<!-- Top wizard & dispatcher banner -->
		<div class="wizard-launch-bar">
			<div class="wizard-launch-info">
				<span class="wizard-launch-title">{m.servers_xray_wizard_launch_title()}</span>
				<span class="wizard-launch-sub">{m.servers_xray_wizard_launch_sub()}</span>
			</div>
			<div class="wizard-launch-actions">
				{#if onOpenDispatcher}
					<Button
						variant="secondary"
						size="sm"
						onclick={() => {
							open = false;
							onOpenDispatcher?.();
						}}
					>
						<ExternalLink size={13} class="mr-1" />
						{m.servers_xray_incoming_connections()}
					</Button>
				{/if}
				<Button
					variant="primary"
					size="sm"
					onclick={() => {
						open = false;
						onopenwizard();
					}}
				>
					<Sparkles size={13} class="mr-1" />
					{m.servers_xray_open_wizard()}
				</Button>
			</div>
		</div>

		<!-- Modal Tabs -->
		<div class="modal-tabs">
			<button
				type="button"
				class="modal-tab"
				class:active={settingsTab === 'general'}
				onclick={() => (settingsTab = 'general')}
			>
				{m.servers_xray_settings_tab_general()}
			</button>
			<button
				type="button"
				class="modal-tab"
				class:active={settingsTab === 'diagnostics'}
				onclick={() => (settingsTab = 'diagnostics')}
			>
				{m.servers_xray_settings_tab_diagnostics()}
			</button>
		</div>

		{#if settingsTab === 'general'}
			<div class="settings-form">
				<div class="cdn-strip">
					<div class="strip-item">
						<span class="strip-label">{m.servers_xray_strip_active_routes()}</span>
						<span class="strip-val font-mono">
							{m.servers_xray_strip_active_routes_val({ on: endpoints.filter((e) => e.enabled).length, total: endpoints.length, domains: config?.public_domain ? formatDomainList(config.public_domain) : m.servers_xray_none() })}
						</span>
					</div>
					<div class="strip-divider"></div>
					<div class="strip-item">
						<span class="strip-label">{m.servers_xray_strip_internal_route()}</span>
						<span class="strip-val font-mono">KeenDNS / CDN &rarr; :{config?.dispatcher_port || 9009} &rarr; :{config?.listen_port || 9008}</span>
					</div>
					<div class="strip-divider"></div>
					<div class="strip-item">
						<span class="strip-label">{m.servers_xray_strip_egress()}</span>
						<span class="strip-val">{shortEgressSummary}</span>
					</div>
					<div class="strip-divider"></div>
					<div class="strip-item">
						<span class="strip-label">{m.servers_xray_strip_core()}</span>
						<span class="strip-val font-mono">
							{status?.backend || 'awgm-xhttpd'}{status?.version ? ` (${status.version})` : ''}{status?.binary_path ? ` · ${status.binary_path}` : ''}
						</span>
					</div>
				</div>

				<p class="field-hint">
					{m.servers_xray_settings_hint_before()}<strong>«{m.servers_xray_routes_title()}»</strong>{m.servers_xray_settings_hint_after()}
				</p>

				<div class="form-row">
					<label class="form-field">
						<span class="field-label">{m.servers_xray_listen_port_label()}</span>
						<input type="number" bind:value={editConfig.listen_port} class="text-input font-mono" />
					</label>
					<label class="form-field">
						<span class="field-label">{m.servers_xray_dispatcher_port_label()}</span>
						<input type="number" bind:value={editConfig.dispatcher_port} class="text-input font-mono" />
					</label>
				</div>

				<div class="reset-zone">
					<div class="reset-zone-info">
						<div class="reset-zone-title">{m.server_reset_zone_title()}</div>
						<p class="reset-zone-desc">{m.server_reset_vless_zone_desc()}</p>
					</div>
					<Button variant="danger" size="sm" onclick={onreset}>
						<Trash2 size={14} class="mr-1" />
						{m.server_reset_button()}
					</Button>
				</div>
			</div>
		{:else if settingsTab === 'diagnostics'}
			<ConnectionChecks kind="xray" currentFingerprint={cardFingerprint} autoRun={true} />
		{/if}
	</div>

	{#snippet actions()}
		<Button variant="secondary" onclick={() => (open = false)}>{m.common_close()}</Button>
		{#if settingsTab === 'general'}
			<Button variant="primary" onclick={saveSettings} disabled={saveConfigLoading}>
				{saveConfigLoading ? m.common_saving() : m.servers_xray_save_ports()}
			</Button>
		{/if}
	{/snippet}
</Modal>

<style>
	/* Settings & Egress Modals */
	.settings-modal-wrap {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}

	.wizard-launch-bar {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 10px;
		padding: 10px 12px;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		flex-wrap: wrap;
	}

	.wizard-launch-info {
		display: flex;
		flex-direction: column;
		gap: 2px;
	}

	.wizard-launch-title {
		font-size: 13px;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.wizard-launch-sub {
		font-size: 11.5px;
		color: var(--color-text-muted);
	}

	.wizard-launch-actions {
		display: flex;
		align-items: center;
		gap: 6px;
		flex-wrap: wrap;
	}

	.modal-tabs {
		display: flex;
		gap: 6px;
		border-bottom: 1px solid var(--color-border);
		padding-bottom: 8px;
		flex-wrap: wrap;
	}

	.modal-tab {
		padding: 6px 12px;
		border-radius: var(--radius-sm);
		border: 1px solid transparent;
		background: transparent;
		color: var(--color-text-secondary);
		font-size: 12.5px;
		cursor: pointer;
	}
	.modal-tab.active {
		background: var(--color-bg-primary);
		border-color: var(--color-border);
		color: var(--color-text-primary);
		font-weight: 600;
	}

	.cdn-strip {
		display: flex;
		align-items: center;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 8px 12px;
		flex-wrap: wrap;
		gap: 10px;
	}

	.strip-item {
		display: flex;
		flex-direction: column;
		gap: 2px;
		min-width: 0;
	}

	.strip-label {
		font-size: 10px;
		text-transform: uppercase;
		color: var(--color-text-muted);
		letter-spacing: 0.04em;
	}

	.strip-val {
		font-size: 12px;
		font-weight: 500;
		color: var(--color-text-primary);
		overflow-wrap: anywhere;
		word-break: break-word;
	}

	.strip-divider {
		width: 1px;
		height: 22px;
		background: var(--color-border);
	}

	.settings-form {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}

	.form-row {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 10px;
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

	.text-input {
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 7px 10px;
		font-size: 13px;
		color: var(--color-text-primary);
		outline: none;
	}
	.text-input:focus {
		border-color: var(--color-accent);
	}

	.field-hint {
		font-size: 11.5px;
		color: var(--color-text-muted);
		margin: 0;
	}

	.font-mono {
		font-family: var(--font-mono);
	}

	.reset-zone {
		margin-top: 10px;
		padding: 12px 14px;
		border: 1px solid color-mix(in srgb, var(--color-error) 35%, var(--color-border));
		border-radius: var(--radius-sm);
		background: color-mix(in srgb, var(--color-error) 6%, var(--color-bg-secondary));
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 12px;
		flex-wrap: wrap;
	}

	.reset-zone-info {
		display: flex;
		flex-direction: column;
		gap: 3px;
		flex: 1 1 260px;
	}

	.reset-zone-title {
		font-size: 13px;
		font-weight: 600;
		color: var(--color-error);
	}

	.reset-zone-desc {
		margin: 0;
		font-size: 12px;
		color: var(--color-text-secondary);
		line-height: 1.4;
	}

	@media (max-width: 640px) {
		.form-row {
			grid-template-columns: 1fr;
		}
		.cdn-strip {
			flex-direction: column;
			align-items: flex-start;
		}
		.strip-divider {
			display: none;
		}
	}
</style>
