<script lang="ts">
	import { m } from '$lib/i18n';
	import { untrack } from 'svelte';
	import { Button, StatusDot } from '$lib/components/ui';
	import { api } from '$lib/api/client';
	import { singboxStatus } from '$lib/stores/singbox';
	import type { DnsCheckResult } from '$lib/types';
	import ChecksGroup, { type GroupLed } from './ChecksGroup.svelte';

	interface Props {
		/** Increment to trigger a DNS check run externally (e.g. from "run all"). */
		triggerRun?: number;
	}

	let { triggerRun = 0 }: Props = $props();

	$effect(() => {
		const gen = triggerRun;
		if (gen <= 0) return;
		// Only depend on `triggerRun`; do not subscribe to state touched inside runCheck().
		untrack(() => {
			void runCheck();
		});
	});

	type CheckStatus = 'pending' | 'ok' | 'fail' | 'warning';

	/** Текст строки: серверный — строкой, клиентский — функцией сообщения (читается при рендере, язык переключается на лету). */
	type Text = string | (() => string);
	const txt = (t: Text): string => (typeof t === 'function' ? t() : t);

	interface CheckRow {
		id: string;
		title: Text;
		status: CheckStatus;
		message: Text;
		detail?: string;
	}

	let running = $state(false);
	/** Prevents overlapping runs when triggerRun fires while a check is already in flight. */
	let runInFlight = $state(false);
	let clientIP = $state('');
	let resolveCheck = $state<CheckRow>({
		id: 'dns_probe',
		title: m.diag_dns_check_resolve_title,
		status: 'pending',
		message: m.diag_dns_check_not_run,
	});
	let policyCheck = $state<CheckRow | null>(null);
	let proxyMode = $derived(clientIP === '127.0.0.1' || clientIP === '::1' || clientIP === '');
	let expanded = $state(false);
	let staleNotice = $state(false);
	let prevRunning: boolean | undefined = undefined;

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

	// Reset stale result when Sing-box is toggled: its running state changes
	// whether the DNS probe is applicable, so a prior result no longer reflects
	// reality. Track ONLY `running`; do all comparison reads + mutations under
	// untrack so reading hasResult (derived) doesn't make this effect depend on
	// resolveCheck/policyCheck (which would loop).
	$effect(() => {
		const cur = $singboxStatus.data?.running ?? false;
		untrack(() => {
			if (prevRunning !== undefined && prevRunning !== cur && hasResult) {
				resolveCheck = {
					id: 'dns_probe',
					title: m.diag_dns_check_resolve_title,
					status: 'pending',
					message: m.diag_dns_check_not_run,
				};
				policyCheck = null;
				staleNotice = true;
			}
			prevRunning = cur;
		});
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
		staleNotice = false;
		// External trigger (no click event): skip if already running to avoid piling requests onto NDMS.
		if (!e && runInFlight) return;
		runInFlight = true;
		running = true;
		try {
			expanded = true;
			resolveCheck = { ...resolveCheck, status: 'pending', message: m.diag_dns_check_querying };
			policyCheck = null;

			// При работающем sing-box пробы не будет (DNS обрабатывает он), значит
			// и запись awgm-dnscheck.test заводить незачем — лёгкая ручка её не
			// трогает и отдаёт всё, что здесь нужно: клиента и его политику.
			const singboxOn = $singboxStatus.data?.running ?? false;
			// Проба строго ПОСЛЕ start, не параллельно с ним: запись
			// awgm-dnscheck.test заводится на время проверки и до ответа start
			// ещё не существует.
			const start = singboxOn
				? await api.getDnsCheckClient().catch(() => null)
				: await api.startDnsCheck().catch(() => null);
			if (start) {
				clientIP = start.clientIP;
				const policy = start.checks.find((c) => c.id === 'client_policy');
				if (policy) policyCheck = toRow(policy);
			}

			// Бэкенд сообщил, что записи завести не вышло — пробовать нечего,
			// её провал прочитался бы как «клиент ходит мимо роутера».
			const armIssue = start?.checks.find((c) => c.id === 'dns_probe' && c.status !== 'pending');
			if (armIssue) {
				resolveCheck = toRow(armIssue);
			} else if (!singboxOn && !start) {
				resolveCheck = {
					id: 'dns_probe',
					title: m.diag_dns_check_resolve_title,
					status: 'fail',
					message: m.diag_dns_check_start_failed,
				};
			} else {
				resolveCheck = await doResolveProbe();
			}
		} finally {
			running = false;
			runInFlight = false;
		}
	}

	async function doResolveProbe(): Promise<CheckRow> {
		if ($singboxStatus.data?.running ?? false) {
			return {
				id: 'dns_probe',
				title: m.diag_dns_check_resolve_title,
				status: 'warning',
				message: m.diag_dns_check_singbox_skipped,
			};
		}
		try {
			const port = window.location.port || (window.location.protocol === 'https:' ? '443' : '80');
			const scheme = window.location.protocol === 'https:' ? 'https' : 'http';
			const probeUrl = `${scheme}://awgm-dnscheck.test:${port}/api/dns-check/probe`;
			const resp = await fetch(probeUrl, { signal: AbortSignal.timeout(3000) });
			if (resp.ok) {
				return {
					id: 'dns_probe',
					title: m.diag_dns_check_resolve_title,
					status: 'ok',
					message: m.diag_dns_check_reached,
				};
			}
			return {
				id: 'dns_probe',
				title: m.diag_dns_check_resolve_title,
				status: 'fail',
				message: () => m.diag_dns_check_http_status({ status: resp.status }),
			};
		} catch {
			return {
				id: 'dns_probe',
				title: m.diag_dns_check_resolve_title,
				status: 'fail',
				message: m.diag_dns_check_unreached,
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
	name={m.diag_dns_check_group_name()}
	subtitle={m.diag_dns_check_group_subtitle()}
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
			{running ? m.diag_group_running() : m.diag_group_check()}
		</Button>
	{/snippet}

	{#snippet body()}
		{#if staleNotice && !hasResult}
			<div class="proxy-banner">
				<strong>{m.diag_dns_check_stale_title()}</strong>
				<p>{m.diag_dns_check_stale_text()}</p>
			</div>
		{/if}
		{#if hasResult && proxyMode}
			<div class="proxy-banner">
				<strong>{m.diag_dns_check_proxy_title()}</strong>
				<p>
					{m.diag_dns_check_proxy_sees()} <code>{clientIP || 'loopback'}</code>. {m.diag_dns_check_proxy_irrelevant()}
				</p>
			</div>
		{:else if hasResult}
			<div class="check-row">
				<StatusDot variant={variantOf(resolveCheck.status)} size="sm" />
				<div class="check-content">
					<span class="check-title">{txt(resolveCheck.title)}</span>
					<span class="check-msg">{txt(resolveCheck.message)}</span>
				</div>
			</div>

			{#if policyCheck}
				<div class="check-row">
					<StatusDot variant={variantOf(policyCheck.status)} size="sm" />
					<div class="check-content">
						<span class="check-title">{txt(policyCheck.title)}</span>
						<span class="check-msg">{txt(policyCheck.message)}</span>
						{#if policyCheck.detail}
							<span class="check-detail">{policyCheck.detail}</span>
						{/if}
					</div>
				</div>
			{/if}

			<p class="ip-line">
				{m.diag_dns_check_client_ip()} <code>{clientIP || '—'}</code>
			</p>
		{:else}
			<p class="hint">
				{m.diag_dns_check_hint()}
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
