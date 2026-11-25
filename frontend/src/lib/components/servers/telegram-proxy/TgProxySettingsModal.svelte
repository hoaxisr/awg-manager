<script lang="ts">
	// «Настройки и диагностика Telegram»: черновик настроек и его сохранение
	// живут здесь; владелец получает сохранённый конфиг через onsaved.
	import { m } from '$lib/i18n';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import type { TgWebProxyConfig, TgWebProxyStatus, TelegramUpstream, WebProxyEndpoint } from '$lib/types';
	import type { KeenDNSDiscovery } from '$lib/types/serverWizard';
	import { Button, Modal } from '$lib/components/ui';
	import TgProxySettingsGeneral from './TgProxySettingsGeneral.svelte';
	import TgProxySecurityPane from './TgProxySecurityPane.svelte';
	import TgProxyDiagnosticsPane from './TgProxyDiagnosticsPane.svelte';
	import {
		buildEditableUpstreams,
		cleanUpstreams,
		getPresetMeta,
		inferEndpointPreset,
		primaryDirectDevice,
		upstreamsDraftError,
		type EgressChoice,
	} from './tgProxyModel';
	import { ExternalLink, Sparkles } from 'lucide-svelte';

	type SettingsTab = 'general' | 'security' | 'diagnostics';

	interface Props {
		config: TgWebProxyConfig | null;
		status: TgWebProxyStatus | null;
		egressOptions: EgressChoice[];
		keendnsInfo?: KeenDNSDiscovery | null;
		egressSummaryLabel: string;
		cardFingerprint: string;
		onOpenDispatcher?: () => void;
		onopenwizard: () => void;
		onrequestreset: () => void;
		/** Перечитать список выходов с учётом текущего интерфейса. */
		onloadegress: (currentUpstream?: string) => void;
		onsaved: (config: TgWebProxyConfig) => void | Promise<void>;
		onstatus: (status: TgWebProxyStatus, reload: boolean) => void | Promise<void>;
	}

	let {
		config,
		status,
		egressOptions,
		keendnsInfo = null,
		egressSummaryLabel,
		cardFingerprint,
		onOpenDispatcher,
		onopenwizard,
		onrequestreset,
		onloadegress,
		onsaved,
		onstatus,
	}: Props = $props();

	let settingsOpen = $state(false);
	let settingsTab = $state<SettingsTab>('general');
	let editConfig = $state<Partial<TgWebProxyConfig>>({});
	let editUpstreams = $state<TelegramUpstream[]>([]);
	let saveConfigLoading = $state(false);

	export function show(tab: SettingsTab = 'general') {
		settingsTab = tab;
		editConfig = config ? JSON.parse(JSON.stringify(config)) : {};
		if (!editConfig.endpoints || editConfig.endpoints.length === 0) {
			const isCDN =
				editConfig.scenario === 'cdn_http' ||
				editConfig.scenario === 'dual' ||
				(editConfig.public_hostname || '').includes('edge') ||
				(editConfig.public_hostname || '').includes('cdn');
			const ep: WebProxyEndpoint = {
				id: 'ep-1',
				hostname: editConfig.public_hostname || '',
				listen_port: 8084,
				admin_port: 8086,
				description: 'Основной домен', // данные на роутере, не зависят от языка UI
				is_cdn: isCDN,
				enabled: true,
			};
			ep.preset = inferEndpointPreset(ep, editConfig);
			ep.carrier_mode = editConfig.carrier_mode || getPresetMeta(ep.preset).carrier_mode;
			editConfig.endpoints = [ep];
		} else {
			editConfig.endpoints = editConfig.endpoints.map((ep) => {
				const preset = inferEndpointPreset(ep, editConfig);
				const meta = getPresetMeta(preset);
				return {
					...ep,
					preset,
					is_cdn: meta.is_cdn,
					carrier_mode: ep.carrier_mode || meta.carrier_mode,
				};
			});
		}
		if (!editConfig.network_preset && editConfig.endpoints && editConfig.endpoints.length > 0) {
			editConfig.network_preset =
				editConfig.endpoints[0].preset || inferEndpointPreset(editConfig.endpoints[0], editConfig);
		}
		if (!editConfig.upstream_device) {
			editConfig.upstream_device = 'direct';
		}
		editUpstreams = buildEditableUpstreams(editConfig);
		onloadegress(editConfig.upstream_device);
		settingsOpen = true;
	}

	export function close() {
		settingsOpen = false;
	}

	async function saveSettings() {
		const draftError = upstreamsDraftError(editUpstreams);
		if (draftError) {
			notifications.error(draftError);
			return;
		}
		saveConfigLoading = true;
		try {
			const clean = cleanUpstreams(editUpstreams);
			editConfig.upstreams = clean;
			editConfig.upstream_device = primaryDirectDevice(clean);
			if (editConfig.endpoints && editConfig.endpoints.length > 0) {
				editConfig.endpoints = editConfig.endpoints.map((ep) => {
					const p = ep.preset || inferEndpointPreset(ep, editConfig);
					const meta = getPresetMeta(p);
					return {
						...ep,
						preset: p,
						is_cdn: meta.is_cdn,
						carrier_mode: ep.carrier_mode || meta.carrier_mode,
					};
				});
				editConfig.public_hostname = editConfig.endpoints[0].hostname;
				const ep0Preset =
					editConfig.endpoints[0].preset || inferEndpointPreset(editConfig.endpoints[0], editConfig);
				editConfig.network_preset = ep0Preset;
				editConfig.carrier_mode =
					editConfig.endpoints[0].carrier_mode || getPresetMeta(ep0Preset).carrier_mode;
			}
			const saved = await api.updateTgWebProxyConfig(editConfig);
			notifications.success(m.servers_tg_settings_saved());
			settingsOpen = false;
			await onsaved(saved);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.servers_tg_settings_save_error());
		} finally {
			saveConfigLoading = false;
		}
	}
</script>

<Modal bind:open={settingsOpen} title={m.servers_tg_settings_title()} onclose={() => (settingsOpen = false)} size="lg">
	<div class="settings-modal-wrap">
		<!-- Top wizard & dispatcher banner -->
		<div class="wizard-launch-bar">
			<div class="wizard-launch-info">
				<span class="wizard-launch-title">{m.servers_tg_settings_wizard_title()}</span>
				<span class="wizard-launch-sub">{m.servers_tg_settings_wizard_sub()}</span>
			</div>
			<div class="wizard-launch-actions">
				{#if onOpenDispatcher}
					<Button
						variant="secondary"
						size="sm"
						onclick={() => {
							settingsOpen = false;
							onOpenDispatcher?.();
						}}
					>
						<ExternalLink size={13} class="mr-1" />
						{m.servers_tg_settings_dispatcher()}
					</Button>
				{/if}
				<Button
					variant="primary"
					size="sm"
					onclick={() => {
						settingsOpen = false;
						onopenwizard();
					}}
				>
					<Sparkles size={13} class="mr-1" />
					{m.servers_tg_settings_open_wizard()}
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
				{m.servers_tg_settings_tab_general()}
			</button>
			<button
				type="button"
				class="modal-tab"
				class:active={settingsTab === 'security'}
				onclick={() => (settingsTab = 'security')}
			>
				{m.servers_tg_settings_tab_security()}
			</button>
			<button
				type="button"
				class="modal-tab"
				class:active={settingsTab === 'diagnostics'}
				onclick={() => (settingsTab = 'diagnostics')}
			>
				{m.servers_tg_settings_tab_diagnostics()}
			</button>
		</div>

		{#if settingsTab === 'general'}
			<TgProxySettingsGeneral bind:editConfig bind:editUpstreams {egressOptions} {keendnsInfo} {onrequestreset} />
		{:else if settingsTab === 'security'}
			<TgProxySecurityPane {status} onchanged={onstatus} />
		{:else if settingsTab === 'diagnostics'}
			<TgProxyDiagnosticsPane {config} {status} {egressSummaryLabel} {cardFingerprint} />
		{/if}
	</div>

	{#snippet actions()}
		<Button variant="secondary" onclick={() => (settingsOpen = false)}>{m.common_close()}</Button>
		{#if settingsTab === 'general'}
			<Button variant="primary" onclick={saveSettings} disabled={saveConfigLoading}>
				{saveConfigLoading ? m.common_saving() : m.servers_tg_settings_save()}
			</Button>
		{/if}
	{/snippet}
</Modal>

<style>
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
</style>
