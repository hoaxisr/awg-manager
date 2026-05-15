<script lang="ts">
	import { onDestroy, onMount } from 'svelte';
	import { page } from '$app/stores';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import type {
		IPResult,
		ConnectivityResult,
		IPCheckService,
		SpeedTestInfo,
		SpeedTestResult,
		Subscription,
	} from '$lib/types';
	import { FormToggle, Button, Dropdown, type DropdownOption } from '$lib/components/ui';
	import { PageContainer } from '$lib/components/layout';

	let subscriptionId = $derived($page.params.id as string);
	let subscription = $state<Subscription | null>(null);
	let loaded = $state(false);

	let selectorTag = $derived(subscription?.selectorTag ?? '');
	let kernelIface = $derived(
		subscription && subscription.proxyIndex >= 0 ? `t2s${subscription.proxyIndex}` : '',
	);
	let displayName = $derived(subscription?.label || selectorTag || subscriptionId);

	let ipServices = $state<IPCheckService[]>([]);
	let selectedServiceIndex = $state(0);
	let customServiceURL = $state('');
	let useCustomService = $state(false);

	let connectivityLoading = $state(false);
	let connectivityResult: ConnectivityResult | null = $state(null);

	let ipLoading = $state(false);
	let ipResult: IPResult | null = $state(null);

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
	let currentSecond = $state(0);
	let activeEventSource: EventSource | null = $state(null);

	let selectedServer = $derived(speedTestInfo?.servers[selectedServerIndex] ?? null);
	let isRunning = $derived(speedPhase === 'download' || speedPhase === 'upload');

	onMount(async () => {
		try {
			subscription = await api.getSubscription(subscriptionId);
		} catch {
			subscription = null;
		} finally {
			loaded = true;
		}
		try {
			ipServices = await api.getIPCheckServices();
		} catch {
			// fallback mode with custom URL
		}
		try {
			speedTestInfo = await api.getSpeedTestInfo();
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Не удалось загрузить информацию о тесте скорости');
		} finally {
			infoLoading = false;
		}
	});

	onDestroy(() => {
		activeEventSource?.close();
		activeEventSource = null;
	});

	async function checkConnectivity() {
		if (!selectorTag || !kernelIface) return;
		connectivityLoading = true;
		connectivityResult = null;
		try {
			connectivityResult = await api.singboxCheckConnectivity(selectorTag, kernelIface);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка проверки соединения');
		} finally {
			connectivityLoading = false;
		}
	}

	async function checkIP() {
		if (!selectorTag || !kernelIface) return;
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
			ipResult = await api.singboxCheckIP(selectorTag, serviceURL || undefined, kernelIface);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка проверки IP');
		} finally {
			ipLoading = false;
		}
	}

	function parseCustomServer(): { host: string; port: number } | null {
		const val = customServer.trim();
		if (!val) return null;
		const lastColon = val.lastIndexOf(':');
		if (lastColon === -1) return { host: val, port: 5201 };
		const host = val.substring(0, lastColon);
		const port = parseInt(val.substring(lastColon + 1), 10);
		if (isNaN(port) || port < 1 || port > 65535) return { host, port: 5201 };
		return { host, port };
	}

	function friendlyError(msg: string): string {
		if (msg.includes('exit 1') || msg.includes('server busy') || msg.includes('the server is busy')) {
			return 'Сервер занят, попробуйте позже или выберите другой';
		}
		if (msg.includes('timed out') || msg.includes('timeout')) return 'Превышено время ожидания — сервер не отвечает';
		if (msg.includes('connection refused') || msg.includes('No route')) return 'Не удалось подключиться к серверу';
		if (msg.includes('tunnel not running')) return 'Туннель не запущен';
		if (msg.includes('no IPv4 address') || msg.includes('no kernel interface') || (msg.includes('interface') && msg.includes('not found'))) {
			return 'Интерфейс туннеля недоступен';
		}
		return msg;
	}

	function runStream(server: string, port: number): Promise<void> {
		return new Promise((resolve, reject) => {
			currentBandwidth = 0;
			currentSecond = 0;
			activeEventSource = api.singboxSpeedTestStream(
				selectorTag,
				server,
				port,
				(phase) => {
					speedPhase = phase;
					currentBandwidth = 0;
					currentSecond = 0;
				},
				(interval) => {
					currentBandwidth = interval.bandwidth;
					currentSecond = interval.second;
				},
				(result) => {
					if (result.phase === 'download') {
						downloadResult = {
							server,
							direction: 'download',
							bandwidth: result.bandwidth,
							bytes: result.bytes,
							duration: result.duration,
							retransmits: 0,
						};
					} else if (result.phase === 'upload') {
						uploadResult = {
							server,
							direction: 'upload',
							bandwidth: result.bandwidth,
							bytes: result.bytes,
							duration: result.duration,
							retransmits: 0,
						};
					}
				},
				() => {
					activeEventSource = null;
					resolve();
				},
				(error) => {
					activeEventSource = null;
					reject(new Error(error));
				},
				kernelIface,
			);
		});
	}

	async function runSpeedTest() {
		if (!selectorTag || !kernelIface) return;
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
		} else return;

		speedPhase = 'download';
		downloadResult = null;
		uploadResult = null;
		speedError = null;
		currentBandwidth = 0;
		currentSecond = 0;

		try {
			await runStream(server, port);
			speedPhase = 'done';
		} catch (e) {
			const raw = e instanceof Error ? e.message : 'Ошибка теста скорости';
			speedError = friendlyError(raw);
			speedPhase = 'error';
		}
	}

	function formatBandwidth(mbps: number): string {
		if (mbps >= 100) return mbps.toFixed(0);
		if (mbps >= 10) return mbps.toFixed(1);
		return mbps.toFixed(2);
	}
</script>

<PageContainer>
	<div class="page-header test-page-header">
		<a href="/?tab=subscriptions" class="back-link">
			<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="20" height="20">
				<line x1="19" y1="12" x2="5" y2="12"/>
				<polyline points="12 19 5 12 12 5"/>
			</svg>
			К списку подписок
		</a>
		<h1 class="page-title">Тестирование: {displayName}</h1>
	</div>

	<div class="tests-grid">
		{#if !loaded}
			<div class="card test-card"><h3>Загрузка...</h3></div>
		{:else if !subscription}
			<div class="card test-card"><h3>Тестирование недоступно</h3><p class="test-desc">Подписка не найдена.</p></div>
		{:else if !kernelIface || !selectorTag}
			<div class="card test-card"><h3>Тестирование недоступно</h3><p class="test-desc">Для подписки не удалось определить интерфейс тестирования.</p></div>
		{:else}
			<div class="card test-card">
				<h3>
					<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="20" height="20">
						<path d="M22 11.08V12a10 10 0 1 1-5.93-9.14"/>
						<polyline points="22 4 12 14.01 9 11.01"/>
					</svg>
					Проверка соединения
				</h3>
				<p class="test-desc">Проверить доступ в интернет через подписку.</p>
				{#if connectivityResult}
					<div class="test-result">
						{#if connectivityResult.connected}
							<span class="result-success">Подключено</span>
						{:else}
							<span class="result-error">Нет соединения</span>
						{/if}
						{#if connectivityResult.latency}<span class="result-detail">Задержка: {connectivityResult.latency} мс</span>{/if}
						{#if connectivityResult.reason}<span class="result-detail">Причина: {connectivityResult.reason}</span>{/if}
					</div>
				{/if}
				<div class="card-spacer"></div>
				<Button variant="primary" fullWidth onclick={checkConnectivity} loading={connectivityLoading}>Проверить соединение</Button>
			</div>

			<div class="card test-card">
				<h3>
					<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="20" height="20">
						<circle cx="12" cy="12" r="10"/>
						<line x1="2" y1="12" x2="22" y2="12"/>
						<path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z"/>
					</svg>
					Проверка IP
				</h3>
				<p class="test-desc">Убедиться, что IP меняется при использовании подписки.</p>
				<div class="server-section">
					<div class="server-header">
						<span class="server-label">Сервис</span>
						<FormToggle bind:checked={useCustomService} disabled={ipLoading} label="Свой" size="sm" />
					</div>
					{#if useCustomService || ipServices.length === 0}
						<input type="text" placeholder="https://example.com/ip" bind:value={customServiceURL} disabled={ipLoading} />
					{:else}
						{@const serviceOpts: DropdownOption[] = ipServices.map((service, i) => ({ value: String(i), label: service.label }))}
						<Dropdown value={String(selectedServiceIndex)} options={serviceOpts} onchange={(v) => (selectedServiceIndex = Number(v))} disabled={ipLoading} fullWidth />
					{/if}
				</div>
				{#if ipResult}
					<div class="test-result ip-result">
						<div class="ip-row"><span class="ip-label">Прямой IP:</span><span class="ip-value">{ipResult.directIp}</span></div>
						<div class="ip-row"><span class="ip-label">VPN IP:</span><span class="ip-value">{ipResult.vpnIp}</span></div>
						<div class="ip-status">{#if ipResult.ipChanged}<span class="result-success">IP изменился — туннель работает!</span>{:else}<span class="result-warning">IP не изменился</span>{/if}</div>
					</div>
				{/if}
				<div class="card-spacer"></div>
				<Button variant="primary" fullWidth onclick={checkIP} loading={ipLoading}>Проверить IP</Button>
			</div>

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
						iperf3 не найден. Доступно только на NDMS 5.x.
					</p>
				{:else}
					<p class="test-desc">Измерить скорость через подписку с помощью iperf3.</p>

					<div class="server-section">
						<div class="server-header">
							<span class="server-label">Сервер</span>
							<FormToggle bind:checked={useCustomServer} disabled={isRunning} label="Свой" size="sm" />
						</div>
						{#if useCustomServer}
							<input type="text" placeholder="host:port (порт по умолчанию 5201)" bind:value={customServer} disabled={isRunning} />
						{:else}
							{@const serverOpts: DropdownOption[] = speedTestInfo.servers.map((server, i) => ({ value: String(i), label: server.label }))}
							<Dropdown value={String(selectedServerIndex)} options={serverOpts} onchange={(v) => (selectedServerIndex = Number(v))} disabled={isRunning} fullWidth />
						{/if}
					</div>

					{#if speedPhase === 'download'}
						<div class="test-result">
							<div class="progress-steps">
								<div class="step active">
									<span class="spinner step-spinner"></span>
									<span>Загрузка</span>
									{#if currentSecond > 0}
										<span class="live-bw">{formatBandwidth(currentBandwidth)} Мбит/с</span>
									{:else}
										<span class="live-hint">подключение...</span>
									{/if}
								</div>
								<div class="progress-bar">
									<div class="progress-fill download" style="width: {Math.min(currentSecond * 10, 100)}%"></div>
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
									<span>Отдача</span>
									{#if currentSecond > 0}
										<span class="live-bw">{formatBandwidth(currentBandwidth)} Мбит/с</span>
									{:else}
										<span class="live-hint">подключение...</span>
									{/if}
								</div>
								<div class="progress-bar">
									<div class="progress-fill upload" style="width: {Math.min(currentSecond * 10, 100)}%"></div>
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
					<Button variant="primary" fullWidth onclick={runSpeedTest} disabled={isRunning} loading={isRunning}>
						{speedPhase === 'done' || speedPhase === 'error' ? 'Повторить тест' : 'Начать тест'}
					</Button>
				{/if}
			</div>
		{/if}
	</div>
</PageContainer>

<style>
	.test-page-header { justify-content: flex-start; gap: 1rem; }
	.back-link { display: flex; align-items: center; gap: 0.25rem; color: var(--text-secondary); font-size: 0.875rem; }
	.back-link:hover { color: var(--text-primary); }
	.tests-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(300px, 1fr)); gap: 1rem; }
	.test-card { display: flex; flex-direction: column; gap: 1rem; }
	.card-title { display: flex; align-items: center; gap: 0.5rem; font-size: 1rem; white-space: nowrap; margin: 0; }
	.test-card h3 { display: flex; align-items: center; gap: 0.5rem; font-size: 1rem; margin: 0; }
	.test-desc { color: var(--text-muted); font-size: 0.875rem; margin: 0; }
	.loading-placeholder { display: flex; align-items: center; justify-content: center; padding: 2rem 0; }
	.unavailable { display: flex; align-items: flex-start; gap: 0.5rem; color: var(--text-muted); font-style: italic; padding: 0.75rem; background: var(--bg-tertiary); border-radius: var(--radius-sm); }
	.unavailable svg { flex-shrink: 0; margin-top: 1px; }
	.test-result { padding: 1rem; background: var(--bg-tertiary); border-radius: var(--radius-sm); }
	.result-success { color: var(--success); font-weight: 500; }
	.result-error { color: var(--error); font-weight: 500; }
	.result-warning { color: var(--warning); font-weight: 500; }
	.result-detail { display: block; margin-top: 0.5rem; font-size: 0.875rem; color: var(--text-secondary); }
	.ip-result { display: flex; flex-direction: column; gap: 0.5rem; }
	.ip-row { display: flex; justify-content: space-between; font-size: 0.875rem; }
	.ip-label { color: var(--text-muted); }
	.ip-value { font-family: monospace; }
	.ip-status { margin-top: 0.5rem; padding-top: 0.5rem; border-top: 1px solid var(--border); font-size: 0.875rem; }
	.server-section { display: flex; flex-direction: column; gap: 0.5rem; }
	.server-header { display: flex; align-items: center; justify-content: space-between; }
	.server-label { font-size: 0.8125rem; font-weight: 500; color: var(--text-secondary); text-transform: uppercase; letter-spacing: 0.03em; }
	.progress-steps { display: flex; flex-direction: column; gap: 0.75rem; }
	.step { display: flex; align-items: center; gap: 0.625rem; font-size: 0.875rem; }
	.step.active { color: var(--accent); font-weight: 500; }
	.step.pending { color: var(--text-muted); }
	.step.completed { color: var(--success); }
	.step.completed strong { color: var(--text-primary); }
	.step-spinner { width: 14px; height: 14px; flex-shrink: 0; }
	.live-bw { margin-left: auto; font-size: 1.125rem; font-weight: 600; font-variant-numeric: tabular-nums; color: var(--text-primary); }
	.live-hint { margin-left: auto; font-size: 0.8125rem; color: var(--text-muted); font-style: italic; }
	.progress-bar { height: 4px; background: var(--bg-tertiary); border-radius: 2px; overflow: hidden; }
	.progress-fill { height: 100%; border-radius: 2px; transition: width 0.5s ease; }
	.progress-fill.download { background: var(--success); }
	.progress-fill.upload { background: var(--accent); }
	.step-dot { width: 14px; height: 14px; display: flex; align-items: center; justify-content: center; flex-shrink: 0; }
	.step-dot::after { content: ''; width: 6px; height: 6px; border-radius: 50%; background: var(--text-muted); }
	.results-panel { display: flex; flex-direction: column; gap: 0; }
	.result-row { display: flex; align-items: center; gap: 0.75rem; padding: 0.5rem 0; }
	.result-icon { width: 32px; height: 32px; display: flex; align-items: center; justify-content: center; border-radius: 6px; font-weight: 700; font-size: 1.125rem; flex-shrink: 0; }
	.result-icon.download { background: rgba(158, 206, 106, 0.12); color: var(--success); }
	.result-icon.upload { background: rgba(122, 162, 247, 0.12); color: var(--accent); }
	.result-info { flex: 1; min-width: 0; }
	.result-label { font-size: 0.875rem; color: var(--text-secondary); }
	.result-value { font-size: 1.25rem; font-weight: 600; font-variant-numeric: tabular-nums; color: var(--text-primary); line-height: 1; white-space: nowrap; }
	.result-value small { font-size: 0.75rem; font-weight: 500; color: var(--text-muted); margin-left: 0.25rem; }
	.result-value.na { font-size: 1rem; color: var(--text-muted); }
	.result-divider { border: none; border-top: 1px solid var(--border); margin: 0.5rem 0; }
	.result-meta { font-size: 0.8125rem; color: var(--text-muted); text-align: center; padding: 0.25rem 0; }
	.result-meta-warn { color: var(--warning); }
	.error-panel { display: flex; align-items: center; gap: 0.75rem; color: var(--error); font-size: 0.875rem; }
	.error-panel svg { flex-shrink: 0; }
	.servers-link { display: block; text-align: center; font-size: 0.8125rem; color: var(--text-muted); text-decoration: none; margin-top: auto; padding: 0.25rem; border-radius: var(--radius-sm); transition: color 0.15s ease, background 0.15s ease; }
	.servers-link:hover { color: var(--text-secondary); background: var(--bg-tertiary); }
	.card-spacer { flex: 1; }
</style>
