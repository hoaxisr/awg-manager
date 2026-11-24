<script lang="ts">
	// Колонка ссылок окна «Подключить Telegram»: веб-мост или прямой MTProto.
	import { m } from '$lib/i18n';
	import type {
		TgWebProxyConfig,
		TgWebProxyRevealData,
		TgUserRevealData,
		WebProxyLink,
	} from '$lib/types';
	import { Button, IconButton } from '$lib/components/ui';
	import { formatTelegramCarrierLabel } from '../external-access/connectionLabels';
	import TgProxySecretEditor from './TgProxySecretEditor.svelte';
	import { cleanEndpointDescription, getPresetMeta, inferEndpointPreset } from './tgProxyModel';
	import { Copy, Send } from 'lucide-svelte';

	interface Props {
		shareTab: 'webproxy' | 'mtproxy';
		revealData: TgWebProxyRevealData;
		activeUser: TgUserRevealData | TgWebProxyRevealData | null;
		activeEp: WebProxyLink | null;
		selectedWebProxyId: string;
		config: TgWebProxyConfig | null;
		secretInput: string;
		secretSaving: boolean;
		onselectendpoint: (id: string) => void;
		onregenerate: () => void;
		onsavesecret: () => void;
		oncopy: (text: string, label: string) => void;
	}

	let {
		shareTab,
		revealData,
		activeUser,
		activeEp,
		selectedWebProxyId,
		config,
		secretInput = $bindable(''),
		secretSaving,
		onselectendpoint,
		onregenerate,
		onsavesecret,
		oncopy,
	}: Props = $props();
</script>

<div class="reveal-links-col">
	{#if shareTab === 'webproxy'}
		{@const activeTgUrl = activeEp ? activeEp.tg_url : activeUser?.tg_url || ''}
		{@const activeTmeUrl = activeEp ? activeEp.tme_url : activeUser?.tme_url || ''}
		{@const activeBridgeUrl = activeEp ? activeEp.bridge_url : activeUser?.bridge_url || ''}
		{@const activeWebProxies = activeUser?.web_proxies || revealData.web_proxies || []}

		<div class="honest-note">
			{m.servers_tg_web_honest_note()}
		</div>

		{#if activeWebProxies.length > 1}
			<div class="ep-selector-box">
				<span class="ep-selector-label">{m.servers_tg_pick_domain()}</span>
				<div class="ep-chips">
					{#each activeWebProxies as ep (ep.id)}
						{@const epMeta = getPresetMeta(ep.preset || inferEndpointPreset(ep, config))}
						<button
							type="button"
							class="ep-chip"
							class:active={(selectedWebProxyId || activeWebProxies[0].id) === ep.id}
							onclick={() => onselectendpoint(ep.id)}
						>
							<span class="ep-chip-host font-mono">{ep.hostname}</span>
							<span class="badge {epMeta.badgeClass} text-[10px] ml-1">{epMeta.badge()}</span>
							{#if ep.description}
								<span class="text-muted text-[11px] ml-1">({cleanEndpointDescription(ep.description)})</span>
							{/if}
						</button>
					{/each}
				</div>
			</div>
		{/if}

		<div class="link-block">
			<div class="link-header">
				<span class="link-label">{m.servers_tg_link_tg_webproxy()}</span>
			</div>
			<div class="input-with-actions">
				<input type="text" readonly value={activeTgUrl} class="link-input font-mono" />
				<Button variant="primary" size="sm" onclick={() => oncopy(activeTgUrl, m.servers_tg_label_link())}>
					<Copy size={14} class="mr-1" />
					{m.common_copy()}
				</Button>
				<Button variant="primary" size="sm" href={activeTgUrl}>
					<Send size={14} class="mr-1" />
					{m.servers_tg_open()}
				</Button>
			</div>
		</div>

		<div class="link-block">
			<div class="link-header">
				<span class="link-label">{m.servers_tg_link_tme_webproxy()}</span>
			</div>
			<div class="input-with-actions">
				<input type="text" readonly value={activeTmeUrl} class="link-input font-mono" />
				<Button variant="secondary" size="sm" onclick={() => oncopy(activeTmeUrl, m.servers_tg_label_web_link())}>
					<Copy size={14} class="mr-1" />
					{m.common_copy()}
				</Button>
			</div>
		</div>

		<div class="link-block">
			<div class="link-header">
				<span class="link-label">{m.servers_tg_link_bridge({ carrier: formatTelegramCarrierLabel(activeEp?.carrier_mode || config?.carrier_mode) })}</span>
			</div>
			<div class="input-with-actions">
				<input type="text" readonly value={activeBridgeUrl} class="link-input font-mono" />
				<Button variant="secondary" size="sm" onclick={() => oncopy(activeBridgeUrl, m.servers_tg_label_bridge())}>
					<Copy size={14} class="mr-1" />
					{m.common_copy()}
				</Button>
			</div>
		</div>

		<div class="manual-box">
			<div class="manual-item manual-item-wide">
				<TgProxySecretEditor bind:value={secretInput} saving={secretSaving} {onregenerate} onsave={onsavesecret} />
			</div>
		</div>
	{:else}
		<!-- Direct MTProxy Share -->
		<div class="link-block">
			<div class="link-header">
				<span class="link-label">{m.servers_tg_link_mtproxy()}</span>
			</div>
			<div class="input-with-actions">
				<input type="text" readonly value={activeUser?.mtproxy_url || ''} class="link-input font-mono" />
				<Button variant="primary" size="sm" onclick={() => oncopy(activeUser?.mtproxy_url || '', m.servers_tg_label_mtproxy_link())}>
					<Copy size={14} class="mr-1" />
					{m.common_copy()}
				</Button>
				<Button variant="primary" size="sm" href={activeUser?.mtproxy_url || ''}>
					<Send size={14} class="mr-1" />
					{m.servers_tg_open()}
				</Button>
			</div>
		</div>

		<div class="link-block">
			<div class="link-header">
				<span class="link-label">{m.servers_tg_link_mtproxy_tme()}</span>
			</div>
			<div class="input-with-actions">
				<input type="text" readonly value={activeUser?.mtproxy_tme_url || ''} class="link-input font-mono" />
				<Button variant="secondary" size="sm" onclick={() => oncopy(activeUser?.mtproxy_tme_url || '', m.servers_tg_label_tme_link())}>
					<Copy size={14} class="mr-1" />
					{m.common_copy()}
				</Button>
			</div>
		</div>

		<div class="manual-box">
			<div class="manual-grid">
				<div class="manual-item">
					<span class="m-label">{m.servers_tg_label_server_colon()}</span>
					<div class="m-val-row">
						<code class="m-code">{revealData.direct_host}</code>
						<IconButton ariaLabel={m.common_copy()} onclick={() => oncopy(revealData.direct_host || '', m.servers_tg_label_server())}>
							<Copy size={12} />
						</IconButton>
					</div>
				</div>
				<div class="manual-item">
					<span class="m-label">{m.servers_tg_label_port_colon()}</span>
					<div class="m-val-row">
						<code class="m-code">{revealData.direct_port}</code>
						<IconButton ariaLabel={m.common_copy()} onclick={() => oncopy(String(revealData.direct_port || 8443), m.servers_tg_label_port())}>
							<Copy size={12} />
						</IconButton>
					</div>
				</div>
				<div class="manual-item manual-item-wide">
					<TgProxySecretEditor bind:value={secretInput} saving={secretSaving} {onregenerate} onsave={onsavesecret} />
				</div>
				<div class="manual-item manual-item-wide">
					<span class="m-label">{m.servers_tg_label_secret_colon()}</span>
					<div class="m-val-row">
						<code class="m-code font-mono text-xs">{activeUser?.mtproxy_secret || revealData.mtproxy_secret}</code>
						<IconButton
							ariaLabel={m.common_copy()}
							onclick={() => oncopy(activeUser?.mtproxy_secret || revealData.mtproxy_secret || '', m.servers_tg_label_secret())}
						>
							<Copy size={12} />
						</IconButton>
					</div>
				</div>
			</div>
		</div>
	{/if}
</div>

<style>
	.reveal-links-col {
		display: flex;
		flex-direction: column;
		gap: 0.875rem;
	}

	.honest-note {
		font-size: 11.5px;
		color: var(--color-text-secondary);
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 8px 10px;
		line-height: 1.4;
	}

	.ep-selector-box {
		display: flex;
		flex-direction: column;
		gap: 6px;
		background: var(--color-bg-primary);
		padding: 10px 12px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
	}

	.ep-selector-label {
		font-size: 11px;
		font-weight: 500;
		color: var(--color-text-muted);
		text-transform: uppercase;
		letter-spacing: 0.05em;
	}

	.ep-chips {
		display: flex;
		flex-wrap: wrap;
		gap: 6px;
	}

	.ep-chip {
		display: inline-flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 4px;
		padding: 4px 10px;
		border-radius: var(--radius-sm);
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		color: var(--color-text-primary);
		font-size: 12px;
		cursor: pointer;
		text-align: left;
		max-width: 100%;
		overflow-wrap: anywhere;
		word-break: break-word;
	}
	.ep-chip.active {
		background: var(--color-accent-tint);
		border-color: var(--color-accent-border);
		color: var(--color-accent);
		font-weight: 600;
	}

	.badge {
		font-size: 11px;
		padding: 2px 8px;
		border-radius: var(--radius-sm);
		font-weight: 500;
	}
	.badge-success {
		background: var(--color-success-tint);
		border: 1px solid var(--color-success-border);
		color: var(--color-success);
	}
	.badge-neutral {
		background: var(--color-bg-tertiary);
		color: var(--color-text-muted);
	}
	.badge-info {
		background: var(--color-info-tint);
		border: 1px solid var(--color-info-border);
		color: var(--color-info);
	}
	.badge-purple {
		background: var(--color-purple-tint);
		border: 1px solid rgba(187, 154, 247, 0.25);
		color: var(--color-purple);
	}

	.ml-1 {
		margin-left: 4px;
	}

	.link-block {
		display: flex;
		flex-direction: column;
		gap: 4px;
	}

	.link-header {
		display: flex;
		align-items: center;
	}

	.link-label {
		font-size: 12px;
		font-weight: 500;
		color: var(--color-text-secondary);
	}

	.input-with-actions {
		display: flex;
		gap: 6px;
		flex-wrap: wrap;
	}

	.link-input {
		flex: 1;
		min-width: 160px;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 5px 8px;
		font-size: 12px;
		color: var(--color-text-primary);
		outline: none;
	}

	.manual-box {
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 0.75rem;
	}

	.manual-grid {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 8px;
	}

	.manual-item-wide {
		grid-column: span 2;
	}

	.m-label {
		font-size: 11px;
		color: var(--color-text-muted);
		display: block;
		margin-bottom: 2px;
	}

	.m-val-row {
		display: flex;
		align-items: center;
		gap: 6px;
	}

	.m-code {
		font-size: 12px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		padding: 2px 6px;
		border-radius: var(--radius-sm);
		flex: 1;
		word-break: break-all;
	}

	.font-mono {
		font-family: var(--font-mono);
	}
</style>
