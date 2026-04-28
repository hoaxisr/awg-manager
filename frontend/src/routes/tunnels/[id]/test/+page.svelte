<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/stores';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import type { IPResult, ConnectivityResult, AWGTunnel, IPCheckService } from '$lib/types';
	import { SpeedTestCard } from '$lib/components/tunnels';
	import { FormToggle, Button, Dropdown, type DropdownOption } from '$lib/components/ui';
	import { PageContainer } from '$lib/components/layout';

	let tunnelId = $derived($page.params.id as string);

	// Tunnel data for display
	let tunnel: AWGTunnel | null = $state(null);
	let displayName = $derived((tunnel as AWGTunnel | null)?.name ?? tunnelId);

	// IP check services
	let ipServices = $state<IPCheckService[]>([]);
	let selectedServiceIndex = $state(0);
	let customServiceURL = $state('');
	let useCustomService = $state(false);

	onMount(async () => {
		try {
			tunnel = await api.getTunnel(tunnelId);
		} catch (e) {
			// Fallback to tunnelId if fetch fails
		}
		try {
			ipServices = await api.getIPCheckServices();
		} catch (e) {
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
		if (!tunnelId) return;
		connectivityLoading = true;
		connectivityResult = null;
		try {
			connectivityResult = await api.checkConnectivity(tunnelId);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка проверки соединения');
		} finally {
			connectivityLoading = false;
		}
	}

	async function checkIP() {
		if (!tunnelId) return;

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
			ipResult = await api.checkIP(tunnelId, serviceURL || undefined);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка проверки IP');
		} finally {
			ipLoading = false;
		}
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
	<h1 class="page-title">Тестирование: {displayName}</h1>
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
		<Button variant="primary" fullWidth onclick={checkConnectivity} loading={connectivityLoading}>
			Проверить соединение
		</Button>
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

		<!-- Service selection -->
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
		<Button variant="primary" fullWidth onclick={checkIP} loading={ipLoading}>
			Проверить IP
		</Button>
	</div>

	<!-- Speed Test -->
	<SpeedTestCard {tunnelId} />
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

	/* Server/service selection section */
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
</style>
