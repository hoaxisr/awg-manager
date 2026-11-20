<script lang="ts">
	import type { WizardKind, RevealCredentials } from '$lib/types/serverWizard';
	import { Button, Modal } from '$lib/components/ui';
	import { copyToClipboard } from '$lib/utils/clipboard';
	import { notifications } from '$lib/stores/notifications';
	import { renderCrispQrDataUrl } from '$lib/utils/qrSvg';
	import { Copy, ExternalLink, Lock, Eye, Download, Check } from 'lucide-svelte';

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

	async function copy(text: string, key: string, label = 'Скопировано') {
		await copyToClipboard(text);
		copiedKey = key;
		notifications.success(label);
		setTimeout(() => {
			if (copiedKey === key) copiedKey = null;
		}, 2000);
	}

	function downloadJson(filename: string, content: string) {
		const blob = new Blob([content], { type: 'application/json' });
		const url = URL.createObjectURL(blob);
		const a = document.createElement('a');
		a.href = url;
		a.download = filename;
		document.body.appendChild(a);
		a.click();
		document.body.removeChild(a);
		URL.revokeObjectURL(url);
		notifications.success('Файл скачан');
	}
</script>

<div class="result-container">
	{#if !creds}
		<!-- Secret Isolation Gate: Reveal CTA -->
		<div class="gate-card">
			<div class="gate-icon">
				<Lock size={28} />
			</div>
			<div class="gate-text">
				<h3 class="gate-title">Данные подключения защищены</h3>
				<p class="gate-desc">
					В целях безопасности секретные ключи и персональные ссылки не передаются при опросе статуса и открываются только по запросу.
				</p>
			</div>
			<Button variant="primary" onclick={onreveal} disabled={loading}>
				<Eye size={16} class="mr-2" />
				{loading ? 'Получение ключей...' : 'Показать ключи и ссылки'}
			</Button>
		</div>
	{:else}
		{#if kind === 'both'}
			<div class="format-tabs" style="margin-bottom: 12px;">
				<button
					type="button"
					class="tab-btn"
					class:active={activeServiceTab === 'xray'}
					onclick={() => (activeServiceTab = 'xray')}
				>
					Сервер VLESS (XHTTP)
				</button>
				<button
					type="button"
					class="tab-btn"
					class:active={activeServiceTab === 'tgwebproxy'}
					onclick={() => (activeServiceTab = 'tgwebproxy')}
				>
					Прокси Telegram
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
							title="Нажмите, чтобы увеличить QR-код"
						>
							<img src={qrDataUrl} alt="Telegram Proxy QR" class="qr-img" />
						</button>
						<span class="qr-hint">Сканируйте камерой смартфона (нажмите на QR для увеличения)</span>
					{/if}

					{#if creds.direct_link}
						<a
							href={creds.direct_link}
							target="_blank"
							rel="noopener noreferrer"
							class="btn-telegram"
						>
							<ExternalLink size={16} />
							Открыть в Telegram
						</a>
					{/if}
				</div>

				<!-- Right: Links & Secret Details -->
				<div class="details-panel">
					{#if creds.direct_link}
						<div class="form-group">
							<span class="field-label">Прямая ссылка MTProxy:</span>
							<div class="copy-input-row">
								<input
									readonly
									value={creds.direct_link}
									class="field-input mono"
								/>
								<Button size="sm" variant="secondary" onclick={() => copy(creds.direct_link!, 'direct')}>
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
							<span class="field-label">Ссылка веб-прокси Telegram:</span>
							<div class="copy-input-row">
								<input
									readonly
									value={creds.web_link}
									class="field-input mono"
								/>
								<Button size="sm" variant="secondary" onclick={() => copy(creds.web_link!, 'web')}>
									{#if copiedKey === 'web'}
										<Check size={14} class="text-success" />
									{:else}
										<Copy size={14} />
									{/if}
								</Button>
							</div>
						</div>
					{/if}

					<div class="params-box">
						<div class="params-header">Параметры вручную:</div>
						<div class="params-grid">
							<span class="param-name">Сервер:</span>
							<span class="param-val">{creds.direct_host || 'router.local'}</span>
							<span class="param-name">Порт:</span>
							<span class="param-val">{creds.direct_port || 8443}</span>
							<span class="param-name">Секрет:</span>
							<span class="param-val">{creds.tg_secret || '••••'}</span>
							{#if creds.tg_egress}
								<span class="param-name">Выход трафика:</span>
								<span class="param-val">{creds.tg_egress}</span>
							{/if}
						</div>
					</div>
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
						VLESS Ссылка & QR
					</button>
					{#if creds.happ_json}
						<button
							type="button"
							class="tab-btn"
							class:active={activeFormat === 'happ'}
							onclick={() => (activeFormat = 'happ')}
						>
							Happ (Телефон)
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
					<div class="result-grid items-center">
						<div class="qr-panel">
							{#if qrDataUrl}
								<button
									type="button"
									class="qr-wrapper qr-zoom-trigger"
									onclick={() => (qrZoomOpen = true)}
									title="Нажмите, чтобы увеличить QR-код"
								>
									<img src={qrDataUrl} alt="VLESS QR" class="qr-img" />
								</button>
								<span class="qr-hint">Сканируйте в приложении Happ / v2rayNG / Streisand (нажмите на QR для увеличения)</span>
							{/if}
						</div>
						<div class="details-panel">
							<span class="field-label">Универсальная ссылка VLESS:</span>
							<div class="copy-input-row">
								<input
									readonly
									value={creds.vless_url}
									class="field-input mono"
								/>
								<Button size="sm" variant="secondary" onclick={() => copy(creds.vless_url!, 'vless')}>
									{#if copiedKey === 'vless'}
										<Check size={14} class="text-success" />
									{:else}
										<Copy size={14} />
									{/if}
								</Button>
							</div>

							<div class="params-box">
								<div class="client-meta-row">
									<span class="meta-label">Клиент:</span>
									<span class="meta-val">{creds.remark || 'Основное устройство'}</span>
								</div>
								<div class="client-meta-row">
									<span class="meta-label">UUID:</span>
									<span class="meta-val mono">{creds.uuid}</span>
								</div>
								{#if creds.xray_egress}
									<div class="client-meta-row">
										<span class="meta-label">Выход трафика (VLESS):</span>
										<span class="meta-val mono">{creds.xray_egress}</span>
									</div>
								{/if}
								{#if creds.tg_egress}
									<div class="client-meta-row">
										<span class="meta-label">Выход трафика (Telegram):</span>
										<span class="meta-val mono">{creds.tg_egress}</span>
									</div>
								{/if}
								{#if creds.origin_host}
									<div class="client-meta-row">
										<span class="meta-label">Сервер-источник:</span>
										<span class="meta-val mono">{creds.origin_scheme || 'http'}://{creds.origin_host}:{creds.origin_port || 9009}</span>
									</div>
								{/if}
							</div>
						</div>
					</div>
				{:else if activeFormat === 'happ' && creds.happ_json}
					<div class="code-export-flow">
						<div class="code-export-header">
							<span class="field-label">Happ Proxy JSON snippet:</span>
							<div class="export-actions">
								<Button size="sm" variant="secondary" onclick={() => copy(creds.happ_json!, 'happ')}>
									Копировать
								</Button>
								<Button size="sm" variant="secondary" onclick={() => downloadJson('happ-singbox.json', creds.happ_json!)}>
									<Download size={14} class="mr-1" /> Скачать
								</Button>
							</div>
						</div>
						<pre class="code-preview">{creds.happ_json}</pre>
					</div>
				{:else if activeFormat === 'singbox' && creds.singbox_json}
					<div class="code-export-flow">
						<div class="code-export-header">
							<span class="field-label">Sing-box Outbound JSON:</span>
							<div class="export-actions">
								<Button size="sm" variant="secondary" onclick={() => copy(creds.singbox_json!, 'singbox')}>
									Копировать
								</Button>
								<Button size="sm" variant="secondary" onclick={() => downloadJson('singbox-outbound.json', creds.singbox_json!)}>
									<Download size={14} class="mr-1" /> Скачать
								</Button>
							</div>
						</div>
						<pre class="code-preview">{creds.singbox_json}</pre>
					</div>
				{:else if activeFormat === 'mihomo' && creds.mihomo_yaml}
					<div class="code-export-flow">
						<div class="code-export-header">
							<span class="field-label">Mihomo Proxy YAML:</span>
							<div class="export-actions">
								<Button size="sm" variant="secondary" onclick={() => copy(creds.mihomo_yaml!, 'mihomo')}>
									Копировать
								</Button>
								<Button size="sm" variant="secondary" onclick={() => downloadJson('mihomo-proxy.yaml', creds.mihomo_yaml!)}>
									<Download size={14} class="mr-1" /> Скачать
								</Button>
							</div>
						</div>
						<pre class="code-preview">{creds.mihomo_yaml}</pre>
					</div>
				{/if}
			</div>
		{/if}
	{/if}
</div>

<Modal
	bind:open={qrZoomOpen}
	title="QR-код для сканирования"
	onclose={() => (qrZoomOpen = false)}
	size="sm"
>
	{#if qrDataUrl}
		<div class="qr-zoom-body">
			<div class="qr-zoom-frame">
				<img src={qrDataUrl} alt="QR-код крупным планом" class="qr-zoom-img" />
			</div>
			<p class="qr-zoom-note">Наведите камеру или сканер в приложении на экран</p>
		</div>
	{/if}
	{#snippet actions()}
		<Button variant="secondary" onclick={() => (qrZoomOpen = false)}>Закрыть</Button>
	{/snippet}
</Modal>

<style>
	.result-container {
		display: flex;
		flex-direction: column;
		gap: 1.25rem;
		padding: 0.5rem 0;
	}

	.gate-card {
		padding: 2rem;
		border-radius: var(--radius);
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		text-align: center;
		gap: 1rem;
	}

	.gate-icon {
		width: 56px;
		height: 56px;
		border-radius: 50%;
		background: var(--color-accent-tint);
		border: 1px solid var(--color-accent-border);
		color: var(--color-accent);
		display: flex;
		align-items: center;
		justify-content: center;
	}

	.gate-text {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
		max-width: 24rem;
	}

	.gate-title {
		font-size: 1rem;
		font-weight: 600;
		color: var(--color-text-primary);
		margin: 0;
	}

	.gate-desc {
		font-size: 0.75rem;
		color: var(--color-text-muted);
		margin: 0;
		line-height: 1.4;
	}

	.result-grid {
		display: grid;
		grid-template-columns: 1fr 1.2fr;
		gap: 1.5rem;
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
		width: 240px;
		height: 240px;
		image-rendering: crisp-edges;
		display: block;
	}

	.qr-zoom-body {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.75rem;
	}

	.qr-zoom-frame {
		padding: 16px;
		background: #ffffff;
		border-radius: var(--radius);
		border: 1px solid var(--color-border);
	}

	.qr-zoom-img {
		width: min(360px, 78vw);
		height: auto;
		aspect-ratio: 1 / 1;
		image-rendering: crisp-edges;
		display: block;
	}

	.qr-zoom-note {
		font-size: 0.75rem;
		color: var(--color-text-muted);
		text-align: center;
		margin: 0;
	}

	.qr-hint {
		font-size: 0.6875rem;
		color: var(--color-text-muted);
		text-align: center;
	}

	.btn-telegram {
		width: 100%;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		gap: 0.5rem;
		padding: 0.625rem 1rem;
		border-radius: var(--radius-sm);
		background: #229ed9;
		color: #ffffff;
		font-weight: 600;
		font-size: 0.75rem;
		text-decoration: none;
		transition: opacity var(--t-fast) ease;
	}

	.btn-telegram:hover {
		opacity: 0.9;
	}

	.details-panel {
		display: flex;
		flex-direction: column;
		gap: 1rem;
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
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		color: var(--color-text-primary);
		font-size: 0.75rem;
		padding: 0.4375rem 0.625rem;
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
		grid-template-columns: auto 1fr;
		gap: 0.375rem 0.75rem;
		font-family: var(--font-mono);
		font-size: 0.6875rem;
	}

	.param-name {
		color: var(--color-text-muted);
	}

	.param-val {
		color: var(--color-text-primary);
		text-align: right;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
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

	.code-export-flow {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.code-export-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
	}

	.export-actions {
		display: flex;
		gap: 0.5rem;
	}

	.code-preview {
		margin: 0;
		padding: 0.875rem;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		font-family: var(--font-mono);
		font-size: 0.6875rem;
		color: var(--color-text-primary);
		overflow-x: auto;
		max-height: 14rem;
	}

	:global(.text-success) {
		color: var(--color-success);
	}
</style>
