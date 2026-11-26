<script lang="ts">
	import type { WizardKind, RevealCredentials } from '$lib/types/serverWizard';
	import { m } from '$lib/i18n';
	import { Button } from '$lib/components/ui';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import { notifications } from '$lib/stores/notifications';
	import { renderCrispQrDataUrl } from '$lib/utils/qrSvg';
	import { Copy, ExternalLink, Check } from 'lucide-svelte';
	import RevealGate from './RevealGate.svelte';
	import QrZoomModal from './QrZoomModal.svelte';
	import CodeExportBlock from './CodeExportBlock.svelte';

	interface Props {
		kind: WizardKind;
		creds: RevealCredentials | null;
		loading?: boolean;
		onreveal: () => void;
	}

	let { kind, creds, loading = false, onreveal }: Props = $props();

	let activeServiceTab = $state<'xray' | 'tgwebproxy'>('xray');
	let effectiveViewKind = $derived<'xray' | 'tgwebproxy'>(
		kind === 'both' ? activeServiceTab : kind
	);
	let activeFormat = $state<'vless' | 'happ' | 'singbox' | 'mihomo'>('vless');
	let qrDataUrl = $state('');
	let qrZoomOpen = $state(false);
	let copiedKey = $state<string | null>(null);

	$effect(() => {
		if (creds) {
			const target =
				effectiveViewKind === 'tgwebproxy'
					? creds.direct_link || creds.web_link || ''
					: creds.vless_url || creds.direct_link || creds.web_link || '';
			if (target) {
				void generateQR(target);
			}
		}
	});

	async function generateQR(text: string) {
		try {
			qrDataUrl = await renderCrispQrDataUrl(text);
		} catch {
			qrDataUrl = '';
		}
	}

	async function copy(text: string, key: string) {
		await copyToClipboard(text);
		copiedKey = key;
		notifications.success(m.common_copied());
		setTimeout(() => {
			if (copiedKey === key) copiedKey = null;
		}, 2000);
	}

</script>

<div class="result-container">
	{#if !creds}
		<RevealGate {loading} {onreveal} />
	{:else}
		{#if kind === 'both'}
			<div class="format-tabs">
				<button
					type="button"
					class="tab-btn"
					class:active={activeServiceTab === 'xray'}
					onclick={() => (activeServiceTab = 'xray')}
				>
					{m.servers_wizard_result_tab_vless()}
				</button>
				<button
					type="button"
					class="tab-btn"
					class:active={activeServiceTab === 'tgwebproxy'}
					onclick={() => (activeServiceTab = 'tgwebproxy')}
				>
					{m.servers_wizard_result_tab_telegram()}
				</button>
			</div>
		{/if}
		<!-- Result content for Telegram Web Proxy -->
		{#if effectiveViewKind === 'tgwebproxy'}
			<div class="result-grid">
				<!-- Left: QR Code & Direct Actions -->
				<div class="qr-panel">
					{#if qrDataUrl}
						<button
							type="button"
							class="qr-wrapper qr-zoom-trigger"
							onclick={() => (qrZoomOpen = true)}
							title={m.servers_wizard_result_qr_zoom_hint()}
						>
							<img src={qrDataUrl} alt="Telegram Proxy QR" class="qr-img" />
						</button>
						<span class="qr-hint">{m.servers_wizard_result_qr_hint_tg()}</span>
					{/if}

					{#if creds.direct_link}
						<Button
							href={creds.direct_link}
							target="_blank"
							rel="noopener noreferrer"
							variant="primary"
							size="md"
							fullWidth
						>
							{#snippet iconBefore()}<ExternalLink size={16} />{/snippet}
							{m.servers_wizard_result_open_telegram()}
						</Button>
					{/if}
				</div>

				<!-- Right: Links & Secret Details -->
				<div class="details-panel">
					{#if creds.direct_link}
						<div class="form-group">
							<span class="field-label">{m.servers_wizard_result_mtproxy_link()}</span>
							<div class="copy-input-row">
								<input
									readonly
									value={creds.direct_link}
									class="field-input mono"
								/>
								<Button size="sm" variant="secondary" ariaLabel={m.common_copy()} onclick={() => copy(creds.direct_link!, 'direct')}>
									{#if copiedKey === 'direct'}
										<Check size={14} class="text-success" />
									{:else}
										<Copy size={14} />
									{/if}
								</Button>
							</div>
						</div>
					{/if}

					{#if creds.web_link}
						<div class="form-group">
							<span class="field-label">{m.servers_wizard_result_web_link()}</span>
							<div class="copy-input-row">
								<input
									readonly
									value={creds.web_link}
									class="field-input mono"
								/>
								<Button size="sm" variant="secondary" ariaLabel={m.common_copy()} onclick={() => copy(creds.web_link!, 'web')}>
									{#if copiedKey === 'web'}
										<Check size={14} class="text-success" />
									{:else}
										<Copy size={14} />
									{/if}
								</Button>
							</div>
						</div>
					{/if}

					{#if creds.direct_link && creds.mtproxy_secret}
						<div class="params-box">
							<div class="params-header">{m.servers_wizard_result_manual_params()}</div>
							<div class="params-grid">
								<span class="param-name">{m.servers_wizard_result_param_server()}</span>
								<span class="param-val">{creds.direct_host || 'router.local'}</span>
								<span class="param-name">{m.servers_wizard_result_param_port()}</span>
								<span class="param-val">{creds.direct_port || 8443}</span>
								<span class="param-name">{m.servers_wizard_result_param_secret()}</span>
								<span class="param-val">{creds.mtproxy_secret}</span>
								{#if creds.tg_egress}
									<span class="param-name">{m.servers_wizard_result_param_egress()}</span>
									<span class="param-val">{creds.tg_egress}</span>
								{/if}
							</div>
						</div>
					{/if}
				</div>
			</div>

		<!-- Result content for Xray VLESS -->
		{:else}
			<div class="xray-result-flow">
				<!-- Format switcher tabs -->
				<div class="format-tabs">
					<button
						type="button"
						class="tab-btn"
						class:active={activeFormat === 'vless'}
						onclick={() => (activeFormat = 'vless')}
					>
						{m.servers_wizard_result_fmt_vless()}
					</button>
					{#if creds.happ_json}
						<button
							type="button"
							class="tab-btn"
							class:active={activeFormat === 'happ'}
							onclick={() => (activeFormat = 'happ')}
						>
							{m.servers_wizard_result_fmt_happ()}
						</button>
					{/if}
					{#if creds.singbox_json}
						<button
							type="button"
							class="tab-btn"
							class:active={activeFormat === 'singbox'}
							onclick={() => (activeFormat = 'singbox')}
						>
							Sing-box JSON
						</button>
					{/if}
					{#if creds.mihomo_yaml}
						<button
							type="button"
							class="tab-btn"
							class:active={activeFormat === 'mihomo'}
							onclick={() => (activeFormat = 'mihomo')}
						>
							Mihomo YAML
						</button>
					{/if}
				</div>

				{#if activeFormat === 'vless'}
					<div class="result-grid">
						<div class="qr-panel">
							{#if qrDataUrl}
								<button
									type="button"
									class="qr-wrapper qr-zoom-trigger"
									onclick={() => (qrZoomOpen = true)}
									title={m.servers_wizard_result_qr_zoom_hint()}
								>
									<img src={qrDataUrl} alt="VLESS QR" class="qr-img" />
								</button>
								<span class="qr-hint">{m.servers_wizard_result_qr_hint_vless()}</span>
							{/if}
						</div>
						<div class="details-panel">
							<span class="field-label">{m.servers_wizard_result_vless_link()}</span>
							<div class="copy-input-row">
								<input
									readonly
									value={creds.vless_url}
									class="field-input mono"
								/>
								<Button size="sm" variant="secondary" ariaLabel={m.common_copy()} onclick={() => copy(creds.vless_url!, 'vless')}>
									{#if copiedKey === 'vless'}
										<Check size={14} class="text-success" />
									{:else}
										<Copy size={14} />
									{/if}
								</Button>
							</div>

							<div class="params-box">
								<div class="client-meta-row">
									<span class="meta-label">{m.servers_wizard_result_meta_client()}</span>
									<span class="meta-val">{creds.remark || m.servers_ext_default_device()}</span>
								</div>
								<div class="client-meta-row">
									<span class="meta-label">UUID:</span>
									<span class="meta-val mono">{creds.uuid}</span>
								</div>
								{#if creds.xray_egress}
									<div class="client-meta-row">
										<span class="meta-label">{m.servers_wizard_result_meta_egress_vless()}</span>
										<span class="meta-val mono">{creds.xray_egress}</span>
									</div>
								{/if}
								{#if creds.tg_egress}
									<div class="client-meta-row">
										<span class="meta-label">{m.servers_wizard_result_meta_egress_tg()}</span>
										<span class="meta-val mono">{creds.tg_egress}</span>
									</div>
								{/if}
								{#if creds.origin_host}
									<div class="client-meta-row">
										<span class="meta-label">{m.servers_wizard_result_meta_origin()}</span>
										<span class="meta-val mono">{creds.origin_scheme || 'http'}://{creds.origin_host}:{creds.origin_port || 9009}</span>
									</div>
								{/if}
							</div>
						</div>
					</div>
				{:else if activeFormat === 'happ' && creds.happ_json}
					<CodeExportBlock
						label="Happ Proxy JSON snippet:"
						content={creds.happ_json}
						filename="happ-singbox.json"
						oncopy={() => copy(creds.happ_json!, 'happ')}
					/>
				{:else if activeFormat === 'singbox' && creds.singbox_json}
					<CodeExportBlock
						label="Sing-box Outbound JSON:"
						content={creds.singbox_json}
						filename="singbox-outbound.json"
						oncopy={() => copy(creds.singbox_json!, 'singbox')}
					/>
				{:else if activeFormat === 'mihomo' && creds.mihomo_yaml}
					<CodeExportBlock
						label="Mihomo Proxy YAML:"
						content={creds.mihomo_yaml}
						filename="mihomo-proxy.yaml"
						oncopy={() => copy(creds.mihomo_yaml!, 'mihomo')}
					/>
				{/if}
			</div>
		{/if}
	{/if}
</div>

<QrZoomModal bind:open={qrZoomOpen} dataUrl={qrDataUrl} />

<style>
	.result-container {
		display: flex;
		flex-direction: column;
		gap: 1.25rem;
		padding: 0.5rem 0;
	}

	.result-grid {
		display: grid;
		grid-template-columns: minmax(0, 240px) minmax(0, 1fr);
		gap: 1.25rem;
		align-items: start;
	}

	@media (max-width: 640px) {
		.result-grid {
			grid-template-columns: 1fr;
		}
	}

	.qr-panel {
		display: flex;
		flex-direction: column;
		align-items: center;
		padding: 1.25rem;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		gap: 1rem;
	}

	.qr-wrapper {
		padding: 10px;
		background: #ffffff;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
		box-shadow: 0 2px 8px rgba(0, 0, 0, 0.08);
	}

	.qr-zoom-trigger {
		cursor: zoom-in;
		transition: transform var(--t-fast) ease, box-shadow var(--t-fast) ease;
	}

	.qr-zoom-trigger:hover {
		transform: scale(1.02);
		box-shadow: 0 4px 14px rgba(0, 0, 0, 0.14);
	}

	.qr-img {
		width: 200px;
		max-width: 100%;
		height: auto;
		aspect-ratio: 1 / 1;
		image-rendering: crisp-edges;
		display: block;
	}

	.qr-hint {
		font-size: 0.6875rem;
		color: var(--color-text-muted);
		text-align: center;
	}

	.details-panel {
		display: flex;
		flex-direction: column;
		gap: 1rem;
		min-width: 0;
	}

	.form-group {
		display: flex;
		flex-direction: column;
		gap: 0.375rem;
	}

	.field-label {
		font-size: 0.75rem;
		font-weight: 600;
		color: var(--color-text-secondary);
	}

	.copy-input-row {
		display: flex;
		gap: 0.375rem;
		align-items: center;
	}

	.field-input {
		flex: 1;
		min-width: 0;
		height: 28px;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		color: var(--color-text-primary);
		font-size: 0.75rem;
		padding: 0 0.625rem;
		line-height: 1.4;
	}

	.field-input.mono {
		font-family: var(--font-mono);
	}

	.params-box {
		padding: 0.875rem 1rem;
		border-radius: var(--radius-sm);
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		min-width: 0;
	}

	.params-header {
		font-size: 0.6875rem;
		font-weight: 700;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--color-text-muted);
	}

	.params-grid {
		display: grid;
		grid-template-columns: auto minmax(0, 1fr);
		align-items: baseline;
		gap: 0.4rem 0.75rem;
		font-family: var(--font-mono);
		font-size: 0.6875rem;
	}

	.param-name {
		color: var(--color-text-muted);
	}

	.param-val {
		color: var(--color-text-primary);
		text-align: right;
		min-width: 0;
		overflow-wrap: anywhere;
		word-break: break-all;
		line-height: 1.4;
		user-select: text;
	}

	.client-meta-row {
		display: flex;
		justify-content: space-between;
		font-size: 0.75rem;
		gap: 0.5rem;
		flex-wrap: wrap;
	}

	.meta-label {
		color: var(--color-text-muted);
		flex-shrink: 0;
	}

	.meta-val {
		color: var(--color-text-primary);
		font-weight: 500;
		min-width: 0;
		overflow-wrap: anywhere;
		word-break: break-word;
	}

	.meta-val.mono {
		font-family: var(--font-mono);
		font-size: 0.6875rem;
		word-break: break-all;
	}

	.xray-result-flow {
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}

	.format-tabs {
		display: flex;
		gap: 0.25rem;
		padding: 0.25rem;
		border-radius: var(--radius-sm);
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		flex-wrap: wrap;
	}

	.tab-btn {
		flex: 1;
		padding: 0.375rem 0.5rem;
		border-radius: 4px;
		font-size: 0.75rem;
		font-weight: 500;
		color: var(--color-text-muted);
		background: transparent;
		border: none;
		cursor: pointer;
		transition: all var(--t-fast) ease;
	}

	.tab-btn.active {
		background: var(--color-bg-primary);
		color: var(--color-text-primary);
		font-weight: 600;
		box-shadow: 0 1px 2px rgba(0, 0, 0, 0.1);
	}

	:global(.text-success) {
		color: var(--color-success);
	}
</style>
