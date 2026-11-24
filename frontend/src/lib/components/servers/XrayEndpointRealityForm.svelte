<script lang="ts">
	import { api } from '$lib/api/client';
	import { m } from '$lib/i18n';
	import { notifications } from '$lib/stores/notifications';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import { Button } from '$lib/components/ui';
	import type { XrayRealityState, XrayRealityTransport } from '$lib/types/xray';
	import RealityFields from './reality/RealityFields.svelte';
	import { Copy, KeyRound, RotateCw, Zap } from 'lucide-svelte';

	// Форма маршрута Reality в окне настройки маршрута: поля REALITY, выбор
	// транспорта и ключи (копирование pbk, перевыпуск).
	interface Props {
		realityState: XrayRealityState | null;
		port: number;
		transport: XrayRealityTransport;
		dest: string;
		serverName: string;
		publicHost: string;
		/** Ключи перевыпущены — владелец обновляет зависящее от ссылок. */
		onkeyschanged: () => void;
	}

	let {
		realityState = $bindable(null),
		port = $bindable(2053),
		transport = $bindable('tcp'),
		dest = $bindable(''),
		serverName = $bindable(''),
		publicHost = $bindable(''),
		onkeyschanged,
	}: Props = $props();

	let realityRegenerating = $state(false);

	async function copyPbk() {
		if (await copyToClipboard(realityState?.settings?.public_key || '')) {
			notifications.success(m.servers_xray_reality_pbk_copied());
		}
	}

	async function handleRegenerateRealityKeys() {
		if (!confirm(m.servers_xray_reality_regen_confirm())) {
			return;
		}
		realityRegenerating = true;
		try {
			realityState = await api.updateXrayReality({ regenerate_keys: true });
			notifications.success(m.servers_xray_reality_regen_done());
			onkeyschanged();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : m.servers_xray_reality_regen_failed());
		} finally {
			realityRegenerating = false;
		}
	}
</script>

<div class="settings-form">
	<RealityFields
		rs={realityState}
		bind:port
		bind:transport
		bind:dest
		bind:serverName
		bind:publicHost
		showTransport={false}
		surface="modal"
		onrefreshed={(fresh) => (realityState = fresh)}
	/>

	<div class="tuning-section">
		<div class="tuning-header">
			<div class="tuning-title-group">
				<div class="tuning-title">
					<Zap size={15} class="banner-icon-warn" />
					<span>{m.servers_xray_reality_protocol_title()}</span>
				</div>
				<span class="tuning-badge font-mono">
					{m.servers_xray_reality_protocol_badge({ transport: transport === 'xhttp' ? 'XHTTP (stream-one)' : 'TCP (raw)', port })}
				</span>
			</div>
		</div>

		<div class="preset-tabs preset-tabs-2">
			<button
				type="button"
				class="preset-tab"
				class:active={transport === 'tcp'}
				onclick={() => (transport = 'tcp')}
			>
				<div class="preset-name">{m.servers_xray_reality_tcp_fast()}</div>
				<div class="preset-desc">{m.servers_xray_reality_tcp_fast_desc()}</div>
			</button>
			<button
				type="button"
				class="preset-tab"
				class:active={transport === 'xhttp'}
				onclick={() => (transport = 'xhttp')}
			>
				<div class="preset-name">XHTTP (stream-one) · HTTP/2</div>
				<div class="preset-desc">{m.servers_xray_reality_xhttp_h2_desc()}</div>
			</button>
		</div>

		{#if realityState?.settings?.public_key}
			<div class="reality-keys-box">
				<div class="reality-key-row">
					<KeyRound size={14} class="summary-icon" />
					<span class="field-label">Public key (pbk):</span>
					<code class="checklist-code font-mono">{realityState.settings.public_key}</code>
					<Button variant="secondary" size="sm" onclick={() => void copyPbk()}>
						{#snippet iconBefore()}<Copy size={12} />{/snippet}
						{m.common_copy()}
					</Button>
					<Button
						variant="secondary"
						size="sm"
						onclick={() => void handleRegenerateRealityKeys()}
						disabled={realityRegenerating}
					>
						<RotateCw size={13} class="mr-1" />
						{realityRegenerating ? m.servers_xray_updating() : m.servers_xray_reality_change_keys()}
					</Button>
				</div>
				{#if realityState.settings.short_ids && realityState.settings.short_ids.length > 0}
					<div class="reality-key-row">
						<span class="field-label">Short ID (sid):</span>
						<code class="checklist-code font-mono">{realityState.settings.short_ids[0]}</code>
					</div>
				{/if}
			</div>
		{/if}
	</div>
</div>

<style>
	.settings-form {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}

	.field-label {
		font-size: 12px;
		font-weight: 500;
		color: var(--color-text-primary);
	}

	.font-mono {
		font-family: var(--font-mono);
	}

	.tuning-section {
		display: flex;
		flex-direction: column;
		gap: 12px;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 12px;
	}

	.tuning-header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 10px;
		flex-wrap: wrap;
	}

	.tuning-title-group {
		display: flex;
		align-items: center;
		gap: 8px;
		flex-wrap: wrap;
	}

	.tuning-title {
		display: flex;
		align-items: center;
		gap: 6px;
		font-size: 13px;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.tuning-badge {
		font-size: 10px;
		padding: 2px 6px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		color: var(--color-accent);
	}

	.preset-tabs {
		display: grid;
		grid-template-columns: repeat(3, 1fr);
		gap: 8px;
	}

	.preset-tabs-2 {
		grid-template-columns: repeat(2, 1fr);
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
	.preset-tab:hover {
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

	.reality-keys-box {
		display: flex;
		flex-direction: column;
		gap: 6px;
		padding-top: 8px;
		border-top: 1px solid var(--color-border);
	}

	.reality-key-row {
		display: flex;
		align-items: center;
		gap: 8px;
		flex-wrap: wrap;
		font-size: 12px;
	}

	.checklist-code {
		padding: 2px 6px;
		border-radius: var(--radius-sm);
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		color: var(--color-text-primary);
		font-size: 11px;
		word-break: break-all;
	}

	.preset-desc {
		font-size: 11px;
		color: var(--color-text-muted);
		line-height: 1.35;
	}

	@media (max-width: 640px) {
		.preset-tabs {
			grid-template-columns: 1fr;
		}
	}
</style>
