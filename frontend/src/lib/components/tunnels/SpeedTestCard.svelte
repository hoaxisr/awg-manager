<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { Toggle } from '$lib/components/ui';
	import type { SpeedTestInfo, SpeedTestResult } from '$lib/types';

	interface Props {
		tunnelId: string;
	}

	let { tunnelId }: Props = $props();

	let speedTestInfo = $state<SpeedTestInfo | null>(null);
	let infoLoading = $state(true);
	let selectedServerIndex = $state(0);
	let customServer = $state('');
	let useCustomServer = $state(false);
	let speedPhase = $state<'idle' | 'download' | 'upload' | 'done' | 'error'>('idle');
	let downloadResult: SpeedTestResult | null = $state(null);
	let uploadResult: SpeedTestResult | null = $state(null);
	let speedError: string | null = $state(null);

	let selectedServer = $derived(
		speedTestInfo?.servers[selectedServerIndex] ?? null
	);

	let isRunning = $derived(speedPhase === 'download' || speedPhase === 'upload');

	onMount(async () => {
		try {
			speedTestInfo = await api.getSpeedTestInfo();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Не удалось загрузить информацию о тесте скорости');
		} finally {
			infoLoading = false;
		}
	});

	function parseCustomServer(): { host: string; port: number } | null {
		const val = customServer.trim();
		if (!val) return null;

		const lastColon = val.lastIndexOf(':');
		if (lastColon === -1) {
			return { host: val, port: 5201 };
		}

		const host = val.substring(0, lastColon);
		const port = parseInt(val.substring(lastColon + 1), 10);
		if (isNaN(port) || port < 1 || port > 65535) {
			return { host: val, port: 5201 };
		}
		return { host, port };
	}

	function friendlyError(msg: string): string {
		if (msg.includes('exit 1') || msg.includes('server busy') || msg.includes('the server is busy')) {
			return 'Сервер занят, попробуйте позже или выберите другой';
		}
		if (msg.includes('timed out') || msg.includes('timeout')) {
			return 'Превышено время ожидания — сервер не отвечает';
		}
		if (msg.includes('connection refused') || msg.includes('No route')) {
			return 'Не удалось подключиться к серверу';
		}
		if (msg.includes('tunnel not running')) {
			return 'Туннель не запущен';
		}
		if (msg.includes('no IPv4 address') || msg.includes('interface') && msg.includes('not found')) {
			return 'Интерфейс туннеля недоступен';
		}
		return msg;
	}

	async function runSpeedTest() {
		let server: string;
		let port: number;

		if (useCustomServer) {
			const parsed = parseCustomServer();
			if (!parsed || !parsed.host) {
				notifications.error('Введите адрес сервера');
				return;
			}
			server = parsed.host;
			port = parsed.port;
		} else if (selectedServer) {
			server = selectedServer.host;
			port = selectedServer.port;
		} else {
			return;
		}

		speedPhase = 'download';
		downloadResult = null;
		uploadResult = null;
		speedError = null;

		try {
			downloadResult = await api.speedTest(tunnelId, server, port, 'download');
		} catch (e) {
			const raw = e instanceof Error ? e.message : 'Ошибка теста скорости';
			speedError = friendlyError(raw);
			speedPhase = 'error';
			return;
		}

		speedPhase = 'upload';
		try {
			uploadResult = await api.speedTest(tunnelId, server, port, 'upload');
		} catch (e) {
			const raw = e instanceof Error ? e.message : 'Ошибка теста скорости';
			speedError = friendlyError(raw);
		}
		speedPhase = 'done';
	}

	function formatBandwidth(mbps: number): string {
		if (mbps >= 100) return mbps.toFixed(0);
		if (mbps >= 10) return mbps.toFixed(1);
		return mbps.toFixed(2);
	}
</script>

<div class="card test-card">
	<h3 class="card-title">
		<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="20" height="20">
			<path d="M13 2L3 14h9l-1 8 10-12h-9l1-8z"/>
		</svg>
		Тест скорости
	</h3>

	{#if infoLoading}
		<div class="loading-placeholder">
			<span class="spinner"></span>
		</div>
	{:else if !speedTestInfo?.available}
		<p class="test-desc unavailable">
			<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="16" height="16">
				<circle cx="12" cy="12" r="10"/>
				<line x1="12" y1="8" x2="12" y2="12"/>
				<line x1="12" y1="16" x2="12.01" y2="16"/>
			</svg>
			iperf3 не найден. Доступно только на Keenetic OS 5.x.
		</p>
	{:else}
		<p class="test-desc">Измерить скорость через туннель с помощью iperf3.</p>

		<!-- Server selection -->
		<div class="server-section">
			<div class="server-header">
				<span class="server-label">Сервер</span>
				<Toggle
					checked={useCustomServer}
					onchange={(v) => { useCustomServer = v; }}
					disabled={isRunning}
					label="Свой"
					size="sm"
				/>
			</div>

			{#if useCustomServer}
				<input
					type="text"
					placeholder="host:port (порт по умолчанию 5201)"
					bind:value={customServer}
					disabled={isRunning}
				/>
			{:else}
				<select bind:value={selectedServerIndex} disabled={isRunning}>
					{#each speedTestInfo.servers as server, i}
						<option value={i}>{server.label}</option>
					{/each}
				</select>
			{/if}
		</div>

		<!-- Results -->
		{#if speedPhase === 'download'}
			<div class="test-result">
				<div class="progress-steps">
					<div class="step active">
						<span class="spinner step-spinner"></span>
						<span>Тест загрузки...</span>
					</div>
					<div class="step pending">
						<span class="step-dot"></span>
						<span>Тест отдачи</span>
					</div>
				</div>
			</div>
		{:else if speedPhase === 'upload'}
			<div class="test-result">
				<div class="progress-steps">
					<div class="step completed">
						<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" width="14" height="14">
							<polyline points="20 6 9 17 4 12"/>
						</svg>
						<span>Загрузка: <strong>{downloadResult ? formatBandwidth(downloadResult.bandwidth) : '–'}</strong> Мбит/с</span>
					</div>
					<div class="step active">
						<span class="spinner step-spinner"></span>
						<span>Тест отдачи...</span>
					</div>
				</div>
			</div>
		{:else if speedPhase === 'done'}
			<div class="test-result results-panel">
				<div class="result-row">
					<span class="result-icon download">&#8595;</span>
					<div class="result-info">
						<span class="result-label">Загрузка</span>
					</div>
					<span class="result-value">{downloadResult ? formatBandwidth(downloadResult.bandwidth) : '–'} <small>Мбит/с</small></span>
				</div>
				<hr class="result-divider" />
				<div class="result-row">
					<span class="result-icon upload">&#8593;</span>
					<div class="result-info">
						<span class="result-label">Отдача</span>
					</div>
					{#if uploadResult}
						<span class="result-value">{formatBandwidth(uploadResult.bandwidth)} <small>Мбит/с</small></span>
					{:else}
						<span class="result-value na">N/A</span>
					{/if}
				</div>
				{#if !uploadResult && speedError}
					<hr class="result-divider" />
					<div class="result-meta result-meta-warn">{speedError}</div>
				{/if}
				{#if uploadResult && uploadResult.retransmits > 0}
					<hr class="result-divider" />
					<div class="result-meta">
						Ретрансмиты: {uploadResult.retransmits}
					</div>
				{/if}
			</div>
		{:else if speedPhase === 'error'}
			<div class="test-result error-panel">
				<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="18" height="18">
					<circle cx="12" cy="12" r="10"/>
					<line x1="15" y1="9" x2="9" y2="15"/>
					<line x1="9" y1="9" x2="15" y2="15"/>
				</svg>
				<span>{speedError}</span>
			</div>
		{/if}

		<div class="card-spacer"></div>
		<a class="servers-link" href="https://iperf3serverlist.net" target="_blank" rel="noopener noreferrer">
			Публично доступные серверы iperf3 ↗
		</a>
		<button
			class="btn btn-primary btn-block"
			onclick={runSpeedTest}
			disabled={isRunning}
		>
			{#if isRunning}
				<span class="spinner"></span>
			{/if}
			{speedPhase === 'done' || speedPhase === 'error' ? 'Повторить тест' : 'Начать тест'}
		</button>
	{/if}
</div>

<style>
	.card-title {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		font-size: 1rem;
		white-space: nowrap;
	}

	.test-desc {
		color: var(--text-muted);
		font-size: 0.875rem;
	}

	/* Loading placeholder */
	.loading-placeholder {
		display: flex;
		align-items: center;
		justify-content: center;
		padding: 2rem 0;
	}

	/* Unavailable message */
	.unavailable {
		display: flex;
		align-items: flex-start;
		gap: 0.5rem;
		color: var(--text-muted);
		font-style: italic;
		padding: 0.75rem;
		background: var(--bg-tertiary);
		border-radius: var(--radius-sm);
	}

	.unavailable svg {
		flex-shrink: 0;
		margin-top: 1px;
	}

	/* Server selection section */
	.server-section {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.server-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
	}

	.server-label {
		font-size: 0.8125rem;
		font-weight: 500;
		color: var(--text-secondary);
		text-transform: uppercase;
		letter-spacing: 0.03em;
	}

	/* Progress steps */
	.progress-steps {
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
	}

	.step {
		display: flex;
		align-items: center;
		gap: 0.625rem;
		font-size: 0.875rem;
	}

	.step.active {
		color: var(--accent);
		font-weight: 500;
	}

	.step.pending {
		color: var(--text-muted);
	}

	.step.completed {
		color: var(--success);
	}

	.step.completed strong {
		color: var(--text-primary);
	}

	.step-spinner {
		width: 14px;
		height: 14px;
		flex-shrink: 0;
	}

	.step-dot {
		width: 14px;
		height: 14px;
		display: flex;
		align-items: center;
		justify-content: center;
		flex-shrink: 0;
	}

	.step-dot::after {
		content: '';
		width: 6px;
		height: 6px;
		border-radius: 50%;
		background: var(--text-muted);
	}

	/* Results panel */
	.results-panel {
		display: flex;
		flex-direction: column;
		gap: 0;
	}

	.result-row {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		padding: 0.5rem 0;
	}

	.result-icon {
		width: 32px;
		height: 32px;
		display: flex;
		align-items: center;
		justify-content: center;
		border-radius: 6px;
		font-weight: 700;
		font-size: 1.125rem;
		flex-shrink: 0;
	}

	.result-icon.download {
		background: rgba(158, 206, 106, 0.12);
		color: var(--success);
	}

	.result-icon.upload {
		background: rgba(122, 162, 247, 0.12);
		color: var(--accent);
	}

	.result-info {
		flex: 1;
		min-width: 0;
	}

	.result-label {
		font-size: 0.875rem;
		color: var(--text-secondary);
	}

	.result-value {
		font-size: 1.375rem;
		font-weight: 600;
		font-variant-numeric: tabular-nums;
		white-space: nowrap;
	}

	.result-value small {
		font-size: 0.75rem;
		font-weight: 400;
		color: var(--text-muted);
	}

	.result-divider {
		border: none;
		border-top: 1px dashed var(--border);
		margin: 0;
	}

	.result-value.na {
		color: var(--text-muted);
	}

	.result-meta {
		padding-top: 0.5rem;
		font-size: 0.8125rem;
		color: var(--text-muted);
	}

	.result-meta-warn {
		color: var(--warning);
	}

	/* Error panel */
	.error-panel {
		display: flex;
		align-items: flex-start;
		gap: 0.5rem;
		color: var(--error);
		font-size: 0.875rem;
		line-height: 1.4;
	}

	.error-panel svg {
		flex-shrink: 0;
		margin-top: 1px;
	}

	.card-spacer {
		flex: 1;
	}

	.servers-link {
		display: block;
		text-align: center;
		font-size: 0.8125rem;
		color: var(--text-muted);
		text-decoration: none;
		padding: 0.5rem 0;
	}

	.servers-link:hover {
		color: var(--accent);
	}

	/* Full-width button */
	.btn-block {
		width: 100%;
	}
</style>
