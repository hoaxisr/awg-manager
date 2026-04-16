<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/stores';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import type { IPResult, ConnectivityResult, IPCheckService, SpeedTestInfo, SpeedTestResult } from '$lib/types';
	import { FormToggle } from '$lib/components/ui';
	import { PageContainer } from '$lib/components/layout';

	let tunnelName = $derived($page.params.name as string);

	// IP check services
	let ipServices = $state<IPCheckService[]>([]);
	let selectedServiceIndex = $state(0);
	let customServiceURL = $state('');
	let useCustomService = $state(false);

	onMount(async () => {
		try {
			ipServices = await api.getIPCheckServices();
		} catch {
			// Services will be empty — fallback mode
		}
	});

	// Connectivity test
	let connectivityLoading = $state(false);
	let connectivityResult: ConnectivityResult | null = $state(null);

	// IP test
	let ipLoading = $state(false);
	let ipResult: IPResult | null = $state(null);

	async function checkConnectivity() {
		if (!tunnelName) return;
		connectivityLoading = true;
		connectivityResult = null;
		try {
			connectivityResult = await api.checkSystemTunnelConnectivity(tunnelName);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка проверки соединения');
		} finally {
			connectivityLoading = false;
		}
	}

	async function checkIP() {
		if (!tunnelName) return;

		let serviceURL = '';
		if (useCustomService) {
			serviceURL = customServiceURL.trim();
			if (!serviceURL) {
				notifications.error('Введите URL сервиса');
				return;
			}
		} else if (ipServices.length > 0) {
			serviceURL = ipServices[selectedServiceIndex]?.url ?? '';
		}

		ipLoading = true;
		ipResult = null;
		try {
			ipResult = await api.checkSystemTunnelIP(tunnelName, serviceURL || undefined);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка проверки IP');
		} finally {
			ipLoading = false;
		}
	}

	// Speed test
	let speedTestInfo = $state<SpeedTestInfo | null>(null);
	let infoLoading = $state(true);
	let selectedServerIndex = $state(0);
	let customServer = $state('');
	let useCustomServer = $state(false);
	let speedPhase = $state<'idle' | 'download' | 'upload' | 'done' | 'error'>('idle');
	let downloadResult: SpeedTestResult | null = $state(null);
	let uploadResult: SpeedTestResult | null = $state(null);
	let speedError: string | null = $state(null);
	let currentBandwidth = $state(0);
	let activeEventSource: EventSource | null = $state(null);

	let selectedServer = $derived(
		speedTestInfo?.servers[selectedServerIndex] ?? null
	);

	onMount(async () => {
		try {
			speedTestInfo = await api.getSpeedTestInfo();
		} catch {
			// No speed test servers
		} finally {
			infoLoading = false;
		}
	});

	function formatBandwidth(mbps: number): string {
		if (mbps >= 100) return mbps.toFixed(0);
		if (mbps >= 10) return mbps.toFixed(1);
		return mbps.toFixed(2);
	}

	async function runSpeedTest() {
		speedPhase = 'download';
		downloadResult = null;
		uploadResult = null;
		speedError = null;
		currentBandwidth = 0;

		const server = useCustomServer ? customServer : (selectedServer?.host ?? '');
		const port = useCustomServer ? 5201 : (selectedServer?.port ?? 5201);

		if (!server) {
			speedError = 'Не выбран сервер';
			speedPhase = 'error';
			return;
		}

		try {
			await runPhase(server, port, 'download');
			speedPhase = 'upload';
			currentBandwidth = 0;
			await runPhase(server, port, 'upload');
			speedPhase = 'done';
		} catch (e) {
			speedError = e instanceof Error ? e.message : 'Ошибка теста';
			speedPhase = 'error';
		}
	}

	function runPhase(server: string, port: number, direction: 'download' | 'upload'): Promise<void> {
		return new Promise((resolve, reject) => {
			activeEventSource = api.systemTunnelSpeedTestStream(
				tunnelName, server, port, direction,
				(data) => { currentBandwidth = data.bandwidth; },
				(result) => {
					if (direction === 'download') downloadResult = result;
					else uploadResult = result;
					activeEventSource = null;
					resolve();
				},
				(error) => {
					activeEventSource = null;
					reject(new Error(error));
				}
			);
		});
	}

	function cancelSpeedTest() {
		activeEventSource?.close();
		activeEventSource = null;
		speedPhase = 'idle';
	}
</script>

<PageContainer>
<div class="page-header test-page-header">
	<a href="/" class="back-link">
		<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="20" height="20">
			<line x1="19" y1="12" x2="5" y2="12"/>
			<polyline points="12 19 5 12 12 5"/>
		</svg>
		К списку туннелей
	</a>
	<h1 class="page-title">Тестирование: {tunnelName}</h1>
</div>

<div class="tests-grid">
	<!-- Connectivity Test -->
	<div class="card test-card">
		<h3>
			<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="20" height="20">
				<path d="M22 11.08V12a10 10 0 1 1-5.93-9.14"/>
				<polyline points="22 4 12 14.01 9 11.01"/>
			</svg>
			Проверка соединения
		</h3>
		<p class="test-desc">Проверить доступ в интернет через туннель.</p>

		{#if connectivityResult}
			<div class="test-result">
				{#if connectivityResult.connected}
					<span class="result-success">
						<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="24" height="24">
							<path d="M22 11.08V12a10 10 0 1 1-5.93-9.14"/>
							<polyline points="22 4 12 14.01 9 11.01"/>
						</svg>
						Подключено
					</span>
					{#if connectivityResult.latency}
						<span class="result-detail">Задержка: {connectivityResult.latency} мс</span>
					{/if}
				{:else}
					<span class="result-error">
						<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="24" height="24">
							<circle cx="12" cy="12" r="10"/>
							<line x1="15" y1="9" x2="9" y2="15"/>
							<line x1="9" y1="9" x2="15" y2="15"/>
						</svg>
						Нет соединения
					</span>
					{#if connectivityResult.reason}
						<span class="result-detail">Причина: {connectivityResult.reason}</span>
					{/if}
				{/if}
			</div>
		{/if}

		<div class="card-spacer"></div>
		<button class="btn btn-primary btn-block" onclick={checkConnectivity} disabled={connectivityLoading}>
			{#if connectivityLoading}
				<span class="spinner"></span>
			{/if}
			Проверить соединение
		</button>
	</div>

	<!-- IP Test -->
	<div class="card test-card">
		<h3>
			<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="20" height="20">
				<circle cx="12" cy="12" r="10"/>
				<line x1="2" y1="12" x2="22" y2="12"/>
				<path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z"/>
			</svg>
			Проверка IP
		</h3>
		<p class="test-desc">Убедиться, что IP меняется при использовании туннеля.</p>

		{#if ipServices.length > 0}
			<div class="server-section">
				<div class="server-header">
					<span class="server-label">Сервис</span>
					<FormToggle
						bind:checked={useCustomService}
						disabled={ipLoading}
						label="Свой"
						size="sm"
					/>
				</div>

				{#if useCustomService}
					<input
						type="text"
						placeholder="https://example.com/ip"
						bind:value={customServiceURL}
						disabled={ipLoading}
					/>
				{:else}
					<select bind:value={selectedServiceIndex} disabled={ipLoading}>
						{#each ipServices as service, i}
							<option value={i}>{service.label}</option>
						{/each}
					</select>
				{/if}
			</div>
		{/if}

		{#if ipResult}
			<div class="test-result ip-result">
				<div class="ip-row">
					<span class="ip-label">Прямой IP:</span>
					<span class="ip-value">{ipResult.directIp}</span>
				</div>
				<div class="ip-row">
					<span class="ip-label">VPN IP:</span>
					<span class="ip-value">{ipResult.vpnIp}</span>
				</div>
				{#if ipResult.endpointIp}
					<div class="ip-row">
						<span class="ip-label">IP сервера:</span>
						<span class="ip-value">{ipResult.endpointIp}</span>
					</div>
				{/if}
				<div class="ip-status">
					{#if ipResult.ipChanged}
						<span class="result-success">IP изменился — туннель работает!</span>
					{:else}
						<span class="result-warning">IP не изменился</span>
					{/if}
				</div>
			</div>
		{/if}

		<div class="card-spacer"></div>
		<button class="btn btn-primary btn-block" onclick={checkIP} disabled={ipLoading}>
			{#if ipLoading}
				<span class="spinner"></span>
			{/if}
			Проверить IP
		</button>
	</div>

	<!-- Speed Test -->
	<div class="card test-card">
		<h3>
			<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="20" height="20">
				<path d="M13 2L3 14h9l-1 8 10-12h-9l1-8z"/>
			</svg>
			Тест скорости
		</h3>
		<p class="test-desc">Измерить скорость загрузки и выгрузки через туннель.</p>

		{#if !infoLoading && speedTestInfo?.servers?.length}
			<div class="server-section">
				<div class="server-header">
					<span class="server-label">Сервер</span>
					<FormToggle
						bind:checked={useCustomServer}
						disabled={speedPhase !== 'idle' && speedPhase !== 'done' && speedPhase !== 'error'}
						label="Свой"
						size="sm"
					/>
				</div>

				{#if useCustomServer}
					<input
						type="text"
						placeholder="server:port"
						bind:value={customServer}
						disabled={speedPhase !== 'idle' && speedPhase !== 'done' && speedPhase !== 'error'}
					/>
				{:else}
					<select bind:value={selectedServerIndex} disabled={speedPhase !== 'idle' && speedPhase !== 'done' && speedPhase !== 'error'}>
						{#each speedTestInfo.servers as server, i}
							<option value={i}>{server.label}</option>
						{/each}
					</select>
				{/if}
			</div>
		{/if}

		{#if speedPhase === 'download' || speedPhase === 'upload'}
			<div class="test-result">
				<span class="speed-phase">{speedPhase === 'download' ? 'Загрузка' : 'Выгрузка'}...</span>
				<span class="speed-value">{formatBandwidth(currentBandwidth)} Мбит/с</span>
			</div>
		{/if}

		{#if downloadResult || uploadResult}
			<div class="test-result ip-result">
				{#if downloadResult}
					<div class="ip-row">
						<span class="ip-label">Загрузка:</span>
						<span class="ip-value">{formatBandwidth(downloadResult.bandwidth)} Мбит/с</span>
					</div>
				{/if}
				{#if uploadResult}
					<div class="ip-row">
						<span class="ip-label">Выгрузка:</span>
						<span class="ip-value">{formatBandwidth(uploadResult.bandwidth)} Мбит/с</span>
					</div>
				{/if}
			</div>
		{/if}

		{#if speedError}
			<div class="test-result">
				<span class="result-error">{speedError}</span>
			</div>
		{/if}

		<div class="card-spacer"></div>
		{#if speedPhase === 'download' || speedPhase === 'upload'}
			<button class="btn btn-secondary btn-block" onclick={cancelSpeedTest}>
				Отмена
			</button>
		{:else}
			<button class="btn btn-primary btn-block" onclick={runSpeedTest} disabled={infoLoading || (!speedTestInfo?.servers?.length && !useCustomServer)}>
				Запустить тест
			</button>
		{/if}
	</div>
</div>
</PageContainer>

<style>
	.test-page-header {
		justify-content: flex-start;
		gap: 1rem;
	}

	.back-link {
		display: flex;
		align-items: center;
		gap: 0.25rem;
		color: var(--text-secondary);
		font-size: 0.875rem;
	}

	.back-link:hover {
		color: var(--text-primary);
	}

	.tests-grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(300px, 1fr));
		gap: 1rem;
	}

	.test-card {
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}

	.test-card h3 {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		font-size: 1rem;
	}

	.test-desc {
		color: var(--text-muted);
		font-size: 0.875rem;
	}

	.test-result {
		padding: 1rem;
		background: var(--bg-tertiary);
		border-radius: var(--radius-sm);
	}

	.result-success {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		color: var(--success);
		font-weight: 500;
	}

	.result-error {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		color: var(--error);
		font-weight: 500;
	}

	.result-warning {
		color: var(--warning);
		font-weight: 500;
	}

	.result-detail {
		display: block;
		margin-top: 0.5rem;
		font-size: 0.875rem;
		color: var(--text-secondary);
	}

	.ip-result {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.ip-row {
		display: flex;
		justify-content: space-between;
		font-size: 0.875rem;
	}

	.ip-label {
		color: var(--text-muted);
	}

	.ip-value {
		font-family: monospace;
	}

	.ip-status {
		margin-top: 0.5rem;
		padding-top: 0.5rem;
		border-top: 1px solid var(--border);
		font-size: 0.875rem;
	}

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

	.card-spacer {
		flex: 1;
	}

	.btn-block {
		width: 100%;
	}

	.speed-phase {
		display: block;
		font-size: 0.8125rem;
		color: var(--text-muted);
		margin-bottom: 0.25rem;
	}

	.speed-value {
		font-size: 1.25rem;
		font-weight: 600;
		font-family: monospace;
		color: var(--text-primary);
	}
</style>
