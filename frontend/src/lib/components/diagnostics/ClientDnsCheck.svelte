<script lang="ts">
	import { Button, StatusDot } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import type { DnsCheckResult } from '$lib/types';
	import ChecksGroup, { type GroupLed } from './ChecksGroup.svelte';

	type CheckStatus = 'pending' | 'ok' | 'fail' | 'warning';

	interface CheckRow {
		id: string;
		title: string;
		status: CheckStatus;
		message: string;
		detail?: string;
	}

	let running = $state(false);
	let clientIP = $state('');
	let resolveCheck = $state<CheckRow>({
		id: 'dns_probe',
		title: 'Резолв через клиентский DNS',
		status: 'pending',
		message: 'Не запускалось',
	});
	let policyCheck = $state<CheckRow | null>(null);
	let proxyMode = $derived(clientIP === '127.0.0.1' || clientIP === '::1' || clientIP === '');
	let expanded = $state(false);

	const hasResult = $derived(
		resolveCheck.status !== 'pending' || policyCheck !== null,
	);

	let okCount = $derived(
		(resolveCheck.status === 'ok' ? 1 : 0) + (policyCheck?.status === 'ok' ? 1 : 0),
	);
	let totalCount = $derived(1 + (policyCheck ? 1 : 0));
	let hasFail = $derived(
		resolveCheck.status === 'fail' || policyCheck?.status === 'fail',
	);
	let hasWarn = $derived(
		resolveCheck.status === 'warning' || policyCheck?.status === 'warning',
	);

	let led: GroupLed = $derived.by(() => {
		if (running) return 'running';
		if (!hasResult) return 'gray';
		if (hasFail) return 'red';
		if (hasWarn) return 'yellow';
		return 'green';
	});

	let summary = $derived.by(() => {
		if (running) return '⟳';
		if (!hasResult) return '— · 0/2';
		if (hasFail) return `${okCount}/${totalCount} ✕`;
		if (hasWarn) return `${okCount}/${totalCount} △`;
		return `${okCount}/${totalCount} ✓`;
	});

	$effect(() => {
		if (hasResult && (hasFail || hasWarn)) expanded = true;
	});

	function toRow(r: DnsCheckResult): CheckRow {
		return {
			id: r.id,
			title: r.title,
			status: r.status === 'pending' ? 'pending' : r.status,
			message: r.message,
			detail: r.detail,
		};
	}

	async function runCheck(e?: Event) {
		e?.stopPropagation();
		running = true;
		expanded = true;
		resolveCheck = { ...resolveCheck, status: 'pending', message: 'Запрос к awgm-dnscheck.test...' };
		policyCheck = null;

		const startPromise = api.startDnsCheck().catch(() => null);
		const probePromise = doResolveProbe();

		const start = await startPromise;
		if (start) {
			clientIP = start.clientIP;
			const policy = start.checks.find((c) => c.id === 'client_policy');
			if (policy) policyCheck = toRow(policy);
		}

		const probe = await probePromise;
		resolveCheck = probe;

		running = false;
	}

	async function doResolveProbe(): Promise<CheckRow> {
		try {
			const port = window.location.port || (window.location.protocol === 'https:' ? '443' : '80');
			const scheme = window.location.protocol === 'https:' ? 'https' : 'http';
			const probeUrl = `${scheme}://awgm-dnscheck.test:${port}/api/dns-check/probe`;
			const resp = await fetch(probeUrl, { signal: AbortSignal.timeout(3000) });
			if (resp.ok) {
				return {
					id: 'dns_probe',
					title: 'Резолв через клиентский DNS',
					status: 'ok',
					message: 'DNS-запрос успешно достиг роутера',
				};
			}
			return {
				id: 'dns_probe',
				title: 'Резолв через клиентский DNS',
				status: 'fail',
				message: `Ответ ${resp.status} — DNS-запрос не достиг роутера`,
			};
		} catch {
			return {
				id: 'dns_probe',
				title: 'Резолв через клиентский DNS',
				status: 'fail',
				message: 'DNS-запрос не достиг роутера. Клиент использует внешний DNS, а не роутер.',
			};
		}
	}

	function variantOf(s: CheckStatus): 'success' | 'error' | 'warning' | 'muted' {
		if (s === 'ok') return 'success';
		if (s === 'fail') return 'error';
		if (s === 'warning') return 'warning';
		return 'muted';
	}
</script>

<ChecksGroup
	name="Маршрутизация по DNS"
	subtitle="DNS клиента + access policy"
	{led}
	{summary}
	{expanded}
	onToggle={() => (expanded = !expanded)}
	highlight
>
	{#snippet actions()}
		<Button
			variant="secondary"
			size="sm"
			onclick={runCheck}
			loading={running}
		>
			{running ? 'Идёт' : 'Проверить'}
		</Button>
	{/snippet}

	{#snippet body()}
		{#if hasResult && proxyMode}
			<div class="proxy-banner">
				<strong>Подключение через reverse proxy</strong>
				<p>
					Сервер видит вас как <code>{clientIP || 'loopback'}</code>. DNS-проверки
					неактуальны — тестируйте напрямую с устройств в локальной сети, минуя прокси.
				</p>
			</div>
		{:else if hasResult}
			<div class="check-row">
				<StatusDot variant={variantOf(resolveCheck.status)} size="sm" />
				<div class="check-content">
					<span class="check-title">{resolveCheck.title}</span>
					<span class="check-msg">{resolveCheck.message}</span>
				</div>
			</div>

			{#if policyCheck}
				<div class="check-row">
					<StatusDot variant={variantOf(policyCheck.status)} size="sm" />
					<div class="check-content">
						<span class="check-title">{policyCheck.title}</span>
						<span class="check-msg">{policyCheck.message}</span>
						{#if policyCheck.detail}
							<span class="check-detail">{policyCheck.detail}</span>
						{/if}
					</div>
				</div>
			{/if}

			<p class="ip-line">
				IP клиента: <code>{clientIP || '—'}</code>
			</p>
		{:else}
			<p class="hint">
				Проверяет, что DNS-запросы клиента доходят до роутера и что устройство
				находится в политике доступа по умолчанию.
			</p>
		{/if}
	{/snippet}
</ChecksGroup>

<style>
	.proxy-banner {
		padding: 8px 12px;
		background: var(--color-warning-tint);
		border: 1px solid var(--color-warning-border);
		border-radius: var(--radius-sm);
		display: flex;
		flex-direction: column;
		gap: 4px;
	}

	.proxy-banner strong {
		font-size: 12px;
		color: var(--color-warning);
	}

	.proxy-banner p {
		margin: 0;
		font-size: 11px;
		line-height: 1.4;
		color: var(--color-text-secondary);
	}

	.proxy-banner code,
	.ip-line code {
		font-family: var(--font-mono);
		font-size: 11px;
		padding: 0 4px;
		background: var(--color-bg-primary);
		border-radius: var(--radius-sm);
		color: var(--color-text-secondary);
	}

	.check-row {
		display: flex;
		align-items: flex-start;
		gap: 8px;
		padding: 6px 0;
		min-width: 0;
	}

	.check-content {
		display: flex;
		flex-direction: column;
		min-width: 0;
		flex: 1;
		gap: 2px;
	}

	.check-title {
		font-size: 13px;
		color: var(--color-text-primary);
		font-weight: 500;
	}

	.check-msg {
		font-size: 11px;
		color: var(--color-text-muted);
		word-wrap: break-word;
	}

	.check-detail {
		font-size: 11px;
		color: var(--color-text-muted);
		opacity: 0.8;
		font-style: italic;
		word-wrap: break-word;
	}

	.ip-line {
		margin: 6px 0 0 0;
		font-size: 11px;
		color: var(--color-text-muted);
	}

	.hint {
		margin: 0;
		font-size: 12px;
		color: var(--color-text-muted);
		line-height: 1.45;
	}
</style>
