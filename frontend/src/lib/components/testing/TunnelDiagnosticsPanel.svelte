<script lang="ts">
	import { onDestroy, onMount } from 'svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import type {
		IPResult,
		ConnectivityResult,
		IPCheckService,
		SpeedTestInfo,
		SpeedTestResult,
	} from '$lib/types';
	import { BackLink, FormToggle, Button, Dropdown, SpeedGauge, type DropdownOption } from '$lib/components/ui';
	import { CircleCheck, CircleX, Globe, Zap, CircleAlert, Check, ChevronDown } from 'lucide-svelte';
	import { PageContainer } from '$lib/components/layout';
	import TunnelTestIcon from '$lib/components/tunnels/TunnelTestIcon.svelte';

	type DiagnosticsKind = 'awg' | 'system' | 'singbox' | 'subscription';
	type DiagnosticsSubjectLabel = 'туннель' | 'подписку';
	type DiagnosticsMode = 'page' | 'modal';

	interface Props {
		kind: DiagnosticsKind;
		targetId: string;
		displayName: string;
		backHref: string;
		backLabel: string;
		subjectLabel: DiagnosticsSubjectLabel;
		iface?: string;
		loading?: boolean;
		unavailableReason?: string;
		mode?: DiagnosticsMode;
	}

	let {
		kind,
		targetId,
		displayName,
		backHref,
		backLabel,
		subjectLabel,
		iface,
		loading = false,
		unavailableReason,
		mode = 'page',
	}: Props = $props();

	// IP check services
	let ipServices = $state<IPCheckService[]>([]);
	let selectedServiceIndex = $state(0);
	let customServiceURL = $state('');
	let useCustomService = $state(false);

	// Connectivity test
	let connectivityLoading = $state(false);
	let connectivityResult: ConnectivityResult | null = $state(null);

	// IP test
	let ipLoading = $state(false);
	let ipResult: IPResult | null = $state(null);

	// Speed test
	let speedTestInfo = $state<SpeedTestInfo | null>(null);
	let infoLoading = $state(true);
	let selectedServerIndex = $state(0);
	let customServer = $state('');
	let useCustomServer = $state(false);
	let speedPhase = $state<'idle' | 'ping' | 'download' | 'upload' | 'done' | 'error' | 'cancelled'>('idle');
	let downloadResult: SpeedTestResult | null = $state(null);
	let uploadResult: SpeedTestResult | null = $state(null);
	let speedError: string | null = $state(null);
	let currentBandwidth = $state(0);
	let currentSecond = $state(0);
	let activeEventSource: EventSource | null = $state(null);

	const AUX_TESTS_STORAGE_KEY = 'awg-manager:tunnel-diagnostics:aux-tests-open';
	let auxTestsOpen = $state(false);

	function loadAuxTestsPreference(): void {
		if (typeof window === 'undefined') return;

		try {
			auxTestsOpen = window.localStorage.getItem(AUX_TESTS_STORAGE_KEY) === 'true';
		} catch {
			// localStorage can be unavailable; keep the default collapsed state.
		}
	}

	function saveAuxTestsPreference(open: boolean): void {
		if (typeof window === 'undefined') return;

		try {
			window.localStorage.setItem(AUX_TESTS_STORAGE_KEY, open ? 'true' : 'false');
		} catch {
			// Ignore storage errors.
		}
	}

	function handleAuxTestsToggle(event: Event): void {
		const details = event.currentTarget as HTMLDetailsElement;
		auxTestsOpen = details.open;
		saveAuxTestsPreference(auxTestsOpen);
	}

	const TOTAL_SECONDS = 10;

	let selectedServer = $derived(speedTestInfo?.servers[selectedServerIndex] ?? null);

	let gaugeMax = $derived.by(() => {
		const dBw = downloadResult ? downloadResult.bandwidth : 0;
		const uBw = uploadResult ? uploadResult.bandwidth : 0;
		return Math.max(25, currentBandwidth * 1.15, dBw * 1.2, uBw * 1.2);
	});
	const gaugePhase = $derived<'idle' | 'download' | 'upload' | 'done'>(
		speedPhase === 'download'
			? 'download'
			: speedPhase === 'upload'
				? 'upload'
				: speedPhase === 'done'
					? 'done'
					: 'idle',
	);
	const gaugeLabel = $derived(
		gaugePhase === 'download'
			? 'ЗАГРУЗКА'
			: gaugePhase === 'upload'
				? 'ОТДАЧА'
				: gaugePhase === 'done'
					? 'ГОТОВО'
					: '',
	);

	let displayDownload = $derived.by(() => {
		if (speedPhase === 'download' && currentBandwidth > 0) return currentBandwidth;
		return downloadResult ? downloadResult.bandwidth : null;
	});
	let displayUpload = $derived.by(() => {
		if (speedPhase === 'upload' && currentBandwidth > 0) return currentBandwidth;
		return uploadResult ? uploadResult.bandwidth : null;
	});
	let progressPct = $derived((speedPhase === 'download' || speedPhase === 'upload') && currentSecond > 0 ? Math.min(100, (currentSecond / TOTAL_SECONDS) * 100) : 0);

	let isRunning = $derived(speedPhase === 'ping' || speedPhase === 'download' || speedPhase === 'upload');

	let diagnosticsTitlePrefix = $derived.by(() => {
		if (kind === 'awg') return 'AWG';
		if (kind === 'system') return 'AWG';
		if (kind === 'singbox') return 'Sing-box';
		return 'subscription';
	});

	onMount(async () => {
		loadAuxTestsPreference();

		try {
			ipServices = await api.getIPCheckServices();
		} catch {
			// fallback mode with custom service
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
		if (!targetId || unavailableReason) return;
		connectivityLoading = true;
		connectivityResult = null;
		try {
			if (kind === 'awg') {
				connectivityResult = await api.checkConnectivity(targetId);
			} else if (kind === 'system') {
				connectivityResult = await api.checkSystemTunnelConnectivity(targetId);
			} else if (kind === 'subscription') {
				connectivityResult = await api.singboxCheckConnectivity(targetId, iface);
			} else {
				connectivityResult = await api.singboxCheckConnectivity(targetId);
			}
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка проверки соединения');
		} finally {
			connectivityLoading = false;
		}
	}

	async function checkIP() {
		if (!targetId || unavailableReason) return;

		const shouldUseCustomService = useCustomService || (kind === 'subscription' && ipServices.length === 0);

		let serviceURL = '';
		if (shouldUseCustomService) {
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
			if (kind === 'awg') {
				ipResult = await api.checkIP(targetId, serviceURL || undefined);
			} else if (kind === 'system') {
				ipResult = await api.checkSystemTunnelIP(targetId, serviceURL || undefined);
			} else if (kind === 'subscription') {
				ipResult = await api.singboxCheckIP(targetId, serviceURL || undefined, iface);
			} else {
				ipResult = await api.singboxCheckIP(targetId, serviceURL || undefined);
			}
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
		if (lastColon === -1) {
			return { host: val, port: 5201 };
		}

		const host = val.substring(0, lastColon);
		const port = parseInt(val.substring(lastColon + 1), 10);
		if (isNaN(port) || port < 1 || port > 65535) {
			return { host, port: 5201 };
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
		if (msg.includes('no IPv4 address') || msg.includes('no kernel interface') || (msg.includes('interface') && msg.includes('not found'))) {
			return 'Интерфейс туннеля недоступен';
		}
		return msg;
	}

	function formatMetric(mbps: number | null): string {
		if (mbps === null) return '—';
		return mbps.toFixed(mbps >= 10 ? 1 : 2);
	}

	type StepState = 'pending' | 'active' | 'done';
	function stepState(step: 'ping' | 'download' | 'upload'): StepState {
		const order = ['ping', 'download', 'upload'];
		const curIdx = speedPhase === 'done' ? 3 : order.indexOf(speedPhase);
		const stepIdx = order.indexOf(step);
		if (curIdx < 0) return 'pending';
		if (stepIdx < curIdx) return 'done';
		if (stepIdx === curIdx) return 'active';
		return 'pending';
	}

	function cancelSpeedTest(): void {
		activeEventSource?.close();
		activeEventSource = null;
		speedPhase = 'cancelled';
		currentBandwidth = 0;
		currentSecond = 0;
		notifications.info('Тест скорости отменён');
	}

	function runSystemStreamPhase(
		server: string,
		port: number,
		direction: 'download' | 'upload',
	): Promise<SpeedTestResult> {
		return new Promise((resolve, reject) => {
			currentBandwidth = 0;
			currentSecond = 0;
			activeEventSource = api.systemTunnelSpeedTestStream(
				targetId,
				server,
				port,
				direction,
				(interval) => {
					currentBandwidth = interval.bandwidth;
					currentSecond = interval.second;
				},
				(result) => {
					activeEventSource = null;
					resolve(result);
				},
				(error) => {
					activeEventSource = null;
					reject(new Error(error));
				},
			);
		});
	}

	function runAwgStreamPhase(
		server: string,
		port: number,
		direction: 'download' | 'upload',
	): Promise<SpeedTestResult> {
		return new Promise((resolve, reject) => {
			currentBandwidth = 0;
			currentSecond = 0;
			activeEventSource = api.speedTestStream(
				targetId,
				server,
				port,
				direction,
				(interval) => {
					currentBandwidth = interval.bandwidth;
					currentSecond = interval.second;
				},
				(result) => {
					activeEventSource = null;
					resolve(result);
				},
				(error) => {
					activeEventSource = null;
					reject(new Error(error));
				},
			);
		});
	}

	function runSingboxStream(server: string, port: number): Promise<void> {
		return new Promise((resolve, reject) => {
			currentBandwidth = 0;
			currentSecond = 0;
			activeEventSource = api.singboxSpeedTestStream(
				targetId,
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
					const normalized: SpeedTestResult = {
						server,
						direction: result.phase === 'upload' ? 'upload' : 'download',
						bandwidth: result.bandwidth,
						bytes: result.bytes,
						duration: result.duration,
						retransmits: 0,
					};

					if (normalized.direction === 'download') {
						downloadResult = normalized;
					} else {
						uploadResult = normalized;
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
				iface,
			);
		});
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

		speedPhase = 'ping';
		downloadResult = null;
		uploadResult = null;
		speedError = null;
		currentBandwidth = 0;
		currentSecond = 0;

		if (kind === 'awg' || kind === 'system') {
			const runPhase = kind === 'system' ? runSystemStreamPhase : runAwgStreamPhase;

			speedPhase = 'download';
			try {
				downloadResult = await runPhase(server, port, 'download');
			} catch (e) {
				const raw = e instanceof Error ? e.message : 'Ошибка теста скорости';
				speedError = friendlyError(raw);
				speedPhase = 'error';
				return;
			}

			speedPhase = 'upload';
			currentBandwidth = 0;
			currentSecond = 0;
			try {
				uploadResult = await runPhase(server, port, 'upload');
			} catch (e) {
				const raw = e instanceof Error ? e.message : 'Ошибка теста скорости';
				speedError = friendlyError(raw);
			}
			speedPhase = 'done';
		} else {
			try {
				await runSingboxStream(server, port);
				speedPhase = 'done';
			} catch (e) {
				const raw = e instanceof Error ? e.message : 'Ошибка теста скорости';
				speedError = friendlyError(raw);
				speedPhase = 'error';
			}
		}
	}

</script>

{#snippet connectivityCard()}
	<div class="card test-card">
		<h3>
			<TunnelTestIcon size={20} />
			Проверка соединения
		</h3>

		<p class="test-desc">Проверить доступ в интернет через {subjectLabel}.</p>

		{#if connectivityResult}
			<div class="test-result">
				{#if kind === 'awg'}
					{#if connectivityResult.connected}
						<span class="result-success">
							<CircleCheck size={24} aria-hidden="true" />
							Подключено
						</span>
					{:else}
						<span class="result-error">
							<CircleX size={24} aria-hidden="true" />
							Нет соединения
						</span>
					{/if}
				{:else if connectivityResult.connected}
					<span class="result-success">Подключено</span>
				{:else}
					<span class="result-error">Нет соединения</span>
				{/if}

				{#if connectivityResult.latency}
					<span class="result-detail">Задержка: {connectivityResult.latency} мс</span>
				{/if}

				{#if connectivityResult.reason}
					<span class="result-detail">Причина: {connectivityResult.reason}</span>
				{/if}
			</div>
		{/if}

		<div class="card-spacer"></div>

		<Button
			variant="primary"
			fullWidth
			onclick={checkConnectivity}
			loading={connectivityLoading}
		>
			Проверить соединение
		</Button>
	</div>
{/snippet}

{#snippet ipCard()}
	<div class="card test-card">
		<h3>
			<Globe size={20} aria-hidden="true" />
			Проверка IP
		</h3>

		<p class="test-desc">Убедиться, что IP меняется при использовании {subjectLabel}.</p>

		{#if kind === 'subscription' || ipServices.length > 0}
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

				{#if useCustomService || ipServices.length === 0}
					<input
						type="text"
						placeholder="https://example.com/ip"
						bind:value={customServiceURL}
						disabled={ipLoading}
					/>
				{:else}
					{@const serviceOpts: DropdownOption[] = ipServices.map((service, i) => ({
						value: String(i),
						label: service.label,
					}))}

					<Dropdown
						value={String(selectedServiceIndex)}
						options={serviceOpts}
						onchange={(v) => (selectedServiceIndex = Number(v))}
						disabled={ipLoading}
						fullWidth
					/>
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

		<Button
			variant="primary"
			fullWidth
			onclick={checkIP}
			loading={ipLoading}
		>
			Проверить IP
		</Button>
	</div>
{/snippet}

{#snippet speedCard()}
	<div class="card test-card">
		<h3 class="card-title">
			<Zap size={20} aria-hidden="true" />
			Тест скорости
		</h3>

		{#if infoLoading}
			<div class="loading-placeholder">
				<span class="spinner"></span>
			</div>
		{:else if !speedTestInfo?.available}
			<p class="test-desc unavailable">
				<CircleAlert size={16} aria-hidden="true" />
				iperf3 не найден. Доступно только на NDMS 5.x.
			</p>
		{:else if kind === 'singbox' && !iface}
			<p class="test-desc unavailable">
				<CircleAlert size={16} aria-hidden="true" />
				Тест iperf для sing-box туннеля требует включённого NDMS Proxy
				(нужен kernel-интерфейс t2sX для bind).
			</p>
		{:else}
			<p class="test-desc">Измерить скорость через {subjectLabel} с помощью iperf3.</p>

			<div class="speed-test-panel">
				<div class="metrics">
					<div class="metric">
						<div class="m-label">ЗАГРУЗКА</div>
						<div class="m-value" class:has-value={displayDownload !== null}>
							{formatMetric(displayDownload)}<span class="m-unit">Mbps</span>
						</div>
					</div>

					<div class="metric">
						<div class="m-label">ОТДАЧА</div>
						<div class="m-value upload" class:has-value={displayUpload !== null}>
							{formatMetric(displayUpload)}<span class="m-unit">Mbps</span>
						</div>
					</div>
				</div>

				<div class="speed-gauge-compact">
					<SpeedGauge value={currentBandwidth} max={gaugeMax} phase={gaugePhase} label={gaugeLabel} />
				</div>

				{#if isRunning || speedPhase === 'done'}
					<div class="step-row">
						{#each [
							{ key: 'ping', label: 'Пинг' },
							{ key: 'download', label: 'Загрузка' },
							{ key: 'upload', label: 'Отдача' },
						] as s}
							{@const st = stepState(s.key as 'ping' | 'download' | 'upload')}

							<div class="speed-step" class:active={st === 'active'} class:done={st === 'done'}>
								{#if st === 'done'}
									<Check class="step-mark" size={12} strokeWidth={3} aria-hidden="true" />
								{:else if st === 'active'}
									<span class="speed-step-spinner"></span>
								{:else}
									<span class="speed-step-dot"></span>
								{/if}

								<span class="step-label">{s.label}</span>
							</div>
						{/each}
					</div>

					{#if speedPhase === 'ping'}
						<div class="progress-hint">Устанавливаем соединение…</div>
					{:else if speedPhase === 'download' || speedPhase === 'upload'}
						<div class="progress-track">
							<div
								class="speed-progress-fill"
								class:download={speedPhase === 'download'}
								class:upload={speedPhase === 'upload'}
								style="width: {progressPct}%"
							></div>
						</div>

						<div class="progress-hint">
							{#if currentSecond > 0}
								{currentSecond} / {TOTAL_SECONDS} сек
							{:else}
								подключение…
							{/if}
						</div>
					{/if}
				{/if}

				{#if speedPhase === 'cancelled'}
					<div class="hint hint-muted">Тест отменён. Можно запустить заново.</div>
				{/if}

				{#if speedPhase === 'error' && speedError}
					<div class="speed-error">{speedError}</div>
				{/if}

				{#if speedPhase === 'done' && !uploadResult && speedError}
					<div class="hint hint-warning">{speedError}</div>
				{/if}

				{#if uploadResult && uploadResult.retransmits > 0}
					<div class="hint hint-muted">Ретрансмиты: {uploadResult.retransmits}</div>
				{/if}
			</div>

			<div class="server-section">
				<div class="server-header">
					<span class="server-label">Сервер</span>
					<FormToggle
						bind:checked={useCustomServer}
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
					{@const serverOpts: DropdownOption[] = speedTestInfo.servers.map((server, i) => ({
						value: String(i),
						label: server.label,
					}))}

					<Dropdown
						value={String(selectedServerIndex)}
						options={serverOpts}
						onchange={(v) => (selectedServerIndex = Number(v))}
						disabled={isRunning}
						fullWidth
					/>
				{/if}
			</div>

			<div class="card-spacer"></div>

			<a
				class="servers-link"
				href="https://iperf3serverlist.net"
				target="_blank"
				rel="noopener noreferrer"
			>
				Публично доступные серверы iperf3 ↗
			</a>

			{#if isRunning}
				<Button variant="ghost" fullWidth onclick={cancelSpeedTest}>
					Отмена
				</Button>
			{:else}
				<Button
					variant="primary"
					fullWidth
					onclick={runSpeedTest}
					disabled={!selectedServer && !useCustomServer}
				>
					{speedPhase === 'idle'
						? 'Запустить'
						: speedPhase === 'cancelled'
							? 'Запустить заново'
							: 'Повторить'}
				</Button>
			{/if}
		{/if}
	</div>
{/snippet}

{#snippet diagnosticsBody()}
	<div class="tests-grid" class:modal-grid={mode === 'modal'}>
		{#if loading}
			<div class="card test-card">
				<h3>Загрузка...</h3>
				<p class="test-desc">Проверяем доступность тестирования.</p>
			</div>
		{:else if unavailableReason}
			<div class="card test-card">
				<h3>Тестирование недоступно</h3>
				<p class="test-desc">{unavailableReason}</p>
			</div>
		{:else}
			{#if mode === 'modal'}
				{@render speedCard()}

				<details class="aux-tests" open={auxTestsOpen} ontoggle={handleAuxTestsToggle}>
					<summary class="aux-tests-summary">
						<span class="aux-tests-copy">
							<span class="aux-tests-title">Дополнительные проверки</span>
							<span class="aux-tests-desc">Проверка соединения и Проверка IP</span>
						</span>

						<ChevronDown class="aux-tests-chevron" size={16} aria-hidden="true" />
					</summary>

					<div class="aux-tests-content">
						{@render connectivityCard()}
						{@render ipCard()}
					</div>
				</details>
			{:else}
				{@render connectivityCard()}
				{@render ipCard()}
				{@render speedCard()}
			{/if}
		{/if}
	</div>
{/snippet}

{#if mode === 'page'}
	<PageContainer>
		<div class="page-header test-page-header">
			<BackLink href={backHref} label={backLabel} />

			<h1 class="page-title">{diagnosticsTitlePrefix} тестирование: {displayName}</h1>
		</div>

		{@render diagnosticsBody()}
	</PageContainer>
{:else}
	<div class="diagnostics-modal-panel">
		{@render diagnosticsBody()}
	</div>
{/if}


<style>
	.test-page-header {
		justify-content: flex-start;
		gap: 1rem;
	}

	.tests-grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(300px, 1fr));
		gap: 1rem;
	}

	.tests-grid.modal-grid {
		grid-template-columns: 1fr;
	}

	.diagnostics-modal-panel {
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}

	.test-card {
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}

	.card-title {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		font-size: 1rem;
		white-space: nowrap;
		margin: 0;
	}

	.test-card h3 {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		font-size: 1rem;
		margin: 0;
	}

	.test-desc {
		color: var(--text-muted);
		font-size: 0.875rem;
		margin: 0;
	}

	.loading-placeholder {
		display: flex;
		align-items: center;
		justify-content: center;
		padding: 2rem 0;
	}

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

	.unavailable :global(svg) {
		flex-shrink: 0;
		margin-top: 1px;
	}

	.test-result {
		padding: 1rem;
		background: var(--bg-tertiary);
		border-radius: var(--radius-sm);
	}

	.result-success {
		color: var(--success);
		font-weight: 500;
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}

	.result-error {
		color: var(--error);
		font-weight: 500;
		display: flex;
		align-items: center;
		gap: 0.5rem;
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

	.speed-test-panel {
		display: flex;
		flex-direction: column;
		gap: 16px;
		padding: 8px 0;
	}

	.metrics {
		display: grid;
		grid-template-columns: repeat(2, 1fr);
		gap: 12px;
		padding-bottom: 12px;
		border-bottom: 1px solid var(--border);
	}

	.metric {
		display: flex;
		flex-direction: column;
		gap: 4px;
	}

	.m-label {
		font-size: 0.7rem;
		color: var(--text-muted);
		letter-spacing: 0.1em;
		font-weight: 600;
	}

	.m-value {
		font-size: 1.6rem;
		font-weight: 600;
		font-variant-numeric: tabular-nums;
		color: var(--text-primary);
	}

	.m-value.has-value {
		color: var(--success);
	}

	.m-value.upload.has-value {
		color: var(--accent);
	}

	.m-unit {
		font-size: 0.75rem;
		color: var(--text-muted);
		margin-left: 4px;
		font-weight: normal;
	}

	.step-row {
		display: flex;
		justify-content: center;
		gap: 18px;
		padding: 4px 0;
	}

	.speed-step {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		font-size: 12px;
		color: var(--text-muted);
		font-weight: 500;
	}

	.speed-step.active {
		color: var(--accent);
	}

	.speed-step.done {
		color: var(--success);
	}

	.speed-step-dot {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		background: currentColor;
		opacity: 0.35;
	}

	.speed-step.active .speed-step-dot {
		opacity: 1;
	}

	:global(.step-mark) {
		color: var(--success);
	}

	.speed-step-spinner {
		width: 10px;
		height: 10px;
		border-radius: 50%;
		border: 2px solid currentColor;
		border-top-color: transparent;
		animation: speed-spin 0.8s linear infinite;
	}

	@keyframes speed-spin {
		to {
			transform: rotate(360deg);
		}
	}

	.progress-track {
		height: 4px;
		background: var(--bg-secondary);
		border-radius: 2px;
		overflow: hidden;
	}

	.speed-progress-fill {
		height: 100%;
		background: var(--text-muted);
		transition: width 0.25s linear;
	}

	.speed-progress-fill.download {
		background: var(--success);
	}

	.speed-progress-fill.upload {
		background: var(--accent);
	}

	.progress-hint {
		font-size: 11px;
		color: var(--text-muted);
		text-align: center;
		letter-spacing: 0.05em;
	}

	.speed-error {
		padding: 8px 12px;
		background: rgba(239, 68, 68, 0.08);
		border-left: 2px solid var(--error);
		border-radius: 3px;
		font-size: 12px;
		color: var(--error);
	}

	.hint {
		padding: 6px 10px;
		font-size: 12px;
		border-radius: 3px;
	}

	.hint-muted {
		background: var(--bg-secondary);
		color: var(--text-muted);
	}

	.hint-warning {
		background: rgba(245, 158, 11, 0.08);
		color: var(--warning);
	}

	.servers-link {
		display: block;
		text-align: center;
		font-size: 0.8125rem;
		color: var(--text-muted);
		text-decoration: none;
		margin-top: auto;
		padding: 0.25rem;
		border-radius: var(--radius-sm);
		transition: color 0.15s ease, background 0.15s ease;
	}

	.servers-link:hover {
		color: var(--text-secondary);
		background: var(--bg-tertiary);
	}

	.card-spacer {
		flex: 1;
	}

	.speed-gauge-compact {
		width: min(220px, 72%);
		max-width: 220px;
		margin: -8px auto -14px;
	}

	.speed-gauge-compact :global(.value) {
		font-size: 2.1rem;
	}

	.speed-gauge-compact :global(.unit) {
		font-size: 0.72rem;
		margin-top: 2px;
	}

	.speed-gauge-compact :global(.phase-label) {
		margin-top: 4px;
		font-size: 0.68rem;
	}

	:global(.diagnostics-modal-panel) .speed-gauge-compact {
		width: min(190px, 68%);
		max-width: 190px;
		margin: -12px auto -18px;
	}

	.tests-grid.modal-grid {
		gap: 0.75rem;
	}

	:global(.diagnostics-modal-panel) .test-card {
		gap: 0.75rem;
	}

	:global(.diagnostics-modal-panel) .speed-test-panel {
		gap: 10px;
		padding: 2px 0;
	}

	:global(.diagnostics-modal-panel) .metrics {
		padding-bottom: 8px;
	}

	:global(.diagnostics-modal-panel) .m-value {
		font-size: 1.35rem;
	}

	.aux-tests {
		border: 1px solid var(--border);
		border-radius: var(--radius-md, 10px);
		background: var(--bg-secondary);
		overflow: hidden;
	}

	.aux-tests-summary {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.75rem;
		padding: 0.8rem 1rem;
		cursor: pointer;
		user-select: none;
		list-style: none;
	}

	.aux-tests-summary::-webkit-details-marker {
		display: none;
	}

	.aux-tests-summary:hover {
		background: var(--bg-tertiary);
	}

	.aux-tests-copy {
		display: flex;
		flex-direction: column;
		gap: 0.15rem;
		min-width: 0;
	}

	.aux-tests-title {
		font-size: 0.95rem;
		font-weight: 600;
		color: var(--text-primary);
	}

	.aux-tests-desc {
		font-size: 0.78rem;
		color: var(--text-muted);
	}

	:global(.aux-tests-chevron) {
		flex-shrink: 0;
		color: var(--text-muted);
		transition: transform 0.15s ease;
	}

	.aux-tests[open] :global(.aux-tests-chevron) {
		transform: rotate(180deg);
	}

	.aux-tests-content {
		display: grid;
		grid-template-columns: 1fr;
		gap: 0.75rem;
		padding: 0 0.75rem 0.75rem;
		border-top: 1px solid var(--border);
	}

	.aux-tests-content .test-card {
		margin-top: 0.75rem;
	}
</style>
