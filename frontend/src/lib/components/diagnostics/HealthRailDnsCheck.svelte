<script lang="ts">
	import { Button, Card, Badge, StatusDot } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import type { DnsCheckResult } from '$lib/types';

	type CheckStatus = 'pending' | 'ok' | 'fail' | 'warning';

	interface CheckRow {
		id: string;
		title: string;
		status: CheckStatus;
		message: string;
		detail?: string;
	}

	let expanded = $state(false);
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

	const hasResult = $derived(
		resolveCheck.status !== 'pending' || policyCheck !== null,
	);

	function toRow(r: DnsCheckResult): CheckRow {
		return {
			id: r.id,
			title: r.title,
			status: r.status === 'pending' ? 'pending' : r.status,
			message: r.message,
			detail: r.detail,
		};
	}

	async function runCheck() {
		running = true;
		resolveCheck = { ...resolveCheck, status: 'pending', message: 'Запрос к awgm-dnscheck.test...' };
		policyCheck = null;

		// Fire server-side checks (gives clientIP + access-policy result) and
		// the client-side resolve probe in parallel — the resolve probe goes
		// directly to a domain that should be re-routed to this router via the
		// client's DNS, so it MUST be a fetch from the browser, not a backend call.
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
				return { id: 'dns_probe', title: 'Резолв через клиентский DNS', status: 'ok', message: 'DNS-запрос успешно достиг роутера' };
			}
			return { id: 'dns_probe', title: 'Резолв через клиентский DNS', status: 'fail', message: `Ответ ${resp.status} — DNS-запрос не достиг роутера` };
		} catch {
			return { id: 'dns_probe', title: 'Резолв через клиентский DNS', status: 'fail', message: 'DNS-запрос не достиг роутера. Клиент использует внешний DNS, а не роутер.' };
		}
	}

	function variantOf(s: CheckStatus): 'success' | 'error' | 'warning' | 'muted' {
		if (s === 'ok') return 'success';
		if (s === 'fail') return 'error';
		if (s === 'warning') return 'warning';
		return 'muted';
	}
</script>

<Card variant="nested" padding="md">
	<button
		type="button"
		class="header"
		onclick={() => (expanded = !expanded)}
		aria-expanded={expanded}
	>
		<strong>DNS-проверка</strong>
		{#if hasResult}
			<span class="counts">
				{#if resolveCheck.status === 'ok'}<Badge variant="success" size="sm">DNS</Badge>{/if}
				{#if resolveCheck.status === 'fail'}<Badge variant="error" size="sm">DNS</Badge>{/if}
				{#if policyCheck?.status === 'ok'}<Badge variant="success" size="sm">POLICY</Badge>{/if}
				{#if policyCheck?.status === 'warning'}<Badge variant="warning" size="sm">POLICY</Badge>{/if}
			</span>
		{:else}
			<span class="placeholder">Не запускалось</span>
		{/if}
		<span class="chevron" class:rotated={expanded}>›</span>
	</button>

	{#if expanded}
		<div class="body">
			<p class="hint">
				Проверяет, что DNS-запросы клиента доходят до роутера и что устройство получает
				альтернативную политику доступа.
			</p>

			<Button variant="primary" fullWidth onclick={runCheck} loading={running}>
				Проверить
			</Button>

			{#if hasResult && proxyMode}
				<div class="proxy-banner">
					<strong>Подключение через reverse proxy</strong>
					<p>
						Сервер видит вас как <code>{clientIP || 'loopback'}</code>. DNS-проверки
						неактуальны — тестируйте напрямую с устройств в локальной сети, минуя прокси.
					</p>
				</div>
			{/if}

			{#if hasResult && !proxyMode}
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
					Сервер видит клиент как <code>{clientIP || '—'}</code>
				</p>
			{/if}
		</div>
	{/if}
</Card>

<style>
	.header {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		background: transparent;
		border: none;
		padding: 0;
		width: 100%;
		cursor: pointer;
		font: inherit;
		color: inherit;
		min-width: 0;
	}

	.counts {
		display: inline-flex;
		align-items: center;
		gap: 0.25rem;
		margin-left: auto;
		margin-right: 0.5rem;
		flex-shrink: 0;
	}

	.placeholder {
		margin-left: auto;
		margin-right: 0.5rem;
		color: var(--color-text-muted);
		font-size: 12px;
		flex-shrink: 0;
	}

	.chevron {
		transition: transform var(--t-fast) ease;
		color: var(--color-text-muted);
		flex-shrink: 0;
	}
	.chevron.rotated {
		transform: rotate(90deg);
	}

	.body {
		display: flex;
		flex-direction: column;
		gap: 0.625rem;
		margin-top: 0.75rem;
		min-width: 0;
	}

	.hint {
		margin: 0;
		font-size: 11px;
		line-height: 1.4;
		color: var(--color-text-muted);
	}

	.proxy-banner code,
	.ip-line code {
		font-family: var(--font-mono);
		font-size: 11px;
		padding: 0 0.25rem;
		background: var(--color-bg-primary);
		border-radius: var(--radius-sm);
		color: var(--color-text-secondary);
	}

	.check-row {
		display: flex;
		align-items: flex-start;
		gap: 0.5rem;
		padding: 0.4375rem 0;
		min-width: 0;
	}

	.check-content {
		display: flex;
		flex-direction: column;
		min-width: 0;
		flex: 1;
		gap: 0.125rem;
	}

	.check-title {
		font-size: 12px;
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

	.proxy-banner {
		padding: 0.625rem 0.75rem;
		background: var(--color-warning-tint);
		border: 1px solid var(--color-warning-border);
		border-radius: var(--radius-sm);
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
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

	.ip-line {
		margin: 0;
		font-size: 11px;
		color: var(--color-text-muted);
	}
</style>
